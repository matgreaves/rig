package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadHeader(t *testing.T) {
	hdr, err := readHeader("testdata/passed.jsonl")
	if err != nil {
		t.Fatalf("readHeader: %v", err)
	}
	if hdr.Type != "log.header" {
		t.Errorf("type = %q, want log.header", hdr.Type)
	}
	if hdr.Environment != "TestBasic" {
		t.Errorf("environment = %q, want TestBasic", hdr.Environment)
	}
	if hdr.Outcome != "passed" {
		t.Errorf("outcome = %q, want passed", hdr.Outcome)
	}
	if len(hdr.Services) != 2 {
		t.Errorf("services = %v, want [db api]", hdr.Services)
	}
	if hdr.DurationMs != 1200 {
		t.Errorf("duration_ms = %v, want 1200", hdr.DurationMs)
	}
}

func TestReadHeaderFailed(t *testing.T) {
	hdr, err := readHeader("testdata/failed.jsonl")
	if err != nil {
		t.Fatalf("readHeader: %v", err)
	}
	if hdr.Outcome != "failed" {
		t.Errorf("outcome = %q, want failed", hdr.Outcome)
	}
	if hdr.Environment != "TestOrderFlow" {
		t.Errorf("environment = %q, want TestOrderFlow", hdr.Environment)
	}
}

func TestReadHeaderNotAHeader(t *testing.T) {
	// mixed_traffic.jsonl starts with a normal event, not a log.header.
	_, err := readHeader("testdata/mixed_traffic.jsonl")
	if err == nil {
		t.Fatal("expected error for non-header first line")
	}
}

// setupLsDir creates a temp rig dir with test log files and sets RIG_DIR.
func setupLsDir(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	logDir := filepath.Join(dir, "logs")
	os.MkdirAll(logDir, 0o755)

	copyFile(t, "testdata/passed.jsonl", filepath.Join(logDir, "TestBasic-19480a00000-aabbccdd.jsonl"))
	copyFile(t, "testdata/failed.jsonl", filepath.Join(logDir, "TestOrderFlow-19480a00001-11223344.jsonl"))
	copyFile(t, "testdata/crashed.jsonl", filepath.Join(logDir, "TestCrash-19480a00002-deadbeef.jsonl"))

	t.Setenv("RIG_DIR", dir)
}

func TestRunLsWithDir(t *testing.T) {
	setupLsDir(t)

	// Test: list all.
	output := captureStdout(t, func() {
		if err := runLs(nil); err != nil {
			t.Fatalf("runLs: %v", err)
		}
	})
	if !strings.Contains(output, "TestBasic") {
		t.Errorf("missing TestBasic in output:\n%s", output)
	}
	if !strings.Contains(output, "TestOrderFlow") {
		t.Errorf("missing TestOrderFlow in output:\n%s", output)
	}
	if !strings.Contains(output, "TestCrash") {
		t.Errorf("missing TestCrash in output:\n%s", output)
	}

	// Test: --failed filter.
	output = captureStdout(t, func() {
		if err := runLs([]string{"--failed"}); err != nil {
			t.Fatalf("runLs --failed: %v", err)
		}
	})
	if strings.Contains(output, "TestBasic") {
		t.Errorf("--failed should not show passed test:\n%s", output)
	}
	if !strings.Contains(output, "TestOrderFlow") {
		t.Errorf("--failed should show TestOrderFlow:\n%s", output)
	}
	if !strings.Contains(output, "TestCrash") {
		t.Errorf("--failed should show TestCrash:\n%s", output)
	}

	// Test: --passed filter.
	output = captureStdout(t, func() {
		if err := runLs([]string{"--passed"}); err != nil {
			t.Fatalf("runLs --passed: %v", err)
		}
	})
	if !strings.Contains(output, "TestBasic") {
		t.Errorf("--passed should show TestBasic:\n%s", output)
	}
	if strings.Contains(output, "TestOrderFlow") {
		t.Errorf("--passed should not show failed test:\n%s", output)
	}

	// Test: glob filter.
	output = captureStdout(t, func() {
		if err := runLs([]string{"Order"}); err != nil {
			t.Fatalf("runLs Order: %v", err)
		}
	})
	if !strings.Contains(output, "TestOrderFlow") {
		t.Errorf("glob should match TestOrderFlow:\n%s", output)
	}
	if strings.Contains(output, "TestBasic") {
		t.Errorf("glob should not match TestBasic:\n%s", output)
	}
}

func TestRunLsQuiet(t *testing.T) {
	setupLsDir(t)

	output := captureStdout(t, func() {
		if err := runLs([]string{"-q"}); err != nil {
			t.Fatalf("runLs -q: %v", err)
		}
	})

	lines := strings.Split(strings.TrimSpace(output), "\n")
	if len(lines) != 3 {
		t.Fatalf("expected 3 file paths, got %d: %s", len(lines), output)
	}
	for _, line := range lines {
		if !strings.HasSuffix(line, ".jsonl") {
			t.Errorf("expected .jsonl path, got: %s", line)
		}
	}
}

func TestRunLsQuietFailed(t *testing.T) {
	setupLsDir(t)

	output := captureStdout(t, func() {
		if err := runLs([]string{"--failed", "-q"}); err != nil {
			t.Fatalf("runLs --failed -q: %v", err)
		}
	})

	lines := strings.Split(strings.TrimSpace(output), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected 2 file paths (failed + crashed), got %d: %s", len(lines), output)
	}
}

func TestRunLsLimit(t *testing.T) {
	setupLsDir(t)

	output := captureStdout(t, func() {
		if err := runLs([]string{"-q", "-n", "1"}); err != nil {
			t.Fatalf("runLs -q -n 1: %v", err)
		}
	})

	lines := strings.Split(strings.TrimSpace(output), "\n")
	if len(lines) != 1 {
		t.Fatalf("expected 1 file path with -n 1, got %d: %s", len(lines), output)
	}
	// Should be the most recent (TestCrash has the latest timestamp in our fixtures).
	if !strings.Contains(lines[0], "TestCrash") {
		t.Errorf("-n 1 should return most recent file, got: %s", lines[0])
	}
}

func TestRunLsNoResults(t *testing.T) {
	setupLsDir(t)

	// Search for a pattern that won't match anything.
	err := runLs([]string{"nonexistent_pattern_xyz"})
	if err != errNoResults {
		t.Errorf("expected errNoResults, got: %v", err)
	}
}

func TestRunLsEmptyDir(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("RIG_DIR", dir)
	// No logs/ dir at all.
	err := runLs(nil)
	if err != errNoResults {
		t.Errorf("expected errNoResults for missing dir, got: %v", err)
	}
}

func copyFile(t *testing.T, src, dst string) {
	t.Helper()
	data, err := os.ReadFile(src)
	if err != nil {
		t.Fatalf("read %s: %v", src, err)
	}
	if err := os.WriteFile(dst, data, 0o644); err != nil {
		t.Fatalf("write %s: %v", dst, err)
	}
}

func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stdout = w
	// Disable colors since we're capturing to a pipe.
	oldColor := colorEnabled
	colorEnabled = false
	defer func() {
		colorEnabled = oldColor
		os.Stdout = old
	}()

	fn()

	w.Close()
	data := make([]byte, 64*1024)
	n, _ := r.Read(data)
	return string(data[:n])
}
