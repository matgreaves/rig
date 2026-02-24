package proxy

import (
	"bytes"
	"context"
	"encoding/binary"
	"io"
	"net"
	"sync"
	"testing"

	"github.com/matgreaves/rig/internal/spec"
)

// --- kafkaReader / kafkaWriter round-trip tests ---

func TestKafkaReaderWriter_Int16(t *testing.T) {
	w := newKafkaWriter()
	w.writeInt16(42)
	w.writeInt16(-1)

	r := newKafkaReader(w.bytes())
	v1, err := r.int16()
	if err != nil {
		t.Fatal(err)
	}
	if v1 != 42 {
		t.Errorf("got %d, want 42", v1)
	}
	v2, err := r.int16()
	if err != nil {
		t.Fatal(err)
	}
	if v2 != -1 {
		t.Errorf("got %d, want -1", v2)
	}
}

func TestKafkaReaderWriter_Int32(t *testing.T) {
	w := newKafkaWriter()
	w.writeInt32(100_000)
	w.writeInt32(-999)

	r := newKafkaReader(w.bytes())
	v1, err := r.int32()
	if err != nil {
		t.Fatal(err)
	}
	if v1 != 100_000 {
		t.Errorf("got %d, want 100000", v1)
	}
	v2, err := r.int32()
	if err != nil {
		t.Fatal(err)
	}
	if v2 != -999 {
		t.Errorf("got %d, want -999", v2)
	}
}

func TestKafkaReaderWriter_String(t *testing.T) {
	w := newKafkaWriter()
	w.writeString("hello")
	w.writeString("")

	r := newKafkaReader(w.bytes())
	s1, err := r.string()
	if err != nil {
		t.Fatal(err)
	}
	if s1 != "hello" {
		t.Errorf("got %q, want %q", s1, "hello")
	}
	s2, err := r.string()
	if err != nil {
		t.Fatal(err)
	}
	if s2 != "" {
		t.Errorf("got %q, want %q", s2, "")
	}
}

func TestKafkaReaderWriter_NullableString(t *testing.T) {
	w := newKafkaWriter()
	s := "rack-1"
	w.writeNullableString(&s)
	w.writeNullableString(nil)

	r := newKafkaReader(w.bytes())
	v1, err := r.nullableString()
	if err != nil {
		t.Fatal(err)
	}
	if v1 == nil || *v1 != "rack-1" {
		t.Errorf("got %v, want %q", v1, "rack-1")
	}
	v2, err := r.nullableString()
	if err != nil {
		t.Fatal(err)
	}
	if v2 != nil {
		t.Errorf("got %v, want nil", *v2)
	}
}

func TestKafkaReaderWriter_CompactString(t *testing.T) {
	w := newKafkaWriter()
	w.writeCompactString("broker-1")
	w.writeCompactString("")

	r := newKafkaReader(w.bytes())
	s1, err := r.compactString()
	if err != nil {
		t.Fatal(err)
	}
	if s1 != "broker-1" {
		t.Errorf("got %q, want %q", s1, "broker-1")
	}
	s2, err := r.compactString()
	if err != nil {
		t.Fatal(err)
	}
	if s2 != "" {
		t.Errorf("got %q, want %q", s2, "")
	}
}

func TestKafkaReaderWriter_CompactNullableString(t *testing.T) {
	w := newKafkaWriter()
	s := "rack-a"
	w.writeCompactNullableString(&s)
	w.writeCompactNullableString(nil)

	r := newKafkaReader(w.bytes())
	v1, err := r.compactNullableString()
	if err != nil {
		t.Fatal(err)
	}
	if v1 == nil || *v1 != "rack-a" {
		t.Errorf("got %v, want %q", v1, "rack-a")
	}
	v2, err := r.compactNullableString()
	if err != nil {
		t.Fatal(err)
	}
	if v2 != nil {
		t.Errorf("got %v, want nil", *v2)
	}
}

