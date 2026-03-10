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

	// HostEnv is the host process environment captured by the SDK.
	// It is merged as a base layer under wiring env vars so that child
	// processes (process/go types) inherit PATH, JAVA_HOME, etc.
	HostEnv map[string]string `json:"host_env,omitempty"`

	// Dir is the working directory of the test process, captured by the SDK.
	// Used as the default working directory for process/go child services
	// when no per-service Dir is specified.
	Dir string `json:"dir,omitempty"`

	// TTL is the maximum lifetime of the environment as a Go duration string
	// (e.g. "30m", "2h"). When set, the server tears down the environment
	// after this duration regardless of client state. The client SDK skips
	// sending DELETE on cleanup, allowing the environment to outlive the test
	// process for manual inspection.
	TTL string `json:"ttl,omitempty"`
}

// ResolvedEnvironment is the runtime view of an environment after all
// ports have been allocated and services have published their endpoints.
type ResolvedEnvironment struct {
	ID       string                     `json:"id"`
	Name     string                     `json:"name"`
	Services map[string]ResolvedService `json:"services"`
}

// ResolvedService is the runtime view of a single service.
// Ingresses and egresses use ResolvedEndpoint — all attribute templates
// have been expanded to concrete values.
type ResolvedService struct {
	Ingresses map[string]ResolvedEndpoint `json:"ingresses"`
	Egresses  map[string]ResolvedEndpoint `json:"egresses"`
	Status    ServiceStatus               `json:"status"`
}
