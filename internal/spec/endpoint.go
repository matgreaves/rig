package spec

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

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

// Endpoint is a fully resolved, concrete address produced at runtime.
// The spec never contains endpoints — they are created by the server
// during the publish phase when ports are allocated.
//
// Attribute values may contain ${VAR} template references that are
// resolved at the point of consumption (env var emission, environment.up
// event) via ResolveAttributes. Only built-in variables are available:
// HOST, PORT, and HOSTPORT — seeded from the endpoint's address.
//
// Internal wiring between services keeps templates so container/proxy
// address adjustment is just changing ep.Host/ep.Port — no attribute
// rewriting needed.
type Endpoint struct {
	Host       string         `json:"host"`
	Port       int            `json:"port"`
	Protocol   Protocol       `json:"protocol"`
	Attributes map[string]any `json:"attributes,omitempty"`
}

// ResolvedEndpoint is an Endpoint with all attribute templates expanded to
// concrete values. Create via Endpoint.Resolve(). Output boundary functions
// (BuildServiceEnv, buildResolvedEnvironment, dispatchCallback) should accept
// this type to ensure templates are never leaked to consumers.
type ResolvedEndpoint struct {
	Host       string         `json:"host"`
	Port       int            `json:"port"`
	Protocol   Protocol       `json:"protocol"`
	Attributes map[string]any `json:"attributes,omitempty"`
}

// Resolve expands ${VAR} template references in ep.Attributes and returns
// a ResolvedEndpoint with concrete values.
//
// Templates may reference built-in variables only: HOST, PORT, HOSTPORT.
// Returns an error if a template references an unknown variable.
func (ep Endpoint) Resolve() (ResolvedEndpoint, error) {
	re := ResolvedEndpoint{
		Host:     ep.Host,
		Port:     ep.Port,
		Protocol: ep.Protocol,
	}
	if ep.Attributes == nil {
		return re, nil
	}
	attrs, err := ResolveAttributes(ep)
	if err != nil {
		return re, err
	}
	re.Attributes = attrs
	return re, nil
}

// ResolveAttributes expands ${VAR} template references in ep.Attributes
// against the endpoint's built-in variables (HOST, PORT, HOSTPORT).
//
// Returns nil if ep.Attributes is nil. Returns a new map — the original
// ep.Attributes is never mutated.
//
// Returns an error if a template references an unknown variable
// (e.g. ${TYPO} or ${HOOST}).
func ResolveAttributes(ep Endpoint) (map[string]any, error) {
	if ep.Attributes == nil {
		return nil, nil
	}
	if len(ep.Attributes) == 0 {
		return make(map[string]any), nil
	}

	portStr := strconv.Itoa(ep.Port)
	builtins := map[string]string{
		"HOST":     ep.Host,
		"PORT":     portStr,
		"HOSTPORT": ep.Host + ":" + portStr,
	}

	resolved := make(map[string]any, len(ep.Attributes))
	for k, v := range ep.Attributes {
		s, ok := v.(string)
		if !ok {
			resolved[k] = v
			continue
		}
		if !strings.Contains(s, "$") {
			resolved[k] = s
			continue
		}
		var unknownVar string
		expanded := os.Expand(s, func(key string) string {
			if val, ok := builtins[key]; ok {
				return val
			}
			unknownVar = key
			return ""
		})
		if unknownVar != "" {
			return nil, fmt.Errorf(
				"attribute %q template %q references unknown variable ${%s}; "+
					"only HOST, PORT, and HOSTPORT are available",
				k, s, unknownVar,
			)
		}
		resolved[k] = expanded
	}
	return resolved, nil
}
