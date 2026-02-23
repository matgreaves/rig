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

	binPath, override, err := findBinary()
	if err != nil {
		return "", err
	}

	// When RIG_BINARY is set (override), use unversioned file names for
	// backwards compatibility. Otherwise use per-version names so multiple
	// SDK versions can coexist.
	addrFile := filepath.Join(rigDir, "rigd.addr")
	lockFile := filepath.Join(rigDir, "rigd.lock")
	if !override {
		addrFile = filepath.Join(rigDir, "rigd-v"+RigdVersion+".addr")
		lockFile = filepath.Join(rigDir, "rigd-v"+RigdVersion+".lock")
	}

	// Fast path: existing instance.
	if addr, err := os.ReadFile(addrFile); err == nil {
		if probeHealth(string(addr)) {
			return "http://" + string(addr), nil
		}
	}

	// Acquire lock to prevent concurrent starts.
	if err := os.MkdirAll(rigDir, 0o755); err != nil {
		return "", fmt.Errorf("create rig dir: %w", err)
	}
	unlock, err := acquireLock(lockFile)
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

	// If no binary found, download the version this SDK targets.
	if binPath == "" {
		binPath = filepath.Join(rigDir, "bin", "v"+RigdVersion, "rigd")
		url := downloadURL(RigdVersion)
		if err := downloadBinary(url, binPath); err != nil {
			return "", fmt.Errorf("download rigd v%s: %w", RigdVersion, err)
		}
	}

	// Start rigd as a detached subprocess.
	args := []string{"--idle", "5m", "--rig-dir", rigDir}
	if !override {
		args = append(args, "--addr-file", addrFile)
	}
	cmd := exec.Command(binPath, args...)
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

// findBinary locates the rigd binary. Returns the path and whether this is an
// explicit override (RIG_BINARY). When no binary is found, returns ("", false, nil)
// so the caller can download one.
//
// Search order:
//  1. RIG_BINARY env var (explicit override for dev/CI)
//  2. ~/.rig/bin/v{RigdVersion}/rigd (versioned managed path)
//  3. ~/.rig/bin/rigd (legacy unversioned)
//  4. PATH lookup
//  5. Not found — caller will download
func findBinary() (path string, override bool, err error) {
	if p := os.Getenv("RIG_BINARY"); p != "" {
		if _, err := os.Stat(p); err == nil {
			return p, true, nil
		}
		return "", false, fmt.Errorf("RIG_BINARY=%q: file not found", p)
	}
	home, err := os.UserHomeDir()
	if err == nil {
		// Versioned path.
		p := filepath.Join(home, ".rig", "bin", "v"+RigdVersion, "rigd")
		if _, err := os.Stat(p); err == nil {
			return p, false, nil
		}
		// Legacy unversioned path.
		p = filepath.Join(home, ".rig", "bin", "rigd")
		if _, err := os.Stat(p); err == nil {
			return p, false, nil
		}
	}
	if p, err := exec.LookPath("rigd"); err == nil {
		return p, false, nil
	}
	return "", false, nil
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