func TestKafkaReaderWriter_Uvarint(t *testing.T) {
	w := newKafkaWriter()
	w.writeUvarint(0)
	w.writeUvarint(127)
	w.writeUvarint(128)
	w.writeUvarint(16384)

	r := newKafkaReader(w.bytes())
	for _, want := range []uint64{0, 127, 128, 16384} {
		got, err := r.uvarint()
		if err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Errorf("got %d, want %d", got, want)
		}
	}
}

func TestKafkaReaderWriter_TagBuffer(t *testing.T) {
	// Build a tag buffer with one tag: key=0, data=[0xAB, 0xCD].
	w := newKafkaWriter()
	w.writeUvarint(1) // 1 tag
	w.writeUvarint(0) // tag key
	w.writeUvarint(2) // tag size
	w.buf = append(w.buf, 0xAB, 0xCD)

	original := make([]byte, len(w.bytes()))
	copy(original, w.bytes())

	r := newKafkaReader(w.bytes())
	tagBuf, err := r.tagBuffer()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(tagBuf, original) {
		t.Errorf("tag buffer round-trip mismatch")
	}
	if len(r.remaining()) != 0 {
		t.Errorf("expected no remaining bytes, got %d", len(r.remaining()))
	}
}

// --- Correlation tracker tests ---

func TestCorrelationTracker(t *testing.T) {
	tracker := newCorrelationTracker()

	tracker.track(1, 3, 5) // Metadata v5
	tracker.track(2, 0, 2) // Produce v2

	info, ok := tracker.lookup(1)
	if !ok {
		t.Fatal("expected correlation ID 1 to be found")
	}
	if info.apiKey != 3 || info.apiVersion != 5 {
		t.Errorf("got apiKey=%d version=%d, want 3/5", info.apiKey, info.apiVersion)
	}

	// Should be consumed — second lookup fails.
	_, ok = tracker.lookup(1)
	if ok {
		t.Error("expected correlation ID 1 to be consumed after first lookup")
	}

	info, ok = tracker.lookup(2)
	if !ok {
		t.Fatal("expected correlation ID 2 to be found")
	}
	if info.apiKey != 0 {
		t.Errorf("got apiKey=%d, want 0", info.apiKey)
	}

	// Unknown ID.
	_, ok = tracker.lookup(99)
	if ok {
		t.Error("expected unknown correlation ID to not be found")
	}
}

// --- Metadata rewrite tests ---

// buildClassicMetadataResponse builds a Metadata v1 (classic encoding) response payload.
func buildClassicMetadataResponse(correlationID int32, throttleMs int32, brokers []testBroker, trailingData []byte) []byte {
	w := newKafkaWriter()
	w.writeInt32(correlationID)
	w.writeInt32(throttleMs)                  // throttle_time_ms (v1+)
	w.writeInt32(int32(len(brokers)))         // broker count
	for _, b := range brokers {
		w.writeInt32(b.nodeID)
		w.writeString(b.host)
		w.writeInt32(b.port)
		w.writeNullableString(b.rack) // rack (v1+)
	}
	w.writeRaw(trailingData)
	return w.bytes()
}

// buildFlexibleMetadataResponse builds a Metadata v9+ (flexible encoding) response payload.
func buildFlexibleMetadataResponse(correlationID int32, throttleMs int32, brokers []testBroker, trailingData []byte) []byte {
	w := newKafkaWriter()
	w.writeInt32(correlationID)
	w.writeUvarint(0) // response header tag buffer (empty)
	w.writeInt32(throttleMs)
	w.writeUvarint(uint64(len(brokers)) + 1) // compact array count+1
	for _, b := range brokers {
		w.writeInt32(b.nodeID)
		w.writeCompactString(b.host)
		w.writeInt32(b.port)
		w.writeCompactNullableString(b.rack)
		w.writeUvarint(0) // per-broker tag buffer (empty)
	}
	w.writeRaw(trailingData)
	return w.bytes()
}

type testBroker struct {
	nodeID int32
	host   string
	port   int32
	rack   *string
}

func strPtr(s string) *string { return &s }

