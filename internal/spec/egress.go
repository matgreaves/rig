package spec

// EgressSpec declares a dependency from one service to another service's ingress.
type EgressSpec struct {
	// Service is the name of the target service.
	Service string `json:"service"`

	// Ingress is the name of the target ingress on the target service.
	// If omitted, defaults to the sole ingress on the target service.
	// Validation fails if the target has multiple ingresses and this is empty.
	Ingress string `json:"ingress,omitempty"`
}
