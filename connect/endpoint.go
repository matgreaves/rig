// Package connect defines shared types for rig service endpoints and wiring.
//
// These types are used by the rig SDK (client/) and by connect packages
// (httpx/, etc.). Service runtime code can also use these types directly
// without depending on the full rig SDK.
package connect

import "fmt"

// Protocol identifies the application-layer protocol an endpoint speaks.
type Protocol string

const (
	TCP   Protocol = "tcp"
	HTTP  Protocol = "http"
	GRPC  Protocol = "grpc"
	Kafka Protocol = "kafka"
)

// Endpoint is a resolved service endpoint with connection helpers.
type Endpoint struct {
	Host       string         `json:"host"`
	Port       int            `json:"port"`
	Protocol   Protocol       `json:"protocol"`
	Attributes map[string]any `json:"attributes,omitempty"`
}

// Addr returns "host:port" suitable for net.Dial, grpc.Dial, etc.
func (e Endpoint) Addr() string {
	return fmt.Sprintf("%s:%d", e.Host, e.Port)
}

// Attr returns the value of a named attribute as a string. Returns "" if
// the attribute is not found.
func (e Endpoint) Attr(name string) string {
	v, ok := e.Attributes[name]
	if !ok {
		return ""
	}
	return fmt.Sprintf("%v", v)
}