func TestRewriteMetadataResponse_ClassicV0(t *testing.T) {
	// v0: no throttle_time_ms, no rack.
	w := newKafkaWriter()
	w.writeInt32(42)     // correlation_id
	w.writeInt32(1)      // 1 broker
	w.writeInt32(0)      // node_id
	w.writeString("10.0.0.1") // host
	w.writeInt32(9092)   // port
	// trailing: cluster metadata
	trailing := []byte{0xDE, 0xAD}
	w.writeRaw(trailing)

	rewritten, err := rewriteMetadataResponse(w.bytes(), 0, "127.0.0.1", 19092)
	if err != nil {
		t.Fatal(err)
	}

	r := newKafkaReader(rewritten)
	corr, _ := r.int32()
	if corr != 42 {
		t.Errorf("correlation_id = %d, want 42", corr)
	}
	count, _ := r.int32()
	if count != 1 {
		t.Errorf("broker count = %d, want 1", count)
	}
	nodeID, _ := r.int32()
	if nodeID != 0 {
		t.Errorf("node_id = %d, want 0", nodeID)
	}
	host, _ := r.string()
	if host != "127.0.0.1" {
		t.Errorf("host = %q, want 127.0.0.1", host)
	}
	port, _ := r.int32()
	if port != 19092 {
		t.Errorf("port = %d, want 19092", port)
	}
	rem := r.remaining()
	if !bytes.Equal(rem, trailing) {
		t.Errorf("trailing data = %x, want %x", rem, trailing)
	}
}

func TestRewriteMetadataResponse_ClassicV1(t *testing.T) {
	trailing := []byte{0x01, 0x02, 0x03}
	payload := buildClassicMetadataResponse(
		7, 100,
		[]testBroker{
			{nodeID: 1, host: "broker-1.internal", port: 9092, rack: strPtr("us-east-1a")},
			{nodeID: 2, host: "broker-2.internal", port: 9093, rack: nil},
		},
		trailing,
	)

	rewritten, err := rewriteMetadataResponse(payload, 1, "127.0.0.1", 19092)
	if err != nil {
		t.Fatal(err)
	}

	r := newKafkaReader(rewritten)
	corr, _ := r.int32()
	if corr != 7 {
		t.Errorf("correlation_id = %d, want 7", corr)
	}
	throttle, _ := r.int32()
	if throttle != 100 {
		t.Errorf("throttle_time_ms = %d, want 100", throttle)
	}
	count, _ := r.int32()
	if count != 2 {
		t.Errorf("broker count = %d, want 2", count)
	}

	// Broker 1.
	nodeID1, _ := r.int32()
	if nodeID1 != 1 {
		t.Errorf("broker 1 node_id = %d, want 1", nodeID1)
	}
	host1, _ := r.string()
	if host1 != "127.0.0.1" {
		t.Errorf("broker 1 host = %q, want 127.0.0.1", host1)
	}
	port1, _ := r.int32()
	if port1 != 19092 {
		t.Errorf("broker 1 port = %d, want 19092", port1)
	}
	rack1, _ := r.nullableString()
	if rack1 == nil || *rack1 != "us-east-1a" {
		t.Errorf("broker 1 rack = %v, want us-east-1a", rack1)
	}

	// Broker 2.
	nodeID2, _ := r.int32()
	if nodeID2 != 2 {
		t.Errorf("broker 2 node_id = %d, want 2", nodeID2)
	}
	host2, _ := r.string()
	if host2 != "127.0.0.1" {
		t.Errorf("broker 2 host = %q, want 127.0.0.1", host2)
	}
	port2, _ := r.int32()
	if port2 != 19092 {
		t.Errorf("broker 2 port = %d, want 19092", port2)
	}
	rack2, _ := r.nullableString()
	if rack2 != nil {
		t.Errorf("broker 2 rack = %v, want nil", *rack2)
	}

	rem := r.remaining()
	if !bytes.Equal(rem, trailing) {
		t.Errorf("trailing data = %x, want %x", rem, trailing)
	}
}

