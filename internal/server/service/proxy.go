package service

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/matgreaves/rig/internal/server/proxy"
	"github.com/matgreaves/rig/internal/spec"
	"github.com/matgreaves/run"
)

// ProxyConfig is the type-specific config for a proxy service node.
// Stored in spec.Service.Config as JSON.
type ProxyConfig struct {
	Source        string `json:"source"`                    // consuming service name or "~test"
	TargetSvc     string `json:"target_svc"`                // real target service name
	Ingress       string `json:"ingress"`                   // real target ingress name
	ReflectionKey string `json:"reflection_key,omitempty"`  // cache key for gRPC reflection descriptors
}

// Proxy implements service.Type for transparent traffic proxy nodes.
// These are injected by the spec transformation and are not user-facing.
// Holds an in-memory reflection cache shared across all proxy instances
// within a single rigd process, avoiding redundant reflection probes
// for the same gRPC service across test runs.
type Proxy struct {
	mu          sync.Mutex
	reflections map[string]*proxy.GRPCDecoder // keyed by ReflectionKey
}

// NewProxy creates a Proxy with an initialized reflection cache.
func NewProxy() *Proxy {
	return &Proxy{reflections: make(map[string]*proxy.GRPCDecoder)}
}

// cachedReflection returns a cached decoder or nil.
func (p *Proxy) cachedReflection(key string) *proxy.GRPCDecoder {
	if key == "" {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.reflections[key]
}

// cacheReflection stores a decoder in the cache.
func (p *Proxy) cacheReflection(key string, dec *proxy.GRPCDecoder) {
	if key == "" || dec == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.reflections[key] = dec
}

// Publish resolves the proxy's ingress endpoint by copying the target's
// protocol and attributes from the resolved "target" egress, then
// binding to the allocated port.
func (p *Proxy) Publish(_ context.Context, params PublishParams) (map[string]spec.Endpoint, error) {
	target, ok := params.Egresses["target"]
	if !ok {
		return nil, fmt.Errorf("proxy: no resolved egress \"target\"")
	}

	port, ok := params.Ports["default"]
	if !ok {
		return nil, fmt.Errorf("proxy: no port allocated for ingress \"default\"")
	}

	// Copy target's attributes so address-derived templates (e.g.
	// ${HOSTPORT}) resolve against the proxy's own address.
	var attrs map[string]any
	if target.Attributes != nil {
		attrs = make(map[string]any, len(target.Attributes))
		for k, v := range target.Attributes {
			attrs[k] = v
		}
	}

	return map[string]spec.Endpoint{
		"default": {
			HostPort:   fmt.Sprintf("127.0.0.1:%d", port),
			Protocol:   target.Protocol,
			Attributes: attrs,
		},
	}, nil
}

// Runner starts the proxy forwarder, relaying traffic from the allocated
// listen port to the real target endpoint.
func (p *Proxy) Runner(params StartParams) run.Runner {
	return run.Func(func(ctx context.Context) error {
		var cfg ProxyConfig
		if err := json.Unmarshal(params.Spec.Config, &cfg); err != nil {
			return fmt.Errorf("proxy: unmarshal config: %w", err)
		}

		target, ok := params.Egresses["target"]
		if !ok {
			return fmt.Errorf("proxy: no resolved egress \"target\"")
		}

		ingress, ok := params.Ingresses["default"]
		if !ok {
			return fmt.Errorf("proxy: no resolved ingress \"default\"")
		}

		fwd := &proxy.Forwarder{
			ListenAddr: ingress.HostPort,
			Target:     target,
			Source:     cfg.Source,
			TargetSvc:  cfg.TargetSvc,
			Ingress:    cfg.Ingress,
			Protocol:   string(target.Protocol),
			Emit:       params.ProxyEmit,
		}

		// For gRPC targets, check the reflection cache first, then
		// fall back to a live probe. Results are cached by ReflectionKey
		// (target service name + ingress) so identical targets across
		// test runs share descriptors.
		if target.Protocol == spec.GRPC {
			if dec := p.cachedReflection(cfg.ReflectionKey); dec != nil {
				fwd.Decoder = dec
			} else {
				dec = proxy.ProbeReflection(ctx, target.HostPort)
				fwd.Decoder = dec
				p.cacheReflection(cfg.ReflectionKey, dec)
			}
		}

		return fwd.Runner().Run(ctx)
	})
}
