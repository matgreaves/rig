//go:build generate

// This file generates JSONL test fixtures by running real rigd environments.
// Run with: make fixtures
// Or manually: make build && RIG_BINARY=./bin/rigd go test -tags generate ./explain/ -run TestGenerate -v

package explain

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	rig "github.com/matgreaves/rig/client"
	"github.com/matgreaves/rig/connect/httpx"
)

func TestGenerate(t *testing.T) {
	if os.Getenv("RIG_BINARY") == "" {
		t.Skip("RIG_BINARY not set — run 'make build' first")
	}

	testdataDir := filepath.Join("testdata")
	if err := os.MkdirAll(testdataDir, 0o755); err != nil {
		t.Fatal(err)
	}

	t.Run("assertion_failure", func(t *testing.T) {
		generateAssertionFailure(t, testdataDir)
	})
	t.Run("service_crash", func(t *testing.T) {
		generateServiceCrash(t, testdataDir)
	})
	t.Run("passed", func(t *testing.T) {
		generatePassed(t, testdataDir)
	})
}

// generateAssertionFailure creates an environment where:
// - A service starts fine and serves HTTP
// - A request returns 500
// - The test records a test.note assertion
func generateAssertionFailure(t *testing.T, outDir string) {
	t.Helper()

	// Register copy BEFORE rig.Up so it runs AFTER rig's cleanup (LIFO).
	t.Cleanup(func() {
		copyLogFile(t, outDir, "assertion_failure")
	})

	svc := rig.Func(func(ctx context.Context) error {
		mux := http.NewServeMux()
		mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(200)
		})
		mux.HandleFunc("/bad", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(500)
			fmt.Fprintf(w, `{"error":"something went wrong"}`)
			fmt.Fprintln(os.Stderr, "error: something went wrong")
		})
		return httpx.ListenAndServe(ctx, mux)
	})

	env := rig.Up(t, rig.Services{
		"api": svc,
	})

	// Make a request that triggers a 500.
	client := httpx.New(env.Endpoint("api"))
	resp, err := client.Get("/bad")
	if err != nil {
		t.Fatalf("GET /bad: %v", err)
	}
	defer resp.Body.Close()

	// Record an assertion failure via the rig TB.
	env.T.Errorf("expected 200, got %d", resp.StatusCode)
}

// generateServiceCrash creates an environment where a service exits immediately.
func generateServiceCrash(t *testing.T, outDir string) {
	t.Helper()

	// Register copy BEFORE rig.TryUp so it runs AFTER rig's cleanup (LIFO).
	t.Cleanup(func() {
		copyLogFile(t, outDir, "service_crash")
	})

	svc := rig.Func(func(ctx context.Context) error {
		return fmt.Errorf("intentional crash: startup failed")
	})

	_, err := rig.TryUp(t, rig.Services{
		"crasher": svc,
	})
	if err == nil {
		t.Fatal("expected TryUp to fail for crashing service")
	}
	t.Logf("TryUp failed as expected: %v", err)
}

// generatePassed creates an environment where everything works.
func generatePassed(t *testing.T, outDir string) {
	t.Helper()

	// Register copy BEFORE rig.Up so it runs AFTER rig's cleanup (LIFO).
	t.Cleanup(func() {
		copyLogFile(t, outDir, "passed")
	})

	svc := rig.Func(func(ctx context.Context) error {
		mux := http.NewServeMux()
		mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(200)
		})
		mux.HandleFunc("/hello", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"message":"hello"}`)
		})
		return httpx.ListenAndServe(ctx, mux)
	})

	env := rig.Up(t, rig.Services{
		"api": svc,
	})

	// Make a successful request.
	client := httpx.New(env.Endpoint("api"))
	resp, err := client.Get("/hello")
	if err != nil {
		t.Fatalf("GET /hello: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	t.Logf("GET /hello: %d %s", resp.StatusCode, body)
}

// copyLogFile finds the most recent log file from the rig logs directory
// and copies it to outDir/name.jsonl.
func copyLogFile(t *testing.T, outDir, name string) {
	t.Helper()

	rigDir := os.Getenv("RIG_DIR")
	if rigDir == "" {
		home, _ := os.UserHomeDir()
		rigDir = filepath.Join(home, ".rig")
	}
	logsDir := filepath.Join(rigDir, "logs")

	// Find the most recent .jsonl file matching our test name.
	entries, err := os.ReadDir(logsDir)
	if err != nil {
		t.Logf("warning: cannot read logs dir %s: %v", logsDir, err)
		return
	}

	var bestPath string
	var bestTime int64
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		path := filepath.Join(logsDir, e.Name())
		info, err := e.Info()
		if err != nil {
			continue
		}
		modTime := info.ModTime().UnixNano()

		// Read header to check environment name.
		f, err := os.Open(path)
		if err != nil {
			continue
		}
		var hdr struct {
			Type string `json:"type"`
			Env  string `json:"environment"`
		}
		json.NewDecoder(f).Decode(&hdr)
		f.Close()

		if hdr.Type != "log.header" {
			continue
		}
		if !matchesScenario(hdr.Env, name) {
			continue
		}
		if modTime > bestTime {
			bestTime = modTime
			bestPath = path
		}
	}

	if bestPath == "" {
		t.Logf("warning: no log file found for scenario %q in %s", name, logsDir)
		return
	}

	src, err := os.ReadFile(bestPath)
	if err != nil {
		t.Logf("warning: cannot read log file %s: %v", bestPath, err)
		return
	}

	dst := filepath.Join(outDir, name+".jsonl")
	if err := os.WriteFile(dst, src, 0o644); err != nil {
		t.Logf("warning: cannot write fixture %s: %v", dst, err)
		return
	}
	t.Logf("wrote fixture: %s (%d bytes)", dst, len(src))
}

// matchesScenario checks if the environment name from the header matches
// our expected scenario name.
func matchesScenario(envName, scenario string) bool {
	return strings.Contains(strings.ToLower(envName), strings.ToLower(scenario))
}
