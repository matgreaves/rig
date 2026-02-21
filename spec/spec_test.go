package spec_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/matgreaves/rig/spec"
)

func TestProtocolValid(t *testing.T) {
	tests := []struct {
		p    spec.Protocol
		want bool
	}{
		{spec.TCP, true},
		{spec.HTTP, true},
		{spec.GRPC, true},
		{"websocket", false},
		{"", false},
	}

	for _, tt := range tests {
		if got := tt.p.Valid(); got != tt.want {
			t.Errorf("Protocol(%q).Valid() = %v, want %v", tt.p, got, tt.want)
		}
	}
}

func TestEndpointRoundTrip(t *testing.T) {
	ep := spec.Endpoint{
		Host:     "127.0.0.1",
		Port:     5432,
		Protocol: spec.TCP,
		Attributes: map[string]any{
			"PGHOST":     "127.0.0.1",
			"PGPORT":     "5432",
			"PGDATABASE": "testdb",
		},
	}

	data, err := json.Marshal(ep)
	if err != nil {
		t.Fatal(err)
	}

	var got spec.Endpoint
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}

	if got.Host != ep.Host || got.Port != ep.Port || got.Protocol != ep.Protocol {
		t.Errorf("round-trip mismatch: got %+v, want %+v", got, ep)
	}
	if got.Attributes["PGDATABASE"] != "testdb" {
		t.Errorf("attributes lost in round-trip")
	}
}

func TestEndpointOmitsEmptyAttributes(t *testing.T) {
	ep := spec.Endpoint{
		Host:     "127.0.0.1",
		Port:     8080,
		Protocol: spec.HTTP,
	}

	data, err := json.Marshal(ep)
	if err != nil {
		t.Fatal(err)
	}

	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}

	if _, ok := raw["attributes"]; ok {
		t.Error("expected attributes to be omitted from JSON when nil")
	}
}

func TestDurationRoundTrip(t *testing.T) {
	tests := []struct {
		name string
		dur  spec.Duration
		json string
	}{
		{"zero", spec.Duration{}, `""`},
		{"100ms", spec.Duration{Duration: 100 * time.Millisecond}, `"100ms"`},
		{"5s", spec.Duration{Duration: 5 * time.Second}, `"5s"`},
		{"2m", spec.Duration{Duration: 2 * time.Minute}, `"2m0s"`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data, err := json.Marshal(tt.dur)
			if err != nil {
				t.Fatal(err)
			}
			if string(data) != tt.json {
				t.Errorf("Marshal = %s, want %s", data, tt.json)
			}

			var got spec.Duration
			if err := json.Unmarshal(data, &got); err != nil {
				t.Fatal(err)
			}
			if got.Duration != tt.dur.Duration {
				t.Errorf("Unmarshal = %v, want %v", got.Duration, tt.dur.Duration)
			}
		})
	}
}

func TestReadySpecRoundTrip(t *testing.T) {
	rs := spec.ReadySpec{
		Type:     "http",
		Path:     "/healthz",
		Interval: spec.Duration{Duration: 200 * time.Millisecond},
		Timeout:  spec.Duration{Duration: 30 * time.Second},
	}

	data, err := json.Marshal(rs)
	if err != nil {
		t.Fatal(err)
	}

	var got spec.ReadySpec
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}

	if got.Type != rs.Type || got.Path != rs.Path {
		t.Errorf("round-trip mismatch: got %+v, want %+v", got, rs)
	}
	if got.Interval.Duration != rs.Interval.Duration {
		t.Errorf("interval: got %v, want %v", got.Interval, rs.Interval)
	}
	if got.Timeout.Duration != rs.Timeout.Duration {
		t.Errorf("timeout: got %v, want %v", got.Timeout, rs.Timeout)
	}
}

