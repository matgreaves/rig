package service_test

import (
	"context"
	"testing"

	"github.com/matgreaves/rig/internal/server/service"
	"github.com/matgreaves/rig/internal/spec"
)

func TestProcessPublish_SingleIngress(t *testing.T) {
	p := service.Process{}
	endpoints, err := p.Publish(context.Background(), service.PublishParams{
		ServiceName: "api",
		Ingresses: map[string]spec.IngressSpec{
			"default": {Protocol: spec.HTTP},
		},
		Ports: map[string]int{"default": 8080},
	})
	if err != nil {
		t.Fatal(err)
	}

	ep, ok := endpoints["default"]
	if !ok {
		t.Fatal("missing default endpoint")
	}
	if ep.HostPort != "127.0.0.1:8080" {
		t.Errorf("hostport = %q, want 127.0.0.1:8080", ep.HostPort)
	}
	if ep.Protocol != spec.HTTP {
		t.Errorf("protocol = %q, want http", ep.Protocol)
	}
}

func TestProcessPublish_MultipleIngresses(t *testing.T) {
	p := service.Process{}
	endpoints, err := p.Publish(context.Background(), service.PublishParams{
		ServiceName: "api",
		Ingresses: map[string]spec.IngressSpec{
			"http": {Protocol: spec.HTTP},
			"grpc": {Protocol: spec.GRPC},
		},
		Ports: map[string]int{"http": 8080, "grpc": 9090},
	})
	if err != nil {
		t.Fatal(err)
	}

	if len(endpoints) != 2 {
		t.Fatalf("expected 2 endpoints, got %d", len(endpoints))
	}
	if endpoints["http"].HostPort != "127.0.0.1:8080" {
		t.Errorf("http hostport = %q, want 127.0.0.1:8080", endpoints["http"].HostPort)
	}
	if endpoints["grpc"].HostPort != "127.0.0.1:9090" {
		t.Errorf("grpc hostport = %q, want 127.0.0.1:9090", endpoints["grpc"].HostPort)
	}
}

func TestProcessPublish_MissingPort(t *testing.T) {
	p := service.Process{}
	_, err := p.Publish(context.Background(), service.PublishParams{
		ServiceName: "api",
		Ingresses: map[string]spec.IngressSpec{
			"default": {Protocol: spec.HTTP},
		},
		Ports: map[string]int{}, // no ports allocated
	})
	if err == nil {
		t.Fatal("expected error for missing port")
	}
}

func TestProcessPublish_PreservesAttributes(t *testing.T) {
	p := service.Process{}
	endpoints, err := p.Publish(context.Background(), service.PublishParams{
		ServiceName: "db",
		Ingresses: map[string]spec.IngressSpec{
			"default": {
				Protocol:   spec.TCP,
				Attributes: map[string]any{"PGHOST": "127.0.0.1"},
			},
		},
		Ports: map[string]int{"default": 5432},
	})
	if err != nil {
		t.Fatal(err)
	}

	ep := endpoints["default"]
	if ep.Attributes["PGHOST"] != "127.0.0.1" {
		t.Errorf("attributes not preserved: %v", ep.Attributes)
	}
}

func TestRegistry_GetUnknown(t *testing.T) {
	reg := service.NewRegistry()
	_, err := reg.Get("nonexistent")
	if err == nil {
		t.Error("expected error for unknown type")
	}
}

func TestRegistry_RegisterAndGet(t *testing.T) {
	reg := service.NewRegistry()
	reg.Register("process", service.Process{})

	svcType, err := reg.Get("process")
	if err != nil {
		t.Fatal(err)
	}
	if svcType == nil {
		t.Error("expected non-nil service type")
	}
}
