package proxy

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

// Kafka wire protocol constants.
const (
	kafkaAPIKeyMetadata        = 3
	kafkaAPIKeyFindCoordinator = 10
	kafkaMaxFrameSize          = 256 * 1024 * 1024 // 256 MB — matches Kafka's default message.max.bytes
)

// apiInfo tracks the API key, version, timing, and size for a correlated request/response pair.
type apiInfo struct {
	apiKey      int16
	apiVersion  int16
	startTime   time.Time
	requestSize int64
}

// correlationTracker maps correlation IDs to their request API key and version.
type correlationTracker struct {
	mu sync.Mutex
	m  map[int32]apiInfo
}

func newCorrelationTracker() *correlationTracker {
	return &correlationTracker{m: make(map[int32]apiInfo)}
}

func (t *correlationTracker) track(correlationID int32, key int16, version int16, startTime time.Time, requestSize int64) {
	t.mu.Lock()
	t.m[correlationID] = apiInfo{apiKey: key, apiVersion: version, startTime: startTime, requestSize: requestSize}
	t.mu.Unlock()
}

func (t *correlationTracker) lookup(correlationID int32) (apiInfo, bool) {
	t.mu.Lock()
	info, ok := t.m[correlationID]
	if ok {
		delete(t.m, correlationID)
	}
	t.mu.Unlock()
	return info, ok
}

// runKafka starts a Kafka-aware TCP proxy that rewrites Metadata responses
// so broker advertised addresses point at the proxy instead of the real broker.
func (f *Forwarder) runKafka(ctx context.Context) error {
	ln, err := f.getListener()
	if err != nil {
		return fmt.Errorf("proxy %s→%s: listen: %w", f.Source, f.TargetSvc, err)
	}

	go func() {
		<-ctx.Done()
		ln.Close()
	}()

	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("proxy %s→%s: accept: %w", f.Source, f.TargetSvc, err)
		}
		go f.handleKafkaConn(ctx, conn)
	}
}

func (f *Forwarder) handleKafkaConn(ctx context.Context, client net.Conn) {
	start := time.Now()

	f.Emit(Event{
		Type: "connection.opened",
		Connection: &ConnectionInfo{
			Source:  f.Source,
			Target:  f.TargetSvc,
			Ingress: f.Ingress,
		},
	})

	target, err := net.DialTimeout("tcp", f.Target.HostPort, 5*time.Second)
	if err != nil {
		client.Close()
		f.Emit(Event{
			Type: "connection.closed",
			Connection: &ConnectionInfo{
				Source:     f.Source,
				Target:     f.TargetSvc,
				Ingress:    f.Ingress,
				DurationMs: float64(time.Since(start).Microseconds()) / 1000.0,
			},
		})
		return
	}

	go func() {
		<-ctx.Done()
		client.Close()
		target.Close()
	}()

	tracker := newCorrelationTracker()
	proxyHost, proxyPortStr, _ := net.SplitHostPort(f.ListenAddr)
	proxyPort64, _ := strconv.ParseInt(proxyPortStr, 10, 32)
	proxyPort := int32(proxyPort64)

	var bytesIn, bytesOut atomic.Int64
	var wg sync.WaitGroup
	wg.Add(2)

	// client → broker: parse request headers to track correlation IDs.
	go func() {
		defer wg.Done()
		n := relayKafkaRequests(client, target, tracker)
		bytesIn.Store(n)
		if tc, ok := target.(*net.TCPConn); ok {
			tc.CloseWrite()
		}
	}()

	// broker → client: intercept Metadata responses and rewrite broker addresses.
	respRelay := &kafkaResponseRelay{
		tracker:   tracker,
		proxyHost: proxyHost,
		proxyPort: proxyPort,
		source:    f.Source,
		target:    f.TargetSvc,
		ingress:   f.Ingress,
		emit:      f.Emit,
	}
	go func() {
		defer wg.Done()
		n := respRelay.relay(target, client)
		bytesOut.Store(n)
		if tc, ok := client.(*net.TCPConn); ok {
			tc.CloseWrite()
		}
	}()

	wg.Wait()
	client.Close()
	target.Close()

	f.Emit(Event{
		Type: "connection.closed",
		Connection: &ConnectionInfo{
			Source:     f.Source,
			Target:     f.TargetSvc,
			Ingress:    f.Ingress,
			BytesIn:    bytesIn.Load(),
			BytesOut:   bytesOut.Load(),
			DurationMs: float64(time.Since(start).Microseconds()) / 1000.0,
		},
	})
}

