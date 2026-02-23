package spec

// IngressSpec declares an endpoint that a service exposes.
// Declared without concrete addresses — the server allocates ports at
// instantiation time.
type IngressSpec struct {
	// ContainerPort is the fixed port inside the container.
	// Required for container-type services, ignored for others.
	ContainerPort int `json:"container_port,omitempty"`

	// Protocol is the application-layer protocol (tcp, http, grpc).
	Protocol Protocol `json:"protocol"`

	// Ready overrides the default health check for this ingress.
	Ready *ReadySpec `json:"ready,omitempty"`

	// Attributes are static attributes published with this ingress.
	// Service types may add dynamic attributes at publish time.
	Attributes map[string]any `json:"attributes,omitempty"`
}
