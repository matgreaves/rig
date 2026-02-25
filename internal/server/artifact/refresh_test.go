package artifact

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestIsMutableRef(t *testing.T) {
	tests := []struct {
		ref     string
		mutable bool
	}{
		{"postgres:16", true},
		{"redis:7-alpine", true},
		{"myimage:latest", true},
		{"myimage", true},
		{"ghcr.io/org/image:v1.2.3", true},
		{"myimage@sha256:abc123def456", false},
		{"ghcr.io/org/image@sha256:abc123", false},
	}
	for _, tt := range tests {
		if got := isMutableRef(tt.ref); got != tt.mutable {
			t.Errorf("isMutableRef(%q) = %v, want %v", tt.ref, got, tt.mutable)
		}
	}
}

func TestShouldRefresh(t *testing.T) {
	t.Run("no last-checked file", func(t *testing.T) {
		dir := t.TempDir()
		if !shouldRefresh(dir, time.Hour) {
			t.Error("expected shouldRefresh=true when .last-checked is missing")
		}
	})

	t.Run("recent last-checked", func(t *testing.T) {
		dir := t.TempDir()
		touchLastChecked(dir)
		if shouldRefresh(dir, time.Hour) {
			t.Error("expected shouldRefresh=false when .last-checked is recent")
		}
	})

	t.Run("old last-checked", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, ".last-checked")
		os.WriteFile(path, nil, 0o644)
		old := time.Now().Add(-2 * time.Hour)
		os.Chtimes(path, old, old)
		if !shouldRefresh(dir, time.Hour) {
			t.Error("expected shouldRefresh=true when .last-checked is old")
		}
	})
}

func TestScanDockerEntries(t *testing.T) {
	cacheDir := t.TempDir()
	dockerDir := filepath.Join(cacheDir, "docker")

	// Entry with both files — should be included.
	good := filepath.Join(dockerDir, "entry1")
	os.MkdirAll(good, 0o755)
	os.WriteFile(filepath.Join(good, ".image-ref"), []byte("postgres:16"), 0o644)
	os.WriteFile(filepath.Join(good, ".image-id"), []byte("sha256:abc123"), 0o644)

	// Entry missing .image-ref — should be skipped.
	noRef := filepath.Join(dockerDir, "entry2")
	os.MkdirAll(noRef, 0o755)
	os.WriteFile(filepath.Join(noRef, ".image-id"), []byte("sha256:def456"), 0o644)

	// Entry missing .image-id — should be skipped.
	noID := filepath.Join(dockerDir, "entry3")
	os.MkdirAll(noID, 0o755)
	os.WriteFile(filepath.Join(noID, ".image-ref"), []byte("redis:7"), 0o644)

	// Entry with empty .image-ref — should be skipped.
	emptyRef := filepath.Join(dockerDir, "entry4")
	os.MkdirAll(emptyRef, 0o755)
	os.WriteFile(filepath.Join(emptyRef, ".image-ref"), []byte(""), 0o644)
	os.WriteFile(filepath.Join(emptyRef, ".image-id"), []byte("sha256:ghi789"), 0o644)

	entries := scanDockerEntries(cacheDir)
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	if entries[0].imageRef != "postgres:16" {
		t.Errorf("imageRef = %q, want %q", entries[0].imageRef, "postgres:16")
	}
}

func TestScanDockerEntries_NoCacheDir(t *testing.T) {
	entries := scanDockerEntries("/nonexistent/path")
	if len(entries) != 0 {
		t.Errorf("expected 0 entries for nonexistent dir, got %d", len(entries))
	}
}

// newTestRefresher creates a Refresher backed by a temp-dir cache with a fake
// resolve function. The cache dir is returned for setting up test fixtures.
func newTestRefresher(t *testing.T, staleAfter time.Duration, resolve resolveFunc) (*Refresher, string) {
	t.Helper()
	cacheDir := t.TempDir()
	r := NewRefresher(NewCache(cacheDir), staleAfter)
	r.resolve = resolve
	return r, cacheDir
}

// writeDockerEntry creates a cached Docker image entry in cacheDir for testing.
func writeDockerEntry(t *testing.T, cacheDir, name, imageRef, imageID string) string {
	t.Helper()
	dir := filepath.Join(cacheDir, "docker", name)
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, ".image-ref"), []byte(imageRef), 0o644)
	os.WriteFile(filepath.Join(dir, ".image-id"), []byte(imageID), 0o644)
	return dir
}

func TestRefreshOnce_UpdatesStaleEntry(t *testing.T) {
	r, cacheDir := newTestRefresher(t, 0, func(ctx context.Context, imageRef, outputDir string) error {
		if imageRef != "postgres:16" {
			t.Errorf("resolve imageRef = %q, want %q", imageRef, "postgres:16")
		}
		return os.WriteFile(filepath.Join(outputDir, ".image-id"), []byte("sha256:new"), 0o644)
	})
	dir := writeDockerEntry(t, cacheDir, "entry1", "postgres:16", "sha256:old")

	r.RefreshOnce(context.Background())

	data, err := os.ReadFile(filepath.Join(dir, ".image-id"))
	if err != nil {
		t.Fatalf("read .image-id: %v", err)
	}
	if string(data) != "sha256:new" {
		t.Errorf(".image-id = %q, want %q", string(data), "sha256:new")
	}
	if _, err := os.Stat(filepath.Join(dir, ".last-checked")); err != nil {
		t.Errorf("expected .last-checked to exist: %v", err)
	}
}

