package server_test

import (
	"testing"

	"github.com/matgreaves/rig/internal/server"
	"github.com/matgreaves/rig/internal/spec"
)

func TestBuildServiceEnv_ServiceLevel(t *testing.T) {
	env := server.BuildServiceEnv("my-api", nil, nil, "/tmp/rig/abc/my-api", "/tmp/rig/abc")
	assertEnvVar(t, env, "RIG_TEMP_DIR", "/tmp/rig/abc/my-api")
	assertEnvVar(t, env, "RIG_ENV_DIR", "/tmp/rig/abc")
	assertEnvVar(t, env, "RIG_SERVICE", "my-api")
}

func TestBuildServiceEnv_DefaultIngressUnprefixed(t *testing.T) {
	ingresses := map[string]spec.Endpoint{
		"default": {
			Host:     "127.0.0.1",
			Port:     8080,
			Protocol: spec.HTTP,
			Attributes: map[string]any{
				"PGHOST": "127.0.0.1",
				"PGPORT": "5432",
			},
		},
	}

	env := server.BuildServiceEnv("db", ingresses, nil, "/tmp", "/tmp")

	// Default ingress HOST/PORT are unprefixed.
	assertEnvVar(t, env, "HOST", "127.0.0.1")
	assertEnvVar(t, env, "PORT", "8080")
	assertEnvVar(t, env, "PGHOST", "127.0.0.1")
	assertEnvVar(t, env, "PGPORT", "5432")
}

func TestBuildServiceEnv_NamedIngressPrefixed(t *testing.T) {
	ingresses := map[string]spec.Endpoint{
		"default": {Host: "127.0.0.1", Port: 8080, Protocol: spec.HTTP},
		"admin":   {Host: "127.0.0.1", Port: 9090, Protocol: spec.HTTP},
	}

	env := server.BuildServiceEnv("api", ingresses, nil, "/tmp", "/tmp")

	// Default ingress is unprefixed.
	assertEnvVar(t, env, "HOST", "127.0.0.1")
	assertEnvVar(t, env, "PORT", "8080")

	// Named ingress "admin" is prefixed.
	assertEnvVar(t, env, "ADMIN_HOST", "127.0.0.1")
	assertEnvVar(t, env, "ADMIN_PORT", "9090")
}

func TestBuildServiceEnv_EgressAlwaysPrefixed(t *testing.T) {
	egresses := map[string]spec.Endpoint{
		"database": {
			Host:     "127.0.0.1",
			Port:     54321,
			Protocol: spec.TCP,
			Attributes: map[string]any{
				"PGHOST":     "127.0.0.1",
				"PGPORT":     "54321",
				"PGDATABASE": "orders",
			},
		},
	}

	env := server.BuildServiceEnv("api", nil, egresses, "/tmp", "/tmp")

	assertEnvVar(t, env, "DATABASE_HOST", "127.0.0.1")
	assertEnvVar(t, env, "DATABASE_PORT", "54321")
	assertEnvVar(t, env, "DATABASE_PGHOST", "127.0.0.1")
	assertEnvVar(t, env, "DATABASE_PGPORT", "54321")
	assertEnvVar(t, env, "DATABASE_PGDATABASE", "orders")
}

func TestBuildServiceEnv_MultipleEgresses(t *testing.T) {
	egresses := map[string]spec.Endpoint{
		"orders-db": {
			Host:       "127.0.0.1",
			Port:       54321,
			Protocol:   spec.TCP,
			Attributes: map[string]any{"PGDATABASE": "orders"},
		},
		"users-db": {
			Host:       "127.0.0.1",
			Port:       54322,
			Protocol:   spec.TCP,
			Attributes: map[string]any{"PGDATABASE": "users"},
		},
	}

	env := server.BuildServiceEnv("api", nil, egresses, "/tmp", "/tmp")

	assertEnvVar(t, env, "ORDERS_DB_PGDATABASE", "orders")
	assertEnvVar(t, env, "USERS_DB_PGDATABASE", "users")
	assertEnvVar(t, env, "ORDERS_DB_PORT", "54321")
	assertEnvVar(t, env, "USERS_DB_PORT", "54322")
}

func TestBuildServiceEnv_HyphenatedEgressName(t *testing.T) {
	egresses := map[string]spec.Endpoint{
		"order-db": {Host: "127.0.0.1", Port: 5432, Protocol: spec.TCP},
	}

	env := server.BuildServiceEnv("api", nil, egresses, "/tmp", "/tmp")

	assertEnvVar(t, env, "ORDER_DB_HOST", "127.0.0.1")
	assertEnvVar(t, env, "ORDER_DB_PORT", "5432")
}

func TestBuildServiceEnv_NoDefaultIngress(t *testing.T) {
	// A service with only named ingresses (no "default") — all should be prefixed.
	ingresses := map[string]spec.Endpoint{
		"grpc": {Host: "127.0.0.1", Port: 9090, Protocol: spec.GRPC},
		"http": {Host: "127.0.0.1", Port: 8080, Protocol: spec.HTTP},
	}

	env := server.BuildServiceEnv("api", ingresses, nil, "/tmp", "/tmp")

	assertEnvVar(t, env, "GRPC_HOST", "127.0.0.1")
	assertEnvVar(t, env, "GRPC_PORT", "9090")
	assertEnvVar(t, env, "HTTP_HOST", "127.0.0.1")
	assertEnvVar(t, env, "HTTP_PORT", "8080")

	// No unprefixed HOST/PORT should exist.
	if _, ok := env["HOST"]; ok {
		t.Error("expected no unprefixed HOST when no default ingress")
	}
	if _, ok := env["PORT"]; ok {
		t.Error("expected no unprefixed PORT when no default ingress")
	}
}

