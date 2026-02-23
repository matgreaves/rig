// Package temporalx provides a Temporal client built on rig endpoints.
//
// In tests, construct from a resolved environment endpoint:
//
//	c, err := temporalx.Dial(env.Endpoint("temporal"))
//	defer c.Close()
//
// In service code, construct from parsed wiring:
//
//	w, _ := connect.ParseWiring(ctx)
//	c, err := temporalx.Dial(w.Egress("temporal"))
package temporalx

import (
	"github.com/matgreaves/rig/connect"
	"go.temporal.io/sdk/client"
)

// Addr extracts the TEMPORAL_ADDRESS attribute from the endpoint.
func Addr(ep connect.Endpoint) string {
	return ep.Attr("TEMPORAL_ADDRESS")
}

// Namespace extracts the TEMPORAL_NAMESPACE attribute from the endpoint.
func Namespace(ep connect.Endpoint) string {
	return ep.Attr("TEMPORAL_NAMESPACE")
}

// Dial creates a Temporal client from a rig endpoint.
// It reads TEMPORAL_ADDRESS and TEMPORAL_NAMESPACE from the endpoint attributes.
// An optional client.Options can be provided to override defaults such as
// Logger or Identity; HostPort and Namespace are always set from the endpoint.
func Dial(ep connect.Endpoint, opts ...client.Options) (client.Client, error) {
	var o client.Options
	if len(opts) > 0 {
		o = opts[0]
	}
	o.HostPort = Addr(ep)
	o.Namespace = Namespace(ep)
	return client.Dial(o)
}