// relayKafkaRequests reads Kafka request frames from src, records
// (correlation_id → api_key, api_version) in the tracker, and forwards
// the complete frame unchanged to dst. Returns total bytes forwarded.
func relayKafkaRequests(src io.Reader, dst io.Writer, tracker *correlationTracker) int64 {
	var total int64
	hdr := make([]byte, 4)
	for {
		// Read frame length.
		if _, err := io.ReadFull(src, hdr); err != nil {
			return total
		}
		frameLen := binary.BigEndian.Uint32(hdr)
		if frameLen > kafkaMaxFrameSize {
			return total
		}

		payload := make([]byte, frameLen)
		if _, err := io.ReadFull(src, payload); err != nil {
			return total
		}

		// Parse request header: api_key(2) + api_version(2) + correlation_id(4).
		if len(payload) >= 8 {
			apiKey := int16(binary.BigEndian.Uint16(payload[0:2]))
			apiVersion := int16(binary.BigEndian.Uint16(payload[2:4]))
			correlationID := int32(binary.BigEndian.Uint32(payload[4:8]))
			tracker.track(correlationID, apiKey, apiVersion, time.Now(), int64(4)+int64(frameLen))
		}

		// Forward the complete frame unchanged.
		if _, err := dst.Write(hdr); err != nil {
			return total
		}
		if _, err := dst.Write(payload); err != nil {
			return total
		}
		total += int64(4) + int64(frameLen)
	}
}

// kafkaResponseRelay holds the configuration for relaying Kafka response
// frames from a broker back to a client, rewriting addresses as needed.
type kafkaResponseRelay struct {
	tracker   *correlationTracker
	proxyHost string
	proxyPort int32
	source    string // for event emission
	target    string
	ingress   string
	emit      func(Event) // nil to skip event emission
}

// relay reads Kafka response frames from src, checks the correlation tracker
// to identify Metadata/FindCoordinator responses, rewrites broker host:port
// entries, emits per-request events, and forwards everything to dst.
// Returns total bytes forwarded.
func (k *kafkaResponseRelay) relay(src io.Reader, dst io.Writer) int64 {
	var total int64
	hdr := make([]byte, 4)
	for {
		if _, err := io.ReadFull(src, hdr); err != nil {
			return total
		}
		frameLen := binary.BigEndian.Uint32(hdr)
		if frameLen > kafkaMaxFrameSize {
			return total
		}

		payload := make([]byte, frameLen)
		if _, err := io.ReadFull(src, payload); err != nil {
			return total
		}

		// Response header starts with correlation_id (4 bytes).
		if len(payload) < 4 {
			// Malformed — forward as-is.
			dst.Write(hdr)
			dst.Write(payload)
			total += int64(4) + int64(frameLen)
			continue
		}

		correlationID := int32(binary.BigEndian.Uint32(payload[0:4]))
		info, ok := k.tracker.lookup(correlationID)

		responseSize := int64(4) + int64(frameLen)

		if ok && k.emit != nil {
			latencyMs := float64(time.Since(info.startTime).Microseconds()) / 1000.0
			k.emit(Event{
				Type: "kafka.request.completed",
				KafkaRequest: &KafkaRequestInfo{
					Source:        k.source,
					Target:        k.target,
					Ingress:       k.ingress,
					APIKey:        info.apiKey,
					APIName:       kafkaAPIName(info.apiKey),
					APIVersion:    info.apiVersion,
					CorrelationID: correlationID,
					LatencyMs:     latencyMs,
					RequestSize:   info.requestSize,
					ResponseSize:  responseSize,
				},
			})
		}

		var rewritten []byte
		if ok {
			var rewriteErr error
			switch info.apiKey {
			case kafkaAPIKeyMetadata:
				rewritten, rewriteErr = rewriteMetadataResponse(payload, info.apiVersion, k.proxyHost, k.proxyPort)
			case kafkaAPIKeyFindCoordinator:
				rewritten, rewriteErr = rewriteFindCoordinatorResponse(payload, info.apiVersion, k.proxyHost, k.proxyPort)
			}
			if rewriteErr != nil {
				rewritten = nil // fall through to forward original
			}
		}

		if rewritten == nil {
			// Forward unchanged.
			if _, err := dst.Write(hdr); err != nil {
				return total
			}
			if _, err := dst.Write(payload); err != nil {
				return total
			}
			total += responseSize
			continue
		}

		// Write new length + rewritten payload.
		newHdr := make([]byte, 4)
		binary.BigEndian.PutUint32(newHdr, uint32(len(rewritten)))
		if _, err := dst.Write(newHdr); err != nil {
			return total
		}
		if _, err := dst.Write(rewritten); err != nil {
			return total
		}
		total += int64(4 + len(rewritten))
	}
}