func TestFullEnvironmentRoundTrip(t *testing.T) {
	env := spec.Environment{
		Name: "order-workflow",
		Services: map[string]spec.Service{
			"postgres": {
				Type:   "postgres",
				Config: json.RawMessage(`{"database":"orders","user":"test","password":"test"}`),
				Ingresses: map[string]spec.IngressSpec{
					"default": {Protocol: spec.TCP, ContainerPort: 5432},
				},
				Hooks: &spec.Hooks{
					Init: []*spec.HookSpec{{
						Type:   "initdb",
						Config: json.RawMessage(`{"migrations":"./testdata/migrations"}`),
					}},
				},
			},
			"order-service": {
				Type: "go",
				Config: json.RawMessage(`{"module":"./cmd/order-service"}`),
				Args: []string{"-c", "${RIG_TEMP_DIR}/config.json"},
				Ingresses: map[string]spec.IngressSpec{
					"api": {
						Protocol: spec.HTTP,
						Ready:    &spec.ReadySpec{Type: "http"},
					},
				},
				Egresses: map[string]spec.EgressSpec{
					"database": {Service: "postgres", Ingress: "default"},
				},
				Hooks: &spec.Hooks{
					Prestart: []*spec.HookSpec{{
						Type:       "client_func",
						ClientFunc: &spec.ClientFuncSpec{Name: "write-order-config"},
					}},
				},
			},
		},
	}

	data, err := json.Marshal(env)
	if err != nil {
		t.Fatal(err)
	}

	var got spec.Environment
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}

	if got.Name != env.Name {
		t.Errorf("name: got %q, want %q", got.Name, env.Name)
	}
	if len(got.Services) != len(env.Services) {
		t.Errorf("services count: got %d, want %d", len(got.Services), len(env.Services))
	}

	// Verify postgres service round-tripped.
	pg := got.Services["postgres"]
	if pg.Type != "postgres" {
		t.Errorf("postgres type: got %q", pg.Type)
	}
	if pg.Ingresses["default"].ContainerPort != 5432 {
		t.Errorf("postgres container_port: got %d", pg.Ingresses["default"].ContainerPort)
	}
	if pg.Hooks == nil || len(pg.Hooks.Init) == 0 || pg.Hooks.Init[0].Type != "initdb" {
		t.Error("postgres init hook lost in round-trip")
	}

	// Verify order-service round-tripped.
	os := got.Services["order-service"]
	if os.Type != "go" {
		t.Errorf("order-service type: got %q", os.Type)
	}
	if len(os.Args) != 2 || os.Args[1] != "${RIG_TEMP_DIR}/config.json" {
		t.Errorf("order-service args: got %v", os.Args)
	}
	if os.Egresses["database"].Service != "postgres" {
		t.Error("order-service egress lost in round-trip")
	}
	if os.Hooks == nil || len(os.Hooks.Prestart) == 0 || os.Hooks.Prestart[0].ClientFunc == nil {
		t.Error("order-service prestart hook lost in round-trip")
	}
	if os.Hooks.Prestart[0].ClientFunc.Name != "write-order-config" {
		t.Errorf("client_func name: got %q", os.Hooks.Prestart[0].ClientFunc.Name)
	}
}

func TestEnvironmentFromJSON(t *testing.T) {
	// Test parsing the JSON wire format from the plan.
	raw := `{
		"name": "order-workflow",
		"services": {
			"postgres": {
				"type": "postgres",
				"config": {"database": "orders", "user": "test", "password": "test"},
				"hooks": {
					"init": [{
						"type": "initdb",
						"config": {"migrations": "./testdata/migrations"}
					}]
				}
			},
			"order-service": {
				"type": "go",
				"config": {"module": "./cmd/order-service"},
				"args": ["-c", "${RIG_TEMP_DIR}/config.json"],
				"ingresses": {
					"api": {
						"protocol": "http",
						"ready": {"type": "http"}
					}
				},
				"egresses": {
					"database": {"service": "postgres"}
				},
				"hooks": {
					"prestart": [{
						"type": "client_func",
						"client_func": {"name": "write-order-config"}
					}]
				}
			}
		}
	}`

	var env spec.Environment
	if err := json.Unmarshal([]byte(raw), &env); err != nil {
		t.Fatal(err)
	}

	if env.Name != "order-workflow" {
		t.Errorf("name: got %q", env.Name)
	}
	if len(env.Services) != 2 {
		t.Errorf("services: got %d", len(env.Services))
	}
	if env.Services["order-service"].Egresses["database"].Service != "postgres" {
		t.Error("egress not parsed correctly")
	}
}

