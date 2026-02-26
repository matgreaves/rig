package proxy

import (
	"context"
	"net"

	"github.com/matgreaves/rig/internal/spec"
	"github.com/matgreaves/run"
)

// Forwarder observes traffic for a single egress edge or external connection.
// It listens on a single port and forwards to the real service endpoint,
// emitting events for each request or connection.
type Forwarder struct {
	ListenAddr string
	Target     spec.Endpoint // real service endpoint to forward to
	Source     string        // source service name or "external"
	TargetSvc  string        // target service name
	Ingress    string        // target ingress name
	Protocol   string        // from spec: "http", "tcp", etc.
	Emit       func(Event)   // publish to event log
	Decoder    *GRPCDecoder  // set once before traffic flows; nil if reflection unavailable
	Listener   net.Listener // pre-opened listener; avoids TOCTOU race when set
}

// Endpoint returns the proxy endpoint that callers should connect to.
// Attributes are copied unchanged — they contain templates (e.g. "${HOST}")
// that resolve correctly against the proxy's HostPort when consumed.
func (f *Forwarder) Endpoint() spec.Endpoint {
	// Shallow copy attributes so mutations downstream don't corrupt the target.
	var attrs map[string]any
	if f.Target.Attributes != nil {
		attrs = make(map[string]any, len(f.Target.Attributes))
		for k, v := range f.Target.Attributes {
			attrs[k] = v
		}
	}
	return spec.Endpoint{
		HostPort:   f.ListenAddr,
		Protocol:   f.Target.Protocol,
		Attributes: attrs,
	}
}

// Runner returns a run.Runner that listens and forwards traffic.
// Dispatches to HTTP reverse proxy or TCP relay based on Protocol.
func (f *Forwarder) Runner() run.Runner {
	return run.Func(func(ctx context.Context) error {
		switch f.Protocol {
		case "http":
			return f.runHTTP(ctx)
		case "grpc":
			return f.runGRPC(ctx)
		case "kafka":
			return f.runKafka(ctx)
		default:
			// TCP relay for tcp and anything else.
			return f.runTCP(ctx)
		}
	})
}

// getListener returns the pre-opened listener if set, otherwise opens a new one.
func (f *Forwarder) getListener() (net.Listener, error) {
	if f.Listener != nil {
		return f.Listener, nil
	}
	return net.Listen("tcp", f.ListenAddr)
}
