package service

import (
	"context"
	"fmt"
	"io"

	"github.com/matgreaves/rig/spec"
	"github.com/matgreaves/run"
)

// PublishParams provides the context needed for the publish phase.
type PublishParams struct {
	ServiceName string
	Spec        spec.Service
	Ingresses   map[string]spec.IngressSpec
	Ports       map[string]int // ingress name → allocated port
}

// StartParams provides the context needed for the start phase.
type StartParams struct {
	ServiceName string
	Spec        spec.Service
	Ingresses   map[string]spec.Endpoint // resolved ingresses (from publish)
	Egresses    map[string]spec.Endpoint // resolved egresses (from wiring)
	Env         map[string]string        // pre-built environment variables
	Args        []string                 // pre-expanded command arguments
	TempDir     string
	EnvDir      string
	Stdout      io.Writer
	Stderr      io.Writer
}

// Type defines how a service type publishes endpoints and starts.
type Type interface {
	// Publish resolves ingress endpoints for this service. Called after ports
	// are allocated. Returns the fully resolved ingress endpoints.
	Publish(ctx context.Context, params PublishParams) (map[string]spec.Endpoint, error)

	// Runner returns a run.Runner that starts and runs the service.
	// The runner should block until the service exits or ctx is cancelled.
	Runner(params StartParams) run.Runner
}

// Registry maps service type names to their implementations.
type Registry struct {
	types map[string]Type
}

// NewRegistry creates a registry with no types registered.
func NewRegistry() *Registry {
	return &Registry{types: make(map[string]Type)}
}

// Register adds a service type to the registry.
func (r *Registry) Register(name string, t Type) {
	r.types[name] = t
}

// Get returns the service type for the given name, or an error if not found.
func (r *Registry) Get(name string) (Type, error) {
	t, ok := r.types[name]
	if !ok {
		return nil, fmt.Errorf("unknown service type: %q", name)
	}
	return t, nil
}
