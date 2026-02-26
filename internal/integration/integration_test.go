package integration_test

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
	"github.com/matgreaves/rig/connect"
	"github.com/matgreaves/rig/connect/httpx"
	"github.com/matgreaves/rig/internal/server"
	"github.com/matgreaves/rig/internal/server/service"
	"github.com/matgreaves/rig/internal/testdata/services/echo"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
)

var sharedServerURL string

// moduleRoot returns the internal module root by finding go.mod relative to
// the test working directory.
func moduleRoot(t testing.TB) string {
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

func TestMain(m *testing.M) {
	// Find module root without testing.TB.
	wd, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "getwd: %v\n", err)
		os.Exit(1)
	}
	dir := wd
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			break
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			fmt.Fprintf(os.Stderr, "could not find go.mod\n")
			os.Exit(1)
		}
		dir = parent
	}

	reg := service.NewRegistry()
	reg.Register("process", service.Process{})
	reg.Register("go", service.Go{})
	reg.Register("client", service.Client{})
	reg.Register("container", service.Container{})
	reg.Register("postgres", service.Postgres{})
	reg.Register("temporal", service.Temporal{})
	reg.Register("proxy", service.NewProxy())
	reg.Register("test", service.Test{})

	rigDir := filepath.Join(dir, "..", ".rig")
	tmpDir, err := os.MkdirTemp("", "rig-integration-")
	if err != nil {
		fmt.Fprintf(os.Stderr, "tmpdir: %v\n", err)
		os.Exit(1)
	}

	s := server.NewServer(
		server.NewPortAllocator(),
		reg,
		tmpDir,
		0, // idle timeout disabled
		rigDir,
	)
	ts := httptest.NewServer(s)
	sharedServerURL = ts.URL

	code := m.Run()

	ts.Close()
	os.RemoveAll(tmpDir)
	os.Exit(code)
}

// repoRoot returns the top-level repo directory (parent of internal/).
func repoRoot(t testing.TB) string {
	t.Helper()
	return filepath.Join(moduleRoot(t), "..")
}

