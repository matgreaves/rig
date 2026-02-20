package server_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// moduleRoot returns the module root directory by finding go.mod.
func moduleRoot(t *testing.T) string {
	t.Helper()
	// We're in the server/ package; module root is one level up.
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Dir(wd)
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("could not find go.mod at %s: %v", root, err)
	}
	return root
}

// buildTestBinary compiles a test service binary and returns the path.
// srcDir is relative to the module root (e.g. "testdata/services/echo/cmd").
func buildTestBinary(t *testing.T, srcDir string) string {
	t.Helper()
	root := moduleRoot(t)
	absSrc := filepath.Join(root, srcDir)
	bin := filepath.Join(t.TempDir(), filepath.Base(srcDir))
	cmd := exec.Command("go", "build", "-o", bin, absSrc)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("build %s: %v", srcDir, err)
	}
	return bin
}

func mustJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
