package artifact

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

// GoBuild resolves a Go module by compiling it with the local Go toolchain.
// Module is either an absolute local path ("/abs/path/cmd/server") or a remote
// module reference ("github.com/myorg/tool@v1.2.3"). Detection: if Module
// starts with "/" it is local; otherwise remote.
type GoBuild struct {
	Module string // absolute local path or remote module reference
	GOOS   string // defaults to runtime.GOOS
	GOARCH string // defaults to runtime.GOARCH
}

func (g GoBuild) goos() string {
	if g.GOOS != "" {
		return g.GOOS
	}
	return runtime.GOOS
}

func (g GoBuild) goarch() string {
	if g.GOARCH != "" {
		return g.GOARCH
	}
	return runtime.GOARCH
}

func (g GoBuild) isLocal() bool {
	return strings.HasPrefix(g.Module, "/")
}

// CacheKey returns a content-based hash suitable for use as a cache directory
// name. For local modules the hash covers GOOS, GOARCH, and all source file
// paths and contents. For remote modules the hash covers GOOS, GOARCH, and
// the module reference (which must include a @version suffix).
func (g GoBuild) CacheKey() (string, error) {
	if g.isLocal() {
		return g.localCacheKey()
	}
	return g.remoteCacheKey()
}

// localCacheKey hashes GOOS, GOARCH, Go version, and all source files.
//
// Known limitations:
//   - go.mod replace directives pointing at local paths: changes in the
//     replaced module are not reflected in the cache key. Users must clean
//     the cache manually (or rig cache clean, when available).
//   - //go:embed files that are not .go/go.mod/go.sum: embedded assets
//     (templates, SQL migrations, etc.) are not hashed. Same workaround.
func (g GoBuild) localCacheKey() (string, error) {
	h := sha256.New()
	fmt.Fprintf(h, "goos:%s\ngoarch:%s\ngoversion:%s\n", g.goos(), g.goarch(), runtime.Version())

	// Try git ls-files first — fast and excludes build artifacts.
	files, err := gitSourceFiles(g.Module)
	if err != nil {
		// Not a git repo or git not available — fall back to WalkDir.
		files, err = walkSourceFiles(g.Module)
		if err != nil {
			return "", fmt.Errorf("list source files: %w", err)
		}
	}

	for _, f := range files {
		if err := hashFile(h, g.Module, f); err != nil {
			return "", fmt.Errorf("hash file %s: %w", f, err)
		}
	}

	return "go/" + hex.EncodeToString(h.Sum(nil)), nil
}

func (g GoBuild) remoteCacheKey() (string, error) {
	if !strings.Contains(g.Module, "@") {
		return "", fmt.Errorf("remote module %q must include a version suffix (e.g. module@v1.2.3)", g.Module)
	}
	// The module reference is the version pin; no file hashing needed.
	raw := fmt.Sprintf("goos:%s\ngoarch:%s\ngoversion:%s\nmodule:%s", g.goos(), g.goarch(), runtime.Version(), g.Module)
	sum := sha256.Sum256([]byte(raw))
	return "go/" + hex.EncodeToString(sum[:]), nil
}

// Cached checks whether a compiled binary exists in outputDir from a previous
// resolution. GoBuild artifacts live entirely within rig's cache directory,
// so a simple file existence check is sufficient — no Validator needed.
func (g GoBuild) Cached(outputDir string) (Output, bool) {
	p := filepath.Join(outputDir, "binary")
	info, err := os.Stat(p)
	if err != nil || info.Size() == 0 {
		return Output{}, false
	}
	return Output{
		Path: p,
		Meta: map[string]string{"module": g.Module},
	}, true
}

// Resolve compiles the module and places the binary at <outputDir>/binary.
func (g GoBuild) Resolve(ctx context.Context, outputDir string) (Output, error) {
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		return Output{}, fmt.Errorf("create output dir: %w", err)
	}

	outputPath := filepath.Join(outputDir, "binary")

	var cmd *exec.Cmd
	if g.isLocal() {
		// Local builds must run from the module directory so go build
		// resolves against the correct go.mod.
		cmd = exec.CommandContext(ctx, "go", "build", "-trimpath", "-o", outputPath, ".")
		cmd.Dir = g.Module
	} else {
		cmd = exec.CommandContext(ctx, "go", "build", "-trimpath", "-o", outputPath, g.Module)
	}
	cmd.Env = append(os.Environ(),
		"GOOS="+g.goos(),
		"GOARCH="+g.goarch(),
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		os.Remove(outputPath) // clean up any partial output
		return Output{}, fmt.Errorf("go build %s: %w\n%s", g.Module, err, string(out))
	}

	return Output{
		Path: outputPath,
		Meta: map[string]string{"module": g.Module},
	}, nil
}

// Retryable returns true for remote modules (network operation) and false for
// local modules (build failures are real failures, not transient).
func (g GoBuild) Retryable() bool {
	return !g.isLocal()
}

// gitSourceFiles returns the absolute paths of all Go source files in dir,
// including both tracked and untracked (but not ignored) files.
// Returns an error if dir is not in a git repository or git is not available.
func gitSourceFiles(dir string) ([]string, error) {
	filter := []string{"--", "*.go", "go.mod", "go.sum"}

	// Tracked files.
	trackedCmd := exec.Command("git", append([]string{"-C", dir, "ls-files"}, filter...)...)
	trackedOut, err := trackedCmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git ls-files: %w", err)
	}

	// Untracked, non-ignored files (new files not yet git-added).
	untrackedCmd := exec.Command("git", append([]string{"-C", dir, "ls-files", "--others", "--exclude-standard"}, filter...)...)
	untrackedOut, err := untrackedCmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git ls-files --others: %w", err)
	}

	seen := make(map[string]struct{})
	var paths []string
	for _, chunk := range [][]byte{trackedOut, untrackedOut} {
		for _, line := range strings.Split(strings.TrimSpace(string(chunk)), "\n") {
			if line == "" {
				continue
			}
			abs := filepath.Join(dir, line)
			if _, dup := seen[abs]; !dup {
				seen[abs] = struct{}{}
				paths = append(paths, abs)
			}
		}
	}
	sort.Strings(paths)
	return paths, nil
}

// walkSourceFiles returns the absolute paths of all .go, go.mod, and go.sum
// files found by recursively walking dir. Used as a fallback when git is
// unavailable or the directory is not in a git repository.
func walkSourceFiles(dir string) ([]string, error) {
	var paths []string
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		name := d.Name()
		if name == "go.mod" || name == "go.sum" || strings.HasSuffix(name, ".go") {
			paths = append(paths, path)
		}
		return nil
	})
	return paths, err
}

// hashFile writes the file's relative path and contents into h.
// Paths are made relative to baseDir so that cache keys are stable
// regardless of where the module is checked out on disk.
func hashFile(h io.Writer, baseDir, path string) error {
	rel, err := filepath.Rel(baseDir, path)
	if err != nil {
		rel = path // fallback to absolute if Rel fails
	}
	fmt.Fprintf(h, "file:%s\n", rel)
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(h, f)
	return err
}