// TestUp runs all integration tests against a shared rig server. Each subtest
// creates its own environment in parallel — exactly how real users would use rig.
func TestUp(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	serverURL := sharedServerURL

	t.Run("GoService", func(t *testing.T) {
		t.Parallel()

		env := rig.Up(t, rig.Services{
			"echo": rig.Go(filepath.Join(root, "internal", "testdata", "services", "echo", "cmd")),
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
		echoBin := buildBinary(t, filepath.Join(root, "internal", "testdata", "services", "echo", "cmd"))

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
			"db": rig.Go(filepath.Join(root, "internal", "testdata", "services", "tcpecho")).
				Ingress("default", rig.IngressTCP()),
			"api": rig.Go(filepath.Join(root, "internal", "testdata", "services", "echo", "cmd")).
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
			"echo": rig.Go(filepath.Join(root, "internal", "testdata", "services", "echo", "cmd")).
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
			"db": rig.Go(filepath.Join(root, "internal", "testdata", "services", "tcpecho")).
				Ingress("default", rig.IngressTCP()),
			"api": rig.Go(filepath.Join(root, "internal", "testdata", "services", "echo", "cmd")).
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
			"worker": rig.Go(filepath.Join(root, "internal", "testdata", "services", "echo", "cmd")).
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
			"db": rig.Go(filepath.Join(root, "internal", "testdata", "services", "tcpecho")).
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

	t.Run("Temporal", func(t *testing.T) {
		t.Parallel()

		env := rig.Up(t, rig.Services{
			"temporal": rig.Temporal(),
		}, rig.WithServer(serverURL), rig.WithTimeout(120*time.Second))

		// gRPC port reachable.
		ep := env.Endpoint("temporal")
		conn, err := net.DialTimeout("tcp", ep.Addr(), 5*time.Second)
		if err != nil {
			t.Fatalf("temporal dial: %v", err)
		}
		conn.Close()

		// Attributes.
		if got := ep.Attr("TEMPORAL_ADDRESS"); got == "" {
			t.Error("TEMPORAL_ADDRESS is empty")
		}
		if got := ep.Attr("TEMPORAL_NAMESPACE"); got != "default" {
			t.Errorf("TEMPORAL_NAMESPACE = %q, want default", got)
		}

		// UI reachable.
		uiEP := env.Endpoint("temporal", "ui")
		resp, err := http.Get("http://" + uiEP.Addr())
		if err != nil {
			t.Fatalf("temporal UI request: %v", err)
		}
		resp.Body.Close()
		if resp.StatusCode >= 500 {
			t.Errorf("temporal UI status: %d, want < 500", resp.StatusCode)
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

		env := rig.Up(t, rig.Services{
			"db": rig.Postgres().InitSQL(
				"CREATE TABLE users (id SERIAL PRIMARY KEY, name TEXT NOT NULL)",
			),
			"api": rig.Go(filepath.Join(root, "internal", "testdata", "services", "userapi")).
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
			"box": rig.Container("nginx:alpine").
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
		_, err := rig.TryUp(t, rig.Services{
			"crasher": rig.Go(filepath.Join(root, "internal", "testdata", "services", "fail")),
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
	serverURL := sharedServerURL

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

// TestObserve verifies that observe mode (on by default) inserts transparent
// traffic proxies and captures request events in the event log.
func TestObserve(t *testing.T) {
	t.Parallel()
	serverURL := sharedServerURL

	// Two services: api (Func) depends on backend (Func).
	// Both are HTTP so we get request.completed events.
	env := rig.Up(t, rig.Services{
		"backend": rig.Func(echo.Run),
		"api":     rig.Func(echo.Run).EgressAs("backend", "backend"),
	}, rig.WithServer(serverURL), rig.WithTimeout(60*time.Second))

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
	// With proxy-as-service, the external proxy source is "~test" and
	// the egress proxy source is the consuming service name.
	testToAPI := 0
	testToBackend := 0
	for _, e := range events {
		if e.Type != "request.completed" || e.Request == nil {
			continue
		}
		if e.Request.Source == "~test" && e.Request.Target == "api" {
			testToAPI++
		}
		if e.Request.Source == "~test" && e.Request.Target == "backend" {
			testToBackend++
		}
	}

	// We made 3 requests to api + health check requests from ready polling.
	// At minimum we should see our 3 explicit requests.
	if testToAPI < 3 {
		t.Errorf("~test→api requests: got %d, want >= 3", testToAPI)
	}
	if testToBackend < 1 {
		t.Errorf("~test→backend requests: got %d, want >= 1", testToBackend)
	}

}

// TestObserveAttributes verifies that the observe proxy rewrites
// address-derived endpoint attributes (TEMPORAL_ADDRESS) so that tools
// reading env vars go through the proxy, not the real service.
func TestObserveAttributes(t *testing.T) {
	t.Parallel()
	serverURL := sharedServerURL

	env := rig.Up(t, rig.Services{
		"temporal": rig.Temporal(),
	}, rig.WithServer(serverURL), rig.WithTimeout(120*time.Second))

	ep := env.Endpoint("temporal")

	// TEMPORAL_ADDRESS must match the proxy endpoint, not the real service.
	wantAddr := fmt.Sprintf("127.0.0.1:%d", ep.Port)
	if got := ep.Attr("TEMPORAL_ADDRESS"); got != wantAddr {
		t.Errorf("TEMPORAL_ADDRESS = %q, want %q (proxy address)", got, wantAddr)
	}

	// Non-address attrs should be unchanged.
	if got := ep.Attr("TEMPORAL_NAMESPACE"); got != "default" {
		t.Errorf("TEMPORAL_NAMESPACE = %q, want default", got)
	}

	// Verify TCP connectivity through the proxy.
	conn, err := net.DialTimeout("tcp", ep.Addr(), 5*time.Second)
	if err != nil {
		t.Fatalf("temporal dial through proxy: %v", err)
	}
	conn.Close()
}

// TestObserveTCP verifies TCP proxy captures connection events.
func TestObserveTCP(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	serverURL := sharedServerURL

	env := rig.Up(t, rig.Services{
		"tcpecho": rig.Go(filepath.Join(root, "internal", "testdata", "services", "tcpecho")).
			Ingress("default", rig.IngressTCP()),
	}, rig.WithServer(serverURL), rig.WithTimeout(60*time.Second))

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

// TestObserveGRPC verifies that gRPC proxy captures grpc.call.completed events
// with correct service, method, and status fields.
func TestObserveGRPC(t *testing.T) {
	t.Parallel()
	serverURL := sharedServerURL

	env := rig.Up(t, rig.Services{
		"temporal": rig.Temporal(),
	}, rig.WithServer(serverURL), rig.WithTimeout(120*time.Second))

	// Make a gRPC health check call through the proxy endpoint.
	ep := env.Endpoint("temporal")
	conn, err := grpc.NewClient(ep.Addr(),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("grpc dial: %v", err)
	}
	defer conn.Close()

	client := healthpb.NewHealthClient(conn)
	resp, err := client.Check(context.Background(), &healthpb.HealthCheckRequest{})
	if err != nil {
		t.Fatalf("grpc health check: %v", err)
	}
	t.Logf("health check response: %v", resp.Status)

	// Give the proxy a moment to emit the event.
	time.Sleep(200 * time.Millisecond)

	// Fetch event log and verify grpc.call.completed events.
	logResp, err := http.Get(fmt.Sprintf("%s/environments/%s/log", serverURL, env.ID))
	if err != nil {
		t.Fatalf("fetch log: %v", err)
	}
	defer logResp.Body.Close()

	var events []struct {
		Type     string `json:"type"`
		GRPCCall *struct {
			Source                string          `json:"source"`
			Target                string          `json:"target"`
			Service               string          `json:"service"`
			Method                string          `json:"method"`
			GRPCStatus            string          `json:"grpc_status"`
			RequestBody           []byte          `json:"request_body,omitempty"`
			RequestBodyTruncated  bool            `json:"request_body_truncated,omitempty"`
			ResponseBody          []byte          `json:"response_body,omitempty"`
			ResponseBodyTruncated bool            `json:"response_body_truncated,omitempty"`
			RequestBodyDecoded    json.RawMessage `json:"request_body_decoded,omitempty"`
			ResponseBodyDecoded   json.RawMessage `json:"response_body_decoded,omitempty"`
		} `json:"grpc_call,omitempty"`
	}
	if err := json.NewDecoder(logResp.Body).Decode(&events); err != nil {
		t.Fatalf("decode log: %v", err)
	}

	var found bool
	for _, e := range events {
		if e.Type != "grpc.call.completed" || e.GRPCCall == nil {
			continue
		}
		g := e.GRPCCall
		t.Logf("grpc event: source=%s target=%s service=%s method=%s status=%s reqBody=%d respBody=%d reqDecoded=%s respDecoded=%s",
			g.Source, g.Target, g.Service, g.Method, g.GRPCStatus,
			len(g.RequestBody), len(g.ResponseBody),
			string(g.RequestBodyDecoded), string(g.ResponseBodyDecoded))
		if g.Service == "grpc.health.v1.Health" && g.Method == "Check" {
			found = true
			if g.GRPCStatus != "OK" {
				t.Errorf("grpc_status = %q, want OK", g.GRPCStatus)
			}
			// Raw bodies should always be captured (gRPC frames).
			if len(g.RequestBody) == 0 {
				t.Error("request_body is empty, want captured gRPC frame")
			}
			if len(g.ResponseBody) == 0 {
				t.Error("response_body is empty, want captured gRPC frame")
			}
			// Temporal dev server supports reflection, so the response
			// (which has a non-empty status field) should be decoded.
			// The request is an empty HealthCheckRequest, so its decoded
			// body is legitimately empty (zero-byte protobuf payload).
			if len(g.ResponseBodyDecoded) == 0 {
				t.Error("response_body_decoded is empty, want decoded JSON (reflection supported)")
			}
		}
	}
	if !found {
		t.Error("no grpc.call.completed event found for grpc.health.v1.Health/Check")
	}
}

// TestFuncLogWriter verifies that connect.LogWriter ships Func service logs
// to rigd's event timeline.
func TestFuncLogWriter(t *testing.T) {
	t.Parallel()
	serverURL := sharedServerURL

	env := rig.Up(t, rig.Services{
		"logger": rig.Func(func(ctx context.Context) error {
			w := connect.LogWriter(ctx)
			fmt.Fprintln(w, "hello from func")
			fmt.Fprintln(w, "second line")
			return httpx.ListenAndServe(ctx, http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
				rw.WriteHeader(http.StatusOK)
			}))
		}),
	}, rig.WithServer(serverURL), rig.WithTimeout(30*time.Second))

	// Hit the service to confirm it's up.
	resp, err := http.Get("http://" + env.Endpoint("logger").Addr() + "/")
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	resp.Body.Close()

	// Give the log writer a moment to flush.
	time.Sleep(200 * time.Millisecond)

	// Fetch the event log and verify service.log events.
	logResp, err := http.Get(fmt.Sprintf("%s/environments/%s/log", serverURL, env.ID))
	if err != nil {
		t.Fatalf("fetch log: %v", err)
	}
	defer logResp.Body.Close()

	var events []struct {
		Type    string `json:"type"`
		Service string `json:"service"`
		Log     *struct {
			Stream string `json:"stream"`
			Data   string `json:"data"`
		} `json:"log,omitempty"`
	}
	if err := json.NewDecoder(logResp.Body).Decode(&events); err != nil {
		t.Fatalf("decode log: %v", err)
	}

	// Collect all log data — lines may be batched into fewer events.
	var allData string
	for _, e := range events {
		if e.Type == "service.log" && e.Service == "logger" && e.Log != nil {
			allData += e.Log.Data + "\n"
		}
	}

	if !strings.Contains(allData, "hello from func") {
		t.Errorf("log output missing 'hello from func': %s", allData)
	}
	if !strings.Contains(allData, "second line") {
		t.Errorf("log output missing 'second line': %s", allData)
	}
}

// --- helpers ---

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
