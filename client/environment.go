package rig

import (
	"fmt"
	"sort"
	"testing"
)

// Environment is the resolved, running environment returned by Up.
// It provides methods to look up service endpoints.
type Environment struct {
	ID       string
	Name     string
	Services map[string]ResolvedService

	// T is a wrapped testing.TB that automatically captures assertion
	// failures (Fatal, Fatalf, Error, Errorf) as test.note events in
	// the rig event log. Pass env.T to assertion libraries (testify,
	// is, require, etc.) so failures appear in the event timeline
	// alongside server-side events. File:line reporting is preserved.
	T testing.TB
}

// ResolvedService holds the resolved endpoints for a single service.
type ResolvedService struct {
	Ingresses map[string]Endpoint
}

// Endpoint returns the ingress endpoint for the named service. If ingress
// is omitted, the default ingress is returned. If the service has a single
// ingress, it is returned regardless of its name.
//
// Panics with a descriptive message if the service or ingress is not found.
func (e *Environment) Endpoint(service string, ingress ...string) Endpoint {
	svc, ok := e.Services[service]
	if !ok {
		panic(fmt.Sprintf("rig: service %q not found in environment %q (available: %s)",
			service, e.Name, sortedKeys(e.Services)))
	}

	ingressName := "default"
	if len(ingress) > 0 {
		ingressName = ingress[0]
	}

	// Single ingress shorthand: if the service has exactly one ingress
	// and no specific name was requested, return it.
	if ingressName == "default" && len(svc.Ingresses) == 1 {
		for _, ep := range svc.Ingresses {
			return ep
		}
	}

	ep, ok := svc.Ingresses[ingressName]
	if !ok {
		panic(fmt.Sprintf("rig: ingress %q not found on service %q (available: %s)",
			ingressName, service, sortedKeys(svc.Ingresses)))
	}
	return ep
}

func sortedKeys[V any](m map[string]V) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return fmt.Sprintf("%v", keys)
}

