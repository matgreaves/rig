package service

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/matgreaves/rig/internal/spec"
)

func TestPostgresArtifacts_DefaultImage(t *testing.T) {
	pg := NewPostgres(NewPostgresPool(99999))
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
	pg := NewPostgres(NewPostgresPool(99999))
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
	pg := NewPostgres(NewPostgresPool(99999))
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
	pg := NewPostgres(NewPostgresPool(99999))
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
