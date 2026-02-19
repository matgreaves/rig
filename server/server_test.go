package server_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/matgreaves/rig/server"
	"github.com/matgreaves/rig/server/service"
	"github.com/matgreaves/rig/spec"
)

// newTestServer creates an httptest.Server backed by a real Server with
// the "process" service type registered. Idle timeout is disabled.
func newTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	reg := service.NewRegistry()
	reg.Register("process", service.Process{})

	s := server.NewServer(
		server.NewPortAllocator(),
		reg,
		t.TempDir(),
		0, // idle timeout disabled
	)
	ts := httptest.NewServer(s)
	t.Cleanup(ts.Close)
	return ts
}

// sseEvents connects to url as a text/event-stream client and returns a
// channel of parsed Events. The channel is closed when the connection ends
// or ctx is cancelled.
func sseEvents(t *testing.T, ctx context.Context, url string) <-chan server.Event {
	t.Helper()
	ch := make(chan server.Event, 64)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}

	go func() {
		defer close(ch)

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return // context cancelled or connection refused
		}
		defer resp.Body.Close()

		scanner := bufio.NewScanner(resp.Body)
		var eventType, data string
		for scanner.Scan() {
			line := scanner.Text()
			switch {
			case strings.HasPrefix(line, "event: "):
				eventType = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "data: "):
				data = strings.TrimPrefix(line, "data: ")
			case line == "":
				if eventType != "" && data != "" {
					var e server.Event
					if jsonErr := json.Unmarshal([]byte(data), &e); jsonErr == nil {
						select {
						case ch <- e:
						case <-ctx.Done():
							return
						}
					}
					eventType, data = "", ""
				}
			}
		}
	}()

	return ch
}

// waitForEvent reads from ch until it finds an event satisfying match,
// then returns it. Fails the test if ch closes or ctx is cancelled first.
func waitForEvent(t *testing.T, ctx context.Context, ch <-chan server.Event, match func(server.Event) bool) server.Event {
	t.Helper()
	for {
		select {
		case e, ok := <-ch:
			if !ok {
				t.Fatal("SSE channel closed before expected event arrived")
			}
			if match(e) {
				return e
			}
		case <-ctx.Done():
			t.Fatal("context cancelled before expected event arrived")
		}
	}
}

// --- tests ---

func TestServer_NotFound(t *testing.T) {
	ts := newTestServer(t)

	cases := []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/environments/no-such-id"},
		{http.MethodGet, "/environments/no-such-id/events"},
		{http.MethodDelete, "/environments/no-such-id"},
	}

	for _, tc := range cases {
		req, _ := http.NewRequest(tc.method, ts.URL+tc.path, nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("%s %s: %v", tc.method, tc.path, err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("%s %s: got %d, want 404", tc.method, tc.path, resp.StatusCode)
		}
	}
}

func TestServer_ValidationErrors(t *testing.T) {
	ts := newTestServer(t)

	body := mustJSON(t, map[string]any{
		"name":     "bad-env",
		"services": map[string]any{},
	})
	resp, err := http.Post(ts.URL+"/environments", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", resp.StatusCode)
	}

	var result map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	if _, ok := result["validation_errors"]; !ok {
		t.Error("response missing 'validation_errors' field")
	}
}

