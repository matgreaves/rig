package rig

import (
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"
)

// EnsureServer finds or starts a rigd instance and returns its base URL
// (e.g. "http://127.0.0.1:12345"). rigDir overrides the default rig
// directory (~/.rig) for addr/lock file discovery; pass "" for default.
func EnsureServer(rigDir string) (string, error) {
	if rigDir == "" {
		rigDir = defaultRigDir()
	}

	binPath, err := findBinary()
	if err != nil {
		return "", err
	}

	addrFile := filepath.Join(rigDir, "rigd.addr")

	// Fast path: existing instance.
	if addr, err := os.ReadFile(addrFile); err == nil {
		if probeHealth(string(addr)) {
			return "http://" + string(addr), nil
		}
	}

	// Acquire lock to prevent concurrent starts.
	lockPath := filepath.Join(rigDir, "rigd.lock")
	if err := os.MkdirAll(rigDir, 0o755); err != nil {
		return "", fmt.Errorf("create rig dir: %w", err)
	}
	unlock, err := acquireLock(lockPath)
	if err != nil {
		return "", fmt.Errorf("acquire lock: %w", err)
	}
	defer unlock()

	// Double-check after acquiring lock.
	if addr, err := os.ReadFile(addrFile); err == nil {
		if probeHealth(string(addr)) {
			return "http://" + string(addr), nil
		}
	}

	// Start rigd as a detached subprocess.
	cmd := exec.Command(binPath, "--idle", "5m", "--rig-dir", rigDir)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}

	// Append stderr to a log file for debugging.
	logPath := filepath.Join(rigDir, "rigd.log")
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err == nil {
		cmd.Stderr = logFile
		defer logFile.Close()
	}

	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("start rigd: %w", err)
	}

	// Poll for addr file.
	const (
		pollInterval = 100 * time.Millisecond
		pollTimeout  = 10 * time.Second
	)
	deadline := time.Now().Add(pollTimeout)
	for time.Now().Before(deadline) {
		if addr, err := os.ReadFile(addrFile); err == nil && len(addr) > 0 {
			addrStr := string(addr)
			if probeHealth(addrStr) {
				return "http://" + addrStr, nil
			}
		}
		time.Sleep(pollInterval)
	}

	return "", fmt.Errorf("rigd did not become healthy within %s (log: %s)", pollTimeout, logPath)
}

// findBinary locates the rigd binary. Checks in order:
// 1. RIG_BINARY env var (explicit override for dev/CI)
// 2. ~/.rig/bin/rigd (managed path — future: per-hash versioned)
// 3. PATH lookup (last resort — risks version mismatch)
func findBinary() (string, error) {
	if p := os.Getenv("RIG_BINARY"); p != "" {
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
		return "", fmt.Errorf("RIG_BINARY=%q: file not found", p)
	}
	home, err := os.UserHomeDir()
	if err == nil {
		p := filepath.Join(home, ".rig", "bin", "rigd")
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}
	if p, err := exec.LookPath("rigd"); err == nil {
		return p, nil
	}
	return "", fmt.Errorf("rigd binary not found; install it, set RIG_BINARY, or set RIG_SERVER_ADDR")
}

// probeHealth sends GET /health to addr and returns true on 200.
func probeHealth(addr string) bool {
	c := http.Client{Timeout: time.Second}
	resp, err := c.Get("http://" + addr + "/health")
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

// acquireLock acquires an exclusive file lock. Returns an unlock function.
func acquireLock(path string) (unlock func(), err error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open lock file: %w", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		f.Close()
		return nil, fmt.Errorf("flock: %w", err)
	}
	return func() {
		syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		f.Close()
		os.Remove(path)
	}, nil
}

// defaultRigDir returns the default rig directory. Mirrors the server's
// DefaultRigDir logic without importing the server package.
func defaultRigDir() string {
	if dir := os.Getenv("RIG_DIR"); dir != "" {
		return dir
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(os.TempDir(), "rig")
	}
	return filepath.Join(home, ".rig")
}
