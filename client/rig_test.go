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
	reg.Register("container", service.Container{})
	reg.Register("postgres", service.Postgres{})

	rigDir := filepath.Join(moduleRoot(t), ".rig")
	s := server.NewServer(
		server.NewPortAllocator(),
		reg,
		t.TempDir(),
		0, // idle timeout disabled
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

	t.Run("Container", func(t *testing.T) {
		t.Parallel()

		env := rig.Up(t, rig.Services{
			"nginx": rig.Container("nginx:alpine").Port(80),
		}, rig.WithServer(serverURL), rig.WithTimeout(60*time.Second))

		ep := env.Endpoint("nginx")
		resp, err := http.Get("http://" + ep.Addr() + "/")
		if err != nil {
			t.Fatalf("nginx request: %v", err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("nginx status: %d, want 200", resp.StatusCode)
		}
	})

	t.Run("Postgres", func(t *testing.T) {
		t.Parallel()

		env := rig.Up(t, rig.Services{
			"db": rig.Postgres(),
		}, rig.WithServer(serverURL), rig.WithTimeout(120*time.Second))

		ep := env.Endpoint("db")

		// Verify TCP connectivity.
		conn, err := net.DialTimeout("tcp", ep.Addr(), 5*time.Second)
		if err != nil {
			t.Fatalf("postgres dial: %v", err)
		}
		conn.Close()

		// Verify endpoint attributes.
		if got := ep.Attr("PGDATABASE"); got != "db" {
			t.Errorf("PGDATABASE = %q, want db", got)
		}
		if got := ep.Attr("PGUSER"); got != "postgres" {
			t.Errorf("PGUSER = %q, want postgres", got)
		}
		if got := ep.Attr("PGPASSWORD"); got != "postgres" {
			t.Errorf("PGPASSWORD = %q, want postgres", got)
		}
		if got := ep.Attr("PGHOST"); got != "127.0.0.1" {
			t.Errorf("PGHOST = %q, want 127.0.0.1", got)
		}
		if got := ep.Attr("PGPORT"); got == "" {
			t.Error("PGPORT is empty")
		}
	})

	t.Run("PostgresInitSQL_BadSQL", func(t *testing.T) {
		t.Parallel()

		_, err := rig.TryUp(t, rig.Services{
			"db": rig.Postgres().InitSQL("INSERT INTO nonexistent_table VALUES (1)"),
		}, rig.WithServer(serverURL), rig.WithTimeout(120*time.Second))
		if err == nil {
			t.Fatal("expected Up to fail due to bad SQL")
		}

		t.Logf("captured failure: %s", err)
	})

	t.Run("PostgresInitSQL", func(t *testing.T) {
		t.Parallel()

		// Use a second InitSQL to verify the first one ran — inserting into
		// the table created by the first statement proves both executed
		// in order. A follow-up InitHook verifies from the client side.
		var initHookRan bool

		env := rig.Up(t, rig.Services{
			"db": rig.Postgres().
				InitSQL(
					"CREATE TABLE test_init (id INT PRIMARY KEY, name TEXT NOT NULL)",
					"INSERT INTO test_init VALUES (1, 'hello')",
				).
				InitHook(func(ctx context.Context, w rig.Wiring) error {
					initHookRan = true
					return nil
				}),
		}, rig.WithServer(serverURL), rig.WithTimeout(120*time.Second))

		if !initHookRan {
			t.Fatal("init hook was not called after InitSQL")
		}

		// Verify service is reachable.
		ep := env.Endpoint("db")
		conn, err := net.DialTimeout("tcp", ep.Addr(), 5*time.Second)
		if err != nil {
			t.Fatalf("postgres dial: %v", err)
		}
		conn.Close()
	})

	t.Run("UserAPI", func(t *testing.T) {
		t.Parallel()

		root := moduleRoot(t)
		env := rig.Up(t, rig.Services{
			"db": rig.Postgres().InitSQL(
				"CREATE TABLE users (id SERIAL PRIMARY KEY, name TEXT NOT NULL)",
			),
			"api": rig.Go(filepath.Join(root, "testdata", "services", "userapi")).
				Egress("db"),
		}, rig.WithServer(serverURL))

		api := httpx.New(env.Endpoint("api"))

		// Create a user.
		resp, err := api.Post("/users", "application/json",
			strings.NewReader(`{"name":"Alice"}`))
		if err != nil {
			t.Fatalf("create user: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("create user: status %d, want 201", resp.StatusCode)
		}
		var created user
		if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
			t.Fatalf("decode created user: %v", err)
		}
		if created.Name != "Alice" || created.ID == 0 {
			t.Fatalf("created user = %+v, want name=Alice id>0", created)
		}

		// Read the user back.
		resp2, err := api.Get(fmt.Sprintf("/users/%d", created.ID))
		if err != nil {
			t.Fatalf("get user: %v", err)
		}
		defer resp2.Body.Close()
		if resp2.StatusCode != http.StatusOK {
			t.Fatalf("get user: status %d, want 200", resp2.StatusCode)
		}
		var fetched user
		if err := json.NewDecoder(resp2.Body).Decode(&fetched); err != nil {
			t.Fatalf("decode fetched user: %v", err)
		}
		if fetched != created {
			t.Fatalf("fetched user = %+v, want %+v", fetched, created)
		}

		// Delete the user.
		req, _ := http.NewRequest(http.MethodDelete, fmt.Sprintf("/users/%d", created.ID), nil)
		resp3, err := api.Do(req)
		if err != nil {
			t.Fatalf("delete user: %v", err)
		}
		resp3.Body.Close()
		if resp3.StatusCode != http.StatusNoContent {
			t.Fatalf("delete user: status %d, want 204", resp3.StatusCode)
		}

		// Confirm it's gone.
		resp4, err := api.Get(fmt.Sprintf("/users/%d", created.ID))
		if err != nil {
			t.Fatalf("get deleted user: %v", err)
		}
		resp4.Body.Close()
		if resp4.StatusCode != http.StatusNotFound {
			t.Fatalf("get deleted user: status %d, want 404", resp4.StatusCode)
		}
	})

	t.Run("ContainerExecHook", func(t *testing.T) {
		t.Parallel()

		// Use nginx:alpine which has a working HTTP server — this ensures
		// the container is healthy (HTTP check passes) before exec hooks run.
		// The exec hook writes a file; a client-side init hook verifies it ran.
		var verified bool
		env := rig.Up(t, rig.Services{
			"box": rig.Container("nginx:alpine").Port(80).
				Exec("sh", "-c", "echo hello > /tmp/exec-test").
				InitHook(func(ctx context.Context, w rig.Wiring) error {
					verified = true
					return nil
				}),
		}, rig.WithServer(serverURL), rig.WithTimeout(60*time.Second))

		if !verified {
			t.Fatal("client-side init hook was not called (exec hook may have failed)")
		}

		if _, ok := env.Services["box"]; !ok {
			t.Error("box service not in resolved environment")
		}
	})

	t.Run("ContainerExecHookFailure", func(t *testing.T) {
		t.Parallel()

		// Exec a command that will fail (nonexistent binary).
		_, err := rig.TryUp(t, rig.Services{
			"box": rig.Container("nginx:alpine").Port(80).
				Exec("nonexistent-binary"),
		}, rig.WithServer(serverURL), rig.WithTimeout(60*time.Second))
		if err == nil {
			t.Fatal("expected Up to fail due to exec hook error")
		}
		t.Logf("captured failure: %s", err)
	})

	t.Run("ContainerExecHookNoIngress", func(t *testing.T) {
		t.Parallel()

		// A no-ingress container with an exec hook. Without waitForContainer,
		// the exec hook races with container creation and fails because the
		// container doesn't exist yet when docker exec is called.
		env := rig.Up(t, rig.Services{
			"box": rig.Container("alpine:latest").
				Cmd("sh", "-c", "sleep 300").
				NoIngress().
				Exec("sh", "-c", "echo hello > /tmp/exec-test"),
		}, rig.WithServer(serverURL), rig.WithTimeout(60*time.Second))

		if _, ok := env.Services["box"]; !ok {
			t.Error("box service not in resolved environment")
		}
	})

	t.Run("InitHookFailure", func(t *testing.T) {
		t.Parallel()

		_, err := rig.TryUp(t, rig.Services{
			"echo": rig.Func(echo.Run).
				InitHook(func(ctx context.Context, w rig.Wiring) error {
					return fmt.Errorf("deliberate init failure")
				}),
		}, rig.WithServer(serverURL))
		if err == nil {
			t.Fatal("expected Up to fail due to init hook error")
		}
		if !strings.Contains(err.Error(), "deliberate init failure") {
			t.Errorf("error does not mention hook failure: %v", err)
		}
	})

	t.Run("PrestartHookFailure", func(t *testing.T) {
		t.Parallel()

		_, err := rig.TryUp(t, rig.Services{
			"db": rig.Func(echo.Run),
			"api": rig.Func(echo.Run).
				Egress("db").
				PrestartHook(func(ctx context.Context, w rig.Wiring) error {
					return fmt.Errorf("deliberate prestart failure")
				}),
		}, rig.WithServer(serverURL))
		if err == nil {
			t.Fatal("expected Up to fail due to prestart hook error")
		}
		if !strings.Contains(err.Error(), "deliberate prestart failure") {
			t.Errorf("error does not mention hook failure: %v", err)
		}
	})

	t.Run("StartupTimeout", func(t *testing.T) {
		t.Parallel()

		// A Func that blocks without listening — the health check will
		// never pass, so Up should fail when the timeout expires.
		start := time.Now()
		_, err := rig.TryUp(t, rig.Services{
			"stuck": rig.Func(func(ctx context.Context) error {
				<-ctx.Done()
				return nil
			}),
		}, rig.WithServer(serverURL), rig.WithTimeout(3*time.Second))
		elapsed := time.Since(start)

		if err == nil {
			t.Fatal("expected Up to fail due to timeout")
		}
		// Should fail around 3s, not hang.
		if elapsed > 10*time.Second {
			t.Errorf("timeout took too long: %v (want ~3s)", elapsed)
		}
	})

	t.Run("ServiceCrash", func(t *testing.T) {
		t.Parallel()

		// The fail service exits immediately with an error. The
		// environment should fail with a clear message.
		root := moduleRoot(t)
		_, err := rig.TryUp(t, rig.Services{
			"crasher": rig.Go(filepath.Join(root, "testdata", "services", "fail")),
		}, rig.WithServer(serverURL))
		if err == nil {
			t.Fatal("expected Up to fail due to service crash")
		}
		if !strings.Contains(err.Error(), "crasher") {
			t.Errorf("error does not mention service name: %v", err)
		}
	})
}

type user struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
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

// TestObserve verifies that WithObserve() inserts transparent traffic proxies
// and captures request events in the event log.
func TestObserve(t *testing.T) {
	t.Parallel()
	serverURL := startTestServer(t)

	// Two services: api (Func) depends on backend (Func).
	// Both are HTTP so we get request.completed events.
	env := rig.Up(t, rig.Services{
		"backend": rig.Func(echo.Run),
		"api":     rig.Func(echo.Run).EgressAs("backend", "backend"),
	}, rig.WithServer(serverURL), rig.WithTimeout(60*time.Second), rig.WithObserve())

	// Make requests through the external proxy (env.Endpoint returns proxy).
	client := httpx.New(env.Endpoint("api"))
	for range 3 {
		resp, err := client.Get("/hello")
		if err != nil {
			t.Fatalf("request: %v", err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status: %d, want 200", resp.StatusCode)
		}
	}

	// Also hit the backend directly through its external proxy.
	backendClient := httpx.New(env.Endpoint("backend"))
	resp, err := backendClient.Get("/direct")
	if err != nil {
		t.Fatalf("backend request: %v", err)
	}
	resp.Body.Close()

	// Fetch the event log and verify request.completed events.
	logResp, err := http.Get(fmt.Sprintf("%s/environments/%s/log", serverURL, env.ID))
	if err != nil {
		t.Fatalf("fetch log: %v", err)
	}
	defer logResp.Body.Close()

	var events []struct {
		Type    string `json:"type"`
		Request *struct {
			Source     string `json:"source"`
			Target     string `json:"target"`
			Method     string `json:"method"`
			Path       string `json:"path"`
			StatusCode int    `json:"status_code"`
		} `json:"request,omitempty"`
		Endpoint *struct {
			Port int `json:"port"`
		} `json:"endpoint,omitempty"`
	}
	if err := json.NewDecoder(logResp.Body).Decode(&events); err != nil {
		t.Fatalf("decode log: %v", err)
	}

	// Count request.completed events by source.
	externalToAPI := 0
	externalToBackend := 0
	for _, e := range events {
		if e.Type != "request.completed" || e.Request == nil {
			continue
		}
		if e.Request.Source == "external" && e.Request.Target == "api" {
			externalToAPI++
		}
		if e.Request.Source == "external" && e.Request.Target == "backend" {
			externalToBackend++
		}
	}

	// We made 3 requests to api + health check requests from ready polling.
	// At minimum we should see our 3 explicit requests.
	if externalToAPI < 3 {
		t.Errorf("external→api requests: got %d, want >= 3", externalToAPI)
	}
	if externalToBackend < 1 {
		t.Errorf("external→backend requests: got %d, want >= 1", externalToBackend)
	}

	// Verify proxy.published events were emitted.
	proxyPublished := 0
	for _, e := range events {
		if e.Type == "proxy.published" {
			proxyPublished++
		}
	}
	if proxyPublished < 2 {
		t.Errorf("proxy.published events: got %d, want >= 2 (one per service ingress)", proxyPublished)
	}
}

// TestObserveTCP verifies TCP proxy captures connection events.
func TestObserveTCP(t *testing.T) {
	t.Parallel()
	root := moduleRoot(t)
	serverURL := startTestServer(t)

	env := rig.Up(t, rig.Services{
		"tcpecho": rig.Go(filepath.Join(root, "testdata", "services", "tcpecho")).
			Ingress("default", rig.IngressTCP()),
	}, rig.WithServer(serverURL), rig.WithTimeout(60*time.Second), rig.WithObserve())

	// Connect through the proxy.
	ep := env.Endpoint("tcpecho")
	conn, err := net.DialTimeout("tcp", ep.Addr(), 2*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	_, err = conn.Write([]byte("hello\n"))
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	buf := make([]byte, 128)
	n, err := conn.Read(buf)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	t.Logf("tcpecho response: %s", buf[:n])
	conn.Close()

	// Give the proxy a moment to emit the connection.closed event.
	time.Sleep(100 * time.Millisecond)

	// Fetch event log and verify connection events.
	logResp, err := http.Get(fmt.Sprintf("%s/environments/%s/log", serverURL, env.ID))
	if err != nil {
		t.Fatalf("fetch log: %v", err)
	}
	defer logResp.Body.Close()

	var events []struct {
		Type       string `json:"type"`
		Connection *struct {
			Source  string `json:"source"`
			Target  string `json:"target"`
			BytesIn int64  `json:"bytes_in"`
		} `json:"connection,omitempty"`
	}
	if err := json.NewDecoder(logResp.Body).Decode(&events); err != nil {
		t.Fatalf("decode log: %v", err)
	}

	var opened, closed int
	for _, e := range events {
		switch e.Type {
		case "connection.opened":
			opened++
		case "connection.closed":
			if e.Connection != nil && e.Connection.BytesIn > 0 {
				closed++
			}
		}
	}
	if opened < 1 {
		t.Errorf("connection.opened events: got %d, want >= 1", opened)
	}
	if closed < 1 {
		t.Errorf("connection.closed events (with bytes): got %d, want >= 1", closed)
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
