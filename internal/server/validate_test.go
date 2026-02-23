package server_test

import (
	"strings"
	"testing"

	"github.com/matgreaves/rig/internal/server"
	"github.com/matgreaves/rig/internal/spec"
)

// validEnv returns a minimal valid environment for tests to modify.
func validEnv() spec.Environment {
	return spec.Environment{
		Name: "test-env",
		Services: map[string]spec.Service{
			"api": {
				Type: "process",
				Ingresses: map[string]spec.IngressSpec{
					"default": {Protocol: spec.HTTP},
				},
			},
		},
	}
}

func TestValidateEnvironment_Valid(t *testing.T) {
	env := validEnv()
	if errs := server.ValidateEnvironment(&env); len(errs) > 0 {
		t.Errorf("expected no errors, got: %v", errs)
	}
}

func TestValidateEnvironment_EmptyName(t *testing.T) {
	env := validEnv()
	env.Name = ""

	errs := server.ValidateEnvironment(&env)
	assertContainsError(t, errs, "environment name is required")
}

func TestValidateEnvironment_NoServices(t *testing.T) {
	env := spec.Environment{Name: "empty", Services: map[string]spec.Service{}}

	errs := server.ValidateEnvironment(&env)
	assertContainsError(t, errs, "at least one service")
}

func TestValidateEnvironment_UnknownServiceType(t *testing.T) {
	env := validEnv()
	env.Services["api"] = spec.Service{
		Type: "quantum-computer",
		Ingresses: map[string]spec.IngressSpec{
			"default": {Protocol: spec.HTTP},
		},
	}

	errs := server.ValidateEnvironment(&env)
	assertContainsError(t, errs, `unknown type "quantum-computer"`)
}

func TestValidateEnvironment_EmptyServiceType(t *testing.T) {
	env := validEnv()
	svc := env.Services["api"]
	svc.Type = ""
	env.Services["api"] = svc

	errs := server.ValidateEnvironment(&env)
	assertContainsError(t, errs, "type is required")
}

func TestValidateEnvironment_InvalidProtocol(t *testing.T) {
	env := validEnv()
	env.Services["api"] = spec.Service{
		Type: "process",
		Ingresses: map[string]spec.IngressSpec{
			"default": {Protocol: "websocket"},
		},
	}

	errs := server.ValidateEnvironment(&env)
	assertContainsError(t, errs, `invalid protocol "websocket"`)
}

func TestValidateEnvironment_ContainerPortOptional(t *testing.T) {
	// ContainerPort 0 is valid for container types — rig-native apps
	// that read RIG_DEFAULT_PORT don't need an explicit container port.
	env := validEnv()
	env.Services["db"] = spec.Service{
		Type: "container",
		Ingresses: map[string]spec.IngressSpec{
			"default": {Protocol: spec.TCP}, // no ContainerPort — valid
		},
	}

	errs := server.ValidateEnvironment(&env)
	if len(errs) > 0 {
		t.Errorf("unexpected validation errors: %v", errs)
	}
}

func TestValidateEnvironment_ContainerPortPresent(t *testing.T) {
	env := spec.Environment{
		Name: "test-env",
		Services: map[string]spec.Service{
			"db": {
				Type: "container",
				Ingresses: map[string]spec.IngressSpec{
					"default": {Protocol: spec.TCP, ContainerPort: 5432},
				},
			},
		},
	}

	if errs := server.ValidateEnvironment(&env); len(errs) > 0 {
		t.Errorf("expected no errors, got: %v", errs)
	}
}

func TestValidateEnvironment_EgressReferencesUnknownService(t *testing.T) {
	env := validEnv()
	svc := env.Services["api"]
	svc.Egresses = map[string]spec.EgressSpec{
		"database": {Service: "postgre"},
	}
	env.Services["api"] = svc

	errs := server.ValidateEnvironment(&env)
	assertContainsError(t, errs, `references unknown service "postgre"`)
}

func TestValidateEnvironment_EgressSuggestsCloseName(t *testing.T) {
	env := validEnv()
	env.Services["postgres"] = spec.Service{
		Type: "postgres",
		Ingresses: map[string]spec.IngressSpec{
			"default": {Protocol: spec.TCP, ContainerPort: 5432},
		},
	}
	svc := env.Services["api"]
	svc.Egresses = map[string]spec.EgressSpec{
		"database": {Service: "postgre"}, // typo
	}
	env.Services["api"] = svc

	errs := server.ValidateEnvironment(&env)
	assertContainsError(t, errs, `did you mean "postgres"`)
}

