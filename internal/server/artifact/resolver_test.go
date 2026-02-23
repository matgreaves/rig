package artifact_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/matgreaves/rig/internal/server/artifact"
)

// stubResolver is a test double for artifact.Resolver.
type stubResolver struct {
	cacheKey   string
	output     artifact.Output
	cachedOut  *artifact.Output // if non-nil, Cached returns this output as a hit
	resolveN   *atomic.Int64    // counts Resolve calls
	retryable  bool
	resolveErr error
}

func (s *stubResolver) CacheKey() (string, error) {
	return s.cacheKey, nil
}

func (s *stubResolver) Cached(outputDir string) (artifact.Output, bool) {
	if s.cachedOut != nil {
		return *s.cachedOut, true
	}
	// Default: check for a "binary" file in outputDir (same as GoBuild).
	p := filepath.Join(outputDir, "binary")
	if _, err := os.Stat(p); err != nil {
		return artifact.Output{}, false
	}
	return artifact.Output{Path: p}, true
}

func (s *stubResolver) Resolve(_ context.Context, outputDir string) (artifact.Output, error) {
	if s.resolveN != nil {
		s.resolveN.Add(1)
	}
	if s.resolveErr != nil {
		return artifact.Output{}, s.resolveErr
	}
	out := s.output
	if out.Path == "" {
		// Write a marker file so future Cached calls find it.
		p := filepath.Join(outputDir, "binary")
		os.WriteFile(p, []byte("stub"), 0o755) //nolint:errcheck
		out.Path = p
	}
	return out, nil
}

func (s *stubResolver) Retryable() bool {
	return s.retryable
}

// validatingResolver wraps stubResolver and implements artifact.Validator.
type validatingResolver struct {
	stubResolver
	valid bool
}

func (v *validatingResolver) Valid(_ artifact.Output) bool {
	return v.valid
}

// blockingResolver blocks until ctx is cancelled, counting how many times Resolve was called.
type blockingResolver struct {
	cacheKey string
	resolveN *atomic.Int64
}

func (b *blockingResolver) CacheKey() (string, error) { return b.cacheKey, nil }
func (b *blockingResolver) Cached(string) (artifact.Output, bool) {
	return artifact.Output{}, false
}
func (b *blockingResolver) Resolve(ctx context.Context, outputDir string) (artifact.Output, error) {
	if b.resolveN != nil {
		b.resolveN.Add(1)
	}
	<-ctx.Done()
	return artifact.Output{}, ctx.Err()
}
func (b *blockingResolver) Retryable() bool { return false }

func TestResolve_CacheHit(t *testing.T) {
	cache := artifact.NewCache(t.TempDir())

	cached := artifact.Output{Path: "/cached/binary"}
	var called atomic.Int64
	resolver := &stubResolver{
		cacheKey:  "abc123",
		cachedOut: &cached, // stub reports a cache hit
		resolveN:  &called,
	}

	artifacts := []artifact.Artifact{{Key: "my-artifact", Resolver: resolver}}

	results, err := artifact.Resolve(context.Background(), artifacts, cache, nil)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	if called.Load() != 0 {
		t.Errorf("Resolve called %d times, want 0 (cache hit)", called.Load())
	}

	out, ok := results["my-artifact"]
	if !ok {
		t.Fatal("result missing for 'my-artifact'")
	}
	if out.Path != cached.Path {
		t.Errorf("Path = %q, want %q", out.Path, cached.Path)
	}
}

func TestResolve_Dedup(t *testing.T) {
	cache := artifact.NewCache(t.TempDir())

	var called atomic.Int64
	resolver := &stubResolver{
		cacheKey: "shared-key",
		resolveN: &called,
	}

	// Two artifacts with the same key — resolver should be called only once.
	artifacts := []artifact.Artifact{
		{Key: "artifact-a", Resolver: resolver},
		{Key: "artifact-a", Resolver: resolver}, // duplicate key
	}

	results, err := artifact.Resolve(context.Background(), artifacts, cache, nil)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	if called.Load() != 1 {
		t.Errorf("Resolve called %d times, want 1 (dedup)", called.Load())
	}

	if _, ok := results["artifact-a"]; !ok {
		t.Error("result missing for 'artifact-a'")
	}
}

func TestResolve_Parallel(t *testing.T) {
	cache := artifact.NewCache(t.TempDir())

	var totalCalls atomic.Int64

	makeResolver := func(key string) *stubResolver {
		return &stubResolver{
			cacheKey: key,
			resolveN: &totalCalls,
		}
	}

	artifacts := []artifact.Artifact{
		{Key: "artifact-1", Resolver: makeResolver("key-1")},
		{Key: "artifact-2", Resolver: makeResolver("key-2")},
		{Key: "artifact-3", Resolver: makeResolver("key-3")},
	}

	results, err := artifact.Resolve(context.Background(), artifacts, cache, nil)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	if totalCalls.Load() != 3 {
		t.Errorf("Resolve called %d times total, want 3", totalCalls.Load())
	}

	for _, key := range []string{"artifact-1", "artifact-2", "artifact-3"} {
		if _, ok := results[key]; !ok {
			t.Errorf("result missing for %q", key)
		}
	}
}

