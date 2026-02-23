package proxy

// Event is the proxy-internal event type emitted by forwarders.
// The lifecycle layer converts these into server.Event entries.
type Event struct {
	Type       string
	Request    *RequestInfo
	Connection *ConnectionInfo
	GRPCCall   *GRPCCallInfo
}

// RequestInfo captures an observed HTTP request/response pair.
type RequestInfo struct {
	Source       string
	Target       string
	Ingress      string
	Method       string
	Path         string
	StatusCode   int
	LatencyMs    float64
	RequestSize  int64
	ResponseSize int64

	RequestHeaders          map[string][]string
	RequestBody             []byte
	RequestBodyTruncated    bool
	ResponseHeaders         map[string][]string
	ResponseBody            []byte
	ResponseBodyTruncated   bool
}

// ConnectionInfo captures an observed TCP connection.
type ConnectionInfo struct {
	Source     string
	Target     string
	Ingress    string
	BytesIn    int64
	BytesOut   int64
	DurationMs float64
}

// GRPCCallInfo captures an observed gRPC call.
type GRPCCallInfo struct {
	Source           string
	Target           string
	Ingress          string
	Service          string              // "pkg.ServiceName"
	Method           string              // "MethodName"
	GRPCStatus       string              // "0" (OK), "5" (NOT_FOUND), etc.
	GRPCMessage      string              // status message
	LatencyMs        float64
	RequestSize      int64
	ResponseSize     int64
	RequestMetadata  map[string][]string
	ResponseMetadata map[string][]string
}