func TestRewriteMetadataResponse_FlexibleV9(t *testing.T) {
	trailing := []byte{0xCA, 0xFE}
	payload := buildFlexibleMetadataResponse(
		99, 0,
		[]testBroker{
			{nodeID: 0, host: "redpanda-0", port: 9092, rack: strPtr("rack-a")},
		},
		trailing,
	)

	rewritten, err := rewriteMetadataResponse(payload, 9, "127.0.0.1", 29092)
	if err != nil {
		t.Fatal(err)
	}

	r := newKafkaReader(rewritten)
	corr, _ := r.int32()
	if corr != 99 {
		t.Errorf("correlation_id = %d, want 99", corr)
	}
	// Response header tag buffer.
	_, err = r.tagBuffer()
	if err != nil {
		t.Fatal(err)
	}
	throttle, _ := r.int32()
	if throttle != 0 {
		t.Errorf("throttle = %d, want 0", throttle)
	}
	countPlusOne, _ := r.uvarint()
	if countPlusOne != 2 {
		t.Errorf("broker count+1 = %d, want 2", countPlusOne)
	}

	nodeID, _ := r.int32()
	if nodeID != 0 {
		t.Errorf("node_id = %d, want 0", nodeID)
	}
	host, _ := r.compactString()
	if host != "127.0.0.1" {
		t.Errorf("host = %q, want 127.0.0.1", host)
	}
	port, _ := r.int32()
	if port != 29092 {
		t.Errorf("port = %d, want 29092", port)
	}
	rack, _ := r.compactNullableString()
	if rack == nil || *rack != "rack-a" {
		t.Errorf("rack = %v, want rack-a", rack)
	}
	// Per-broker tag buffer.
	_, err = r.tagBuffer()
	if err != nil {
		t.Fatal(err)
	}

	rem := r.remaining()
	if !bytes.Equal(rem, trailing) {
		t.Errorf("trailing data = %x, want %x", rem, trailing)
	}
}

func TestRewriteMetadataResponse_EmptyBrokers(t *testing.T) {
	trailing := []byte{0xFF}

	// Classic v1 with 0 brokers.
	payload := buildClassicMetadataResponse(1, 0, nil, trailing)
	rewritten, err := rewriteMetadataResponse(payload, 1, "127.0.0.1", 19092)
	if err != nil {
		t.Fatal(err)
	}
	r := newKafkaReader(rewritten)
	r.int32() // correlation_id
	r.int32() // throttle
	count, _ := r.int32()
	if count != 0 {
		t.Errorf("broker count = %d, want 0", count)
	}
	if !bytes.Equal(r.remaining(), trailing) {
		t.Error("trailing data mismatch")
	}
}

func TestRewriteMetadataResponse_FlexibleV9_MultipleBrokers(t *testing.T) {
	trailing := []byte{0xBE, 0xEF}
	payload := buildFlexibleMetadataResponse(
		55, 200,
		[]testBroker{
			{nodeID: 0, host: "broker-0.prod", port: 9092, rack: strPtr("az-1")},
			{nodeID: 1, host: "broker-1.prod", port: 9093, rack: nil},
			{nodeID: 2, host: "broker-2.prod", port: 9094, rack: strPtr("az-3")},
		},
		trailing,
	)

	rewritten, err := rewriteMetadataResponse(payload, 9, "127.0.0.1", 29092)
	if err != nil {
		t.Fatal(err)
	}

	r := newKafkaReader(rewritten)
	corr, _ := r.int32()
	if corr != 55 {
		t.Errorf("correlation_id = %d, want 55", corr)
	}
	r.tagBuffer() // response header tag buffer
	throttle, _ := r.int32()
	if throttle != 200 {
		t.Errorf("throttle = %d, want 200", throttle)
	}
	countPlusOne, _ := r.uvarint()
	if countPlusOne != 4 { // 3 brokers + 1
		t.Errorf("broker count+1 = %d, want 4", countPlusOne)
	}

	for i, wantRack := range []*string{strPtr("az-1"), nil, strPtr("az-3")} {
		nodeID, _ := r.int32()
		if nodeID != int32(i) {
			t.Errorf("broker %d: node_id = %d, want %d", i, nodeID, i)
		}
		host, _ := r.compactString()
		if host != "127.0.0.1" {
			t.Errorf("broker %d: host = %q, want 127.0.0.1", i, host)
		}
		port, _ := r.int32()
		if port != 29092 {
			t.Errorf("broker %d: port = %d, want 29092", i, port)
		}
		rack, _ := r.compactNullableString()
		if wantRack == nil {
			if rack != nil {
				t.Errorf("broker %d: rack = %q, want nil", i, *rack)
			}
		} else {
			if rack == nil || *rack != *wantRack {
				t.Errorf("broker %d: rack = %v, want %q", i, rack, *wantRack)
			}
		}
		r.tagBuffer() // per-broker tag buffer
	}

	rem := r.remaining()
	if !bytes.Equal(rem, trailing) {
		t.Errorf("trailing data = %x, want %x", rem, trailing)
	}
}

