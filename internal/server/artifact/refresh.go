package artifact

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// DefaultStaleAfter is the default staleness window for background refresh.
// Docker image tags like postgres:16 or redis:8 change infrequently — a daily
// check is more than sufficient.
const DefaultStaleAfter = 24 * time.Hour

// Refresher performs a single pass over cached Docker images, pulling mutable
// tags that haven't been checked recently and updating the cached image ID if
// the upstream changed. It uses the same Cache lock and DockerPull.Resolve
// path as normal artifact resolution, so there's one code path for writing
// cache breadcrumbs and no concurrent-write concerns.
type Refresher struct {
	cache      *Cache
	staleAfter time.Duration
	resolve    resolveFunc // injectable for testing
}

// resolveFunc pulls a Docker image and writes breadcrumbs to outputDir.
// The default implementation uses DockerPull.Resolve; tests inject a fake.
type resolveFunc func(ctx context.Context, imageRef string, outputDir string) error

// NewRefresher creates a Refresher that scans cache for stale Docker entries.
func NewRefresher(cache *Cache, staleAfter time.Duration) *Refresher {
	return &Refresher{
		cache:      cache,
		staleAfter: staleAfter,
		resolve:    defaultResolve,
	}
}

// defaultResolve pulls a Docker image via the same path as normal artifact
// resolution — DockerPull.Resolve writes .image-id and .image-ref breadcrumbs.
func defaultResolve(ctx context.Context, imageRef string, outputDir string) error {
	_, err := DockerPull{Image: imageRef}.Resolve(ctx, outputDir)
	return err
}

// RefreshOnce performs a single refresh pass over all cached Docker entries.
// It checks ctx between entries so callers can cancel mid-scan.
func (r *Refresher) RefreshOnce(ctx context.Context) {
	entries := scanDockerEntries(r.cache.Dir())
	for _, e := range entries {
		if ctx.Err() != nil {
			return
		}
		if !isMutableRef(e.imageRef) {
			continue
		}
		if !shouldRefresh(e.dir, r.staleAfter) {
			continue
		}
		r.refreshEntry(ctx, e)
	}
}

// refreshEntry acquires the cache lock for this entry, then re-resolves the
// image through the normal DockerPull.Resolve path.
func (r *Refresher) refreshEntry(ctx context.Context, e dockerEntry) {
	unlock, err := r.cache.Lock(e.cacheKey)
	if err != nil {
		return
	}
	defer unlock()

	if err := r.resolve(ctx, e.imageRef, e.dir); err != nil {
		// Pull failed — don't touch .last-checked so the entry gets retried
		// on the next idle period rather than being suppressed for the full
		// stale window.
		return
	}
	touchLastChecked(e.dir)
}

// dockerEntry represents a cached Docker image entry on disk.
type dockerEntry struct {
	dir      string
	imageRef string
	cacheKey string
}

// scanDockerEntries walks cacheDir/docker/ and returns entries that have both
// .image-ref and .image-id files. Entries without .image-ref are skipped
// (pre-existing cache entries that haven't been re-resolved yet). Entries
// without .image-id are skipped (resolution never completed).
func scanDockerEntries(cacheDir string) []dockerEntry {
	dockerDir := filepath.Join(cacheDir, "docker")
	dirEntries, err := os.ReadDir(dockerDir)
	if err != nil {
		return nil
	}

	var entries []dockerEntry
	for _, de := range dirEntries {
		if !de.IsDir() {
			continue
		}
		dir := filepath.Join(dockerDir, de.Name())

		refData, err := os.ReadFile(filepath.Join(dir, ".image-ref"))
		if err != nil {
			continue // no .image-ref — skip
		}
		imageRef := strings.TrimSpace(string(refData))
		if imageRef == "" {
			continue
		}

		// Verify .image-id exists — if resolution never completed, skip.
		if _, err := os.Stat(filepath.Join(dir, ".image-id")); err != nil {
			continue
		}

		cacheKey, err := DockerPull{Image: imageRef}.CacheKey()
		if err != nil {
			continue
		}

		entries = append(entries, dockerEntry{
			dir:      dir,
			imageRef: imageRef,
			cacheKey: cacheKey,
		})
	}
	return entries
}

// isMutableRef returns true if the image reference could point to a different
// image over time. Digest references (containing @sha256:) are immutable.
func isMutableRef(ref string) bool {
	return !strings.Contains(ref, "@sha256:")
}

// shouldRefresh returns true if the entry's .last-checked file is missing or
// older than staleAfter.
func shouldRefresh(dir string, staleAfter time.Duration) bool {
	info, err := os.Stat(filepath.Join(dir, ".last-checked"))
	if err != nil {
		return true // no .last-checked — never checked
	}
	return time.Since(info.ModTime()) > staleAfter
}

// touchLastChecked creates or updates the mtime of .last-checked in dir.
func touchLastChecked(dir string) {
	path := filepath.Join(dir, ".last-checked")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	f.Close()
	os.Chtimes(path, time.Now(), time.Now())
}
