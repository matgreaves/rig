package artifact_test

import (
	"os"
	"testing"

	"github.com/matgreaves/rig/server/artifact"
)

func TestCache_OutputDir(t *testing.T) {
	dir := t.TempDir()
	cache := artifact.NewCache(dir)

	outDir := cache.OutputDir("mykey")
	if outDir == "" {
		t.Fatal("OutputDir returned empty string")
	}

	// Directory should exist after OutputDir call.
	if _, err := os.Stat(outDir); err != nil {
		t.Errorf("OutputDir %q does not exist: %v", outDir, err)
	}
}

func TestCache_Lock(t *testing.T) {
	cache := artifact.NewCache(t.TempDir())

	unlock, err := cache.Lock("mykey")
	if err != nil {
		t.Fatalf("Lock: %v", err)
	}
	// Unlock should not panic.
	unlock()
}
