package spec

import "encoding/json"

// Service defines a single service within an environment.
type Service struct {
	// Type identifies how to start the service (e.g. "container", "process",
	// "go", "postgres", "temporal", "redis").
	Type string `json:"type"`

	// Config holds type-specific configuration as raw JSON.
	Config json.RawMessage `json:"config,omitempty"`

	// Args are command-line arguments passed to the service.
	// Supports template expansion (e.g. "${RIG_TEMP_DIR}/config.json").
	Args []string `json:"args,omitempty"`

	// Ingresses declares the endpoints this service exposes.
	// If empty, a single HTTP ingress named "default" is implied.
	Ingresses map[string]IngressSpec `json:"ingresses,omitempty"`

	// Egresses declares dependencies on other services' ingresses.
	Egresses map[string]EgressSpec `json:"egresses,omitempty"`

	// Hooks defines lifecycle hooks for this service.
	Hooks *Hooks `json:"hooks,omitempty"`
}

// Hooks holds the optional prestart and init hooks for a service.
type Hooks struct {
	Prestart []*HookSpec `json:"prestart,omitempty"`
	Init     []*HookSpec `json:"init,omitempty"`
}
