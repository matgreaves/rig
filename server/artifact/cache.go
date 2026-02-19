package artifact

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// Cache manages on-disk cache directories and file locks for artifacts.
// Each cache key maps to a directory under the cache root where resolvers
// store their output. File locks prevent duplicate work when multiple rigd
// instances resolve the same artifact concurrently.
//
// The Cache itself does not know what's inside each directory — that's the
// resolver's responsibility via Cached/Resolve.
type Cache struct {
	dir string
}

// NewCache creates a Cache rooted at dir. The directory is created lazily.
func NewCache(dir string) *Cache {
	return &Cache{dir: dir}
}

// OutputDir returns the directory where a resolver should place its output for
// cacheKey. The directory is created if it does not exist.
func (c *Cache) OutputDir(cacheKey string) string {
	dir := filepath.Join(c.dir, cacheKey)
	os.MkdirAll(dir, 0o755) //nolint:errcheck — best effort; Resolve will fail if dir is needed
	return dir
}

// Lock acquires an exclusive file lock for cacheKey, preventing duplicate
// resolution work across concurrent rigd instances. Returns an unlock function
// that must be called when the critical section is done.
func (c *Cache) Lock(cacheKey string) (unlock func(), err error) {
	if err := os.MkdirAll(c.dir, 0o755); err != nil {
		return nil, fmt.Errorf("create cache dir: %w", err)
	}
	lockPath := filepath.Join(c.dir, cacheKey+".lock")
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open lock file: %w", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		f.Close()
		return nil, fmt.Errorf("acquire lock: %w", err)
	}
	return func() {
		syscall.Flock(int(f.Fd()), syscall.LOCK_UN) //nolint:errcheck
		f.Close()
		os.Remove(lockPath) // best-effort cleanup
	}, nil
}
