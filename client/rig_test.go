package rig_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	rig "github.com/matgreaves/rig/client"
	"github.com/matgreaves/rig/connect/httpx"
	"github.com/matgreaves/rig/server"
	"github.com/matgreaves/rig/server/service"
	"github.com/matgreaves/rig/testdata/services/echo"
)

// moduleRoot returns the module root by finding go.mod relative to the test
// working directory.
func moduleRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	dir := wd
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not find go.mod")
		}
		dir = parent
	}
}

// startTestServer creates an httptest.Server backed by a real server.Server
// with process, go, and client service types registered. Returns the server URL.
// Uses .rig/ in the module root for cache and logs.
func startTestServer(t *testing.T) string {
	t.Helper()
	reg := service.NewRegistry()
	reg.Register("process", service.Process{})
	reg.Register("go", service.Go{})
	reg.Register("client", service.Client{})

	rigDir := filepath.Join(moduleRoot(t), ".rig")
	s := server.NewServer(
		server.NewPortAllocator(),
		reg,
		t.TempDir(),
		0,      // idle timeout disabled
		rigDir,
	)
	ts := httptest.NewServer(s)
	t.Cleanup(ts.Close)
	return ts.URL
}

// TestUp runs all integration tests against a shared rig server. Each subtest
// creates its own environment in parallel — exactly how real users would use rig.
func TestUp(t *testing.T) {
	root := moduleRoot(t)
	serverURL := startTestServer(t)

	t.Run("GoService", func(t *testing.T) {
		t.Parallel()

		env := rig.Up(t, rig.Services{
			"echo": rig.Go(filepath.Join(root, "testdata", "services", "echo", "cmd")),
		}, rig.WithServer(serverURL), rig.WithTimeout(60*time.Second))

		client := httpx.New(env.Endpoint("echo"))
		resp, err := client.Get("/health")
		if err != nil {
			t.Fatalf("health check: %v", err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("health: %d, want 200", resp.StatusCode)
		}
	})

	t.Run("ProcessService", func(t *testing.T) {
		t.Parallel()

		// Build the echo binary first since "process" type needs a pre-built binary.
		echoBin := buildBinary(t, filepath.Join(root, "testdata", "services", "echo", "cmd"))

		env := rig.Up(t, rig.Services{
			"echo": rig.Process(echoBin),
		}, rig.WithServer(serverURL), rig.WithTimeout(30*time.Second))

		client := httpx.New(env.Endpoint("echo"))
		resp, err := client.Get("/health")
		if err != nil {
			t.Fatalf("health check: %v", err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("health: %d, want 200", resp.StatusCode)
		}
	})

	t.Run("WithDependency", func(t *testing.T) {
		t.Parallel()

		env := rig.Up(t, rig.Services{
			"db": rig.Go(filepath.Join(root, "testdata", "services", "tcpecho")).
				Ingress("default", rig.IngressTCP()),
			"api": rig.Go(filepath.Join(root, "testdata", "services", "echo", "cmd")).
				EgressAs("database", "db"),
		}, rig.WithServer(serverURL), rig.WithTimeout(60*time.Second))

		// API should be reachable.
		client := httpx.New(env.Endpoint("api"))
		resp, err := client.Get("/health")
		if err != nil {
			t.Fatalf("api health check: %v", err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("api health: %d, want 200", resp.StatusCode)
		}

		// DB should be reachable via TCP.
		conn, err := net.DialTimeout("tcp", env.Endpoint("db").Addr(), 2*time.Second)
		if err != nil {
			t.Fatalf("db dial: %v", err)
		}
		conn.Close()
	})

	t.Run("InitHook", func(t *testing.T) {
		t.Parallel()

		var hookCalled bool
		var wiringSnapshot rig.Wiring

		env := rig.Up(t, rig.Services{
			"echo": rig.Go(filepath.Join(root, "testdata", "services", "echo", "cmd")).
				InitHook(func(ctx context.Context, w rig.Wiring) error {
					hookCalled = true
					wiringSnapshot = w
					return nil
				}),
		}, rig.WithServer(serverURL), rig.WithTimeout(60*time.Second))

		if !hookCalled {
			t.Fatal("init hook was not called")
		}

		// Init hooks receive ingresses.
		if len(wiringSnapshot.Ingresses) == 0 {
			t.Error("init hook received no ingresses")
		}

		// Init hooks do NOT receive egresses.
		if len(wiringSnapshot.Egresses) != 0 {
			t.Errorf("init hook received egresses (should be empty): %v", wiringSnapshot.Egresses)
		}

		// Service should be reachable.
		client := httpx.New(env.Endpoint("echo"))
		resp, err := client.Get("/health")
		if err != nil {
			t.Fatalf("health check: %v", err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("health: %d, want 200", resp.StatusCode)
		}
	})

	t.Run("PrestartHook", func(t *testing.T) {
		t.Parallel()

		var wiringSnapshot rig.Wiring

		rig.Up(t, rig.Services{
			"db": rig.Go(filepath.Join(root, "testdata", "services", "tcpecho")).
				Ingress("default", rig.IngressTCP()),
			"api": rig.Go(filepath.Join(root, "testdata", "services", "echo", "cmd")).
				EgressAs("database", "db").
				PrestartHook(func(ctx context.Context, w rig.Wiring) error {
					wiringSnapshot = w
					return nil
				}),
		}, rig.WithServer(serverURL), rig.WithTimeout(60*time.Second))

		// Prestart hooks receive egresses.
		if _, ok := wiringSnapshot.Egresses["database"]; !ok {
			t.Error("prestart hook did not receive 'database' egress")
		}

		// Prestart hooks also receive ingresses.
		if len(wiringSnapshot.Ingresses) == 0 {
			t.Error("prestart hook received no ingresses")
		}
	})

	t.Run("NoIngressWorker", func(t *testing.T) {
		t.Parallel()

		// A service with no ingresses (worker pattern) should still become
		// ready. Verifies the lifecycle handles the no-health-check path.
		env := rig.Up(t, rig.Services{
			"worker": rig.Go(filepath.Join(root, "testdata", "services", "echo", "cmd")).
				NoIngress(),
		}, rig.WithServer(serverURL), rig.WithTimeout(60*time.Second))

		// No endpoints to hit — just verify the environment came up with
		// the worker listed.
		if _, ok := env.Services["worker"]; !ok {
			t.Error("worker service not in resolved environment")
		}
	})

	t.Run("FuncService", func(t *testing.T) {
		t.Parallel()

		env := rig.Up(t, rig.Services{
			"echo": rig.Func(echo.Run),
		}, rig.WithServer(serverURL), rig.WithTimeout(60*time.Second))

		client := httpx.New(env.Endpoint("echo"))
		resp, err := client.Get("/health")
		if err != nil {
			t.Fatalf("health check: %v", err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("health: %d, want 200", resp.StatusCode)
		}
	})

	t.Run("FuncServiceWithEgress", func(t *testing.T) {
		t.Parallel()

		env := rig.Up(t, rig.Services{
			"db": rig.Go(filepath.Join(root, "testdata", "services", "tcpecho")).
				Ingress("default", rig.IngressTCP()),
			"api": rig.Func(echo.Run).
				EgressAs("database", "db"),
		}, rig.WithServer(serverURL), rig.WithTimeout(60*time.Second))

		// API should be reachable.
		client := httpx.New(env.Endpoint("api"))
		resp, err := client.Get("/health")
		if err != nil {
			t.Fatalf("api health check: %v", err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("api health: %d, want 200", resp.StatusCode)
		}

		// DB should be reachable via TCP.
		conn, err := net.DialTimeout("tcp", env.Endpoint("db").Addr(), 2*time.Second)
		if err != nil {
			t.Fatalf("db dial: %v", err)
		}
		conn.Close()
	})

	t.Run("FuncServiceWithInitHook", func(t *testing.T) {
		t.Parallel()

		var hookCalled bool

		env := rig.Up(t, rig.Services{
			"echo": rig.Func(echo.Run).
				InitHook(func(ctx context.Context, w rig.Wiring) error {
					hookCalled = true
					return nil
				}),
		}, rig.WithServer(serverURL), rig.WithTimeout(60*time.Second))

		if !hookCalled {
			t.Fatal("init hook was not called")
		}

		client := httpx.New(env.Endpoint("echo"))
		resp, err := client.Get("/health")
		if err != nil {
			t.Fatalf("health check: %v", err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("health: %d, want 200", resp.StatusCode)
		}
	})
}

// TestWrappedTB verifies that env.T captures assertion failures as test.note
// events in the server's event log.
func TestWrappedTB(t *testing.T) {
	t.Parallel()
	serverURL := startTestServer(t)

	// Post a test.note event directly and verify it appears in the log.
	// We avoid using env.T.Errorf in the main test because it would mark
	// the test as failed. Instead we POST the event ourselves — the same
	// code path env.T uses internally.
	env := rig.Up(t, rig.Services{
		"echo": rig.Func(echo.Run),
	}, rig.WithServer(serverURL), rig.WithTimeout(60*time.Second))

	// Post a test.note event (same as env.T.Errorf would).
	payload, _ := json.Marshal(map[string]string{
		"type":  "test.note",
		"error": "simulated assertion: got 500, want 200",
	})
	noteResp, err := http.Post(
		fmt.Sprintf("%s/environments/%s/events", serverURL, env.ID),
		"application/json",
		strings.NewReader(string(payload)),
	)
	if err != nil {
		t.Fatalf("post test.note: %v", err)
	}
	noteResp.Body.Close()

	// Fetch the event log and verify the test.note event appears.
	resp, err := http.Get(fmt.Sprintf("%s/environments/%s/log", serverURL, env.ID))
	if err != nil {
		t.Fatalf("fetch log: %v", err)
	}
	defer resp.Body.Close()

	var events []struct {
		Type  string `json:"type"`
		Error string `json:"error,omitempty"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&events); err != nil {
		t.Fatalf("decode log: %v", err)
	}

	var found bool
	for _, e := range events {
		if e.Type == "test.note" && strings.Contains(e.Error, "simulated assertion") {
			found = true
			break
		}
	}
	if !found {
		t.Error("test.note event not found in event log")
	}
}

func TestEndpoint_Lookup(t *testing.T) {
	t.Parallel()
	env := &rig.Environment{
		Name: "test",
		Services: map[string]rig.ResolvedService{
			"api": {Ingresses: map[string]rig.Endpoint{
				"default": {Host: "127.0.0.1", Port: 8080, Protocol: rig.HTTP},
				"grpc":    {Host: "127.0.0.1", Port: 9090, Protocol: rig.GRPC},
			}},
			"db": {Ingresses: map[string]rig.Endpoint{
				"tcp": {Host: "127.0.0.1", Port: 5432, Protocol: rig.TCP},
			}},
		},
	}

	// Default ingress by name.
	ep := env.Endpoint("api")
	if ep.Port != 8080 {
		t.Errorf("api default port = %d, want 8080", ep.Port)
	}

	// Named ingress.
	ep = env.Endpoint("api", "grpc")
	if ep.Port != 9090 {
		t.Errorf("api grpc port = %d, want 9090", ep.Port)
	}

	// Single ingress shorthand — returns sole ingress even if not named "default".
	ep = env.Endpoint("db")
	if ep.Port != 5432 {
		t.Errorf("db port = %d, want 5432", ep.Port)
	}
}

func TestEndpoint_Lookup_PanicsOnMiss(t *testing.T) {
	t.Parallel()
	env := &rig.Environment{
		Name: "test",
		Services: map[string]rig.ResolvedService{
			"api": {Ingresses: map[string]rig.Endpoint{
				"default": {Host: "127.0.0.1", Port: 8080, Protocol: rig.HTTP},
			}},
		},
	}

	// Unknown service.
	assertPanics(t, "unknown service", func() {
		env.Endpoint("nonexistent")
	})

	// Unknown ingress.
	assertPanics(t, "unknown ingress", func() {
		env.Endpoint("api", "nonexistent")
	})
}

func TestEndpoint_ConnectionHelpers(t *testing.T) {
	t.Parallel()
	httpEP := rig.Endpoint{Host: "127.0.0.1", Port: 8080, Protocol: rig.HTTP}
	if got := httpEP.Addr(); got != "127.0.0.1:8080" {
		t.Errorf("HTTP Addr = %q, want 127.0.0.1:8080", got)
	}

	grpcEP := rig.Endpoint{Host: "127.0.0.1", Port: 9090, Protocol: rig.GRPC}
	if got := grpcEP.Addr(); got != "127.0.0.1:9090" {
		t.Errorf("GRPC Addr = %q, want 127.0.0.1:9090", got)
	}

	tcpEP := rig.Endpoint{Host: "127.0.0.1", Port: 5432, Protocol: rig.TCP}
	if got := tcpEP.Addr(); got != "127.0.0.1:5432" {
		t.Errorf("TCP Addr = %q, want 127.0.0.1:5432", got)
	}
}

func TestEndpoint_Attr(t *testing.T) {
	t.Parallel()
	ep := rig.Endpoint{
		Host:     "127.0.0.1",
		Port:     5432,
		Protocol: rig.TCP,
		Attributes: map[string]any{
			"PGDATABASE": "testdb",
			"PGUSER":     "postgres",
			"PORT":       5432,
		},
	}

	if got := ep.Attr("PGDATABASE"); got != "testdb" {
		t.Errorf("Attr(PGDATABASE) = %q, want testdb", got)
	}
	if got := ep.Attr("PORT"); got != "5432" {
		t.Errorf("Attr(PORT) = %q, want 5432", got)
	}
	if got := ep.Attr("MISSING"); got != "" {
		t.Errorf("Attr(MISSING) = %q, want empty", got)
	}
}

// --- helpers ---

func assertPanics(t *testing.T, name string, fn func()) {
	t.Helper()
	defer func() {
		if r := recover(); r == nil {
			t.Errorf("%s: expected panic, got none", name)
		}
	}()
	fn()
}

func buildBinary(t *testing.T, srcDir string) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), filepath.Base(srcDir))
	cmd := exec.Command("go", "build", "-o", bin, srcDir)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("build %s: %v\n%s", srcDir, err, out)
	}
	return bin
}
