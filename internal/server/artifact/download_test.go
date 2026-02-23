package artifact_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/matgreaves/rig/internal/server/artifact"
)

// buildTarGz creates a tar.gz archive in memory containing a single file.
func buildTarGz(t *testing.T, name string, content []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)

	if err := tw.WriteHeader(&tar.Header{
		Name: name,
		Mode: 0o755,
		Size: int64(len(content)),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(content); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestDownload_CacheKey_Stable(t *testing.T) {
	d := artifact.Download{URL: "https://example.com/foo.tar.gz", Binary: "foo"}

	key1, err := d.CacheKey()
	if err != nil {
		t.Fatal(err)
	}
	key2, err := d.CacheKey()
	if err != nil {
		t.Fatal(err)
	}
	if key1 == "" {
		t.Fatal("empty cache key")
	}
	if key1 != key2 {
		t.Errorf("unstable: %q != %q", key1, key2)
	}
}

func TestDownload_CacheKey_DifferentURLs(t *testing.T) {
	d1 := artifact.Download{URL: "https://example.com/v1.tar.gz", Binary: "foo"}
	d2 := artifact.Download{URL: "https://example.com/v2.tar.gz", Binary: "foo"}

	k1, _ := d1.CacheKey()
	k2, _ := d2.CacheKey()
	if k1 == k2 {
		t.Error("different URLs should produce different cache keys")
	}
}

func TestDownload_Cached(t *testing.T) {
	d := artifact.Download{URL: "https://example.com/foo.tar.gz", Binary: "mybinary"}
	dir := t.TempDir()

	// Miss on empty directory.
	if _, ok := d.Cached(dir); ok {
		t.Error("expected miss on empty dir")
	}

	// Miss on zero-byte file.
	if err := os.WriteFile(filepath.Join(dir, "mybinary"), []byte{}, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, ok := d.Cached(dir); ok {
		t.Error("expected miss on zero-byte binary")
	}

	// Hit on non-empty file.
	if err := os.WriteFile(filepath.Join(dir, "mybinary"), []byte("ELF..."), 0o755); err != nil {
		t.Fatal(err)
	}
	out, ok := d.Cached(dir)
	if !ok {
		t.Fatal("expected cache hit")
	}
	if out.Path != filepath.Join(dir, "mybinary") {
		t.Errorf("Path = %q, want %q", out.Path, filepath.Join(dir, "mybinary"))
	}
}

func TestDownload_Resolve(t *testing.T) {
	// Create a tar.gz with a shell script as the "binary".
	script := []byte("#!/bin/sh\necho hello\n")
	archive := buildTarGz(t, "mybinary", script)

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/gzip")
		w.Write(archive)
	}))
	defer ts.Close()

	d := artifact.Download{URL: ts.URL + "/mybinary.tar.gz", Binary: "mybinary"}
	outputDir := t.TempDir()

	out, err := d.Resolve(context.Background(), outputDir)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	if out.Path == "" {
		t.Fatal("empty Path")
	}

	// Verify content.
	data, err := os.ReadFile(out.Path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != string(script) {
		t.Errorf("content = %q, want %q", data, script)
	}

	// Verify executable.
	info, err := os.Stat(out.Path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&0o111 == 0 {
		t.Error("binary is not executable")
	}
}

func TestDownload_Resolve_NestedPath(t *testing.T) {
	// Binary nested under a directory in the archive.
	script := []byte("#!/bin/sh\necho nested\n")
	archive := buildTarGz(t, "some/dir/mybinary", script)

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(archive)
	}))
	defer ts.Close()

	d := artifact.Download{URL: ts.URL, Binary: "mybinary"}
	outputDir := t.TempDir()

	out, err := d.Resolve(context.Background(), outputDir)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if out.Path != filepath.Join(outputDir, "mybinary") {
		t.Errorf("Path = %q, want binary at output root", out.Path)
	}
}

func TestDownload_Resolve_MissingBinary(t *testing.T) {
	archive := buildTarGz(t, "other-file", []byte("not the binary"))

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(archive)
	}))
	defer ts.Close()

	d := artifact.Download{URL: ts.URL, Binary: "mybinary"}
	_, err := d.Resolve(context.Background(), t.TempDir())
	if err == nil {
		t.Fatal("expected error for missing binary")
	}
}

func TestDownload_Resolve_HTTPError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer ts.Close()

	d := artifact.Download{URL: ts.URL, Binary: "mybinary"}
	_, err := d.Resolve(context.Background(), t.TempDir())
	if err == nil {
		t.Fatal("expected error for 404")
	}
}

func TestDownload_Retryable(t *testing.T) {
	d := artifact.Download{URL: "https://example.com/foo.tar.gz", Binary: "foo"}
	if !d.Retryable() {
		t.Error("Download should be retryable")
	}
}
