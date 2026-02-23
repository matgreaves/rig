package rig

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestDownloadURL(t *testing.T) {
	url := downloadURL("0.1.0")
	want := "https://github.com/matgreaves/rig/releases/download/rigd/v0.1.0/rigd-" + runtime.GOOS + "-" + runtime.GOARCH + ".tar.gz"
	if url != want {
		t.Errorf("downloadURL:\n  got  %s\n  want %s", url, want)
	}
}

// makeTarGz creates a tar.gz archive containing a single file named "rigd"
// with the given content.
func makeTarGz(t *testing.T, content []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)

	if err := tw.WriteHeader(&tar.Header{
		Name: "rigd",
		Size: int64(len(content)),
		Mode: 0o755,
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

func TestDownloadBinary(t *testing.T) {
	payload := []byte("#!/bin/sh\necho hello\n")
	archive := makeTarGz(t, payload)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(archive)
	}))
	defer srv.Close()

	dest := filepath.Join(t.TempDir(), "v0.1.0", "rigd")

	if err := downloadBinary(srv.URL+"/rigd.tar.gz", dest); err != nil {
		t.Fatalf("downloadBinary: %v", err)
	}

	// Verify file exists with correct content.
	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("read extracted binary: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Errorf("content mismatch:\n  got  %q\n  want %q", got, payload)
	}

	// Verify permissions.
	info, err := os.Stat(dest)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm&0o111 == 0 {
		t.Errorf("binary not executable: %o", perm)
	}
}

func TestDownloadBinaryCreatesDirectory(t *testing.T) {
	payload := []byte("binary")
	archive := makeTarGz(t, payload)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(archive)
	}))
	defer srv.Close()

	// Nested path that doesn't exist yet.
	dest := filepath.Join(t.TempDir(), "deep", "nested", "rigd")

	if err := downloadBinary(srv.URL+"/rigd.tar.gz", dest); err != nil {
		t.Fatalf("downloadBinary: %v", err)
	}

	if _, err := os.Stat(dest); err != nil {
		t.Fatalf("binary not found: %v", err)
	}
}

func TestDownloadBinaryHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	dest := filepath.Join(t.TempDir(), "rigd")
	err := downloadBinary(srv.URL+"/rigd.tar.gz", dest)
	if err == nil {
		t.Fatal("expected error for HTTP 404")
	}
}

func TestDownloadBinaryMissingEntry(t *testing.T) {
	// Archive with a different file name — "not-rigd".
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)
	tw.WriteHeader(&tar.Header{Name: "not-rigd", Size: 4, Mode: 0o755})
	tw.Write([]byte("data"))
	tw.Close()
	gw.Close()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(buf.Bytes())
	}))
	defer srv.Close()

	dest := filepath.Join(t.TempDir(), "rigd")
	err := downloadBinary(srv.URL+"/rigd.tar.gz", dest)
	if err == nil {
		t.Fatal("expected error for missing rigd entry")
	}
}

func TestDownloadBinaryNoTempFileLeftover(t *testing.T) {
	payload := []byte("binary")
	archive := makeTarGz(t, payload)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(archive)
	}))
	defer srv.Close()

	dir := t.TempDir()
	dest := filepath.Join(dir, "rigd")

	if err := downloadBinary(srv.URL+"/rigd.tar.gz", dest); err != nil {
		t.Fatal(err)
	}

	// Only "rigd" should exist in dir — no temp file leftovers.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "rigd" {
		names := make([]string, len(entries))
		for i, e := range entries {
			names[i] = e.Name()
		}
		t.Errorf("unexpected files in dir: %v", names)
	}
}
