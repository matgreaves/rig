package integration_test

import (
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	rig "github.com/matgreaves/rig/client"
)

// buildRigd builds the rigd binary into dir and returns the path.
func buildRigd(t *testing.T, dir string) string {
	t.Helper()
	root := repoRoot(t)
	out := filepath.Join(dir, "rigd")
	cmd := exec.Command("go", "build", "-trimpath", "-buildvcs=false", "-o", out, filepath.Join(root, "internal", "cmd", "rigd"))
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build rigd: %v\n%s", err, output)
	}
	return out
}

func TestEnsureServer(t *testing.T) {
	binDir := t.TempDir()
	binPath := buildRigd(t, binDir)
	rigDir := t.TempDir()

	t.Setenv("RIG_BINARY", binPath)

	// First call should start rigd.
	url1, err := rig.EnsureServer(rigDir)
	if err != nil {
		t.Fatalf("first EnsureServer: %v", err)
	}

	resp, err := http.Get(url1 + "/health")
	if err != nil {
		t.Fatalf("health after first start: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("health: got %d, want 200", resp.StatusCode)
	}

	// Second call should reuse the running instance.
	url2, err := rig.EnsureServer(rigDir)
	if err != nil {
		t.Fatalf("second EnsureServer: %v", err)
	}
	if url1 != url2 {
		t.Errorf("expected same URL, got %q and %q", url1, url2)
	}

	// Start a rigd manually so we have a handle to kill it, then verify
	// EnsureServer recovers by starting a new one.
	//
	// Kill the instance EnsureServer started (we don't have its PID, but we
	// can remove the addr file and let the stale instance idle out). Then
	// start a fresh one ourselves so we can kill it deterministically.
	os.Remove(filepath.Join(rigDir, "rigd.addr"))

	cmd := exec.Command(binPath, "--idle", "5m", "--rig-dir", rigDir)
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start rigd: %v", err)
	}

	// Wait for it to be healthy.
	addrFile := filepath.Join(rigDir, "rigd.addr")
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if data, err := os.ReadFile(addrFile); err == nil && len(data) > 0 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	// Confirm EnsureServer reuses it.
	url3, err := rig.EnsureServer(rigDir)
	if err != nil {
		t.Fatalf("EnsureServer with manual instance: %v", err)
	}

	// Kill it and confirm EnsureServer starts a new one.
	cmd.Process.Kill()
	cmd.Wait()
	os.Remove(addrFile)

	url4, err := rig.EnsureServer(rigDir)
	if err != nil {
		t.Fatalf("EnsureServer after kill: %v", err)
	}
	if url4 == url3 {
		t.Error("expected different URL after killing rigd, got the same")
	}

	resp, err = http.Get(url4 + "/health")
	if err != nil {
		t.Fatalf("health after restart: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("health: got %d, want 200", resp.StatusCode)
	}
}
