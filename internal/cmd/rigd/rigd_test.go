package main_test

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// buildRigd builds the rigd binary into dir and returns the path.
func buildRigd(t *testing.T, dir string) string {
	t.Helper()
	out := filepath.Join(dir, "rigd")
	cmd := exec.Command("go", "build", "-trimpath", "-buildvcs=false", "-o", out, ".")
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build rigd: %v\n%s", err, output)
	}
	return out
}

func TestReproducibleBuild(t *testing.T) {
	dir1 := t.TempDir()
	dir2 := t.TempDir()

	bin1 := buildRigd(t, dir1)
	bin2 := buildRigd(t, dir2)

	hash1 := fileHash(t, bin1)
	hash2 := fileHash(t, bin2)

	if hash1 != hash2 {
		t.Errorf("builds not reproducible:\n  build 1: %s\n  build 2: %s", hash1, hash2)
	}
}

func fileHash(t *testing.T, path string) string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		t.Fatal(err)
	}
	return fmt.Sprintf("%x", h.Sum(nil))
}

func TestAddrFileFlag(t *testing.T) {
	binDir := t.TempDir()
	binPath := buildRigd(t, binDir)
	rigDir := t.TempDir()
	customAddrFile := filepath.Join(t.TempDir(), "rigd-v0.1.0.addr")

	cmd := exec.Command(binPath, "--idle", "2s", "--rig-dir", rigDir, "--addr-file", customAddrFile)
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start rigd: %v", err)
	}

	var exited atomic.Bool
	done := make(chan error, 1)
	go func() {
		err := cmd.Wait()
		exited.Store(true)
		done <- err
	}()
	t.Cleanup(func() {
		if !exited.Load() {
			cmd.Process.Kill()
			<-done
		}
	})

	// Poll for custom addr file.
	var addr string
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if data, err := os.ReadFile(customAddrFile); err == nil && len(data) > 0 {
			addr = string(data)
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if addr == "" {
		t.Fatal("rigd did not write custom addr file within 10s")
	}

	// Default addr file should NOT exist.
	defaultAddrFile := filepath.Join(rigDir, "rigd.addr")
	if _, err := os.Stat(defaultAddrFile); err == nil {
		t.Error("default addr file should not exist when --addr-file is set")
	}

	// Health check via custom addr.
	resp, err := http.Get("http://" + addr + "/health")
	if err != nil {
		t.Fatalf("health check: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("health: got %d, want 200", resp.StatusCode)
	}

	// Wait for idle shutdown.
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("rigd did not shut down within 10s after idle timeout")
	}

	// Confirm custom addr file is removed after shutdown.
	if _, err := os.Stat(customAddrFile); !os.IsNotExist(err) {
		t.Error("custom addr file still exists after shutdown")
	}
}

func TestRigdLifecycle(t *testing.T) {
	binDir := t.TempDir()
	binPath := buildRigd(t, binDir)
	rigDir := t.TempDir()

	// Start rigd with a short idle timeout.
	cmd := exec.Command(binPath, "--idle", "2s", "--rig-dir", rigDir)
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start rigd: %v", err)
	}

	// Track whether the process has exited so cleanup doesn't double-Wait.
	var exited atomic.Bool
	done := make(chan error, 1)
	go func() {
		err := cmd.Wait()
		exited.Store(true)
		done <- err
	}()
	t.Cleanup(func() {
		if !exited.Load() {
			cmd.Process.Kill()
			<-done
		}
	})

	// Poll for addr file.
	addrFile := filepath.Join(rigDir, "rigd.addr")
	var addr string
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if data, err := os.ReadFile(addrFile); err == nil && len(data) > 0 {
			addr = string(data)
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if addr == "" {
		t.Fatal("rigd did not write addr file within 10s")
	}

	// GET /health
	baseURL := "http://" + addr
	resp, err := http.Get(baseURL + "/health")
	if err != nil {
		t.Fatalf("health check: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("health: got %d, want 200", resp.StatusCode)
	}

	// Create a simple process environment via POST /environments.
	specJSON := `{
		"name": "rigd-test",
		"services": {
			"echo": {
				"type": "process",
				"command": ["echo", "hello"],
				"ingresses": {
					"default": {"protocol": "http"}
				}
			}
		}
	}`
	createResp, err := http.Post(baseURL+"/environments", "application/json",
		strings.NewReader(specJSON))
	if err != nil {
		t.Fatalf("create environment: %v", err)
	}
	defer createResp.Body.Close()
	if createResp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(createResp.Body)
		t.Fatalf("create environment: HTTP %d: %s", createResp.StatusCode, body)
	}

	var created struct {
		ID string `json:"id"`
	}
	json.NewDecoder(createResp.Body).Decode(&created)

	// Delete the environment.
	req, _ := http.NewRequest(http.MethodDelete,
		fmt.Sprintf("%s/environments/%s", baseURL, created.ID), nil)
	delResp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("delete environment: %v", err)
	}
	delResp.Body.Close()

	// Wait for idle shutdown (2s timeout + grace).
	select {
	case <-done:
		// Process exited — good.
	case <-time.After(10 * time.Second):
		t.Fatal("rigd did not shut down within 10s after idle timeout")
	}

	// Confirm addr file is removed.
	if _, err := os.Stat(addrFile); !os.IsNotExist(err) {
		t.Error("addr file still exists after shutdown")
	}
}