func TestServiceStatusValues(t *testing.T) {
	// Ensure all status values are distinct and non-empty.
	statuses := []spec.ServiceStatus{
		spec.StatusPending,
		spec.StatusStarting,
		spec.StatusHealthy,
		spec.StatusReady,
		spec.StatusFailed,
		spec.StatusStopping,
		spec.StatusStopped,
	}

	seen := make(map[spec.ServiceStatus]bool)
	for _, s := range statuses {
		if s == "" {
			t.Error("empty status value")
		}
		if seen[s] {
			t.Errorf("duplicate status: %q", s)
		}
		seen[s] = true
	}
}

func TestResolvedEnvironmentRoundTrip(t *testing.T) {
	resolved := spec.ResolvedEnvironment{
		ID:   "env-abc123",
		Name: "test-env",
		Services: map[string]spec.ResolvedService{
			"my-api": {
				Ingresses: map[string]spec.Endpoint{
					"default": {
						Host:     "127.0.0.1",
						Port:     8080,
						Protocol: spec.HTTP,
					},
				},
				Egresses: map[string]spec.Endpoint{
					"database": {
						Host:     "127.0.0.1",
						Port:     54321,
						Protocol: spec.TCP,
						Attributes: map[string]any{
							"PGDATABASE": "testdb",
						},
					},
				},
				Status: spec.StatusReady,
			},
		},
	}

	data, err := json.Marshal(resolved)
	if err != nil {
		t.Fatal(err)
	}

	var got spec.ResolvedEnvironment
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}

	if got.ID != resolved.ID || got.Name != resolved.Name {
		t.Errorf("round-trip mismatch: got ID=%q Name=%q", got.ID, got.Name)
	}

	svc := got.Services["my-api"]
	if svc.Status != spec.StatusReady {
		t.Errorf("status: got %q", svc.Status)
	}
	if svc.Ingresses["default"].Port != 8080 {
		t.Errorf("ingress port: got %d", svc.Ingresses["default"].Port)
	}
	if svc.Egresses["database"].Attributes["PGDATABASE"] != "testdb" {
		t.Error("egress attributes lost in round-trip")
	}
}

func TestDecodeEnvironment_Valid(t *testing.T) {
	raw := `{
		"name": "test",
		"services": {
			"api": {"type": "process"},
			"db": {"type": "postgres"}
		}
	}`

	env, err := spec.DecodeEnvironment([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if env.Name != "test" {
		t.Errorf("name: got %q", env.Name)
	}
	if len(env.Services) != 2 {
		t.Errorf("services: got %d", len(env.Services))
	}
}

func TestDecodeEnvironment_DuplicateServiceNames(t *testing.T) {
	raw := `{
		"name": "test",
		"services": {
			"api": {"type": "process"},
			"api": {"type": "container"}
		}
	}`

	_, err := spec.DecodeEnvironment([]byte(raw))
	if err == nil {
		t.Fatal("expected error for duplicate service names")
	}
	if !strings.Contains(err.Error(), "duplicate") {
		t.Errorf("expected duplicate key error, got: %v", err)
	}
}

func TestDecodeEnvironment_DuplicateIngressNames(t *testing.T) {
	raw := `{
		"name": "test",
		"services": {
			"api": {
				"type": "process",
				"ingresses": {
					"default": {"protocol": "http"},
					"default": {"protocol": "grpc"}
				}
			}
		}
	}`

	_, err := spec.DecodeEnvironment([]byte(raw))
	if err == nil {
		t.Fatal("expected error for duplicate ingress names")
	}
	if !strings.Contains(err.Error(), "duplicate") {
		t.Errorf("expected duplicate key error, got: %v", err)
	}
}

func TestDecodeEnvironment_DuplicateEgressNames(t *testing.T) {
	raw := `{
		"name": "test",
		"services": {
			"api": {
				"type": "process",
				"egresses": {
					"database": {"service": "db"},
					"database": {"service": "cache"}
				}
			}
		}
	}`

	_, err := spec.DecodeEnvironment([]byte(raw))
	if err == nil {
		t.Fatal("expected error for duplicate egress names")
	}
	if !strings.Contains(err.Error(), "duplicate") {
		t.Errorf("expected duplicate key error, got: %v", err)
	}
}