func TestRewriteMetadataResponse_FlexibleV9_NonEmptyTagBuffers(t *testing.T) {
	// Build a flexible response with non-empty tag buffers to exercise
	// tag buffer pass-through in the rewrite path.
	w := newKafkaWriter()
	w.writeInt32(77)    // correlation_id

	// Response header tag buffer: 1 tag with key=0, data=[0x01, 0x02, 0x03].
	w.writeUvarint(1)   // 1 tag
	w.writeUvarint(0)   // tag key
	w.writeUvarint(3)   // tag size
	w.buf = append(w.buf, 0x01, 0x02, 0x03)

	w.writeInt32(0)     // throttle_time_ms
	w.writeUvarint(2)   // 1 broker (count+1)

	// Broker with non-empty per-broker tag buffer.
	w.writeInt32(0)                    // node_id
	w.writeCompactString("real-host")  // host
	w.writeInt32(9092)                 // port
	w.writeCompactNullableString(nil)  // rack

	// Per-broker tag buffer: 1 tag with key=5, data=[0xAA].
	w.writeUvarint(1)   // 1 tag
	w.writeUvarint(5)   // tag key
	w.writeUvarint(1)   // tag size
	w.buf = append(w.buf, 0xAA)

	trailing := []byte{0xDD}
	w.writeRaw(trailing)

	rewritten, err := rewriteMetadataResponse(w.bytes(), 9, "127.0.0.1", 19092)
	if err != nil {
		t.Fatal(err)
	}

	r := newKafkaReader(rewritten)
	corr, _ := r.int32()
	if corr != 77 {
		t.Errorf("correlation_id = %d, want 77", corr)
	}

	// Verify response header tag buffer was preserved.
	respTagBuf, _ := r.tagBuffer()
	// Should be: varint(1) + varint(0) + varint(3) + [0x01, 0x02, 0x03]
	if len(respTagBuf) != 6 {
		t.Errorf("response header tag buffer length = %d, want 6", len(respTagBuf))
	}

	r.int32() // throttle
	countPlusOne, _ := r.uvarint()
	if countPlusOne != 2 {
		t.Errorf("broker count+1 = %d, want 2", countPlusOne)
	}

	r.int32() // node_id
	host, _ := r.compactString()
	if host != "127.0.0.1" {
		t.Errorf("host = %q, want 127.0.0.1", host)
	}
	port, _ := r.int32()
	if port != 19092 {
		t.Errorf("port = %d, want 19092", port)
	}
	r.compactNullableString() // rack

	// Verify per-broker tag buffer was preserved.
	brokerTagBuf, _ := r.tagBuffer()
	// Should be: varint(1) + varint(5) + varint(1) + [0xAA]
	if len(brokerTagBuf) != 4 {
		t.Errorf("per-broker tag buffer length = %d, want 4", len(brokerTagBuf))
	}

	rem := r.remaining()
	if !bytes.Equal(rem, trailing) {
		t.Errorf("trailing data = %x, want %x", rem, trailing)
	}
}

