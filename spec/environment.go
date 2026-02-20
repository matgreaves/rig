package spec

// Environment is the top-level spec that describes a collection of
// services with defined relationships. This is the JSON wire format
// sent from SDKs to rigd.
type Environment struct {
	// Name identifies the environment definition.
	Name string `json:"name"`

	// Services maps service names to their specs.
	Services map[string]Service `json:"services"`

	// Observe enables transparent traffic proxying. When true, rig inserts
	// a proxy on every egress edge and every external connection, capturing
	// request/connection events in the event log.
	Observe bool `json:"observe,omitempty"`
}

// ResolvedEnvironment is the runtime view of an environment after all
// ports have been allocated and services have published their endpoints.
type ResolvedEnvironment struct {
	ID       string                     `json:"id"`
	Name     string                     `json:"name"`
	Services map[string]ResolvedService `json:"services"`
}

// ResolvedService is the runtime view of a single service.
type ResolvedService struct {
	Ingresses map[string]Endpoint `json:"ingresses"`
	Egresses  map[string]Endpoint `json:"egresses"`
	Status    ServiceStatus       `json:"status"`
}
