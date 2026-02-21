package service

import (
	"context"
	"fmt"
	"io"

	"github.com/matgreaves/rig/server/artifact"
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
	Ingresses   map[string]spec.Endpoint   // resolved ingresses (from publish)
	Egresses    map[string]spec.Endpoint   // resolved egresses (from wiring)
	Artifacts   map[string]artifact.Output // keyed by Artifact.Key (from artifact phase)
	Env         map[string]string          // pre-built environment variables
	Args        []string                   // pre-expanded command arguments
	TempDir     string
	EnvDir      string
	InstanceID  string // environment instance ID (used for container naming)
	Stdout      io.Writer
	Stderr      io.Writer

	// BuildEnv produces a complete env var map from the given endpoints.
	// Service types that need to adjust endpoints for a different network
	// namespace (e.g. containers) call this with modified endpoints instead
	// of patching the flat Env map directly.
	BuildEnv func(ingresses, egresses map[string]spec.Endpoint) map[string]string

	// Callback dispatches a callback request to the client SDK and blocks
	// until the response arrives. Nil for types that don't use callbacks.
	Callback func(ctx context.Context, name, callbackType string) error
}

// ArtifactParams is passed to ArtifactProvider.Artifacts.
type ArtifactParams struct {
	ServiceName string
	Spec        spec.Service
}

// ArtifactProvider is implemented by service types that require artifacts
// (compiled binaries, pulled images, etc.) before starting. It is optional —
// service types that have no artifacts need not implement it.
type ArtifactProvider interface {
	Artifacts(params ArtifactParams) ([]artifact.Artifact, error)
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

// PublishLocalEndpoints is a shared implementation of Publish for service types
// that run locally. It maps each ingress to a 127.0.0.1 endpoint using the
// allocated port, preserving protocol and attributes.
func PublishLocalEndpoints(params PublishParams) (map[string]spec.Endpoint, error) {
	endpoints := make(map[string]spec.Endpoint, len(params.Ingresses))
	for name, ingSpec := range params.Ingresses {
		port, ok := params.Ports[name]
		if !ok {
			return nil, fmt.Errorf("no port allocated for ingress %q", name)
		}
		endpoints[name] = spec.Endpoint{
			Host:       "127.0.0.1",
			Port:       port,
			Protocol:   ingSpec.Protocol,
			Attributes: ingSpec.Attributes,
		}
	}
	return endpoints, nil
}