func TestValidateEnvironment_SelfReferencingEgress(t *testing.T) {
	env := validEnv()
	svc := env.Services["api"]
	svc.Egresses = map[string]spec.EgressSpec{
		"self": {Service: "api"},
	}
	env.Services["api"] = svc

	errs := server.ValidateEnvironment(&env)
	assertContainsError(t, errs, "cannot reference itself")
}

func TestValidateEnvironment_EgressReferencesNonexistentIngress(t *testing.T) {
	env := validEnv()
	env.Services["db"] = spec.Service{
		Type: "postgres",
		Ingresses: map[string]spec.IngressSpec{
			"default": {Protocol: spec.TCP, ContainerPort: 5432},
		},
	}
	svc := env.Services["api"]
	svc.Egresses = map[string]spec.EgressSpec{
		"database": {Service: "db", Ingress: "admin"},
	}
	env.Services["api"] = svc

	errs := server.ValidateEnvironment(&env)
	assertContainsError(t, errs, `has no ingress "admin"`)
}

func TestValidateEnvironment_SingleIngressShorthandWorks(t *testing.T) {
	env := spec.Environment{
		Name: "test-env",
		Services: map[string]spec.Service{
			"db": {
				Type: "postgres",
				Ingresses: map[string]spec.IngressSpec{
					"default": {Protocol: spec.TCP, ContainerPort: 5432},
				},
			},
			"api": {
				Type: "process",
				Ingresses: map[string]spec.IngressSpec{
					"default": {Protocol: spec.HTTP},
				},
				Egresses: map[string]spec.EgressSpec{
					"database": {Service: "db"}, // no ingress name — shorthand
				},
			},
		},
	}

	if errs := server.ValidateEnvironment(&env); len(errs) > 0 {
		t.Errorf("expected no errors, got: %v", errs)
	}
}

func TestValidateEnvironment_SingleIngressShorthandFailsMultiple(t *testing.T) {
	env := spec.Environment{
		Name: "test-env",
		Services: map[string]spec.Service{
			"temporal": {
				Type: "temporal",
				Ingresses: map[string]spec.IngressSpec{
					"frontend": {Protocol: spec.GRPC},
					"ui":       {Protocol: spec.HTTP},
				},
			},
			"api": {
				Type: "process",
				Ingresses: map[string]spec.IngressSpec{
					"default": {Protocol: spec.HTTP},
				},
				Egresses: map[string]spec.EgressSpec{
					"temporal": {Service: "temporal"}, // ambiguous — 2 ingresses
				},
			},
		},
	}

	errs := server.ValidateEnvironment(&env)
	assertContainsError(t, errs, "has 2 ingresses")
}

func TestValidateEnvironment_CycleDetection(t *testing.T) {
	env := spec.Environment{
		Name: "cycle-test",
		Services: map[string]spec.Service{
			"a": {
				Type: "process",
				Ingresses: map[string]spec.IngressSpec{
					"default": {Protocol: spec.HTTP},
				},
				Egresses: map[string]spec.EgressSpec{
					"to-b": {Service: "b"},
				},
			},
			"b": {
				Type: "process",
				Ingresses: map[string]spec.IngressSpec{
					"default": {Protocol: spec.HTTP},
				},
				Egresses: map[string]spec.EgressSpec{
					"to-c": {Service: "c"},
				},
			},
			"c": {
				Type: "process",
				Ingresses: map[string]spec.IngressSpec{
					"default": {Protocol: spec.HTTP},
				},
				Egresses: map[string]spec.EgressSpec{
					"to-a": {Service: "a"},
				},
			},
		},
	}

	errs := server.ValidateEnvironment(&env)
	assertContainsError(t, errs, "cycle detected")
	// Verify the cycle path includes all three services.
	for _, err := range errs {
		if strings.Contains(err, "cycle detected") {
			if !strings.Contains(err, "a") || !strings.Contains(err, "b") || !strings.Contains(err, "c") {
				t.Errorf("cycle error should mention all services in the cycle, got: %s", err)
			}
		}
	}
}

