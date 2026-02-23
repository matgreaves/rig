package proxy_test

import (
	"testing"

	"github.com/matgreaves/rig/internal/server/proxy"
	"github.com/matgreaves/rig/internal/spec"
)

func TestForwarderEndpoint_RewritesAddressAttrs(t *testing.T) {
	f := &proxy.Forwarder{
		ListenPort: 9999,
		Target: spec.Endpoint{
			Host:     "10.0.0.5",
			Port:     5432,
			Protocol: spec.TCP,
			Attributes: map[string]any{
				"PGHOST":     "10.0.0.5",
				"PGPORT":     "5432",
				"PGDATABASE": "mydb",
			},
			AddressAttrs: map[string]spec.AddrAttr{
				"PGHOST": spec.AttrHost,
				"PGPORT": spec.AttrPort,
			},
		},
	}

	ep := f.Endpoint()

	if ep.Host != "127.0.0.1" {
		t.Errorf("Host = %q, want 127.0.0.1", ep.Host)
	}
	if ep.Port != 9999 {
		t.Errorf("Port = %d, want 9999", ep.Port)
	}
	if ep.Protocol != spec.TCP {
		t.Errorf("Protocol = %q, want tcp", ep.Protocol)
	}

	// Address-derived attrs should reflect the proxy address.
	if got := ep.Attributes["PGHOST"]; got != "127.0.0.1" {
		t.Errorf("PGHOST = %v, want 127.0.0.1", got)
	}
	if got := ep.Attributes["PGPORT"]; got != "9999" {
		t.Errorf("PGPORT = %v, want 9999", got)
	}

	// Non-address attrs should be preserved.
	if got := ep.Attributes["PGDATABASE"]; got != "mydb" {
		t.Errorf("PGDATABASE = %v, want mydb", got)
	}

	// AddressAttrs should be preserved for downstream rewriters.
	if ep.AddressAttrs["PGHOST"] != spec.AttrHost {
		t.Errorf("AddressAttrs[PGHOST] = %v, want %v", ep.AddressAttrs["PGHOST"], spec.AttrHost)
	}
}

func TestForwarderEndpoint_HostPort(t *testing.T) {
	f := &proxy.Forwarder{
		ListenPort: 7233,
		Target: spec.Endpoint{
			Host:     "10.0.0.5",
			Port:     7233,
			Protocol: spec.GRPC,
			Attributes: map[string]any{
				"TEMPORAL_ADDRESS":   "10.0.0.5:7233",
				"TEMPORAL_NAMESPACE": "default",
			},
			AddressAttrs: map[string]spec.AddrAttr{
				"TEMPORAL_ADDRESS": spec.AttrHostPort,
			},
		},
	}

	ep := f.Endpoint()

	if got := ep.Attributes["TEMPORAL_ADDRESS"]; got != "127.0.0.1:7233" {
		t.Errorf("TEMPORAL_ADDRESS = %v, want 127.0.0.1:7233", got)
	}
	if got := ep.Attributes["TEMPORAL_NAMESPACE"]; got != "default" {
		t.Errorf("TEMPORAL_NAMESPACE = %v, want default", got)
	}
}

func TestForwarderEndpoint_NoAddressAttrs(t *testing.T) {
	f := &proxy.Forwarder{
		ListenPort: 8080,
		Target: spec.Endpoint{
			Host:     "10.0.0.5",
			Port:     8080,
			Protocol: spec.HTTP,
			Attributes: map[string]any{
				"FOO": "bar",
			},
		},
	}

	ep := f.Endpoint()

	// Without AddressAttrs, attributes are copied unchanged.
	if got := ep.Attributes["FOO"]; got != "bar" {
		t.Errorf("FOO = %v, want bar", got)
	}
}

func TestForwarderEndpoint_NilAttributes(t *testing.T) {
	f := &proxy.Forwarder{
		ListenPort: 8080,
		Target: spec.Endpoint{
			Host:     "10.0.0.5",
			Port:     8080,
			Protocol: spec.HTTP,
		},
	}

	ep := f.Endpoint()

	if ep.Attributes != nil {
		t.Errorf("Attributes = %v, want nil", ep.Attributes)
	}
}
