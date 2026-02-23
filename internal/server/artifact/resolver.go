package artifact

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// EventKind identifies the type of artifact lifecycle event.
type EventKind string

const (
	EventStarted   EventKind = "started"
	EventCompleted EventKind = "completed"
	EventCached    EventKind = "cached"
	EventFailed    EventKind = "failed"
)

// EmitFunc is called for each artifact lifecycle event.
// err is non-nil only when kind is EventFailed.
type EmitFunc func(kind EventKind, key string, err error)

// Resolve resolves all artifacts, deduplicating by Artifact.Key (first wins).
// Cache-hit artifacts are recorded immediately; cache-miss artifacts are
// resolved in parallel. Returns a map of Artifact.Key → Output.
//
// Retryable resolvers are attempted up to 3 times with exponential backoff
// (1s, 2s). Non-retryable resolvers are attempted once. The first error from
// any artifact cancels in-flight resolutions and is returned.
func Resolve(ctx context.Context, artifacts []Artifact, cache *Cache, emit EmitFunc) (map[string]Output, error) {
	// Deduplicate by key; first occurrence wins.
	seen := make(map[string]struct{}, len(artifacts))
	var unique []Artifact
	for _, a := range artifacts {
		if _, exists := seen[a.Key]; !exists {
			seen[a.Key] = struct{}{}
			unique = append(unique, a)
		}
	}

	results := make(map[string]Output, len(unique))
	var mu sync.Mutex

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	// Capacity matches the maximum number of errors that could be sent.
	errCh := make(chan error, len(unique))
	var wg sync.WaitGroup

	for _, a := range unique {
		cacheKey, err := a.Resolver.CacheKey()
		if err != nil {
			return nil, fmt.Errorf("artifact %q: cache key: %w", a.Key, err)
		}

		outputDir := cache.OutputDir(cacheKey)

		// Check cache before spawning a goroutine.
		if out, ok := checkCached(a.Resolver, outputDir); ok {
			if emit != nil {
				emit(EventCached, a.Key, nil)
			}
			touchLastUsed(outputDir)
			mu.Lock()
			results[a.Key] = out
			mu.Unlock()
			continue
		}

		wg.Add(1)
		go func(a Artifact, cacheKey, outputDir string) {
			defer wg.Done()

			// Acquire per-key file lock to prevent duplicate work across
			// concurrent rigd instances.
			unlock, err := cache.Lock(cacheKey)
			if err != nil {
				errCh <- fmt.Errorf("artifact %q: acquire lock: %w", a.Key, err)
				return
			}
			defer unlock()

			// Re-check cache after acquiring the lock — another process may
			// have resolved this artifact while we were waiting.
			if out, ok := checkCached(a.Resolver, outputDir); ok {
				if emit != nil {
					emit(EventCached, a.Key, nil)
				}
				touchLastUsed(outputDir)
				mu.Lock()
				results[a.Key] = out
				mu.Unlock()
				return
			}

			if emit != nil {
				emit(EventStarted, a.Key, nil)
			}

			out, resolveErr := resolveWithRetry(ctx, a.Resolver, outputDir)
			if resolveErr != nil {
				if emit != nil {
					emit(EventFailed, a.Key, resolveErr)
				}
				cancel()
				errCh <- fmt.Errorf("artifact %q: %w", a.Key, resolveErr)
				return
			}

			if emit != nil {
				emit(EventCompleted, a.Key, nil)
			}

			touchLastUsed(outputDir)
			mu.Lock()
			results[a.Key] = out
			mu.Unlock()
		}(a, cacheKey, outputDir)
	}

	wg.Wait()
	close(errCh)

	// Return the first real error, preferring non-cancellation errors over
	// context.Canceled (which are a side-effect of our own cancel() call).
	var firstErr, firstReal error
	for err := range errCh {
		if firstErr == nil {
			firstErr = err
		}
		if firstReal == nil && !errors.Is(err, context.Canceled) {
			firstReal = err
		}
	}
	if firstReal != nil {
		return nil, firstReal
	}
	if firstErr != nil {
		return nil, firstErr
	}

	return results, nil
}

// checkCached asks the resolver whether a valid cached artifact exists in
// outputDir. If the resolver also implements Validator (for artifacts whose
// backing store is outside rig's control, e.g. Docker images), the output
// is validated against the external source of truth.
func checkCached(r Resolver, outputDir string) (Output, bool) {
	out, ok := r.Cached(outputDir)
	if !ok {
		return Output{}, false
	}
	if v, ok := r.(Validator); ok {
		return out, v.Valid(out)
	}
	return out, true
}

// resolveWithRetry calls r.Resolve, retrying on failure if r.Retryable().
// Retryable resolvers are attempted up to 3 times with 1s, 2s backoff.
func resolveWithRetry(ctx context.Context, r Resolver, outputDir string) (Output, error) {
	maxAttempts := 1
	if r.Retryable() {
		maxAttempts = 3
	}

	backoff := time.Second
	var lastErr error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		if ctx.Err() != nil {
			return Output{}, ctx.Err()
		}
		if attempt > 0 {
			timer := time.NewTimer(backoff)
			select {
			case <-ctx.Done():
				timer.Stop()
				return Output{}, ctx.Err()
			case <-timer.C:
			}
			backoff *= 2
		}

		out, err := r.Resolve(ctx, outputDir)
		if err == nil {
			return out, nil
		}
		lastErr = err
	}
	return Output{}, lastErr
}

// touchLastUsed updates the mtime of a .last-used marker in outputDir.
// A future cache eviction command can use this for LRU ordering.
func touchLastUsed(outputDir string) {
	p := filepath.Join(outputDir, ".last-used")
	now := time.Now()
	if err := os.Chtimes(p, now, now); err != nil {
		// File doesn't exist yet — create it.
		f, err := os.Create(p)
		if err == nil {
			f.Close()
		}
	}
}