// kafkaAPIName returns the human-readable name for a Kafka API key.
func kafkaAPIName(key int16) string {
	switch key {
	case 0:
		return "Produce"
	case 1:
		return "Fetch"
	case 2:
		return "ListOffsets"
	case 3:
		return "Metadata"
	case 8:
		return "OffsetCommit"
	case 9:
		return "OffsetFetch"
	case 10:
		return "FindCoordinator"
	case 11:
		return "JoinGroup"
	case 12:
		return "Heartbeat"
	case 13:
		return "LeaveGroup"
	case 14:
		return "SyncGroup"
	case 18:
		return "ApiVersions"
	case 19:
		return "CreateTopics"
	case 20:
		return "DeleteTopics"
	case 22:
		return "InitProducerId"
	case 36:
		return "SaslAuthenticate"
	default:
		return fmt.Sprintf("API-%d", key)
	}
}

// rewriteMetadataResponse parses a Metadata response payload and rewrites
// each broker's host and port to point at the proxy.
func rewriteMetadataResponse(payload []byte, version int16, proxyHost string, proxyPort int32) ([]byte, error) {
	flexible := version >= 9
	r := newKafkaReader(payload)
	w := newKafkaWriter()

	// Response header: correlation_id (4 bytes).
	correlationID, err := r.int32()
	if err != nil {
		return nil, err
	}
	w.writeInt32(correlationID)

	// Flexible versions have a tagged field section in the response header.
	if flexible {
		tagBuf, err := r.tagBuffer()
		if err != nil {
			return nil, err
		}
		w.writeTagBuffer(tagBuf)
	}

	// v1+: throttle_time_ms.
	if version >= 1 {
		throttle, err := r.int32()
		if err != nil {
			return nil, err
		}
		w.writeInt32(throttle)
	}

	// Brokers array.
	var brokerCount int
	if flexible {
		n, err := r.uvarint()
		if err != nil {
			return nil, err
		}
		if n == 0 {
			// Compact arrays: 0 means null array.
			w.writeUvarint(0)
			w.writeRaw(r.remaining())
			return w.bytes(), nil
		}
		brokerCount = int(n) - 1
		w.writeUvarint(n)
	} else {
		n, err := r.int32()
		if err != nil {
			return nil, err
		}
		brokerCount = int(n)
		w.writeInt32(n)
	}

	for i := 0; i < brokerCount; i++ {
		// node_id
		nodeID, err := r.int32()
		if err != nil {
			return nil, err
		}
		w.writeInt32(nodeID)

		// host — rewrite to proxy host
		if flexible {
			_, err = r.compactString()
		} else {
			_, err = r.string()
		}
		if err != nil {
			return nil, err
		}
		if flexible {
			w.writeCompactString(proxyHost)
		} else {
			w.writeString(proxyHost)
		}

		// port — rewrite to proxy port
		_, err = r.int32()
		if err != nil {
			return nil, err
		}
		w.writeInt32(proxyPort)

		// rack (v1+): nullable string
		if version >= 1 {
			if flexible {
				rack, err := r.compactNullableString()
				if err != nil {
					return nil, err
				}
				w.writeCompactNullableString(rack)
			} else {
				rack, err := r.nullableString()
				if err != nil {
					return nil, err
				}
				w.writeNullableString(rack)
			}
		}

		// Flexible: trailing tag buffer per broker struct.
		if flexible {
			tagBuf, err := r.tagBuffer()
			if err != nil {
				return nil, err
			}
			w.writeTagBuffer(tagBuf)
		}
	}

	// Copy remaining bytes (cluster_id, controller_id, topics, etc.) verbatim.
	w.writeRaw(r.remaining())

	return w.bytes(), nil
}

