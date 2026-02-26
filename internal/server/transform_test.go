package server

import (
	"testing"

	"github.com/matryer/is"
	"github.com/matgreaves/rig/internal/spec"
)

func TestInsertTestNode_Basic(t *testing.T) {
	is := is.New(t)

	env := &spec.Environment{
		Name: "test",
		Services: map[string]spec.Service{
			"api": {
				Type: "go",
				Ingresses: map[string]spec.IngressSpec{
					"default": {Protocol: spec.HTTP},
				},
			},
			"db": {
				Type: "postgres",
				Ingresses: map[string]spec.IngressSpec{
					"default": {Protocol: spec.TCP},
				},
			},
		},
	}

	InsertTestNode(env)

	testSvc, ok := env.Services["~test"]
	is.True(ok)
	is.Equal(testSvc.Type, "test")
	is.True(testSvc.Injected)
	is.Equal(len(testSvc.Egresses), 2) // api + db
	is.Equal(testSvc.Egresses["api"].Service, "api")
	is.Equal(testSvc.Egresses["api"].Ingress, "default")
	is.Equal(testSvc.Egresses["db"].Service, "db")
	is.Equal(testSvc.Egresses["db"].Ingress, "default")
}

func TestInsertTestNode_MultiIngress(t *testing.T) {
	is := is.New(t)

	env := &spec.Environment{
		Name: "test",
		Services: map[string]spec.Service{
			"temporal": {
				Type: "temporal",
				Ingresses: map[string]spec.IngressSpec{
					"default": {Protocol: spec.GRPC},
					"ui":      {Protocol: spec.HTTP},
				},
			},
		},
	}

	InsertTestNode(env)

	testSvc := env.Services["~test"]
	is.Equal(len(testSvc.Egresses), 2)
	is.Equal(testSvc.Egresses["temporal"].Service, "temporal")
	is.Equal(testSvc.Egresses["temporal"].Ingress, "default")
	is.Equal(testSvc.Egresses["temporal~ui"].Service, "temporal")
	is.Equal(testSvc.Egresses["temporal~ui"].Ingress, "ui")
}

func TestInsertTestNode_NoIngress(t *testing.T) {
	is := is.New(t)

	env := &spec.Environment{
		Name: "test",
		Services: map[string]spec.Service{
			"worker": {
				Type: "go",
				// No ingresses.
			},
		},
	}

	InsertTestNode(env)

	testSvc := env.Services["~test"]
	is.Equal(len(testSvc.Egresses), 0) // no-ingress services are skipped
}

func TestTransformObserve_BasicEdge(t *testing.T) {
	is := is.New(t)

	env := &spec.Environment{
		Name:    "test",
		Observe: true,
		Services: map[string]spec.Service{
			"api": {
				Type: "go",
				Ingresses: map[string]spec.IngressSpec{
					"default": {Protocol: spec.HTTP},
				},
				Egresses: map[string]spec.EgressSpec{
					"backend": {Service: "backend", Ingress: "default"},
				},
			},
			"backend": {
				Type: "go",
				Ingresses: map[string]spec.IngressSpec{
					"default": {Protocol: spec.HTTP},
				},
			},
		},
	}

	// Insert ~test first (as orchestrator does).
	InsertTestNode(env)
	TransformObserve(env)

	// Proxy node for api→backend edge.
	proxy, ok := env.Services["backend~proxy~api"]
	is.True(ok)
	is.Equal(proxy.Type, "proxy")
	is.True(proxy.Injected)
	is.Equal(proxy.Egresses["target"].Service, "backend")
	is.Equal(proxy.Egresses["target"].Ingress, "default")

	// api's egress should be retargeted to the proxy.
	apiSvc := env.Services["api"]
	is.Equal(apiSvc.Egresses["backend"].Service, "backend~proxy~api")
	is.Equal(apiSvc.Egresses["backend"].Ingress, "default")

	// ~test should have proxies for external access too.
	_, ok = env.Services["api~proxy~~test"]
	is.True(ok)
	_, ok = env.Services["backend~proxy~~test"]
	is.True(ok)
}

func TestTransformObserve_Disabled(t *testing.T) {
	is := is.New(t)

	env := &spec.Environment{
		Name:    "test",
		Observe: false,
		Services: map[string]spec.Service{
			"api": {
				Type: "go",
				Ingresses: map[string]spec.IngressSpec{
					"default": {Protocol: spec.HTTP},
				},
			},
		},
	}

	InsertTestNode(env)
	TransformObserve(env)

	// ~test exists but no proxies.
	_, ok := env.Services["~test"]
	is.True(ok)

	// No proxy nodes.
	for name, svc := range env.Services {
		if svc.Type == "proxy" {
			t.Errorf("unexpected proxy node %q when observe=false", name)
		}
	}
}

func TestTransformObserve_CustomEgressName(t *testing.T) {
	is := is.New(t)

	env := &spec.Environment{
		Name:    "test",
		Observe: true,
		Services: map[string]spec.Service{
			"api": {
				Type: "go",
				Ingresses: map[string]spec.IngressSpec{
					"default": {Protocol: spec.HTTP},
				},
				Egresses: map[string]spec.EgressSpec{
					"database": {Service: "db", Ingress: "default"},
				},
			},
			"db": {
				Type: "postgres",
				Ingresses: map[string]spec.IngressSpec{
					"default": {Protocol: spec.TCP},
				},
			},
		},
	}

	InsertTestNode(env)
	TransformObserve(env)

	// The egress name "database" is preserved on the source.
	apiSvc := env.Services["api"]
	is.Equal(apiSvc.Egresses["database"].Service, "db~proxy~api")
	is.Equal(apiSvc.Egresses["database"].Ingress, "default")
}

func TestTransformObserve_NonDefaultIngress(t *testing.T) {
	is := is.New(t)

	env := &spec.Environment{
		Name:    "test",
		Observe: true,
		Services: map[string]spec.Service{
			"temporal": {
				Type: "temporal",
				Ingresses: map[string]spec.IngressSpec{
					"default": {Protocol: spec.GRPC},
					"ui":      {Protocol: spec.HTTP},
				},
			},
		},
	}

	InsertTestNode(env)
	TransformObserve(env)

	// ~test has egresses to both ingresses. The proxy for the default
	// ingress uses the short name, the UI uses the long name.
	_, ok := env.Services["temporal~proxy~~test"]
	is.True(ok) // default ingress proxy

	_, ok = env.Services["temporal~ui~proxy~~test"]
	is.True(ok) // ui ingress proxy
}
