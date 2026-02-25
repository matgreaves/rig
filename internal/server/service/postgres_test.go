package service

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/matgreaves/rig/internal/spec"
)

func TestPostgresPublish_InjectsAttributes(t *testing.T) {
	pg := Postgres{}
	endpoints, err := pg.Publish(context.Background(), PublishParams{
		ServiceName: "db",
		Spec:        spec.Service{Type: "postgres"},
		Ingresses: map[string]spec.IngressSpec{
			"default": {Protocol: spec.TCP, ContainerPort: 5432},
		},
		Ports: map[string]int{"default": 54321},
	})
	if err != nil {
		t.Fatal(err)
	}

	ep := endpoints["default"]
	for _, attr := range []string{"PGHOST", "PGPORT", "PGDATABASE", "PGUSER", "PGPASSWORD"} {
		if _, ok := ep.Attributes[attr]; !ok {
			t.Errorf("missing attribute %s", attr)
		}
	}
	// PGHOST and PGPORT are stored as templates.
	if ep.Attributes["PGHOST"] != "${HOST}" {
		t.Errorf("PGHOST = %v, want ${HOST}", ep.Attributes["PGHOST"])
	}
	if ep.Attributes["PGPORT"] != "${PORT}" {
		t.Errorf("PGPORT = %v, want ${PORT}", ep.Attributes["PGPORT"])
	}
	if ep.Attributes["PGUSER"] != "postgres" {
		t.Errorf("PGUSER = %v, want postgres", ep.Attributes["PGUSER"])
	}
	if ep.Attributes["PGPASSWORD"] != "postgres" {
		t.Errorf("PGPASSWORD = %v, want postgres", ep.Attributes["PGPASSWORD"])
	}

	// Templates should resolve correctly against the endpoint.
	resolved, err := spec.ResolveAttributes(ep)
	if err != nil {
		t.Fatal(err)
	}
	if resolved["PGHOST"] != "127.0.0.1" {
		t.Errorf("resolved PGHOST = %v, want 127.0.0.1", resolved["PGHOST"])
	}
	if resolved["PGPORT"] != "54321" {
		t.Errorf("resolved PGPORT = %v, want 54321", resolved["PGPORT"])
	}
}

func TestPostgresPublish_DatabaseIsServiceName(t *testing.T) {
	pg := Postgres{}
	endpoints, err := pg.Publish(context.Background(), PublishParams{
		ServiceName: "mydb",
		Spec:        spec.Service{Type: "postgres"},
		Ingresses: map[string]spec.IngressSpec{
			"default": {Protocol: spec.TCP, ContainerPort: 5432},
		},
		Ports: map[string]int{"default": 54321},
	})
	if err != nil {
		t.Fatal(err)
	}

	if got := endpoints["default"].Attributes["PGDATABASE"]; got != "mydb" {
		t.Errorf("PGDATABASE = %v, want mydb", got)
	}
}

func TestPostgresArtifacts_DefaultImage(t *testing.T) {
	pg := Postgres{}
	arts, err := pg.Artifacts(ArtifactParams{
		ServiceName: "db",
		Spec:        spec.Service{Type: "postgres"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(arts) != 1 {
		t.Fatalf("got %d artifacts, want 1", len(arts))
	}
	if arts[0].Key != "docker:postgres:16-alpine" {
		t.Errorf("key = %q, want docker:postgres:16-alpine", arts[0].Key)
	}
}

func TestPostgresArtifacts_CustomImage(t *testing.T) {
	cfg, _ := json.Marshal(PostgresConfig{Image: "postgres:15"})
	pg := Postgres{}
	arts, err := pg.Artifacts(ArtifactParams{
		ServiceName: "db",
		Spec:        spec.Service{Type: "postgres", Config: cfg},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(arts) != 1 {
		t.Fatalf("got %d artifacts, want 1", len(arts))
	}
	if arts[0].Key != "docker:postgres:15" {
		t.Errorf("key = %q, want docker:postgres:15", arts[0].Key)
	}
}

func TestPostgresInit_UnsupportedHookType(t *testing.T) {
	pg := Postgres{}
	err := pg.Init(context.Background(), InitParams{
		ServiceName: "db",
		Hook: &spec.HookSpec{
			Type:   "unknown",
			Config: json.RawMessage(`{}`),
		},
	})
	if err == nil {
		t.Fatal("expected error for unsupported hook type")
	}
	if !strings.Contains(err.Error(), "unsupported hook type") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestPostgresInit_NoStatements(t *testing.T) {
	pg := Postgres{}
	err := pg.Init(context.Background(), InitParams{
		ServiceName: "db",
		Hook: &spec.HookSpec{
			Type:   "sql",
			Config: json.RawMessage(`{"statements":[]}`),
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}
