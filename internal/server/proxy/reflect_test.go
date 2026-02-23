package proxy

import (
	"encoding/binary"
	"strings"
	"testing"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/dynamicpb"
)

func TestUnpackFrame(t *testing.T) {
	tests := []struct {
		name string
		data []byte
		want []byte
	}{
		{
			name: "nil input",
			data: nil,
			want: nil,
		},
		{
			name: "too short",
			data: []byte{0, 0, 0, 0},
			want: nil,
		},
		{
			name: "compressed flag set",
			data: []byte{1, 0, 0, 0, 3, 'a', 'b', 'c'},
			want: nil,
		},
		{
			name: "incomplete payload",
			data: []byte{0, 0, 0, 0, 5, 'a', 'b'},
			want: nil,
		},
		{
			name: "empty payload",
			data: []byte{0, 0, 0, 0, 0},
			want: nil,
		},
		{
			name: "valid frame",
			data: makeFrame([]byte("hello")),
			want: []byte("hello"),
		},
		{
			name: "extra trailing data ignored",
			data: append(makeFrame([]byte("hi")), 'x', 'y', 'z'),
			want: []byte("hi"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := unpackFrame(tt.data)
			if tt.want == nil && got != nil {
				t.Errorf("got %v, want nil", got)
			} else if tt.want != nil && got == nil {
				t.Errorf("got nil, want %v", tt.want)
			} else if string(got) != string(tt.want) {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestGRPCDecoderDecode(t *testing.T) {
	// Build a minimal file descriptor with a service and method.
	fdp := &descriptorpb.FileDescriptorProto{
		Name:    proto.String("test.proto"),
		Syntax:  proto.String("proto3"),
		Package: proto.String("test"),
		MessageType: []*descriptorpb.DescriptorProto{
			{
				Name: proto.String("Req"),
				Field: []*descriptorpb.FieldDescriptorProto{
					{
						Name:   proto.String("name"),
						Number: proto.Int32(1),
						Type:   descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum(),
						Label:  descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
					},
				},
			},
			{
				Name: proto.String("Resp"),
				Field: []*descriptorpb.FieldDescriptorProto{
					{
						Name:   proto.String("value"),
						Number: proto.Int32(1),
						Type:   descriptorpb.FieldDescriptorProto_TYPE_INT32.Enum(),
						Label:  descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
					},
				},
			},
		},
		Service: []*descriptorpb.ServiceDescriptorProto{
			{
				Name: proto.String("Greeter"),
				Method: []*descriptorpb.MethodDescriptorProto{
					{
						Name:       proto.String("Hello"),
						InputType:  proto.String(".test.Req"),
						OutputType: proto.String(".test.Resp"),
					},
				},
			},
		},
	}

	fds := &descriptorpb.FileDescriptorSet{File: []*descriptorpb.FileDescriptorProto{fdp}}
	resolved, err := protodesc.NewFiles(fds)
	if err != nil {
		t.Fatalf("protodesc.NewFiles: %v", err)
	}

	methods := make(map[string]methodDesc)
	resolved.RangeFiles(func(fd protoreflect.FileDescriptor) bool {
		for i := 0; i < fd.Services().Len(); i++ {
			sd := fd.Services().Get(i)
			for j := 0; j < sd.Methods().Len(); j++ {
				md := sd.Methods().Get(j)
				key := string(sd.FullName()) + "/" + string(md.Name())
				methods[key] = methodDesc{
					input:  md.Input(),
					output: md.Output(),
				}
			}
		}
		return true
	})

	decoder := &grpcDecoder{methods: methods}

	// Build a framed request: Req{name: "world"}
	reqMsg := dynamicpb.NewMessage(methods["test.Greeter/Hello"].input)
	reqMsg.Set(reqMsg.Descriptor().Fields().ByName("name"), protoreflect.ValueOfString("world"))
	reqBytes, err := proto.Marshal(reqMsg)
	if err != nil {
		t.Fatalf("marshal req: %v", err)
	}
	reqFrame := makeFrame(reqBytes)

	// Build a framed response: Resp{value: 42}
	respMsg := dynamicpb.NewMessage(methods["test.Greeter/Hello"].output)
	respMsg.Set(respMsg.Descriptor().Fields().ByName("value"), protoreflect.ValueOfInt32(42))
	respBytes, err := proto.Marshal(respMsg)
	if err != nil {
		t.Fatalf("marshal resp: %v", err)
	}
	respFrame := makeFrame(respBytes)

	t.Run("decode request", func(t *testing.T) {
		got := decoder.Decode("test.Greeter", "Hello", reqFrame, true)
		if got == "" {
			t.Fatal("Decode returned empty string")
		}
		// Should contain "world"
		if !strings.Contains(got, "world") {
			t.Errorf("decoded JSON %q does not contain 'world'", got)
		}
	})

	t.Run("decode response", func(t *testing.T) {
		got := decoder.Decode("test.Greeter", "Hello", respFrame, false)
		if got == "" {
			t.Fatal("Decode returned empty string")
		}
		// Should contain 42
		if !strings.Contains(got, "42") {
			t.Errorf("decoded JSON %q does not contain '42'", got)
		}
	})

	t.Run("unknown method", func(t *testing.T) {
		got := decoder.Decode("test.Greeter", "Unknown", reqFrame, true)
		if got != "" {
			t.Errorf("expected empty string for unknown method, got %q", got)
		}
	})

	t.Run("nil frame", func(t *testing.T) {
		got := decoder.Decode("test.Greeter", "Hello", nil, true)
		if got != "" {
			t.Errorf("expected empty string for nil frame, got %q", got)
		}
	})

	t.Run("compressed frame", func(t *testing.T) {
		compressed := append([]byte{1}, reqFrame[1:]...)
		got := decoder.Decode("test.Greeter", "Hello", compressed, true)
		if got != "" {
			t.Errorf("expected empty string for compressed frame, got %q", got)
		}
	})
}

// makeFrame wraps raw protobuf bytes in a gRPC length-prefixed frame.
func makeFrame(payload []byte) []byte {
	frame := make([]byte, 5+len(payload))
	frame[0] = 0 // not compressed
	binary.BigEndian.PutUint32(frame[1:5], uint32(len(payload)))
	copy(frame[5:], payload)
	return frame
}