func TestBuildInitHookEnv_NoEgresses(t *testing.T) {
	ingresses := map[string]spec.Endpoint{
		"default": {
			Host:     "127.0.0.1",
			Port:     5432,
			Protocol: spec.TCP,
			Attributes: map[string]any{
				"PGHOST":     "127.0.0.1",
				"PGPORT":     "5432",
				"PGDATABASE": "testdb",
			},
		},
	}

	env := server.BuildInitHookEnv("postgres", ingresses, "/tmp/pg", "/tmp")

	// Ingress attributes are present and unprefixed (default ingress).
	assertEnvVar(t, env, "HOST", "127.0.0.1")
	assertEnvVar(t, env, "PORT", "5432")
	assertEnvVar(t, env, "PGHOST", "127.0.0.1")
	assertEnvVar(t, env, "PGDATABASE", "testdb")

	// Service-level attributes.
	assertEnvVar(t, env, "RIG_TEMP_DIR", "/tmp/pg")
	assertEnvVar(t, env, "RIG_SERVICE", "postgres")

	// No egress attributes should be present.
	for k := range env {
		if k == "DATABASE_PGHOST" || k == "DATABASE_HOST" {
			t.Errorf("init hook env should not contain egress attribute %q", k)
		}
	}
}

func TestBuildInitHookEnv_MultipleIngresses(t *testing.T) {
	ingresses := map[string]spec.Endpoint{
		"default": {Host: "127.0.0.1", Port: 7233, Protocol: spec.GRPC,
			Attributes: map[string]any{"TEMPORAL_ADDRESS": "127.0.0.1:7233"}},
		"ui": {Host: "127.0.0.1", Port: 8080, Protocol: spec.HTTP},
	}

	env := server.BuildInitHookEnv("temporal", ingresses, "/tmp", "/tmp")

	// Default ingress unprefixed.
	assertEnvVar(t, env, "HOST", "127.0.0.1")
	assertEnvVar(t, env, "PORT", "7233")
	assertEnvVar(t, env, "TEMPORAL_ADDRESS", "127.0.0.1:7233")

	// Named ingress "ui" prefixed.
	assertEnvVar(t, env, "UI_HOST", "127.0.0.1")
	assertEnvVar(t, env, "UI_PORT", "8080")
}

func TestBuildPrestartHookEnv_HasEgresses(t *testing.T) {
	ingresses := map[string]spec.Endpoint{
		"default": {Host: "127.0.0.1", Port: 8080, Protocol: spec.HTTP},
	}
	egresses := map[string]spec.Endpoint{
		"database": {Host: "127.0.0.1", Port: 5432, Protocol: spec.TCP,
			Attributes: map[string]any{"PGDATABASE": "orders"}},
	}

	env := server.BuildPrestartHookEnv("order-service", ingresses, egresses, "/tmp/os", "/tmp")

	// Has ingress.
	assertEnvVar(t, env, "HOST", "127.0.0.1")
	assertEnvVar(t, env, "PORT", "8080")

	// Has egress.
	assertEnvVar(t, env, "DATABASE_PGDATABASE", "orders")
	assertEnvVar(t, env, "DATABASE_HOST", "127.0.0.1")
}

func TestExpandTemplates(t *testing.T) {
	attrs := map[string]string{
		"RIG_TEMP_DIR": "/tmp/rig/abc/order-service",
		"PORT":         "8080",
		"HOST":         "127.0.0.1",
	}

	templates := []string{
		"-c", "${RIG_TEMP_DIR}/config.json",
		"--port", "${PORT}",
		"--host", "$HOST",
	}

	result := server.ExpandTemplates(templates, attrs)

	expected := []string{
		"-c", "/tmp/rig/abc/order-service/config.json",
		"--port", "8080",
		"--host", "127.0.0.1",
	}

	if len(result) != len(expected) {
		t.Fatalf("expected %d results, got %d", len(expected), len(result))
	}
	for i, want := range expected {
		if result[i] != want {
			t.Errorf("result[%d]: got %q, want %q", i, result[i], want)
		}
	}
}

func TestExpandTemplates_Empty(t *testing.T) {
	result := server.ExpandTemplates(nil, map[string]string{"FOO": "bar"})
	if result != nil {
		t.Errorf("expected nil for empty templates, got %v", result)
	}
}

func TestExpandTemplates_UnknownVarBecomesEmpty(t *testing.T) {
	result := server.ExpandTemplates(
		[]string{"${UNKNOWN_VAR}"},
		map[string]string{},
	)
	if result[0] != "" {
		t.Errorf("expected empty string for unknown var, got %q", result[0])
	}
}

func TestExpandTemplate_Single(t *testing.T) {
	attrs := map[string]string{
		"RIG_TEMP_DIR": "/tmp/test",
	}
	got := server.ExpandTemplate("${RIG_TEMP_DIR}/output.json", attrs)
	if got != "/tmp/test/output.json" {
		t.Errorf("got %q", got)
	}
}

func assertEnvVar(t *testing.T, env map[string]string, key, want string) {
	t.Helper()
	got, ok := env[key]
	if !ok {
		t.Errorf("missing env var %q (have: %v)", key, envKeys(env))
		return
	}
	if got != want {
		t.Errorf("env[%q] = %q, want %q", key, got, want)
	}
}

func envKeys(env map[string]string) []string {
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	return keys
}
