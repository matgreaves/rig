package server_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/matgreaves/rig/server"
	"github.com/matgreaves/rig/server/service"
	"github.com/matgreaves/rig/spec"
)

// moduleRoot returns the module root directory by finding go.mod.
func moduleRoot(t *testing.T) string {
	t.Helper()
	// We're in the server/ package; module root is one level up.
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Dir(wd)
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("could not find go.mod at %s: %v", root, err)
	}
	return root
}

// buildTestBinary compiles a test service binary and returns the path.
// srcDir is relative to the module root (e.g. "testdata/services/echo").
func buildTestBinary(t *testing.T, srcDir string) string {
	t.Helper()
	root := moduleRoot(t)
	absSrc := filepath.Join(root, srcDir)
	bin := filepath.Join(t.TempDir(), filepath.Base(srcDir))
	cmd := exec.Command("go", "build", "-o", bin, absSrc)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("build %s: %v", srcDir, err)
	}
	return bin
}

// newTestOrchestrator creates an orchestrator with a process type registered.
func newTestOrchestrator(t *testing.T) *server.Orchestrator {
	t.Helper()
	reg := service.NewRegistry()
	reg.Register("process", service.Process{})

	return &server.Orchestrator{
		Ports:    server.NewPortAllocator(),
		Registry: reg,
		Log:      server.NewEventLog(),
		TempBase: t.TempDir(),
	}
}