func TestServer_CreateAndStream(t *testing.T) {
	echoBin := buildTestBinary(t, "testdata/services/echo")
	ts := newTestServer(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// POST /environments
	envSpec := map[string]any{
		"name": "test-create-stream",
		"services": map[string]any{
			"echo": map[string]any{
				"type":   "process",
				"config": mustJSON(t, service.ProcessConfig{Command: echoBin}),
				"ingresses": map[string]any{
					"default": map[string]any{"protocol": "http"},
				},
			},
		},
	}
	body := mustJSON(t, envSpec)
	resp, err := http.Post(ts.URL+"/environments", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create: status %d, want 201", resp.StatusCode)
	}

	var created map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	id := created["id"]
	if id == "" {
		t.Fatal("create response missing 'id'")
	}

	// Subscribe to the SSE stream and wait for environment.up, which
	// carries the fully resolved wiring for all services.
	events := sseEvents(t, ctx, ts.URL+"/environments/"+id+"/events")

	upEv := waitForEvent(t, ctx, events, func(e server.Event) bool {
		return e.Type == server.EventEnvironmentUp
	})

	echoIngresses, ok := upEv.Ingresses["echo"]
	if !ok {
		t.Fatal("environment.up missing 'echo' ingresses")
	}
	ep, ok := echoIngresses["default"]
	if !ok || ep.Port == 0 {
		t.Fatal("environment.up missing 'echo' default ingress endpoint")
	}

	// Hit the echo service directly to confirm it is reachable.
	healthURL := fmt.Sprintf("http://%s:%d/health", ep.Host, ep.Port)
	hresp, err := http.Get(healthURL)
	if err != nil {
		t.Fatalf("health check: %v", err)
	}
	hresp.Body.Close()
	if hresp.StatusCode != http.StatusOK {
		t.Errorf("health: %d, want 200", hresp.StatusCode)
	}

	// DELETE /environments/{id}
	delReq, _ := http.NewRequest(http.MethodDelete, ts.URL+"/environments/"+id, nil)
	delResp, err := http.DefaultClient.Do(delReq)
	if err != nil {
		t.Fatal(err)
	}
	delResp.Body.Close()
	if delResp.StatusCode != http.StatusOK {
		t.Errorf("DELETE: %d, want 200", delResp.StatusCode)
	}
}

func TestServer_GetEnvironment(t *testing.T) {
	echoBin := buildTestBinary(t, "testdata/services/echo")
	ts := newTestServer(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	envSpec := map[string]any{
		"name": "test-get-env",
		"services": map[string]any{
			"echo": map[string]any{
				"type":   "process",
				"config": mustJSON(t, service.ProcessConfig{Command: echoBin}),
				"ingresses": map[string]any{
					"default": map[string]any{"protocol": "http"},
				},
			},
		},
	}
	body := mustJSON(t, envSpec)
	resp, err := http.Post(ts.URL+"/environments", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	var created map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	id := created["id"]

	// Wait for the environment to be fully up before inspecting via GET.
	events := sseEvents(t, ctx, ts.URL+"/environments/"+id+"/events")
	waitForEvent(t, ctx, events, func(e server.Event) bool {
		return e.Type == server.EventEnvironmentUp
	})

	getResp, err := http.Get(ts.URL + "/environments/" + id)
	if err != nil {
		t.Fatal(err)
	}
	defer getResp.Body.Close()

	if getResp.StatusCode != http.StatusOK {
		t.Fatalf("GET: status %d, want 200", getResp.StatusCode)
	}

	var resolved spec.ResolvedEnvironment
	if err := json.NewDecoder(getResp.Body).Decode(&resolved); err != nil {
		t.Fatal(err)
	}
	if resolved.ID != id {
		t.Errorf("resolved.ID = %q, want %q", resolved.ID, id)
	}
	echoSvc, ok := resolved.Services["echo"]
	if !ok {
		t.Fatal("'echo' not in resolved services")
	}
	if echoSvc.Status != spec.StatusReady {
		t.Errorf("echo status = %q, want %q", echoSvc.Status, spec.StatusReady)
	}
	ep, ok := echoSvc.Ingresses["default"]
	if !ok || ep.Port == 0 {
		t.Fatal("'default' ingress not resolved in GET response")
	}

	delReq, _ := http.NewRequest(http.MethodDelete, ts.URL+"/environments/"+id, nil)
	delResp, _ := http.DefaultClient.Do(delReq)
	delResp.Body.Close()
}

func TestServer_FailurePropagation(t *testing.T) {
	failBin := buildTestBinary(t, "testdata/services/fail")
	ts := newTestServer(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// The fail service has no ingresses — it exits immediately on start.
	envSpec := map[string]any{
		"name": "test-fail-env",
		"services": map[string]any{
			"broken": map[string]any{
				"type":   "process",
				"config": mustJSON(t, service.ProcessConfig{Command: failBin}),
			},
		},
	}
	body := mustJSON(t, envSpec)
	resp, err := http.Post(ts.URL+"/environments", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create: status %d, want 201", resp.StatusCode)
	}

	var created map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	id := created["id"]

	events := sseEvents(t, ctx, ts.URL+"/environments/"+id+"/events")

	// environment.down should arrive without a preceding environment.up.
	waitForEvent(t, ctx, events, func(e server.Event) bool {
		return e.Type == server.EventEnvironmentDown
	})

	// DELETE should still succeed — environment is tracked until explicitly removed.
	delReq, _ := http.NewRequest(http.MethodDelete, ts.URL+"/environments/"+id, nil)
	delResp, err := http.DefaultClient.Do(delReq)
	if err != nil {
		t.Fatal(err)
	}
	delResp.Body.Close()
	if delResp.StatusCode != http.StatusOK {
		t.Errorf("DELETE after failure: %d, want 200", delResp.StatusCode)
	}
}

func TestServer_ConcurrentDelete(t *testing.T) {
	echoBin := buildTestBinary(t, "testdata/services/echo")
	ts := newTestServer(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	envSpec := map[string]any{
		"name": "test-concurrent-delete",
		"services": map[string]any{
			"echo": map[string]any{
				"type":   "process",
				"config": mustJSON(t, service.ProcessConfig{Command: echoBin}),
				"ingresses": map[string]any{
					"default": map[string]any{"protocol": "http"},
				},
			},
		},
	}
	body := mustJSON(t, envSpec)
	resp, err := http.Post(ts.URL+"/environments", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	var created map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	id := created["id"]

	events := sseEvents(t, ctx, ts.URL+"/environments/"+id+"/events")
	waitForEvent(t, ctx, events, func(e server.Event) bool {
		return e.Type == server.EventEnvironmentUp
	})

	// Issue two concurrent DELETEs — exactly one should succeed (200) and
	// the other should get 404.
	type result struct {
		status int
	}
	results := make(chan result, 2)
	for range 2 {
		go func() {
			req, _ := http.NewRequest(http.MethodDelete, ts.URL+"/environments/"+id, nil)
			r, err := http.DefaultClient.Do(req)
			if err != nil {
				results <- result{0}
				return
			}
			r.Body.Close()
			results <- result{r.StatusCode}
		}()
	}

	statuses := make(map[int]int)
	for range 2 {
		r := <-results
		statuses[r.status]++
	}
	if statuses[http.StatusOK] != 1 {
		t.Errorf("expected exactly 1 DELETE to return 200, got: %v", statuses)
	}
	if statuses[http.StatusNotFound] != 1 {
		t.Errorf("expected exactly 1 DELETE to return 404, got: %v", statuses)
	}
}

func TestServer_IdleTimer(t *testing.T) {
	reg := service.NewRegistry()
	reg.Register("process", service.Process{})

	const idleTimeout = 200 * time.Millisecond
	s := server.NewServer(server.NewPortAllocator(), reg, t.TempDir(), idleTimeout)
	ts := httptest.NewServer(s)
	defer ts.Close()

	// No environments created — idle timer should fire promptly.
	select {
	case <-s.ShutdownCh():
		// expected
	case <-time.After(5 * time.Second):
		t.Fatal("idle timer did not fire within timeout")
	}
}
