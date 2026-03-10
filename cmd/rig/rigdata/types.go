package rigdata

import (
	"encoding/json"
	"time"
)

// Event type constants for traffic display.
const (
	TypeRequestCompleted      = "request.completed"
	TypeConnectionClosed      = "connection.closed"
	TypeGRPCCallCompleted     = "grpc.call.completed"
	TypeKafkaRequestCompleted = "kafka.request.completed"
)

// Event type constants for log display.
const (
	TypeServiceLog = "service.log"
	TypeTestNote   = "test.note"
)

// Event is the top-level JSONL event structure. Only traffic-relevant fields
// are included; lifecycle events are silently skipped.
type Event struct {
	Seq          uint64            `json:"seq"`
	Type         string            `json:"type"`
	Timestamp    time.Time         `json:"timestamp"`
	Request      *RequestInfo      `json:"request,omitempty"`
	Connection   *ConnectionInfo   `json:"connection,omitempty"`
	GRPCCall     *GRPCCallInfo     `json:"grpc_call,omitempty"`
	KafkaRequest *KafkaRequestInfo `json:"kafka_request,omitempty"`
}

// RequestInfo holds HTTP request/response metadata.
type RequestInfo struct {
	Source                string              `json:"source"`
	Target                string              `json:"target"`
	Ingress               string              `json:"ingress"`
	Method                string              `json:"method"`
	Path                  string              `json:"path"`
	StatusCode            int                 `json:"status_code"`
	LatencyMs             float64             `json:"latency_ms"`
	RequestSize           int64               `json:"request_size"`
	ResponseSize          int64               `json:"response_size"`
	RequestHeaders        map[string][]string `json:"request_headers,omitempty"`
	RequestBody           []byte              `json:"request_body,omitempty"`
	RequestBodyTruncated  bool                `json:"request_body_truncated,omitempty"`
	ResponseHeaders       map[string][]string `json:"response_headers,omitempty"`
	ResponseBody          []byte              `json:"response_body,omitempty"`
	ResponseBodyTruncated bool                `json:"response_body_truncated,omitempty"`
}

// ConnectionInfo holds TCP connection metadata.
type ConnectionInfo struct {
	Source     string  `json:"source"`
	Target     string  `json:"target"`
	Ingress    string  `json:"ingress"`
	BytesIn    int64   `json:"bytes_in"`
	BytesOut   int64   `json:"bytes_out"`
	DurationMs float64 `json:"duration_ms"`
}

// GRPCCallInfo holds gRPC call metadata.
type GRPCCallInfo struct {
	Source                string              `json:"source"`
	Target                string              `json:"target"`
	Ingress               string              `json:"ingress"`
	Service               string              `json:"service"`
	Method                string              `json:"method"`
	GRPCStatus            string              `json:"grpc_status"`
	GRPCMessage           string              `json:"grpc_message"`
	LatencyMs             float64             `json:"latency_ms"`
	RequestSize           int64               `json:"request_size"`
	ResponseSize          int64               `json:"response_size"`
	RequestMetadata       map[string][]string `json:"request_metadata,omitempty"`
	ResponseMetadata      map[string][]string `json:"response_metadata,omitempty"`
	RequestBody           []byte              `json:"request_body,omitempty"`
	RequestBodyTruncated  bool                `json:"request_body_truncated,omitempty"`
	ResponseBody          []byte              `json:"response_body,omitempty"`
	ResponseBodyTruncated bool                `json:"response_body_truncated,omitempty"`
	RequestBodyDecoded    json.RawMessage     `json:"request_body_decoded,omitempty"`
	ResponseBodyDecoded   json.RawMessage     `json:"response_body_decoded,omitempty"`
}

// KafkaRequestInfo holds Kafka request metadata.
type KafkaRequestInfo struct {
	Source        string  `json:"source"`
	Target        string  `json:"target"`
	Ingress       string  `json:"ingress"`
	APIKey        int16   `json:"api_key"`
	APIName       string  `json:"api_name"`
	APIVersion    int16   `json:"api_version"`
	CorrelationID int32   `json:"correlation_id"`
	LatencyMs     float64 `json:"latency_ms"`
	RequestSize   int64   `json:"request_size"`
	ResponseSize  int64   `json:"response_size"`
}

// TrafficRow is a normalized row ready for display.
type TrafficRow struct {
	Index    int
	Time     string // relative to first event
	Source   string
	Target   string
	Protocol string // "HTTP", "gRPC", "TCP", "Kafka"
	Method   string
	Path     string // path for HTTP, service/method for gRPC, "—" for TCP
	Status   string
	Latency  string
	Extra    string // e.g. byte counts for TCP

	// Original event, kept for detail rendering.
	Event Event
}

// TrafficFilter defines filter criteria for traffic rows.
type TrafficFilter struct {
	Edge     string
	SlowMs   float64
	Status   string
	Protocol string // "http", "grpc", "tcp", "kafka", or ""
}

// LogEntry holds a single log line with stream info.
type LogEntry struct {
	Stream string `json:"stream"` // "stdout" or "stderr"
	Data   string `json:"data"`
}

// LogEvent is the subset of a JSONL event needed for log display.
type LogEvent struct {
	Seq       uint64    `json:"seq"`
	Type      string    `json:"type"`
	Service   string    `json:"service"`
	Log       *LogEntry `json:"log,omitempty"`
	Error     string    `json:"error,omitempty"` // test.note assertion message
	Timestamp time.Time `json:"timestamp"`
}

// LogRow is a parsed log line ready for display.
type LogRow struct {
	Time    string
	Service string
	Stream  string // "stdout", "stderr", or "note"
	Data    string
}

// LsHeader mirrors the log.header struct written by the server.
type LsHeader struct {
	Type        string    `json:"type"`
	Environment string    `json:"environment"`
	Outcome     string    `json:"outcome"`
	Services    []string  `json:"services"`
	DurationMs  float64   `json:"duration_ms"`
	Timestamp   time.Time `json:"timestamp"`
}

// LsEntry is a parsed log file summary ready for display.
type LsEntry struct {
	Path   string
	Header LsHeader
}

// PsEntry is an environment list entry from the server API.
type PsEntry struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	TTL          string   `json:"ttl,omitempty"`
	RemainingTTL string   `json:"remaining_ttl"`
	Services     []string `json:"services"`
}

// ResolvedEnv is a fully resolved environment from the server API.
type ResolvedEnv struct {
	ID       string                 `json:"id"`
	Name     string                 `json:"name"`
	Services map[string]ResolvedSvc `json:"services"`
}

// ResolvedSvc is a resolved service with its ingresses.
type ResolvedSvc struct {
	Ingresses map[string]ResolvedEP `json:"ingresses"`
	Status    string                `json:"status"`
}

// ResolvedEP is a resolved endpoint with hostport, protocol, and attributes.
type ResolvedEP struct {
	HostPort   string         `json:"hostport"`
	Protocol   string         `json:"protocol"`
	Attributes map[string]any `json:"attributes,omitempty"`
}