// rewriteFindCoordinatorResponse parses a FindCoordinator response payload
// and rewrites the coordinator host and port to point at the proxy.
//
// v0-3: single coordinator response.
// v4+:  batch coordinators response (KIP-699).
func rewriteFindCoordinatorResponse(payload []byte, version int16, proxyHost string, proxyPort int32) ([]byte, error) {
	if version >= 4 {
		return rewriteFindCoordinatorBatch(payload, proxyHost, proxyPort)
	}
	return rewriteFindCoordinatorSingle(payload, version, proxyHost, proxyPort)
}

// rewriteFindCoordinatorSingle handles v0-3 (single coordinator).
//
// Layout:
//
//	correlation_id (4)
//	[v3: response header tag buffer]
//	[v1+: throttle_time_ms (4)]
//	error_code (2)
//	[v1+: error_message (nullable string; compact nullable for v3)]
//	node_id (4)
//	host (string; compact string for v3) → REWRITE
//	port (4) → REWRITE
//	[v3: struct tag buffer]
func rewriteFindCoordinatorSingle(payload []byte, version int16, proxyHost string, proxyPort int32) ([]byte, error) {
	flexible := version >= 3
	r := newKafkaReader(payload)
	w := newKafkaWriter()

	// correlation_id
	cid, err := r.int32()
	if err != nil {
		return nil, err
	}
	w.writeInt32(cid)

	// v3: response header tag buffer
	if flexible {
		tb, err := r.tagBuffer()
		if err != nil {
			return nil, err
		}
		w.writeTagBuffer(tb)
	}

	// v1+: throttle_time_ms
	if version >= 1 {
		throttle, err := r.int32()
		if err != nil {
			return nil, err
		}
		w.writeInt32(throttle)
	}

	// error_code
	ec, err := r.int16()
	if err != nil {
		return nil, err
	}
	w.writeInt16(ec)

	// v1+: error_message (nullable string)
	if version >= 1 {
		if flexible {
			em, err := r.compactNullableString()
			if err != nil {
				return nil, err
			}
			w.writeCompactNullableString(em)
		} else {
			em, err := r.nullableString()
			if err != nil {
				return nil, err
			}
			w.writeNullableString(em)
		}
	}

	// node_id
	nodeID, err := r.int32()
	if err != nil {
		return nil, err
	}
	w.writeInt32(nodeID)

	// host → rewrite
	if flexible {
		_, err = r.compactString()
	} else {
		_, err = r.string()
	}
	if err != nil {
		return nil, err
	}
	if flexible {
		w.writeCompactString(proxyHost)
	} else {
		w.writeString(proxyHost)
	}

	// port → rewrite
	if _, err := r.int32(); err != nil {
		return nil, err
	}
	w.writeInt32(proxyPort)

	// v3: struct tag buffer + remaining
	w.writeRaw(r.remaining())
	return w.bytes(), nil
}

