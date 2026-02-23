package artifact

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
)

// Download resolves an artifact by downloading a tar.gz archive from a URL
// and extracting a single named binary. Used for pre-built CLI tools
// distributed as GitHub release archives (e.g. Temporal CLI).
type Download struct {
	URL    string // full download URL (tar.gz archive)
	Binary string // name of binary to extract (e.g. "temporal")
}

// CacheKey returns a stable hash of the download URL.
func (d Download) CacheKey() (string, error) {
	sum := sha256.Sum256([]byte(d.URL))
	return "downloads/" + hex.EncodeToString(sum[:]), nil
}

// Cached checks whether the extracted binary exists in outputDir.
func (d Download) Cached(outputDir string) (Output, bool) {
	p := filepath.Join(outputDir, d.Binary)
	info, err := os.Stat(p)
	if err != nil || info.Size() == 0 {
		return Output{}, false
	}
	return Output{
		Path: p,
		Meta: map[string]string{"url": d.URL},
	}, true
}

// Resolve downloads the tar.gz archive and extracts the named binary.
func (d Download) Resolve(ctx context.Context, outputDir string) (Output, error) {
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		return Output{}, fmt.Errorf("create output dir: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, d.URL, nil)
	if err != nil {
		return Output{}, fmt.Errorf("create request: %w", err)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return Output{}, fmt.Errorf("download %s: %w", d.URL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return Output{}, fmt.Errorf("download %s: HTTP %d", d.URL, resp.StatusCode)
	}

	gz, err := gzip.NewReader(resp.Body)
	if err != nil {
		return Output{}, fmt.Errorf("gzip reader: %w", err)
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	outputPath := filepath.Join(outputDir, d.Binary)

	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return Output{}, fmt.Errorf("read tar: %w", err)
		}

		// Match entry name ending with the binary name. Handles both
		// "temporal" and "./temporal" and "bin/temporal" paths in the archive.
		name := filepath.Base(hdr.Name)
		if name != d.Binary {
			continue
		}
		if hdr.Typeflag != tar.TypeReg {
			continue
		}

		// Write to a temp file then rename to avoid "text file busy" errors
		// when another process is exec'ing the same cached binary.
		tmpFile, err := os.CreateTemp(outputDir, ".tmp-"+d.Binary+"-")
		if err != nil {
			return Output{}, fmt.Errorf("create temp file: %w", err)
		}
		tmpPath := tmpFile.Name()
		if _, err := io.Copy(tmpFile, tr); err != nil {
			tmpFile.Close()
			os.Remove(tmpPath)
			return Output{}, fmt.Errorf("extract binary: %w", err)
		}
		if err := tmpFile.Close(); err != nil {
			os.Remove(tmpPath)
			return Output{}, fmt.Errorf("close binary: %w", err)
		}
		if err := os.Chmod(tmpPath, 0o755); err != nil {
			os.Remove(tmpPath)
			return Output{}, fmt.Errorf("chmod binary: %w", err)
		}
		if err := os.Rename(tmpPath, outputPath); err != nil {
			os.Remove(tmpPath)
			return Output{}, fmt.Errorf("rename binary: %w", err)
		}

		return Output{
			Path: outputPath,
			Meta: map[string]string{"url": d.URL},
		}, nil
	}

	return Output{}, fmt.Errorf("binary %q not found in archive %s", d.Binary, d.URL)
}

// Retryable returns true — downloads are network operations.
func (d Download) Retryable() bool { return true }