func TestRewriteMetadataResponse_TruncatedPayload(t *testing.T) {
	// A truncated payload should return an error from rewriteMetadataResponse.
	// The relay layer should forward the original frame unchanged.
	truncated := []byte{0x00, 0x00, 0x00, 0x01} // just a correlation_id, then nothing

	_, err := rewriteMetadataResponse(truncated, 1, "127.0.0.1", 19092)
	if err == nil {
		t.Error("expected error from truncated metadata payload")
	}
}

func TestRelayKafkaResponses_RewriteFailureFallback(t *testing.T) {
	tracker := newCorrelationTracker()
	tracker.track(1, kafkaAPIKeyMetadata, 1) // Metadata v1

	// Build a frame with a truncated metadata payload — correlation_id
	// present but no broker data.
	truncated := make([]byte, 4)
	binary.BigEndian.PutUint32(truncated, 1) // correlation_id=1

	var src bytes.Buffer
	hdr := make([]byte, 4)
	binary.BigEndian.PutUint32(hdr, uint32(len(truncated)))
	src.Write(hdr)
	src.Write(truncated)

	var dst bytes.Buffer
	total := relayKafkaResponses(&src, &dst, tracker, "127.0.0.1", 19092)

	if total == 0 {
		t.Fatal("expected non-zero bytes forwarded (fallback to original)")
	}

	// Verify the original frame was forwarded unchanged.
	outBytes := dst.Bytes()
	outFrameLen := binary.BigEndian.Uint32(outBytes[:4])
	if int(outFrameLen) != len(truncated) {
		t.Errorf("forwarded frame length = %d, want %d", outFrameLen, len(truncated))
	}
	if !bytes.Equal(outBytes[4:4+outFrameLen], truncated) {
		t.Error("fallback did not preserve original payload")
	}
}

// --- Request/response relay tests ---

func TestRelayKafkaRequests(t *testing.T) {
	tracker := newCorrelationTracker()

	// Build two request frames: a Metadata request and a Produce request.
	var buf bytes.Buffer

	// Frame 1: api_key=3 (Metadata), version=5, correlation_id=10
	writeRequestFrame(&buf, 3, 5, 10, []byte("metadata-body"))

	// Frame 2: api_key=0 (Produce), version=2, correlation_id=11
	writeRequestFrame(&buf, 0, 2, 11, []byte("produce-body"))

	input := make([]byte, buf.Len())
	copy(input, buf.Bytes())

	var dst bytes.Buffer
	total := relayKafkaRequests(&buf, &dst, tracker)

	if total == 0 {
		t.Fatal("expected non-zero bytes forwarded")
	}

	// Verify tracker has both entries.
	info1, ok := tracker.lookup(10)
	if !ok {
		t.Fatal("expected correlation ID 10 to be tracked")
	}
	if info1.apiKey != 3 || info1.apiVersion != 5 {
		t.Errorf("correlation 10: apiKey=%d version=%d, want 3/5", info1.apiKey, info1.apiVersion)
	}

	info2, ok := tracker.lookup(11)
	if !ok {
		t.Fatal("expected correlation ID 11 to be tracked")
	}
	if info2.apiKey != 0 || info2.apiVersion != 2 {
		t.Errorf("correlation 11: apiKey=%d version=%d, want 0/2", info2.apiKey, info2.apiVersion)
	}

	// Verify output is byte-for-byte identical to input (requests forwarded unchanged).
	if !bytes.Equal(dst.Bytes(), input) {
		t.Error("forwarded request data does not match input")
	}
}

func TestRelayKafkaResponses_PassThrough(t *testing.T) {
	tracker := newCorrelationTracker()
	tracker.track(1, 0, 2) // Produce, not Metadata

	var src bytes.Buffer
	// Response: correlation_id=1, some body.
	writeResponseFrame(&src, 1, []byte("produce-response"))

	input := make([]byte, src.Len())
	copy(input, src.Bytes())

	var dst bytes.Buffer
	total := relayKafkaResponses(&src, &dst, tracker, "127.0.0.1", 19092)

	if total == 0 {
		t.Fatal("expected non-zero bytes forwarded")
	}

	// Verify output is byte-for-byte identical to input.
	if !bytes.Equal(dst.Bytes(), input) {
		t.Error("pass-through response data does not match input")
	}
}

