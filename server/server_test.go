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
	reg.Register("container", service.Container{})

	s := server.NewServer(
		server.NewPortAllocator(),
		reg,
		t.TempDir(),
		0,           // idle timeout disabled
		t.TempDir(), // isolated rig dir
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

// collectUntil reads events from ch until one satisfies match, collecting all
// events (including the matching one) into a slice.
func collectUntil(t *testing.T, ctx context.Context, ch <-chan server.Event, match func(server.Event) bool) []server.Event {
	t.Helper()
	var events []server.Event
	for {
		select {
		case e, ok := <-ch:
			if !ok {
				t.Fatal("SSE channel closed before expected event arrived")
			}
			events = append(events, e)
			if match(e) {
				return events
			}
		case <-ctx.Done():
			t.Fatal("context cancelled before expected event arrived")
		}
	}
}

// hasEvent returns true if events contains one matching the predicate.
func hasEvent(events []server.Event, match func(server.Event) bool) bool {
	for _, e := range events {
		if match(e) {
			return true
		}
	}
	return false
}

// findEvent returns the first event matching the predicate, or zero value.
func findEvent(events []server.Event, match func(server.Event) bool) (server.Event, bool) {
	for _, e := range events {
		if match(e) {
			return e, true
		}
	}
	return server.Event{}, false
}

// --- HTTP API contract tests (no binaries needed) ---

func TestServer_NotFound(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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

func TestServer_IdleTimer(t *testing.T) {
	t.Parallel()
	reg := service.NewRegistry()
	reg.Register("process", service.Process{})

	const idleTimeout = 200 * time.Millisecond
	s := server.NewServer(server.NewPortAllocator(), reg, t.TempDir(), idleTimeout, t.TempDir())
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

// --- integration tests (share binaries via parent test) ---

// TestServer runs integration tests that exercise the HTTP API with real
// service binaries. Binaries are built once and shared across parallel subtests.
func TestServer(t *testing.T) {
	echoBin := buildTestBinary(t, "testdata/services/echo/cmd")
	failBin := buildTestBinary(t, "testdata/services/fail")
	ts := newTestServer(t)

	t.Run("GetEnvironment", func(t *testing.T) {
		t.Parallel()

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
	})

	t.Run("FailurePropagation", func(t *testing.T) {
		t.Parallel()

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
	})

	t.Run("ServiceCrashEvents", func(t *testing.T) {
		t.Parallel()

		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		// fail binary with an ingress — verify the full event sequence when
		// a service crashes during startup.
		envSpec := map[string]any{
			"name": "test-crash-events",
			"services": map[string]any{
				"broken": map[string]any{
					"type":   "process",
					"config": mustJSON(t, service.ProcessConfig{Command: failBin}),
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
		json.NewDecoder(resp.Body).Decode(&created)
		id := created["id"]
		defer func() {
			req, _ := http.NewRequest(http.MethodDelete, ts.URL+"/environments/"+id, nil)
			http.DefaultClient.Do(req)
		}()

		events := sseEvents(t, ctx, ts.URL+"/environments/"+id+"/events")
		all := collectUntil(t, ctx, events, func(e server.Event) bool {
			return e.Type == server.EventEnvironmentDown
		})

		// service.failed must appear with the error message.
		failed, ok := findEvent(all, func(e server.Event) bool {
			return e.Type == server.EventServiceFailed && e.Service == "broken"
		})
		if !ok {
			t.Fatal("no service.failed event for 'broken'")
		}
		if !strings.Contains(failed.Error, "exit status") {
			t.Errorf("service.failed error = %q, want it to contain 'exit status'", failed.Error)
		}

		// service.stopped must appear after the crash.
		if !hasEvent(all, func(e server.Event) bool {
			return e.Type == server.EventServiceStopped && e.Service == "broken"
		}) {
			t.Error("no service.stopped event after crash")
		}

		// environment.up must NOT appear — the environment never became ready.
		if hasEvent(all, func(e server.Event) bool {
			return e.Type == server.EventEnvironmentUp
		}) {
			t.Error("environment.up should not appear when a service crashes")
		}
	})

	t.Run("HealthCheckTimeoutDiagnostics", func(t *testing.T) {
		t.Parallel()

		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		// sleep process with an HTTP ingress and a very short ready timeout.
		// The process stays alive but never listens on the port, so health
		// checks fail until the timeout.
		envSpec := map[string]any{
			"name": "test-health-timeout",
			"services": map[string]any{
				"sleeper": map[string]any{
					"type":   "process",
					"config": mustJSON(t, service.ProcessConfig{Command: "sleep"}),
					"args":   []string{"3600"},
					"ingresses": map[string]any{
						"default": map[string]any{
							"protocol": "http",
							"ready": map[string]any{
								"timeout": "200ms",
							},
						},
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
		json.NewDecoder(resp.Body).Decode(&created)
		id := created["id"]
		defer func() {
			req, _ := http.NewRequest(http.MethodDelete, ts.URL+"/environments/"+id, nil)
			http.DefaultClient.Do(req)
		}()

		events := sseEvents(t, ctx, ts.URL+"/environments/"+id+"/events")
		all := collectUntil(t, ctx, events, func(e server.Event) bool {
			return e.Type == server.EventEnvironmentDown
		})

		// health.check_failed events should have been emitted during polling.
		if !hasEvent(all, func(e server.Event) bool {
			return e.Type == server.EventHealthCheckFailed && e.Service == "sleeper"
		}) {
			t.Error("no health.check_failed events emitted during polling")
		}

		// service.failed should include the last check error (not just "context deadline exceeded").
		failed, ok := findEvent(all, func(e server.Event) bool {
			return e.Type == server.EventServiceFailed && e.Service == "sleeper"
		})
		if !ok {
			t.Fatal("no service.failed event for 'sleeper'")
		}
		if !strings.Contains(failed.Error, "last error:") {
			t.Errorf("timeout error should include last check error, got: %q", failed.Error)
		}
		if !strings.Contains(failed.Error, "200ms") {
			t.Errorf("timeout error should include the timeout duration, got: %q", failed.Error)
		}
	})

	t.Run("MultiServiceCrashCleanup", func(t *testing.T) {
		t.Parallel()

		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		// Two services: echo (healthy) and broken (crashes immediately).
		// Both are independent — no egress dependency. Verify both get
		// stopped when one crashes (the orchestrator tears down everything).
		// broken has an ingress so the ready check runs and fails when
		// the process exits — this prevents environment.up from firing.
		envSpec := map[string]any{
			"name": "test-multi-crash",
			"services": map[string]any{
				"api": map[string]any{
					"type":   "process",
					"config": mustJSON(t, service.ProcessConfig{Command: echoBin}),
					"ingresses": map[string]any{
						"default": map[string]any{"protocol": "http"},
					},
				},
				"broken": map[string]any{
					"type":   "process",
					"config": mustJSON(t, service.ProcessConfig{Command: failBin}),
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
		json.NewDecoder(resp.Body).Decode(&created)
		id := created["id"]
		defer func() {
			req, _ := http.NewRequest(http.MethodDelete, ts.URL+"/environments/"+id, nil)
			http.DefaultClient.Do(req)
		}()

		events := sseEvents(t, ctx, ts.URL+"/environments/"+id+"/events")
		all := collectUntil(t, ctx, events, func(e server.Event) bool {
			return e.Type == server.EventEnvironmentDown
		})

		// broken should have a service.failed event.
		if !hasEvent(all, func(e server.Event) bool {
			return e.Type == server.EventServiceFailed && e.Service == "broken"
		}) {
			t.Error("no service.failed for 'broken'")
		}

		// Both services should get service.stopped — verifies cleanup of
		// the healthy service when a sibling crashes.
		for _, svc := range []string{"api", "broken"} {
			if !hasEvent(all, func(e server.Event) bool {
				return e.Type == server.EventServiceStopped && e.Service == svc
			}) {
				t.Errorf("no service.stopped for %q — cleanup incomplete", svc)
			}
		}

		// environment.up should NOT appear.
		if hasEvent(all, func(e server.Event) bool {
			return e.Type == server.EventEnvironmentUp
		}) {
			t.Error("environment.up should not appear when a service crashes")
		}
	})

	t.Run("ConcurrentDelete", func(t *testing.T) {
		t.Parallel()

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

		events := sseEvents(t, ctx, fmt.Sprintf("%s/environments/%s/events", ts.URL, id))
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
				req, _ := http.NewRequest(http.MethodDelete, fmt.Sprintf("%s/environments/%s", ts.URL, id), nil)
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
	})
}