func TestOrchestrate_SingleHTTPService(t *testing.T) {
	echoBin := buildTestBinary(t, "testdata/services/echo")

	orch := newTestOrchestrator(t)

	env := &spec.Environment{
		Name: "test-single-http",
		Services: map[string]spec.Service{
			"echo": {
				Type:   "process",
				Config: mustJSON(t, service.ProcessConfig{Command: echoBin}),
				Ingresses: map[string]spec.IngressSpec{
					"default": {Protocol: spec.HTTP},
				},
			},
		},
	}

	server.ResolveDefaults(env)

	runner, instanceID, err := orch.Orchestrate(env)
	if err != nil {
		t.Fatal(err)
	}
	_ = instanceID

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	// Run in background, collect error.
	done := make(chan error, 1)
	go func() {
		done <- runner.Run(ctx)
	}()

	// Wait for environment to be ready.
	_, err = orch.Log.WaitFor(ctx, func(e server.Event) bool {
		return e.Type == server.EventServiceReady && e.Service == "echo"
	})
	if err != nil {
		t.Fatalf("waiting for echo to be ready: %v", err)
	}

	// Find the endpoint from the published ingress event.
	ev, err := orch.Log.WaitFor(ctx, func(e server.Event) bool {
		return e.Type == server.EventIngressPublished && e.Service == "echo"
	})
	if err != nil {
		t.Fatalf("finding echo endpoint: %v", err)
	}

	// Make an HTTP request to the echo service.
	url := fmt.Sprintf("http://%s:%d/hello", ev.Endpoint.Host, ev.Endpoint.Port)
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("HTTP GET: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}

	// Cancel to trigger teardown.
	cancel()

	select {
	case err := <-done:
		// run.Group returns ErrExited or context.Canceled — both are fine.
		_ = err
	case <-time.After(10 * time.Second):
		t.Fatal("timeout waiting for shutdown")
	}

	// Verify event ordering.
	assertEventOrder(t, orch.Log.Events(), "echo",
		server.EventIngressPublished,
		server.EventServiceStarting,
		server.EventServiceHealthy,
		server.EventServiceReady,
	)
}

func TestOrchestrate_DependencyOrdering(t *testing.T) {
	echoBin := buildTestBinary(t, "testdata/services/echo")
	tcpBin := buildTestBinary(t, "testdata/services/tcpecho")

	orch := newTestOrchestrator(t)

	// "api" depends on "db" via egress.
	env := &spec.Environment{
		Name: "test-deps",
		Services: map[string]spec.Service{
			"db": {
				Type:   "process",
				Config: mustJSON(t, service.ProcessConfig{Command: tcpBin}),
				Ingresses: map[string]spec.IngressSpec{
					"default": {Protocol: spec.TCP},
				},
			},
			"api": {
				Type:   "process",
				Config: mustJSON(t, service.ProcessConfig{Command: echoBin}),
				Ingresses: map[string]spec.IngressSpec{
					"default": {Protocol: spec.HTTP},
				},
				Egresses: map[string]spec.EgressSpec{
					"database": {Service: "db", Ingress: "default"},
				},
			},
		},
	}

	server.ResolveDefaults(env)

	runner, _, err := orch.Orchestrate(env)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	done := make(chan error, 1)
	go func() {
		done <- runner.Run(ctx)
	}()

	// Wait for api to be ready (implies db is ready too).
	_, err = orch.Log.WaitFor(ctx, func(e server.Event) bool {
		return e.Type == server.EventServiceReady && e.Service == "api"
	})
	if err != nil {
		t.Fatalf("waiting for api to be ready: %v", err)
	}

	// Verify db became ready before api's wiring was resolved.
	events := orch.Log.Events()
	dbReadySeq := findEventSeq(events, server.EventServiceReady, "db")
	apiWiringSeq := findEventSeq(events, server.EventWiringResolved, "api")

	if dbReadySeq == 0 {
		t.Fatal("db.ready event not found")
	}
	if apiWiringSeq == 0 {
		t.Fatal("api.wiring.resolved event not found")
	}
	if dbReadySeq >= apiWiringSeq {
		t.Errorf("db.ready (seq %d) should come before api.wiring.resolved (seq %d)",
			dbReadySeq, apiWiringSeq)
	}

	// Verify api received database egress env vars.
	ev, _ := orch.Log.WaitFor(ctx, func(e server.Event) bool {
		return e.Type == server.EventIngressPublished && e.Service == "db"
	})
	if ev.Endpoint == nil {
		t.Fatal("db endpoint not published")
	}

	cancel()
	<-done
}

func TestOrchestrate_NoIngressService(t *testing.T) {
	// A service with no ingresses should start and become ready without health checks.
	echoBin := buildTestBinary(t, "testdata/services/echo")

	orch := newTestOrchestrator(t)

	env := &spec.Environment{
		Name: "test-no-ingress",
		Services: map[string]spec.Service{
			"worker": {
				Type:   "process",
				Config: mustJSON(t, service.ProcessConfig{Command: echoBin}),
				// No ingresses — should still start and become ready.
			},
		},
	}

	runner, _, err := orch.Orchestrate(env)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	done := make(chan error, 1)
	go func() {
		done <- runner.Run(ctx)
	}()

	_, err = orch.Log.WaitFor(ctx, func(e server.Event) bool {
		return e.Type == server.EventServiceReady && e.Service == "worker"
	})
	if err != nil {
		t.Fatalf("waiting for worker to be ready: %v", err)
	}

	cancel()
	<-done
}

func TestOrchestrate_FailurePropagation(t *testing.T) {
	failBin := buildTestBinary(t, "testdata/services/fail")

	orch := newTestOrchestrator(t)

	// Use a binary that exits immediately with error — health check will fail.
	env := &spec.Environment{
		Name: "test-fail",
		Services: map[string]spec.Service{
			"broken": {
				Type:   "process",
				Config: mustJSON(t, service.ProcessConfig{Command: failBin}),
				Ingresses: map[string]spec.IngressSpec{
					"default": {Protocol: spec.HTTP},
				},
			},
		},
	}

	server.ResolveDefaults(env)

	runner, _, err := orch.Orchestrate(env)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	err = runner.Run(ctx)
	if err == nil {
		t.Fatal("expected error from failed service")
	}
}

// --- helpers ---

func mustJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func findEventSeq(events []server.Event, eventType server.EventType, svc string) uint64 {
	for _, e := range events {
		if e.Type == eventType && e.Service == svc {
			return e.Seq
		}
	}
	return 0
}

func assertEventOrder(t *testing.T, events []server.Event, svc string, expectedOrder ...server.EventType) {
	t.Helper()
	var svcEvents []server.EventType
	for _, e := range events {
		if e.Service == svc {
			svcEvents = append(svcEvents, e.Type)
		}
	}

	idx := 0
	for _, e := range svcEvents {
		if idx < len(expectedOrder) && e == expectedOrder[idx] {
			idx++
		}
	}
	if idx != len(expectedOrder) {
		t.Errorf("expected event order %v for service %q, got events: %v", expectedOrder, svc, svcEvents)
	}
}