func TestResolve_Error(t *testing.T) {
	cache := artifact.NewCache(t.TempDir())

	resolver := &stubResolver{
		cacheKey:   "bad-key",
		resolveErr: errors.New("build failed"),
	}

	artifacts := []artifact.Artifact{{Key: "bad-artifact", Resolver: resolver}}

	_, err := artifact.Resolve(context.Background(), artifacts, cache, nil)
	if err == nil {
		t.Fatal("expected error from failed resolver")
	}
}

func TestResolve_EmitEvents(t *testing.T) {
	cache := artifact.NewCache(t.TempDir())

	resolver := &stubResolver{cacheKey: "emit-key"}
	artifacts := []artifact.Artifact{{Key: "emit-artifact", Resolver: resolver}}

	var events []artifact.EventKind
	emit := func(kind artifact.EventKind, key string, err error) {
		events = append(events, kind)
	}

	if _, err := artifact.Resolve(context.Background(), artifacts, cache, emit); err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	// Should emit EventStarted then EventCompleted for a cache miss.
	if len(events) < 2 {
		t.Fatalf("expected at least 2 events, got %d: %v", len(events), events)
	}
	if events[0] != artifact.EventStarted {
		t.Errorf("first event = %q, want %q", events[0], artifact.EventStarted)
	}
	if events[len(events)-1] != artifact.EventCompleted {
		t.Errorf("last event = %q, want %q", events[len(events)-1], artifact.EventCompleted)
	}
}

func TestResolve_ValidatorInvalidatesCachedResult(t *testing.T) {
	cache := artifact.NewCache(t.TempDir())

	cached := artifact.Output{Path: "/cached/binary"}
	var called atomic.Int64
	resolver := &validatingResolver{
		stubResolver: stubResolver{
			cacheKey:  "val-key",
			cachedOut: &cached,
			resolveN:  &called,
		},
		valid: false, // Validator says cached result is stale
	}

	artifacts := []artifact.Artifact{{Key: "val-artifact", Resolver: resolver}}

	results, err := artifact.Resolve(context.Background(), artifacts, cache, nil)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	// Resolver should have been called because Validator returned false.
	if called.Load() != 1 {
		t.Errorf("Resolve called %d times, want 1 (validator invalidated cache)", called.Load())
	}

	if _, ok := results["val-artifact"]; !ok {
		t.Error("result missing for 'val-artifact'")
	}
}

func TestResolve_ValidatorAcceptsCachedResult(t *testing.T) {
	cache := artifact.NewCache(t.TempDir())

	cached := artifact.Output{Path: "/cached/binary"}
	var called atomic.Int64
	resolver := &validatingResolver{
		stubResolver: stubResolver{
			cacheKey:  "val-key-ok",
			cachedOut: &cached,
			resolveN:  &called,
		},
		valid: true, // Validator says cached result is fine
	}

	artifacts := []artifact.Artifact{{Key: "val-ok", Resolver: resolver}}

	results, err := artifact.Resolve(context.Background(), artifacts, cache, nil)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	if called.Load() != 0 {
		t.Errorf("Resolve called %d times, want 0 (validator accepted cache)", called.Load())
	}

	out, ok := results["val-ok"]
	if !ok {
		t.Fatal("result missing for 'val-ok'")
	}
	if out.Path != cached.Path {
		t.Errorf("Path = %q, want %q", out.Path, cached.Path)
	}
}

func TestResolve_CancelsOnFirstError(t *testing.T) {
	cache := artifact.NewCache(t.TempDir())

	var blockCalls atomic.Int64

	artifacts := []artifact.Artifact{
		{Key: "fast-fail", Resolver: &stubResolver{
			cacheKey:   "fail-key",
			resolveErr: errors.New("immediate failure"),
		}},
		{Key: "slow-block", Resolver: &blockingResolver{
			cacheKey: "block-key",
			resolveN: &blockCalls,
		}},
	}

	start := time.Now()
	_, err := artifact.Resolve(context.Background(), artifacts, cache, nil)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected error")
	}

	// The error should mention the real failure, not context.Canceled.
	if !strings.Contains(err.Error(), "immediate failure") {
		t.Errorf("error = %q, want it to contain %q", err.Error(), "immediate failure")
	}

	// Should complete quickly (not hang), well under 10s.
	if elapsed > 5*time.Second {
		t.Errorf("Resolve took %v, expected fast cancellation", elapsed)
	}
}

func TestResolve_TouchLastUsed(t *testing.T) {
	cacheDir := t.TempDir()
	cache := artifact.NewCache(cacheDir)

	resolver := &stubResolver{cacheKey: "touch-key"}
	artifacts := []artifact.Artifact{{Key: "touch-artifact", Resolver: resolver}}

	if _, err := artifact.Resolve(context.Background(), artifacts, cache, nil); err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	// .last-used should exist in the output directory.
	outputDir := cache.OutputDir("touch-key")
	lastUsed := filepath.Join(outputDir, ".last-used")
	info, err := os.Stat(lastUsed)
	if err != nil {
		t.Fatalf(".last-used not found: %v", err)
	}

	// Should have been touched recently (within last minute).
	if time.Since(info.ModTime()) > time.Minute {
		t.Errorf(".last-used mtime is %v ago, expected recent", time.Since(info.ModTime()))
	}
}
