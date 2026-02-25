package service

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/matgreaves/rig/internal/spec"
)

func TestAdjustIngressEndpoints_Host(t *testing.T) {
	ingresses := map[string]spec.Endpoint{
		"default": {Host: "127.0.0.1", Port: 8080, Protocol: spec.HTTP},
	}
	specs := map[string]spec.IngressSpec{
		"default": {Protocol: spec.HTTP},
	}

	adjusted := adjustIngressEndpoints(ingresses, specs)

	if adjusted["default"].Host != "0.0.0.0" {
		t.Errorf("host = %q, want 0.0.0.0", adjusted["default"].Host)
	}
	// Port unchanged when no ContainerPort set.
	if adjusted["default"].Port != 8080 {
		t.Errorf("port = %d, want 8080", adjusted["default"].Port)
	}
}

func TestAdjustIngressEndpoints_ContainerPort(t *testing.T) {
	ingresses := map[string]spec.Endpoint{
		"default": {Host: "127.0.0.1", Port: 54321, Protocol: spec.HTTP},
	}
	specs := map[string]spec.IngressSpec{
		"default": {Protocol: spec.HTTP, ContainerPort: 80},
	}

	adjusted := adjustIngressEndpoints(ingresses, specs)

	if adjusted["default"].Host != "0.0.0.0" {
		t.Errorf("host = %q, want 0.0.0.0", adjusted["default"].Host)
	}
	if adjusted["default"].Port != 80 {
		t.Errorf("port = %d, want 80 (ContainerPort)", adjusted["default"].Port)
	}
}

func TestAdjustIngressEndpoints_Named(t *testing.T) {
	ingresses := map[string]spec.Endpoint{
		"http": {Host: "127.0.0.1", Port: 54321},
		"grpc": {Host: "127.0.0.1", Port: 54322},
	}
	specs := map[string]spec.IngressSpec{
		"http": {Protocol: spec.HTTP, ContainerPort: 80},
		"grpc": {Protocol: spec.GRPC, ContainerPort: 9090},
	}

	adjusted := adjustIngressEndpoints(ingresses, specs)

	if adjusted["http"].Port != 80 {
		t.Errorf("http port = %d, want 80", adjusted["http"].Port)
	}
	if adjusted["grpc"].Port != 9090 {
		t.Errorf("grpc port = %d, want 9090", adjusted["grpc"].Port)
	}
	for name, ep := range adjusted {
		if ep.Host != "0.0.0.0" {
			t.Errorf("%s host = %q, want 0.0.0.0", name, ep.Host)
		}
	}
}

func TestAdjustIngressEndpoints_DoesNotMutateOriginal(t *testing.T) {
	ingresses := map[string]spec.Endpoint{
		"default": {Host: "127.0.0.1", Port: 54321},
	}
	specs := map[string]spec.IngressSpec{
		"default": {Protocol: spec.HTTP, ContainerPort: 80},
	}

	_ = adjustIngressEndpoints(ingresses, specs)

	// Original should be unchanged.
	if ingresses["default"].Host != "127.0.0.1" {
		t.Error("original ingress was mutated")
	}
	if ingresses["default"].Port != 54321 {
		t.Error("original ingress port was mutated")
	}
}

func TestAdjustIngressEndpoints_TemplateAttrsPassThrough(t *testing.T) {
	ingresses := map[string]spec.Endpoint{
		"default": {
			Host:     "127.0.0.1",
			Port:     54321,
			Protocol: spec.TCP,
			Attributes: map[string]any{
				"PGHOST":     "${HOST}",
				"PGPORT":     "${PORT}",
				"PGDATABASE": "mydb",
			},
		},
	}
	specs := map[string]spec.IngressSpec{
		"default": {Protocol: spec.TCP, ContainerPort: 5432},
	}

	adjusted := adjustIngressEndpoints(ingresses, specs)

	ep := adjusted["default"]
	// Templates should pass through unchanged.
	if ep.Attributes["PGHOST"] != "${HOST}" {
		t.Errorf("PGHOST = %v, want ${HOST}", ep.Attributes["PGHOST"])
	}
	if ep.Attributes["PGPORT"] != "${PORT}" {
		t.Errorf("PGPORT = %v, want ${PORT}", ep.Attributes["PGPORT"])
	}
	if ep.Attributes["PGDATABASE"] != "mydb" {
		t.Errorf("PGDATABASE = %v, want mydb", ep.Attributes["PGDATABASE"])
	}
}

func TestAdjustEgressEndpoints(t *testing.T) {
	egresses := map[string]spec.Endpoint{
		"database": {Host: "127.0.0.1", Port: 5432, Protocol: spec.TCP},
		"cache":    {Host: "127.0.0.1", Port: 6379, Protocol: spec.TCP},
	}

	adjusted := adjustEgressEndpoints(egresses, "host.docker.internal")

	for name, ep := range adjusted {
		if ep.Host != "host.docker.internal" {
			t.Errorf("%s host = %q, want host.docker.internal", name, ep.Host)
		}
	}
	// Ports unchanged.
	if adjusted["database"].Port != 5432 {
		t.Errorf("database port = %d, want 5432", adjusted["database"].Port)
	}
}

func TestAdjustEgressEndpoints_TemplateAttrsPassThrough(t *testing.T) {
	egresses := map[string]spec.Endpoint{
		"database": {
			Host:     "127.0.0.1",
			Port:     5432,
			Protocol: spec.TCP,
			Attributes: map[string]any{
				"PGHOST":     "${HOST}",
				"PGPORT":     "${PORT}",
				"PGDATABASE": "mydb",
			},
		},
	}

	adjusted := adjustEgressEndpoints(egresses, "host.docker.internal")

	ep := adjusted["database"]
	// Templates should pass through unchanged.
	if ep.Attributes["PGHOST"] != "${HOST}" {
		t.Errorf("PGHOST = %v, want ${HOST}", ep.Attributes["PGHOST"])
	}
	if ep.Attributes["PGPORT"] != "${PORT}" {
		t.Errorf("PGPORT = %v, want ${PORT}", ep.Attributes["PGPORT"])
	}
	if ep.Attributes["PGDATABASE"] != "mydb" {
		t.Errorf("PGDATABASE = %v, want mydb", ep.Attributes["PGDATABASE"])
	}
}

