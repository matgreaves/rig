package service

import (
	"context"
	"fmt"
	"io"
	"os"
	"testing"
	"time"

	"github.com/docker/docker/api/types/image"
	"github.com/matgreaves/rig/internal/server/dockerutil"
)

// TestS3PoolSpeedup measures the incremental cost of adding a test
// environment to a warm pool (new bucket) versus starting a fresh
// MinIO container from scratch. This is a one-shot comparison for PR
// documentation, not a permanent test.
func TestS3PoolSpeedup(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping pool benchmark in short mode")
	}

	cli, err := dockerutil.Client()
	if err != nil {
		t.Skipf("Docker not available: %v", err)
	}
	ctx := context.Background()
	if _, err := cli.ServerVersion(ctx); err != nil {
		t.Skipf("Docker daemon not reachable: %v", err)
	}

	const minioImage = s3DefaultImage
	pid := os.Getpid()

	// Pre-pull so image pull time doesn't skew results.
	t.Log("pre-pulling", minioImage)
	rc, err := cli.ImagePull(ctx, minioImage, image.PullOptions{})
	if err != nil {
		t.Fatalf("image pull: %v", err)
	}
	io.Copy(io.Discard, rc)
	rc.Close()
	t.Log("image ready")

	// --- Warm up the pool with one lease (cold start) ---
	pool := NewS3Pool(pid)
	defer pool.Close()

	warmStart := time.Now()
	first, err := pool.Acquire(ctx, minioImage)
	if err != nil {
		t.Fatalf("pool cold start: %v", err)
	}
	coldDur := time.Since(warmStart)

	// --- Pooled: incremental lease on warm pool ---
	incrStart := time.Now()
	second, err := pool.Acquire(ctx, minioImage)
	if err != nil {
		t.Fatalf("pool incremental acquire: %v", err)
	}
	incrDur := time.Since(incrStart)

	pool.Release(second)
	pool.Release(first)

	// --- Unpooled: fresh container from scratch ---
	unpooled := &s3Backend{
		containerName: fmt.Sprintf("rig-bench-s3-unpooled-%d", pid),
	}
	freshStart := time.Now()
	_, _, err = unpooled.Start(ctx)
	if err != nil {
		t.Fatalf("unpooled start: %v", err)
	}
	_, _, err = unpooled.NewLease(ctx)
	if err != nil {
		unpooled.Stop()
		t.Fatalf("unpooled lease: %v", err)
	}
	freshDur := time.Since(freshStart)
	unpooled.Stop()

	// --- Report ---
	t.Logf("")
	t.Logf("Scenario               | Wall-clock")
	t.Logf("-----------------------|-----------")
	t.Logf("Pool cold start        | %s", coldDur.Round(time.Millisecond))
	t.Logf("Pool incremental lease | %s", incrDur.Round(time.Millisecond))
	t.Logf("Fresh container        | %s", freshDur.Round(time.Millisecond))
	t.Logf("")
	t.Logf("Incremental speedup vs fresh container: %.0fx", float64(freshDur)/float64(incrDur))
}