func TestValidateEnvironment_TwoNodeCycle(t *testing.T) {
	env := spec.Environment{
		Name: "cycle-test",
		Services: map[string]spec.Service{
			"a": {
				Type: "process",
				Ingresses: map[string]spec.IngressSpec{
					"default": {Protocol: spec.HTTP},
				},
				Egresses: map[string]spec.EgressSpec{
					"to-b": {Service: "b"},
				},
			},
			"b": {
				Type: "process",
				Ingresses: map[string]spec.IngressSpec{
					"default": {Protocol: spec.HTTP},
				},
				Egresses: map[string]spec.EgressSpec{
					"to-a": {Service: "a"},
				},
			},
		},
	}

	errs := server.ValidateEnvironment(&env)
	assertContainsError(t, errs, "cycle detected")
}

func TestValidateEnvironment_NoCycleFalsePositive(t *testing.T) {
	// Diamond dependency: api → db, api → cache, worker → db
	// No cycle — just shared dependencies.
	env := spec.Environment{
		Name: "diamond",
		Services: map[string]spec.Service{
			"db": {
				Type: "postgres",
				Ingresses: map[string]spec.IngressSpec{
					"default": {Protocol: spec.TCP, ContainerPort: 5432},
				},
			},
			"cache": {
				Type: "redis",
				Ingresses: map[string]spec.IngressSpec{
					"default": {Protocol: spec.TCP, ContainerPort: 6379},
				},
			},
			"api": {
				Type: "process",
				Ingresses: map[string]spec.IngressSpec{
					"default": {Protocol: spec.HTTP},
				},
				Egresses: map[string]spec.EgressSpec{
					"database": {Service: "db"},
					"cache":    {Service: "cache"},
				},
			},
			"worker": {
				Type: "process",
				Ingresses: map[string]spec.IngressSpec{
					"default": {Protocol: spec.HTTP},
				},
				Egresses: map[string]spec.EgressSpec{
					"database": {Service: "db"},
				},
			},
		},
	}

	if errs := server.ValidateEnvironment(&env); len(errs) > 0 {
		t.Errorf("expected no errors for diamond dependency, got: %v", errs)
	}
}

func TestValidateEnvironment_MultipleErrors(t *testing.T) {
	// Environment with several problems — all should be reported.
	env := spec.Environment{
		Name: "",
		Services: map[string]spec.Service{
			"api": {
				Type: "mystery",
				Ingresses: map[string]spec.IngressSpec{
					"default": {Protocol: "ftp"},
				},
				Egresses: map[string]spec.EgressSpec{
					"self":    {Service: "api"},
					"missing": {Service: "nope"},
				},
			},
		},
	}

	errs := server.ValidateEnvironment(&env)
	if len(errs) < 4 {
		t.Errorf("expected at least 4 errors, got %d: %v", len(errs), errs)
	}

	assertContainsError(t, errs, "environment name is required")
	assertContainsError(t, errs, "unknown type")
	assertContainsError(t, errs, "invalid protocol")
	assertContainsError(t, errs, "cannot reference itself")
	assertContainsError(t, errs, "unknown service")
}

func TestValidateEnvironment_ServiceWithNoIngresses(t *testing.T) {
	// A service with no ingresses is valid (e.g. a worker, script, cron job).
	env := spec.Environment{
		Name: "workers",
		Services: map[string]spec.Service{
			"worker": {
				Type: "process",
				// No ingresses — this is fine.
			},
		},
	}

	if errs := server.ValidateEnvironment(&env); len(errs) > 0 {
		t.Errorf("expected no errors, got: %v", errs)
	}
}

func TestValidateEnvironment_EgressToServiceWithNoIngresses(t *testing.T) {
	// An egress to a service with no ingresses should error.
	env := spec.Environment{
		Name: "test",
		Services: map[string]spec.Service{
			"worker": {
				Type: "process",
				// No ingresses.
			},
			"api": {
				Type: "process",
				Ingresses: map[string]spec.IngressSpec{
					"default": {Protocol: spec.HTTP},
				},
				Egresses: map[string]spec.EgressSpec{
					"backend": {Service: "worker"},
				},
			},
		},
	}

	errs := server.ValidateEnvironment(&env)
	assertContainsError(t, errs, `target service "worker" has no ingresses`)
}

func TestResolveDefaults_DoesNotAddDefaultIngress(t *testing.T) {
	env := spec.Environment{
		Name: "test",
		Services: map[string]spec.Service{
			"worker": {Type: "process"},
		},
	}

	server.ResolveDefaults(&env)

	svc := env.Services["worker"]
	if len(svc.Ingresses) != 0 {
		t.Errorf("expected 0 ingresses (no auto-default), got %d", len(svc.Ingresses))
	}
}

