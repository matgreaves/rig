package artifact_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/matgreaves/rig/server/artifact"
)

// moduleRoot returns the module root by finding go.mod relative to the test
// working directory. The artifact package is two levels below the module root
// (server/artifact/).
func moduleRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	// Walk up to find go.mod.
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

func TestGoBuild_CacheKey_Stable(t *testing.T) {
	root := moduleRoot(t)
	echoDir := filepath.Join(root, "testdata", "services", "echo")

	g := artifact.GoBuild{Module: echoDir}

	key1, err := g.CacheKey()
	if err != nil {
		t.Fatalf("CacheKey (first): %v", err)
	}
	key2, err := g.CacheKey()
	if err != nil {
		t.Fatalf("CacheKey (second): %v", err)
	}

	if key1 == "" {
		t.Fatal("CacheKey returned empty string")
	}
	if key1 != key2 {
		t.Errorf("CacheKey not stable: %q != %q", key1, key2)
	}
}

func TestGoBuild_CacheKey_Changes(t *testing.T) {
	// Create a temporary Go module with a single .go file.
	tmpDir := t.TempDir()
	modFile := filepath.Join(tmpDir, "go.mod")
	mainFile := filepath.Join(tmpDir, "main.go")

	if err := os.WriteFile(modFile, []byte("module example.com/tmp\n\ngo 1.21\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(mainFile, []byte("package main\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	g := artifact.GoBuild{Module: tmpDir}
	key1, err := g.CacheKey()
	if err != nil {
		t.Fatalf("CacheKey before modification: %v", err)
	}

	// Modify the file.
	if err := os.WriteFile(mainFile, []byte("package main\nimport \"fmt\"\nfunc main() { fmt.Println(\"changed\") }\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	key2, err := g.CacheKey()
	if err != nil {
		t.Fatalf("CacheKey after modification: %v", err)
	}

	if key1 == key2 {
		t.Error("CacheKey should change after source modification")
	}
}

func TestGoBuild_Resolve(t *testing.T) {
	root := moduleRoot(t)
	echoDir := filepath.Join(root, "testdata", "services", "echo")

	g := artifact.GoBuild{Module: echoDir}
	outputDir := t.TempDir()

	ctx := context.Background()
	out, err := g.Resolve(ctx, outputDir)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	if out.Path == "" {
		t.Fatal("Resolve returned empty Path")
	}

	// Binary should exist and be executable.
	info, err := os.Stat(out.Path)
	if err != nil {
		t.Fatalf("binary not found at %q: %v", out.Path, err)
	}
	if info.Mode()&0o111 == 0 {
		t.Error("binary is not executable")
	}
}

func TestGoBuild_RemoteCacheKey_RequiresVersion(t *testing.T) {
	g := artifact.GoBuild{Module: "github.com/example/tool"} // no @version
	_, err := g.CacheKey()
	if err == nil {
		t.Error("expected error for remote module without @version")
	}
}

func TestGoBuild_Cached(t *testing.T) {
	g := artifact.GoBuild{Module: "/some/module"}
	emptyDir := t.TempDir()

	// Empty directory — should be a miss.
	if _, ok := g.Cached(emptyDir); ok {
		t.Error("expected cache miss on empty directory")
	}

	// Write a zero-byte binary — should also be a miss.
	binaryPath := filepath.Join(emptyDir, "binary")
	if err := os.WriteFile(binaryPath, []byte{}, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, ok := g.Cached(emptyDir); ok {
		t.Error("expected cache miss for zero-byte binary")
	}

	// Write a non-empty binary — should be a hit.
	if err := os.WriteFile(binaryPath, []byte("ELF..."), 0o755); err != nil {
		t.Fatal(err)
	}
	out, ok := g.Cached(emptyDir)
	if !ok {
		t.Fatal("expected cache hit for non-empty binary")
	}
	if out.Path != binaryPath {
		t.Errorf("Path = %q, want %q", out.Path, binaryPath)
	}
	if out.Meta["module"] != "/some/module" {
		t.Errorf("Meta[module] = %q, want %q", out.Meta["module"], "/some/module")
	}
}

func TestGoBuild_Retryable(t *testing.T) {
	local := artifact.GoBuild{Module: "/abs/path"}
	if local.Retryable() {
		t.Error("local GoBuild should not be retryable")
	}

	remote := artifact.GoBuild{Module: "github.com/example/tool@v1.0.0"}
	if !remote.Retryable() {
		t.Error("remote GoBuild should be retryable")
	}
}
