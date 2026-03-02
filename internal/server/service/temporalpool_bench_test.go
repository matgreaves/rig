package service

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestTemporalPoolSpeedup measures the incremental cost of adding a test
// environment to a warm pool (namespace create) versus starting a fresh
// Temporal dev server from scratch. This is a one-shot comparison for PR
// documentation, not a permanent test.
func TestTemporalPoolSpeedup(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping pool benchmark in short mode")
	}

	cacheDir := filepath.Join(os.TempDir(), fmt.Sprintf("rig-temporal-bench-%d", os.Getpid()))
	defer os.RemoveAll(cacheDir)

	ctx := context.Background()

	// --- Warm up the pool with one lease (cold start) ---
	pool := NewTemporalPool(cacheDir)
	defer pool.Close()

	coldStart := time.Now()
	first, err := pool.Acquire(ctx, temporalDefaultVersion)
	if err != nil {
		t.Fatalf("pool cold start: %v", err)
	}
	coldDur := time.Since(coldStart)

	// --- Pooled: incremental lease on warm pool ---
	incrStart := time.Now()
	second, err := pool.Acquire(ctx, temporalDefaultVersion)
	if err != nil {
		t.Fatalf("pool incremental acquire: %v", err)
	}
	incrDur := time.Since(incrStart)

	pool.Release(second)
	pool.Release(first)

	// --- Unpooled: fresh process from scratch ---
	freshCache := filepath.Join(os.TempDir(), fmt.Sprintf("rig-temporal-bench-fresh-%d", os.Getpid()))
	defer os.RemoveAll(freshCache)

	unpooled := &temporalBackend{
		version:  temporalDefaultVersion,
		cacheDir: freshCache,
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
	t.Logf("Fresh process          | %s", freshDur.Round(time.Millisecond))
	t.Logf("")
	t.Logf("Incremental speedup vs fresh process: %.0fx", float64(freshDur)/float64(incrDur))
}
