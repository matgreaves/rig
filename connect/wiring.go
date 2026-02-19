package connect

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strconv"
)

// Wiring provides resolved endpoint information to services and hook
// functions. Use ParseWiring to read from the environment.
type Wiring struct {
	Ingresses  map[string]Endpoint `json:"ingresses,omitempty"`
	Egresses   map[string]Endpoint `json:"egresses,omitempty"`
	Attributes map[string]string   `json:"attributes,omitempty"`
	TempDir    string              `json:"temp_dir,omitempty"`
	EnvDir     string              `json:"env_dir,omitempty"`
}

// Ingress returns the named ingress endpoint. If no name is provided,
// "default" is used. Panics with a descriptive message if not found.
func (w *Wiring) Ingress(name ...string) Endpoint {
	n := "default"
	if len(name) > 0 {
		n = name[0]
	}
	ep, ok := w.Ingresses[n]
	if !ok {
		panic(fmt.Sprintf("rig: ingress %q not found in wiring (available: %s)",
			n, sortedMapKeys(w.Ingresses)))
	}
	return ep
}

// Egress returns the named egress endpoint.
// Panics with a descriptive message if not found.
func (w *Wiring) Egress(name string) Endpoint {
	ep, ok := w.Egresses[name]
	if !ok {
		panic(fmt.Sprintf("rig: egress %q not found in wiring (available: %s)",
			name, sortedMapKeys(w.Egresses)))
	}
	return ep
}

// ParseWiring reads the service wiring from the environment. It parses
// RIG_WIRING if set, falling back to HOST/PORT for standalone use.
//
// The ctx parameter is reserved for future use (e.g. wiring passed
// through context in function runners).
func ParseWiring(_ context.Context) (*Wiring, error) {
	if raw := os.Getenv("RIG_WIRING"); raw != "" {
		var w Wiring
		if err := json.Unmarshal([]byte(raw), &w); err != nil {
			return nil, fmt.Errorf("parse RIG_WIRING: %w", err)
		}
		return &w, nil
	}

	// Fallback: construct minimal wiring from HOST/PORT.
	host := os.Getenv("HOST")
	portStr := os.Getenv("PORT")
	if host == "" || portStr == "" {
		return nil, fmt.Errorf("HOST and PORT must be set (or RIG_WIRING)")
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return nil, fmt.Errorf("invalid PORT %q: %w", portStr, err)
	}
	return &Wiring{
		Ingresses: map[string]Endpoint{
			"default": {Host: host, Port: port},
		},
	}, nil
}

func sortedMapKeys[V any](m map[string]V) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return fmt.Sprintf("%v", keys)
}
