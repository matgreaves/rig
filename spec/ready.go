package spec

import (
	"encoding/json"
	"time"
)

// ReadySpec configures the health check for an ingress.
// If omitted, the check type is inferred from the ingress protocol.
type ReadySpec struct {
	// Type overrides the health check type ("tcp", "http", "grpc").
	// Defaults to the ingress protocol.
	Type string `json:"type,omitempty"`

	// Path is the HTTP GET path for HTTP checks. Default "/".
	Path string `json:"path,omitempty"`

	// Interval is the poll interval. Default 100ms with exponential backoff.
	Interval Duration `json:"interval,omitempty"`

	// Timeout is the maximum wait for the service to become ready.
	// Default from global timeout config.
	Timeout Duration `json:"timeout,omitempty"`
}

// Duration wraps time.Duration with JSON marshalling as a string
// (e.g. "5s", "100ms") instead of nanoseconds.
type Duration struct {
	time.Duration
}

// IsZero reports whether d is the zero duration. Used by encoding/json
// (Go 1.24+) to evaluate omitempty on struct fields.
func (d Duration) IsZero() bool {
	return d.Duration == 0
}

func (d Duration) MarshalJSON() ([]byte, error) {
	if d.Duration == 0 {
		return []byte(`""`), nil
	}
	return json.Marshal(d.Duration.String())
}

func (d *Duration) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	if s == "" {
		d.Duration = 0
		return nil
	}
	dur, err := time.ParseDuration(s)
	if err != nil {
		return err
	}
	d.Duration = dur
	return nil
}
