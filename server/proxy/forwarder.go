package proxy

import (
	"context"
	"fmt"

	"github.com/matgreaves/rig/spec"
	"github.com/matgreaves/run"
)

// Forwarder observes traffic for a single egress edge or external connection.
// It listens on a single port and forwards to the real service endpoint,
// emitting events for each request or connection.
type Forwarder struct {
	ListenPort int
	Target     spec.Endpoint // real service endpoint to forward to
	Source     string        // source service name or "external"
	TargetSvc  string        // target service name
	Ingress    string        // target ingress name
	Protocol   string        // from spec: "http", "tcp", etc.
	Emit       func(Event)   // publish to event log
}

// Endpoint returns the proxy endpoint that callers should connect to.
func (f *Forwarder) Endpoint() spec.Endpoint {
	return spec.Endpoint{
		Host:       "127.0.0.1",
		Port:       f.ListenPort,
		Protocol:   f.Target.Protocol,
		Attributes: f.Target.Attributes,
	}
}

// Runner returns a run.Runner that listens and forwards traffic.
// Dispatches to HTTP reverse proxy or TCP relay based on Protocol.
func (f *Forwarder) Runner() run.Runner {
	return run.Func(func(ctx context.Context) error {
		switch f.Protocol {
		case "http":
			return f.runHTTP(ctx)
		default:
			// TCP relay for tcp, grpc, and anything else.
			return f.runTCP(ctx)
		}
	})
}

// targetAddr returns host:port of the real service.
func (f *Forwarder) targetAddr() string {
	return fmt.Sprintf("%s:%d", f.Target.Host, f.Target.Port)
}

// listenAddr returns the proxy listen address.
func (f *Forwarder) listenAddr() string {
	return fmt.Sprintf("127.0.0.1:%d", f.ListenPort)
}
