package rig

import (
	"encoding/json"
	"time"
)

// Wire types — unexported copies of spec types used to build the JSON
// payload sent to rigd. These must stay in sync with the spec package
// (now at internal/spec/) in terms of JSON tags and structure.

type specEnvironment struct {
	Name     string                 `json:"name"`
	Services map[string]specService `json:"services"`
	Observe  bool                   `json:"observe,omitempty"`
}

type specService struct {
	Type      string                     `json:"type"`
	Config    json.RawMessage            `json:"config,omitempty"`
	Args      []string                   `json:"args,omitempty"`
	Ingresses map[string]specIngressSpec `json:"ingresses,omitempty"`
	Egresses  map[string]specEgressSpec  `json:"egresses,omitempty"`
	Hooks     *specHooks                 `json:"hooks,omitempty"`
}

type specHooks struct {
	Prestart []*specHookSpec `json:"prestart,omitempty"`
	Init     []*specHookSpec `json:"init,omitempty"`
}

type specHookSpec struct {
	Type       string              `json:"type"`
	ClientFunc *specClientFuncSpec `json:"client_func,omitempty"`
	Config     json.RawMessage     `json:"config,omitempty"`
}

type specClientFuncSpec struct {
	Name string `json:"name"`
}

type specIngressSpec struct {
	ContainerPort int            `json:"container_port,omitempty"`
	Protocol      Protocol       `json:"protocol"`
	Ready         *specReadySpec `json:"ready,omitempty"`
	Attributes    map[string]any `json:"attributes,omitempty"`
}

type specEgressSpec struct {
	Service string `json:"service"`
	Ingress string `json:"ingress,omitempty"`
}

type specReadySpec struct {
	Type     string       `json:"type,omitempty"`
	Path     string       `json:"path,omitempty"`
	Interval specDuration `json:"interval,omitempty"`
	Timeout  specDuration `json:"timeout,omitempty"`
}

// specDuration wraps time.Duration with JSON marshalling as a string
// (e.g. "5s", "100ms") instead of nanoseconds.
type specDuration struct {
	time.Duration
}

func (d specDuration) IsZero() bool {
	return d.Duration == 0
}

func (d specDuration) MarshalJSON() ([]byte, error) {
	if d.Duration == 0 {
		return []byte(`""`), nil
	}
	return json.Marshal(d.Duration.String())
}