// rewriteFindCoordinatorBatch handles v4+ (batch coordinators, KIP-699).
//
// Layout:
//
//	correlation_id (4)
//	response header tag buffer
//	throttle_time_ms (4)
//	coordinators compact array, each:
//	  key (compact string)
//	  node_id (4)
//	  host (compact string) → REWRITE
//	  port (4) → REWRITE
//	  error_code (2)
//	  error_message (compact nullable string)
//	  struct tag buffer
//	response tag buffer
func rewriteFindCoordinatorBatch(payload []byte, proxyHost string, proxyPort int32) ([]byte, error) {
	r := newKafkaReader(payload)
	w := newKafkaWriter()

	// correlation_id
	cid, err := r.int32()
	if err != nil {
		return nil, err
	}
	w.writeInt32(cid)

	// response header tag buffer
	tb, err := r.tagBuffer()
	if err != nil {
		return nil, err
	}
	w.writeTagBuffer(tb)

	// throttle_time_ms
	throttle, err := r.int32()
	if err != nil {
		return nil, err
	}
	w.writeInt32(throttle)

	// coordinators compact array
	n, err := r.uvarint()
	if err != nil {
		return nil, err
	}
	w.writeUvarint(n)
	if n == 0 {
		// null array
		w.writeRaw(r.remaining())
		return w.bytes(), nil
	}
	count := int(n) - 1

	for i := 0; i < count; i++ {
		// key (compact string)
		key, err := r.compactString()
		if err != nil {
			return nil, err
		}
		w.writeCompactString(key)

		// node_id
		nodeID, err := r.int32()
		if err != nil {
			return nil, err
		}
		w.writeInt32(nodeID)

		// host → rewrite
		if _, err := r.compactString(); err != nil {
			return nil, err
		}
		w.writeCompactString(proxyHost)

		// port → rewrite
		if _, err := r.int32(); err != nil {
			return nil, err
		}
		w.writeInt32(proxyPort)

		// error_code
		ec, err := r.int16()
		if err != nil {
			return nil, err
		}
		w.writeInt16(ec)

		// error_message (compact nullable string)
		em, err := r.compactNullableString()
		if err != nil {
			return nil, err
		}
		w.writeCompactNullableString(em)

		// struct tag buffer
		stb, err := r.tagBuffer()
		if err != nil {
			return nil, err
		}
		w.writeTagBuffer(stb)
	}

	// response tag buffer + remaining
	w.writeRaw(r.remaining())
	return w.bytes(), nil
}

// kafkaReader reads Kafka wire protocol primitives from a byte slice.
type kafkaReader struct {
	buf []byte
	pos int
}

func newKafkaReader(buf []byte) *kafkaReader {
	return &kafkaReader{buf: buf}
}

func (r *kafkaReader) need(n int) error {
	if r.pos+n > len(r.buf) {
		return fmt.Errorf("kafka: short read at offset %d, need %d bytes, have %d", r.pos, n, len(r.buf)-r.pos)
	}
	return nil
}

func (r *kafkaReader) int16() (int16, error) {
	if err := r.need(2); err != nil {
		return 0, err
	}
	v := int16(binary.BigEndian.Uint16(r.buf[r.pos:]))
	r.pos += 2
	return v, nil
}

func (r *kafkaReader) int32() (int32, error) {
	if err := r.need(4); err != nil {
		return 0, err
	}
	v := int32(binary.BigEndian.Uint32(r.buf[r.pos:]))
	r.pos += 4
	return v, nil
}

// string reads a classic Kafka string (int16 length prefix).
func (r *kafkaReader) string() (string, error) {
	length, err := r.int16()
	if err != nil {
		return "", err
	}
	if length < 0 {
		return "", fmt.Errorf("kafka: unexpected null string")
	}
	n := int(length)
	if err := r.need(n); err != nil {
		return "", err
	}
	s := string(r.buf[r.pos : r.pos+n])
	r.pos += n
	return s, nil
}

// nullableString reads a classic nullable Kafka string (int16 length, -1 = null).
func (r *kafkaReader) nullableString() (*string, error) {
	length, err := r.int16()
	if err != nil {
		return nil, err
	}
	if length < 0 {
		return nil, nil
	}
	n := int(length)
	if err := r.need(n); err != nil {
		return nil, err
	}
	s := string(r.buf[r.pos : r.pos+n])
	r.pos += n
	return &s, nil
}

// compactString reads a flexible-version compact string (unsigned varint length+1).
func (r *kafkaReader) compactString() (string, error) {
	length, err := r.uvarint()
	if err != nil {
		return "", err
	}
	if length == 0 {
		return "", fmt.Errorf("kafka: unexpected null compact string")
	}
	n := int(length) - 1
	if err := r.need(n); err != nil {
		return "", err
	}
	s := string(r.buf[r.pos : r.pos+n])
	r.pos += n
	return s, nil
}

