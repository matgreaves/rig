package spec

// Protocol identifies the application-layer protocol an ingress speaks.
type Protocol string

const (
	TCP  Protocol = "tcp"
	HTTP Protocol = "http"
	GRPC Protocol = "grpc"
)

// ValidProtocols returns the set of recognised protocol values.
func ValidProtocols() []Protocol {
	return []Protocol{TCP, HTTP, GRPC}
}

// Valid reports whether p is a recognised protocol.
func (p Protocol) Valid() bool {
	switch p {
	case TCP, HTTP, GRPC:
		return true
	}
	return false
}

// Endpoint is a fully resolved, concrete address produced at runtime.
// The spec never contains endpoints — they are created by the server
// during the publish phase when ports are allocated.
type Endpoint struct {
	Host       string         `json:"host"`
	Port       int            `json:"port"`
	Protocol   Protocol       `json:"protocol"`
	Attributes map[string]any `json:"attributes,omitempty"`
}