func TestRelayKafkaResponses_UnknownCorrelation(t *testing.T) {
	// Response with a correlation ID that was never tracked — should pass through.
	tracker := newCorrelationTracker()

	var src bytes.Buffer
	writeResponseFrame(&src, 999, []byte("unknown-response"))

	input := make([]byte, src.Len())
	copy(input, src.Bytes())

	var dst bytes.Buffer
	total := relayKafkaResponses(&src, &dst, tracker, "127.0.0.1", 19092)

	if total == 0 {
		t.Fatal("expected non-zero bytes forwarded")
	}
	if !bytes.Equal(dst.Bytes(), input) {
		t.Error("unknown correlation response should pass through unchanged")
	}
}

func TestRelayKafkaResponses_MetadataRewrite(t *testing.T) {
	tracker := newCorrelationTracker()
	tracker.track(42, kafkaAPIKeyMetadata, 1) // Metadata v1

	// Build a Metadata v1 response with one broker at 10.0.0.5:9092.
	payload := buildClassicMetadataResponse(42, 0,
		[]testBroker{{nodeID: 0, host: "10.0.0.5", port: 9092, rack: nil}},
		[]byte{0x00}, // minimal trailing data
	)

	var src bytes.Buffer
	hdr := make([]byte, 4)
	binary.BigEndian.PutUint32(hdr, uint32(len(payload)))
	src.Write(hdr)
	src.Write(payload)

	var dst bytes.Buffer
	relayKafkaResponses(&src, &dst, tracker, "127.0.0.1", 19092)

	// Parse the output frame.
	outBytes := dst.Bytes()
	if len(outBytes) < 4 {
		t.Fatal("output too short")
	}
	outFrameLen := binary.BigEndian.Uint32(outBytes[:4])
	outPayload := outBytes[4 : 4+outFrameLen]

	r := newKafkaReader(outPayload)
	corr, _ := r.int32()
	if corr != 42 {
		t.Errorf("correlation_id = %d, want 42", corr)
	}
	r.int32() // throttle
	count, _ := r.int32()
	if count != 1 {
		t.Errorf("broker count = %d, want 1", count)
	}
	r.int32() // node_id
	host, _ := r.string()
	if host != "127.0.0.1" {
		t.Errorf("host = %q, want 127.0.0.1", host)
	}
	port, _ := r.int32()
	if port != 19092 {
		t.Errorf("port = %d, want 19092", port)
	}
}

// --- Integration-style test: client ↔ proxy ↔ broker ---

