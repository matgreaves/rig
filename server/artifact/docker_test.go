package artifact_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/matgreaves/rig/server/artifact"
	"github.com/matgreaves/rig/server/dockerutil"
)

func requireDocker(t *testing.T) {
	t.Helper()
	cli, err := dockerutil.Client()
	if err != nil {
		t.Fatal("docker not available:", err)
	}
	_, err = cli.Ping(context.Background())
	if err != nil {
		t.Fatal("docker daemon not reachable (is Docker running?):", err)
	}
}

func TestDockerPull_CacheKey(t *testing.T) {
	a := artifact.DockerPull{Image: "alpine:3.20"}
	b := artifact.DockerPull{Image: "alpine:3.20"}
	c := artifact.DockerPull{Image: "nginx:alpine"}

	keyA, err := a.CacheKey()
	if err != nil {
		t.Fatal(err)
	}
	keyB, err := b.CacheKey()
	if err != nil {
		t.Fatal(err)
	}
	keyC, err := c.CacheKey()
	if err != nil {
		t.Fatal(err)
	}

	if keyA != keyB {
		t.Errorf("same image should produce same key: %q != %q", keyA, keyB)
	}
	if keyA == keyC {
		t.Error("different images should produce different keys")
	}
	if keyA[:7] != "docker/" {
		t.Errorf("key should start with docker/: %q", keyA)
	}
}

func TestDockerPull_Retryable(t *testing.T) {
	d := artifact.DockerPull{Image: "alpine:3.20"}
	if !d.Retryable() {
		t.Error("DockerPull should be retryable")
	}
}

func TestDockerPull_CachedMiss(t *testing.T) {
	d := artifact.DockerPull{Image: "alpine:3.20"}
	_, ok := d.Cached(t.TempDir())
	if ok {
		t.Error("should be a cache miss on empty directory")
	}
}

func TestDockerPull_CachedHit(t *testing.T) {
	d := artifact.DockerPull{Image: "alpine:3.20"}
	dir := t.TempDir()

	// Write a breadcrumb file.
	os.WriteFile(filepath.Join(dir, ".image-id"), []byte("sha256:abc123"), 0o644)

	out, ok := d.Cached(dir)
	if !ok {
		t.Fatal("should be a cache hit")
	}
	if out.Meta["image"] != "alpine:3.20" {
		t.Errorf("unexpected image: %q", out.Meta["image"])
	}
	if out.Meta["image_id"] != "sha256:abc123" {
		t.Errorf("unexpected image_id: %q", out.Meta["image_id"])
	}
}

func TestDockerPull_ResolveAndValid(t *testing.T) {
	requireDocker(t)
	t.Parallel()

	d := artifact.DockerPull{Image: "alpine:3.20"}
	dir := t.TempDir()

	out, err := d.Resolve(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}

	if out.Path != "" {
		t.Errorf("DockerPull output should have empty Path, got %q", out.Path)
	}
	if out.Meta["image"] != "alpine:3.20" {
		t.Errorf("unexpected image: %q", out.Meta["image"])
	}
	if out.Meta["image_id"] == "" {
		t.Error("image_id should not be empty after resolve")
	}

	// Breadcrumb should exist.
	data, err := os.ReadFile(filepath.Join(dir, ".image-id"))
	if err != nil {
		t.Fatal("breadcrumb not written:", err)
	}
	if string(data) != out.Meta["image_id"] {
		t.Error("breadcrumb content mismatch")
	}

	// Cached should now return true.
	cachedOut, ok := d.Cached(dir)
	if !ok {
		t.Error("should be a cache hit after resolve")
	}
	if cachedOut.Meta["image_id"] != out.Meta["image_id"] {
		t.Error("cached image_id mismatch")
	}

	// Valid should return true.
	if !d.Valid(out) {
		t.Error("Valid should return true for a just-pulled image")
	}
}
