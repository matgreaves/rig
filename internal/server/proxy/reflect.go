package proxy

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/binary"
	"fmt"
	"io"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	rpb "google.golang.org/grpc/reflection/grpc_reflection_v1"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/dynamicpb"
)

// GRPCDecoder decodes gRPC request/response bodies into JSON using
// descriptors obtained via server reflection.
type GRPCDecoder struct {
	methods map[string]methodDesc // key: "pkg.Service/Method"
}

type methodDesc struct {
	input  protoreflect.MessageDescriptor
	output protoreflect.MessageDescriptor
}

// ProbeReflection dials the target gRPC server and attempts to fetch service
// descriptors via the v1 reflection API. Returns nil if reflection is not
// available or any error occurs. The caller should treat nil as "no decoder".
func ProbeReflection(ctx context.Context, addr string) *GRPCDecoder {
	conn, err := grpc.NewClient(addr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		return nil
	}
	defer conn.Close()

	client := rpb.NewServerReflectionClient(conn)
	stream, err := client.ServerReflectionInfo(ctx)
	if err != nil {
		return nil
	}

	// List all services.
	if err := stream.Send(&rpb.ServerReflectionRequest{
		MessageRequest: &rpb.ServerReflectionRequest_ListServices{ListServices: ""},
	}); err != nil {
		return nil
	}
	listResp, err := stream.Recv()
	if err != nil {
		return nil
	}
	listSvcs := listResp.GetListServicesResponse()
	if listSvcs == nil {
		return nil
	}

	// Fetch file descriptors for each service.
	seen := make(map[string]bool)
	var allFiles []*descriptorpb.FileDescriptorProto
	for _, svc := range listSvcs.Service {
		files, err := fetchFileDescriptors(stream, svc.Name, seen)
		if err != nil {
			return nil
		}
		allFiles = append(allFiles, files...)
	}

	if len(allFiles) == 0 {
		return nil
	}

	// Build a FileDescriptorSet and resolve into a registry.
	fds := &descriptorpb.FileDescriptorSet{File: allFiles}
	resolved, err := protodesc.NewFiles(fds)
	if err != nil {
		return nil
	}

	// Build method map.
	methods := make(map[string]methodDesc)
	resolved.RangeFiles(func(fd protoreflect.FileDescriptor) bool {
		for i := 0; i < fd.Services().Len(); i++ {
			sd := fd.Services().Get(i)
			for j := 0; j < sd.Methods().Len(); j++ {
				md := sd.Methods().Get(j)
				key := fmt.Sprintf("%s/%s", sd.FullName(), md.Name())
				methods[key] = methodDesc{
					input:  md.Input(),
					output: md.Output(),
				}
			}
		}
		return true
	})

	if len(methods) == 0 {
		return nil
	}

	return &GRPCDecoder{methods: methods}
}

// fetchFileDescriptors fetches the file descriptor for a service (by symbol)
// and all its transitive dependencies.
func fetchFileDescriptors(
	stream rpb.ServerReflection_ServerReflectionInfoClient,
	serviceName string,
	seen map[string]bool,
) ([]*descriptorpb.FileDescriptorProto, error) {
	return fetchDescriptors(stream, &rpb.ServerReflectionRequest{
		MessageRequest: &rpb.ServerReflectionRequest_FileContainingSymbol{
			FileContainingSymbol: serviceName,
		},
	}, seen)
}

// fetchDescriptors sends a reflection request, collects the returned file
// descriptors, and recursively fetches any unseen transitive dependencies.
func fetchDescriptors(
	stream rpb.ServerReflection_ServerReflectionInfoClient,
	req *rpb.ServerReflectionRequest,
	seen map[string]bool,
) ([]*descriptorpb.FileDescriptorProto, error) {
	if err := stream.Send(req); err != nil {
		return nil, err
	}

	resp, err := stream.Recv()
	if err != nil {
		return nil, err
	}
	fdResp := resp.GetFileDescriptorResponse()
	if fdResp == nil {
		return nil, fmt.Errorf("no file descriptor response")
	}

	var result []*descriptorpb.FileDescriptorProto
	for _, raw := range fdResp.FileDescriptorProto {
		fdp := &descriptorpb.FileDescriptorProto{}
		if err := proto.Unmarshal(raw, fdp); err != nil {
			return nil, err
		}
		name := fdp.GetName()
		if seen[name] {
			continue
		}
		seen[name] = true
		result = append(result, fdp)

		for _, dep := range fdp.Dependency {
			if seen[dep] {
				continue
			}
			depFiles, err := fetchDescriptors(stream, &rpb.ServerReflectionRequest{
				MessageRequest: &rpb.ServerReflectionRequest_FileByFilename{
					FileByFilename: dep,
				},
			}, seen)
			if err != nil {
				// Non-fatal: some well-known deps may not be served.
				continue
			}
			result = append(result, depFiles...)
		}
	}
	return result, nil
}

// Decode decodes a gRPC framed body (length-prefixed protobuf) into JSON.
// svc is "pkg.Service", method is "Method". isRequest selects which descriptor
// (input or output) to use. Returns "" on any failure.
func (d *GRPCDecoder) Decode(svc, method string, framedData []byte, isRequest bool) string {
	key := svc + "/" + method
	md, ok := d.methods[key]
	if !ok {
		return ""
	}

	raw := unpackFrame(framedData)
	if raw == nil {
		return ""
	}

	var desc protoreflect.MessageDescriptor
	if isRequest {
		desc = md.input
	} else {
		desc = md.output
	}

	msg := dynamicpb.NewMessage(desc)
	if err := proto.Unmarshal(raw, msg); err != nil {
		return ""
	}

	out, err := protojson.Marshal(msg)
	if err != nil {
		return ""
	}
	return string(out)
}

// unpackFrame strips the first gRPC length-prefixed frame header (5 bytes:
// 1 byte compressed flag + 4 bytes big-endian length) and returns the raw
// protobuf message bytes. Handles both uncompressed and gzip-compressed frames.
func unpackFrame(data []byte) []byte {
	if len(data) < 5 {
		return nil
	}
	compressed := data[0]
	msgLen := binary.BigEndian.Uint32(data[1:5])
	if msgLen == 0 {
		return nil // empty frame
	}
	payload := data[5:]
	if uint32(len(payload)) < msgLen {
		return nil // incomplete frame
	}
	payload = payload[:msgLen]
	if compressed == 0 {
		return payload
	}
	// gRPC uses gzip compression by default.
	gr, err := gzip.NewReader(bytes.NewReader(payload))
	if err != nil {
		return nil
	}
	defer gr.Close()
	raw, err := io.ReadAll(gr)
	if err != nil {
		return nil
	}
	return raw
}
