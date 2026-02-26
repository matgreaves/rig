package rig_test

import (
	"testing"

	rig "github.com/matgreaves/rig/client"
)

func TestEndpoint_Lookup(t *testing.T) {
	t.Parallel()
	env := &rig.Environment{
		Name: "test",
		Services: map[string]rig.ResolvedService{
			"api": {Ingresses: map[string]rig.Endpoint{
				"default": {HostPort: "127.0.0.1:8080", Protocol: rig.HTTP},
				"grpc":    {HostPort: "127.0.0.1:9090", Protocol: rig.GRPC},
			}},
			"db": {Ingresses: map[string]rig.Endpoint{
				"tcp": {HostPort: "127.0.0.1:5432", Protocol: rig.TCP},
			}},
		},
	}

	// Default ingress by name.
	ep := env.Endpoint("api")
	if ep.Port() != 8080 {
		t.Errorf("api default port = %d, want 8080", ep.Port())
	}

	// Named ingress.
	ep = env.Endpoint("api", "grpc")
	if ep.Port() != 9090 {
		t.Errorf("api grpc port = %d, want 9090", ep.Port())
	}

	// Single ingress shorthand — returns sole ingress even if not named "default".
	ep = env.Endpoint("db")
	if ep.Port() != 5432 {
		t.Errorf("db port = %d, want 5432", ep.Port())
	}
}

func TestEndpoint_Lookup_PanicsOnMiss(t *testing.T) {
	t.Parallel()
	env := &rig.Environment{
		Name: "test",
		Services: map[string]rig.ResolvedService{
			"api": {Ingresses: map[string]rig.Endpoint{
				"default": {HostPort: "127.0.0.1:8080", Protocol: rig.HTTP},
			}},
		},
	}

	// Unknown service.
	assertPanics(t, "unknown service", func() {
		env.Endpoint("nonexistent")
	})

	// Unknown ingress.
	assertPanics(t, "unknown ingress", func() {
		env.Endpoint("api", "nonexistent")
	})
}

func TestEndpoint_HostPort(t *testing.T) {
	t.Parallel()
	httpEP := rig.Endpoint{HostPort: "127.0.0.1:8080", Protocol: rig.HTTP}
	if got := httpEP.HostPort; got != "127.0.0.1:8080" {
		t.Errorf("HTTP HostPort = %q, want 127.0.0.1:8080", got)
	}

	grpcEP := rig.Endpoint{HostPort: "127.0.0.1:9090", Protocol: rig.GRPC}
	if got := grpcEP.HostPort; got != "127.0.0.1:9090" {
		t.Errorf("GRPC HostPort = %q, want 127.0.0.1:9090", got)
	}

	tcpEP := rig.Endpoint{HostPort: "127.0.0.1:5432", Protocol: rig.TCP}
	if got := tcpEP.HostPort; got != "127.0.0.1:5432" {
		t.Errorf("TCP HostPort = %q, want 127.0.0.1:5432", got)
	}
}

func TestEndpoint_Attr(t *testing.T) {
	t.Parallel()
	ep := rig.Endpoint{
		HostPort: "127.0.0.1:5432",
		Protocol: rig.TCP,
		Attributes: map[string]any{
			"PGDATABASE": "testdb",
			"PGUSER":     "postgres",
			"PORT":       5432,
		},
	}

	if got := ep.Attr("PGDATABASE"); got != "testdb" {
		t.Errorf("Attr(PGDATABASE) = %q, want testdb", got)
	}
	if got := ep.Attr("PORT"); got != "5432" {
		t.Errorf("Attr(PORT) = %q, want 5432", got)
	}
	if got := ep.Attr("MISSING"); got != "" {
		t.Errorf("Attr(MISSING) = %q, want empty", got)
	}
}

// --- helpers ---

func assertPanics(t *testing.T, name string, fn func()) {
	t.Helper()
	defer func() {
		if r := recover(); r == nil {
			t.Errorf("%s: expected panic, got none", name)
		}
	}()
	fn()
}
