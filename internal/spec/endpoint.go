package spec

import "strconv"

// Protocol identifies the application-layer protocol an ingress speaks.
type Protocol string

const (
	TCP   Protocol = "tcp"
	HTTP  Protocol = "http"
	GRPC  Protocol = "grpc"
	Kafka Protocol = "kafka"
)

// ValidProtocols returns the set of recognised protocol values.
func ValidProtocols() []Protocol {
	return []Protocol{TCP, HTTP, GRPC, Kafka}
}

// Valid reports whether p is a recognised protocol.
func (p Protocol) Valid() bool {
	switch p {
	case TCP, HTTP, GRPC, Kafka:
		return true
	}
	return false
}

// AddrAttr declares how an endpoint attribute derives from the address.
type AddrAttr string

const (
	AttrHost     AddrAttr = "host"     // value = ep.Host
	AttrPort     AddrAttr = "port"     // value = strconv.Itoa(ep.Port)
	AttrHostPort AddrAttr = "hostport" // value = ep.Host + ":" + strconv.Itoa(ep.Port)
)

// Endpoint is a fully resolved, concrete address produced at runtime.
// The spec never contains endpoints — they are created by the server
// during the publish phase when ports are allocated.
type Endpoint struct {
	Host         string              `json:"host"`
	Port         int                 `json:"port"`
	Protocol     Protocol            `json:"protocol"`
	Attributes   map[string]any      `json:"attributes,omitempty"`
	AddressAttrs map[string]AddrAttr `json:"address_attrs,omitempty"`
}

// RewriteAddressAttrs returns a copy of source.Attributes with entries
// declared in source.AddressAttrs rewritten to reflect newHost and newPort.
// Non-declared attributes are copied unchanged.
func RewriteAddressAttrs(source Endpoint, newHost string, newPort int) map[string]any {
	if len(source.Attributes) == 0 {
		return source.Attributes
	}
	attrs := make(map[string]any, len(source.Attributes))
	for k, v := range source.Attributes {
		attrs[k] = v
	}
	for key, kind := range source.AddressAttrs {
		switch kind {
		case AttrHost:
			attrs[key] = newHost
		case AttrPort:
			attrs[key] = strconv.Itoa(newPort)
		case AttrHostPort:
			attrs[key] = newHost + ":" + strconv.Itoa(newPort)
		}
	}
	return attrs
}