// compactNullableString reads a flexible-version compact nullable string.
// 0 = null, else unsigned_varint(len+1).
func (r *kafkaReader) compactNullableString() (*string, error) {
	length, err := r.uvarint()
	if err != nil {
		return nil, err
	}
	if length == 0 {
		return nil, nil
	}
	n := int(length) - 1
	if err := r.need(n); err != nil {
		return nil, err
	}
	s := string(r.buf[r.pos : r.pos+n])
	r.pos += n
	return &s, nil
}

// uvarint reads an unsigned variable-length integer (Kafka's compact encoding).
func (r *kafkaReader) uvarint() (uint64, error) {
	var result uint64
	var shift uint
	for {
		if r.pos >= len(r.buf) {
			return 0, fmt.Errorf("kafka: short read in uvarint")
		}
		b := r.buf[r.pos]
		r.pos++
		result |= uint64(b&0x7F) << shift
		if b&0x80 == 0 {
			return result, nil
		}
		shift += 7
		if shift >= 64 {
			return 0, fmt.Errorf("kafka: uvarint overflow")
		}
	}
}

// tagBuffer reads a Kafka tagged field buffer (unsigned varint count of tags,
// then for each: tag varint + size varint + data bytes).
// Returns the raw bytes for pass-through.
func (r *kafkaReader) tagBuffer() ([]byte, error) {
	startPos := r.pos
	numTags, err := r.uvarint()
	if err != nil {
		return nil, err
	}
	for i := uint64(0); i < numTags; i++ {
		// tag key
		if _, err := r.uvarint(); err != nil {
			return nil, err
		}
		// tag size
		size, err := r.uvarint()
		if err != nil {
			return nil, err
		}
		if err := r.need(int(size)); err != nil {
			return nil, err
		}
		r.pos += int(size)
	}
	return r.buf[startPos:r.pos], nil
}

// remaining returns all unread bytes.
func (r *kafkaReader) remaining() []byte {
	if r.pos >= len(r.buf) {
		return nil
	}
	return r.buf[r.pos:]
}

// kafkaWriter builds Kafka wire protocol byte sequences.
type kafkaWriter struct {
	buf []byte
}

func newKafkaWriter() *kafkaWriter {
	return &kafkaWriter{}
}

func (w *kafkaWriter) writeInt16(v int16) {
	w.buf = binary.BigEndian.AppendUint16(w.buf, uint16(v))
}

func (w *kafkaWriter) writeInt32(v int32) {
	w.buf = binary.BigEndian.AppendUint32(w.buf, uint32(v))
}

// writeString writes a classic Kafka string (int16 length prefix).
func (w *kafkaWriter) writeString(s string) {
	w.writeInt16(int16(len(s)))
	w.buf = append(w.buf, s...)
}

// writeNullableString writes a classic nullable Kafka string.
func (w *kafkaWriter) writeNullableString(s *string) {
	if s == nil {
		w.writeInt16(-1)
		return
	}
	w.writeString(*s)
}

// writeCompactString writes a flexible-version compact string.
func (w *kafkaWriter) writeCompactString(s string) {
	w.writeUvarint(uint64(len(s)) + 1)
	w.buf = append(w.buf, s...)
}

// writeCompactNullableString writes a flexible-version compact nullable string.
func (w *kafkaWriter) writeCompactNullableString(s *string) {
	if s == nil {
		w.writeUvarint(0)
		return
	}
	w.writeCompactString(*s)
}

// writeUvarint writes an unsigned variable-length integer.
func (w *kafkaWriter) writeUvarint(v uint64) {
	var buf [binary.MaxVarintLen64]byte
	n := binary.PutUvarint(buf[:], v)
	w.buf = append(w.buf, buf[:n]...)
}

// writeTagBuffer writes raw tag buffer bytes (already encoded).
func (w *kafkaWriter) writeTagBuffer(raw []byte) {
	w.buf = append(w.buf, raw...)
}

// writeRaw appends raw bytes.
func (w *kafkaWriter) writeRaw(data []byte) {
	w.buf = append(w.buf, data...)
}

func (w *kafkaWriter) bytes() []byte {
	return w.buf
}
