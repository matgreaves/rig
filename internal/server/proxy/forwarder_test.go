package proxy_test

import (
	"testing"

	"github.com/matgreaves/rig/internal/server/proxy"
	"github.com/matgreaves/rig/internal/spec"
)

func TestForwarderEndpoint_TemplateAttrsPassThrough(t *testing.T) {
	f := &proxy.Forwarder{
		ListenAddr: "127.0.0.1:9999",
		Target: spec.Endpoint{
			HostPort: "10.0.0.5:5432",
			Protocol: spec.TCP,
			Attributes: map[string]any{
				"PGHOST":     "${HOST}",
				"PGPORT":     "${PORT}",
				"PGDATABASE": "mydb",
			},
		},
	}

	ep := f.Endpoint()

	if ep.HostPort != "127.0.0.1:9999" {
		t.Errorf("HostPort = %q, want 127.0.0.1:9999", ep.HostPort)
	}
	if ep.Protocol != spec.TCP {
		t.Errorf("Protocol = %q, want tcp", ep.Protocol)
	}

	// Template attributes should be copied unchanged — they resolve
	// against the proxy's HostPort when consumed downstream.
	if got := ep.Attributes["PGHOST"]; got != "${HOST}" {
		t.Errorf("PGHOST = %v, want ${HOST}", got)
	}
	if got := ep.Attributes["PGPORT"]; got != "${PORT}" {
		t.Errorf("PGPORT = %v, want ${PORT}", got)
	}

	// Non-template attrs should be preserved.
	if got := ep.Attributes["PGDATABASE"]; got != "mydb" {
		t.Errorf("PGDATABASE = %v, want mydb", got)
	}
}

func TestForwarderEndpoint_HostPort(t *testing.T) {
	f := &proxy.Forwarder{
		ListenAddr: "127.0.0.1:7233",
		Target: spec.Endpoint{
			HostPort: "10.0.0.5:7233",
			Protocol: spec.GRPC,
			Attributes: map[string]any{
				"TEMPORAL_ADDRESS":   "${HOSTPORT}",
				"TEMPORAL_NAMESPACE": "default",
			},
		},
	}

	ep := f.Endpoint()

	// Template should be passed through unchanged.
	if got := ep.Attributes["TEMPORAL_ADDRESS"]; got != "${HOSTPORT}" {
		t.Errorf("TEMPORAL_ADDRESS = %v, want ${HOSTPORT}", got)
	}
	if got := ep.Attributes["TEMPORAL_NAMESPACE"]; got != "default" {
		t.Errorf("TEMPORAL_NAMESPACE = %v, want default", got)
	}

	// Verify that resolving the template against the proxy endpoint
	// produces the correct value.
	resolved, err := spec.ResolveAttributes(ep)
	if err != nil {
		t.Fatal(err)
	}
	if got := resolved["TEMPORAL_ADDRESS"]; got != "127.0.0.1:7233" {
		t.Errorf("resolved TEMPORAL_ADDRESS = %v, want 127.0.0.1:7233", got)
	}
}

func TestForwarderEndpoint_NoAttributes(t *testing.T) {
	f := &proxy.Forwarder{
		ListenAddr: "127.0.0.1:8080",
		Target: spec.Endpoint{
			HostPort: "10.0.0.5:8080",
			Protocol: spec.HTTP,
			Attributes: map[string]any{
				"FOO": "bar",
			},
		},
	}

	ep := f.Endpoint()

	// Without templates, attributes are copied unchanged.
	if got := ep.Attributes["FOO"]; got != "bar" {
		t.Errorf("FOO = %v, want bar", got)
	}
}

func TestForwarderEndpoint_NilAttributes(t *testing.T) {
	f := &proxy.Forwarder{
		ListenAddr: "127.0.0.1:8080",
		Target: spec.Endpoint{
			HostPort: "10.0.0.5:8080",
			Protocol: spec.HTTP,
		},
	}

	ep := f.Endpoint()

	if ep.Attributes != nil {
		t.Errorf("Attributes = %v, want nil", ep.Attributes)
	}
}