func TestKafkaProxy_EndToEnd(t *testing.T) {
	// Set up a fake broker that responds to Metadata requests.
	brokerLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer brokerLn.Close()
	brokerAddr := brokerLn.Addr().(*net.TCPAddr)

	// Set up the proxy listener.
	proxyLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	proxyAddr := proxyLn.Addr().(*net.TCPAddr)

	var events []Event
	var eventMu sync.Mutex

	f := &Forwarder{
		ListenPort: proxyAddr.Port,
		Target: spec.Endpoint{Host: "127.0.0.1", Port: brokerAddr.Port, Protocol: spec.Kafka},
		Source:     "test-client",
		TargetSvc:  "kafka",
		Ingress:    "default",
		Protocol:   "kafka",
		Emit: func(e Event) {
			eventMu.Lock()
			events = append(events, e)
			eventMu.Unlock()
		},
		Listener: proxyLn,
	}

	// Run fake broker.
	var brokerWg sync.WaitGroup
	brokerWg.Add(1)
	go func() {
		defer brokerWg.Done()
		conn, err := brokerLn.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		serveFakeBroker(t, conn, brokerAddr.Port)
	}()

	// Start proxy in background.
	proxyDone := make(chan error, 1)
	proxyCtx, proxyCancel := testContext()
	defer proxyCancel()
	go func() {
		proxyDone <- f.runKafka(proxyCtx)
	}()

	// Client: connect to proxy, send Metadata request, read response.
	client, err := net.Dial("tcp", proxyAddr.String())
	if err != nil {
		t.Fatal(err)
	}

	// Send Metadata v1 request.
	var reqBuf bytes.Buffer
	writeRequestFrame(&reqBuf, kafkaAPIKeyMetadata, 1, 1, nil)
	client.Write(reqBuf.Bytes())

	// Read response.
	respHdr := make([]byte, 4)
	if _, err := io.ReadFull(client, respHdr); err != nil {
		t.Fatal(err)
	}
	frameLen := binary.BigEndian.Uint32(respHdr)
	respPayload := make([]byte, frameLen)
	if _, err := io.ReadFull(client, respPayload); err != nil {
		t.Fatal(err)
	}
	client.Close()

	// Parse the Metadata response — broker address should be the proxy, not the real broker.
	r := newKafkaReader(respPayload)
	r.int32() // correlation_id
	r.int32() // throttle
	count, _ := r.int32()
	if count != 1 {
		t.Fatalf("broker count = %d, want 1", count)
	}
	r.int32() // node_id
	host, _ := r.string()
	if host != "127.0.0.1" {
		t.Errorf("broker host = %q, want 127.0.0.1", host)
	}
	port, _ := r.int32()
	if int(port) != proxyAddr.Port {
		t.Errorf("broker port = %d, want %d", port, proxyAddr.Port)
	}

	// Wait for broker and proxy to finish.
	brokerLn.Close()
	brokerWg.Wait()
	proxyCancel()
	<-proxyDone

	// Verify connection.opened event was emitted. The connection.closed event
	// may race with context cancellation so we only assert on opened.
	eventMu.Lock()
	defer eventMu.Unlock()
	if len(events) < 1 {
		t.Fatal("expected at least 1 event (connection.opened)")
	}
	if events[0].Type != "connection.opened" {
		t.Errorf("event[0] = %q, want connection.opened", events[0].Type)
	}
}

// --- Test helpers ---

func writeRequestFrame(w io.Writer, apiKey, apiVersion int16, correlationID int32, extraBody []byte) {
	kw := newKafkaWriter()
	kw.writeInt16(apiKey)
	kw.writeInt16(apiVersion)
	kw.writeInt32(correlationID)
	kw.writeRaw(extraBody)

	hdr := make([]byte, 4)
	binary.BigEndian.PutUint32(hdr, uint32(len(kw.bytes())))
	w.Write(hdr)
	w.Write(kw.bytes())
}

func writeResponseFrame(w io.Writer, correlationID int32, body []byte) {
	kw := newKafkaWriter()
	kw.writeInt32(correlationID)
	kw.writeRaw(body)

	hdr := make([]byte, 4)
	binary.BigEndian.PutUint32(hdr, uint32(len(kw.bytes())))
	w.Write(hdr)
	w.Write(kw.bytes())
}

// serveFakeBroker reads one Kafka request and responds with a Metadata v1
// response containing the broker's own address.
func serveFakeBroker(t *testing.T, conn net.Conn, brokerPort int) {
	t.Helper()

	// Read request frame.
	hdr := make([]byte, 4)
	if _, err := io.ReadFull(conn, hdr); err != nil {
		return
	}
	frameLen := binary.BigEndian.Uint32(hdr)
	payload := make([]byte, frameLen)
	if _, err := io.ReadFull(conn, payload); err != nil {
		return
	}

	// Parse correlation_id from request.
	correlationID := int32(binary.BigEndian.Uint32(payload[4:8]))

	// Build Metadata v1 response with real broker address.
	resp := buildClassicMetadataResponse(
		correlationID, 0,
		[]testBroker{{nodeID: 0, host: "10.0.0.5", port: int32(brokerPort), rack: nil}},
		[]byte{0x00}, // minimal trailing data
	)

	respHdr := make([]byte, 4)
	binary.BigEndian.PutUint32(respHdr, uint32(len(resp)))
	conn.Write(respHdr)
	conn.Write(resp)
}

func testContext() (context.Context, context.CancelFunc) {
	return context.WithCancel(context.Background())
}