func TestRefreshOnce_SkipsRecentlyChecked(t *testing.T) {
	pullCalled := false
	r, cacheDir := newTestRefresher(t, time.Hour, func(ctx context.Context, imageRef, outputDir string) error {
		pullCalled = true
		return nil
	})
	dir := writeDockerEntry(t, cacheDir, "entry1", "postgres:16", "sha256:old")
	touchLastChecked(dir)

	r.RefreshOnce(context.Background())

	if pullCalled {
		t.Error("expected no resolve for recently-checked entry")
	}
}

func TestRefreshOnce_SkipsImmutableDigest(t *testing.T) {
	pullCalled := false
	r, cacheDir := newTestRefresher(t, 0, func(ctx context.Context, imageRef, outputDir string) error {
		pullCalled = true
		return nil
	})
	writeDockerEntry(t, cacheDir, "entry1", "myimage@sha256:abc123", "sha256:abc123")

	r.RefreshOnce(context.Background())

	if pullCalled {
		t.Error("expected no resolve for immutable digest")
	}
}

func TestRefreshOnce_TouchesLastCheckedOnSuccess(t *testing.T) {
	r, cacheDir := newTestRefresher(t, 0, func(ctx context.Context, imageRef, outputDir string) error {
		// Resolve succeeds without changing the image — .last-checked should still be touched.
		return os.WriteFile(filepath.Join(outputDir, ".image-id"), []byte("sha256:same"), 0o644)
	})
	dir := writeDockerEntry(t, cacheDir, "entry1", "postgres:16", "sha256:same")

	r.RefreshOnce(context.Background())

	if _, err := os.Stat(filepath.Join(dir, ".last-checked")); err != nil {
		t.Errorf("expected .last-checked to exist: %v", err)
	}
}

func TestRefreshOnce_ContextCancelStopsMidScan(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	resolveCount := 0

	r, cacheDir := newTestRefresher(t, 0, func(ctx context.Context, imageRef, outputDir string) error {
		resolveCount++
		cancel() // Cancel after first resolve.
		return os.WriteFile(filepath.Join(outputDir, ".image-id"), []byte("sha256:new"), 0o644)
	})
	writeDockerEntry(t, cacheDir, "entry1", "image:1", "sha256:old")
	writeDockerEntry(t, cacheDir, "entry2", "image:2", "sha256:old")

	r.RefreshOnce(ctx)

	if resolveCount != 1 {
		t.Errorf("expected 1 resolve before cancellation, got %d", resolveCount)
	}
}

func TestRefreshOnce_ResolveErrorSkipsLastChecked(t *testing.T) {
	r, cacheDir := newTestRefresher(t, 0, func(ctx context.Context, imageRef, outputDir string) error {
		return errors.New("network error")
	})
	dir := writeDockerEntry(t, cacheDir, "entry1", "postgres:16", "sha256:old")

	r.RefreshOnce(context.Background())

	// .last-checked should NOT exist — failed resolves don't suppress retries.
	if _, err := os.Stat(filepath.Join(dir, ".last-checked")); !os.IsNotExist(err) {
		t.Error("expected .last-checked to not exist after resolve failure")
	}
	// .image-id should be unchanged.
	data, _ := os.ReadFile(filepath.Join(dir, ".image-id"))
	if string(data) != "sha256:old" {
		t.Errorf(".image-id = %q, want %q", string(data), "sha256:old")
	}
}

func TestRefreshOnce_NoCacheDir(t *testing.T) {
	r := NewRefresher(NewCache("/nonexistent/path"), 0)
	// Should not panic.
	r.RefreshOnce(context.Background())
}

func TestRefreshOnce_AcquiresCacheLock(t *testing.T) {
	r, cacheDir := newTestRefresher(t, 0, func(ctx context.Context, imageRef, outputDir string) error {
		return os.WriteFile(filepath.Join(outputDir, ".image-id"), []byte("sha256:new"), 0o644)
	})
	dir := writeDockerEntry(t, cacheDir, "entry1", "postgres:16", "sha256:old")

	// Pre-acquire the same cache lock that refreshEntry will use.
	cacheKey, err := DockerPull{Image: "postgres:16"}.CacheKey()
	if err != nil {
		t.Fatalf("cache key: %v", err)
	}
	unlock, err := r.cache.Lock(cacheKey)
	if err != nil {
		t.Fatalf("acquire lock: %v", err)
	}

	done := make(chan struct{})
	go func() {
		r.RefreshOnce(context.Background())
		close(done)
	}()

	// Give RefreshOnce time to reach the lock.
	time.Sleep(50 * time.Millisecond)

	select {
	case <-done:
		t.Fatal("RefreshOnce completed while lock was held — expected it to block")
	default:
		// Good — it's blocked on the lock.
	}

	unlock()

	select {
	case <-done:
		// Good — completed after unlock.
	case <-time.After(2 * time.Second):
		t.Fatal("RefreshOnce did not complete after lock release")
	}

	// Verify the resolve ran.
	data, _ := os.ReadFile(filepath.Join(dir, ".image-id"))
	if string(data) != "sha256:new" {
		t.Errorf(".image-id = %q, want %q", string(data), "sha256:new")
	}
}