func TestResolveDefaults_ResolvesEgressShorthand(t *testing.T) {
	env := spec.Environment{
		Name: "test",
		Services: map[string]spec.Service{
			"db": {
				Type: "postgres",
				Ingresses: map[string]spec.IngressSpec{
					"default": {Protocol: spec.TCP, ContainerPort: 5432},
				},
			},
			"api": {
				Type: "process",
				Egresses: map[string]spec.EgressSpec{
					"database": {Service: "db"}, // no ingress name
				},
			},
		},
	}

	server.ResolveDefaults(&env)

	egress := env.Services["api"].Egresses["database"]
	if egress.Ingress != "default" {
		t.Errorf("expected egress ingress to be resolved to 'default', got %q", egress.Ingress)
	}
}

func TestResolveDefaults_DoesNotResolveAmbiguousEgress(t *testing.T) {
	env := spec.Environment{
		Name: "test",
		Services: map[string]spec.Service{
			"temporal": {
				Type: "temporal",
				Ingresses: map[string]spec.IngressSpec{
					"frontend": {Protocol: spec.GRPC},
					"ui":       {Protocol: spec.HTTP},
				},
			},
			"api": {
				Type: "process",
				Egresses: map[string]spec.EgressSpec{
					"temporal": {Service: "temporal"}, // ambiguous
				},
			},
		},
	}

	server.ResolveDefaults(&env)

	egress := env.Services["api"].Egresses["temporal"]
	if egress.Ingress != "" {
		t.Errorf("expected egress ingress to remain empty for ambiguous target, got %q", egress.Ingress)
	}
}

func TestResolveDefaults_ResolvesDefaultIngressAmongMultiple(t *testing.T) {
	// When a target has multiple ingresses but one is named "default",
	// the egress should auto-resolve to "default".
	env := spec.Environment{
		Name: "test",
		Services: map[string]spec.Service{
			"temporal": {
				Type: "temporal",
				Ingresses: map[string]spec.IngressSpec{
					"default": {Protocol: spec.GRPC},
					"ui":      {Protocol: spec.HTTP},
				},
			},
			"api": {
				Type: "process",
				Egresses: map[string]spec.EgressSpec{
					"temporal": {Service: "temporal"}, // no ingress specified
				},
			},
		},
	}

	server.ResolveDefaults(&env)

	egress := env.Services["api"].Egresses["temporal"]
	if egress.Ingress != "default" {
		t.Errorf("expected egress ingress to resolve to 'default', got %q", egress.Ingress)
	}
}

func TestValidateEnvironment_DefaultIngressFallbackValid(t *testing.T) {
	// Egress to a service with multiple ingresses should pass validation
	// when one is named "default".
	env := spec.Environment{
		Name: "test-env",
		Services: map[string]spec.Service{
			"temporal": {
				Type: "temporal",
				Ingresses: map[string]spec.IngressSpec{
					"default": {Protocol: spec.GRPC},
					"ui":      {Protocol: spec.HTTP},
				},
			},
			"api": {
				Type: "process",
				Ingresses: map[string]spec.IngressSpec{
					"default": {Protocol: spec.HTTP},
				},
				Egresses: map[string]spec.EgressSpec{
					"temporal": {Service: "temporal"}, // no ingress — should resolve to "default"
				},
			},
		},
	}

	if errs := server.ValidateEnvironment(&env); len(errs) > 0 {
		t.Errorf("expected no errors, got: %v", errs)
	}
}

func TestResolveDefaults_PreservesExplicitIngresses(t *testing.T) {
	env := spec.Environment{
		Name: "test",
		Services: map[string]spec.Service{
			"api": {
				Type: "process",
				Ingresses: map[string]spec.IngressSpec{
					"grpc": {Protocol: spec.GRPC},
					"http": {Protocol: spec.HTTP},
				},
			},
		},
	}

	server.ResolveDefaults(&env)

	svc := env.Services["api"]
	if len(svc.Ingresses) != 2 {
		t.Fatalf("expected 2 ingresses, got %d", len(svc.Ingresses))
	}
	if _, ok := svc.Ingresses["grpc"]; !ok {
		t.Error("grpc ingress missing")
	}
	if _, ok := svc.Ingresses["http"]; !ok {
		t.Error("http ingress missing")
	}
}

func assertContainsError(t *testing.T, errs []string, substr string) {
	t.Helper()
	for _, err := range errs {
		if strings.Contains(err, substr) {
			return
		}
	}
	t.Errorf("expected an error containing %q, got: %v", substr, errs)
}