func TestAdjustEgressEndpoints_DoesNotMutateOriginal(t *testing.T) {
	egresses := map[string]spec.Endpoint{
		"database": {Host: "127.0.0.1", Port: 5432},
	}

	_ = adjustEgressEndpoints(egresses, "host.docker.internal")

	if egresses["database"].Host != "127.0.0.1" {
		t.Error("original egress was mutated")
	}
}

func TestExpandAll(t *testing.T) {
	env := map[string]string{
		"HOST": "0.0.0.0",
		"PORT": "80",
	}

	result := expandAll([]string{"--listen=${HOST}:${PORT}", "--name=test"}, env)

	if result[0] != "--listen=0.0.0.0:80" {
		t.Errorf("got %q, want --listen=0.0.0.0:80", result[0])
	}
	if result[1] != "--name=test" {
		t.Errorf("got %q, want --name=test", result[1])
	}
}

func TestExpandAll_Nil(t *testing.T) {
	if result := expandAll(nil, nil); result != nil {
		t.Errorf("expected nil, got %v", result)
	}
}

func TestBuildPortBindings_ContainerPort(t *testing.T) {
	ingresses := map[string]spec.Endpoint{
		"default": {Host: "127.0.0.1", Port: 54321},
	}
	ingressSpecs := map[string]spec.IngressSpec{
		"default": {Protocol: spec.TCP, ContainerPort: 5432},
	}

	portBindings, exposedPorts := buildPortBindings(ingresses, ingressSpecs)

	if _, ok := exposedPorts["5432/tcp"]; !ok {
		t.Errorf("expected exposed port 5432/tcp, got %v", exposedPorts)
	}
	bindings := portBindings["5432/tcp"]
	if len(bindings) != 1 || bindings[0].HostPort != "54321" {
		t.Errorf("expected host port 54321, got %v", bindings)
	}
}

func TestBuildPortBindings_RigNativeApp(t *testing.T) {
	ingresses := map[string]spec.Endpoint{
		"default": {Host: "127.0.0.1", Port: 54321},
	}
	ingressSpecs := map[string]spec.IngressSpec{
		"default": {Protocol: spec.HTTP, ContainerPort: 0},
	}

	portBindings, exposedPorts := buildPortBindings(ingresses, ingressSpecs)

	if _, ok := exposedPorts["54321/tcp"]; !ok {
		t.Errorf("expected exposed port 54321/tcp, got %v", exposedPorts)
	}
	bindings := portBindings["54321/tcp"]
	if len(bindings) != 1 || bindings[0].HostPort != "54321" {
		t.Errorf("expected host port 54321, got %v", bindings)
	}
}

func TestEnvMapToSlice(t *testing.T) {
	env := map[string]string{"A": "1", "B": "2"}
	slice := envMapToSlice(env)

	if len(slice) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(slice))
	}
	found := map[string]bool{}
	for _, s := range slice {
		found[s] = true
	}
	if !found["A=1"] || !found["B=2"] {
		t.Errorf("unexpected slice: %v", slice)
	}
}

func TestDockerHostIP(t *testing.T) {
	ip := dockerHostIP()
	if !strings.Contains(ip, "docker") {
		t.Errorf("expected docker host IP, got %q", ip)
	}
}

func TestAdjustTempDirsInWiring(t *testing.T) {
	wiring := map[string]any{
		"ingresses": map[string]any{},
		"egresses":  map[string]any{},
		"temp_dir":  "/tmp/rig/abc123/myservice",
		"env_dir":   "/tmp/rig/abc123",
	}
	b, _ := json.Marshal(wiring)
	env := map[string]string{
		"RIG_WIRING": string(b),
	}

	adjustTempDirsInWiring(env)

	var got map[string]json.RawMessage
	if err := json.Unmarshal([]byte(env["RIG_WIRING"]), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	var tempDir, envDir string
	json.Unmarshal(got["temp_dir"], &tempDir)
	json.Unmarshal(got["env_dir"], &envDir)

	if tempDir != containerTempPath {
		t.Errorf("temp_dir = %q, want %q", tempDir, containerTempPath)
	}
	if envDir != containerEnvPath {
		t.Errorf("env_dir = %q, want %q", envDir, containerEnvPath)
	}
}

func TestAdjustTempDirsInWiring_NoWiring(t *testing.T) {
	env := map[string]string{}
	adjustTempDirsInWiring(env) // should not panic
	if _, ok := env["RIG_WIRING"]; ok {
		t.Error("RIG_WIRING should not be created when absent")
	}
}

func TestAdjustTempDirsInWiring_PreservesOtherFields(t *testing.T) {
	wiring := map[string]any{
		"ingresses": map[string]any{
			"default": map[string]any{"host": "0.0.0.0", "port": 8080},
		},
		"temp_dir": "/host/path/svc",
		"env_dir":  "/host/path",
	}
	b, _ := json.Marshal(wiring)
	env := map[string]string{
		"RIG_WIRING": string(b),
	}

	adjustTempDirsInWiring(env)

	var got map[string]json.RawMessage
	json.Unmarshal([]byte(env["RIG_WIRING"]), &got)

	if _, ok := got["ingresses"]; !ok {
		t.Error("ingresses field was lost")
	}
}
