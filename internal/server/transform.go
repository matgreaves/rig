package server

import (
	"encoding/json"

	"github.com/matgreaves/rig/internal/server/service"
	"github.com/matgreaves/rig/internal/spec"
)

// InsertTestNode adds a virtual ~test service node to the environment.
// The ~test node has an egress to every real service's every ingress,
// using a naming convention that maps back to service/ingress pairs:
//   - single-ingress or default ingress: egress name = service name
//   - non-default ingress on multi-ingress service: egress name = "service~ingress"
//
// The ~test node has no ingresses. Its waitForEgressesStep gates on all
// real services being READY, and emitEnvironmentUp fires from its lifecycle.
func InsertTestNode(env *spec.Environment) {
	egresses := make(map[string]spec.EgressSpec)

	for svcName, svc := range env.Services {
		if svc.Injected {
			continue
		}
		if len(svc.Ingresses) == 0 {
			// No ingresses — still add a dependency so ~test waits for it.
			// Use a synthetic egress that points at the service with no ingress.
			// waitForEgressesStep will gate on service.ready but the endpoint
			// lookup will be a no-op. Actually, we need to wait for services
			// with no ingresses too. But egresses require an ingress target.
			// For no-ingress services, we skip — they become ready independently
			// and ~test doesn't need their endpoints.
			continue
		}
		for ingressName := range svc.Ingresses {
			egressName := svcName
			if ingressName != "default" && len(svc.Ingresses) > 1 {
				egressName = svcName + "~" + ingressName
			}
			egresses[egressName] = spec.EgressSpec{
				Service: svcName,
				Ingress: ingressName,
			}
		}
	}

	env.Services["~test"] = spec.Service{
		Type:     "test",
		Egresses: egresses,
		Injected: true,
	}
}

// TransformObserve inserts proxy service nodes on every egress edge in the
// graph when observe mode is enabled. Each proxy node sits between a source
// service and its target, transparently forwarding traffic while capturing
// events.
//
// For each egress edge (source → target.ingress):
//  1. A proxy node is inserted with name "{target}~proxy~{source}" (or
//     "{target}~{ingress}~proxy~{source}" for non-default ingresses)
//  2. The proxy has ingress "default" (protocol from target's ingress),
//     egress "target" pointing at the real target, and a ProxyConfig
//  3. The source's egress is retargeted to the proxy node's "default" ingress
//     — the egress name (map key) is unchanged, making the proxy transparent
func TransformObserve(env *spec.Environment) {
	if !env.Observe {
		return
	}

	// Collect edges to transform. We can't mutate the map while iterating
	// the outer services, so collect first.
	type edge struct {
		sourceSvc  string
		egressName string
		egress     spec.EgressSpec
	}
	var edges []edge

	for svcName, svc := range env.Services {
		for egressName, egress := range svc.Egresses {
			edges = append(edges, edge{
				sourceSvc:  svcName,
				egressName: egressName,
				egress:     egress,
			})
		}
	}

	for _, e := range edges {
		targetSvc, ok := env.Services[e.egress.Service]
		if !ok {
			continue
		}

		targetIngress := e.egress.Ingress
		targetIngressSpec, ok := targetSvc.Ingresses[targetIngress]
		if !ok {
			continue
		}

		// Build proxy node name.
		proxyName := e.egress.Service + "~proxy~" + e.sourceSvc
		if targetIngress != "default" {
			proxyName = e.egress.Service + "~" + targetIngress + "~proxy~" + e.sourceSvc
		}

		// ReflectionKey caches gRPC reflection descriptors across proxy
		// instances targeting the same service type+config. Only set for
		// gRPC targets — other protocols don't use reflection.
		var reflectionKey string
		if targetIngressSpec.Protocol == "grpc" {
			reflectionKey = e.egress.Service + ":" + targetIngress
		}

		cfg := service.ProxyConfig{
			Source:        e.sourceSvc,
			TargetSvc:     e.egress.Service,
			Ingress:       targetIngress,
			ReflectionKey: reflectionKey,
		}
		cfgJSON, _ := json.Marshal(cfg)

		env.Services[proxyName] = spec.Service{
			Type:   "proxy",
			Config: cfgJSON,
			Ingresses: map[string]spec.IngressSpec{
				"default": {
					Protocol: targetIngressSpec.Protocol,
				},
			},
			Egresses: map[string]spec.EgressSpec{
				"target": {
					Service: e.egress.Service,
					Ingress: targetIngress,
				},
			},
			Injected: true,
		}

		// Retarget the source's egress to the proxy node.
		sourceSvc := env.Services[e.sourceSvc]
		sourceSvc.Egresses[e.egressName] = spec.EgressSpec{
			Service: proxyName,
			Ingress: "default",
		}
		env.Services[e.sourceSvc] = sourceSvc
	}
}
