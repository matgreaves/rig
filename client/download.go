package rig

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
)

// downloadURL returns the GitHub Releases URL for a rigd binary.
func downloadURL(version string) string {
	return fmt.Sprintf(
		"https://github.com/matgreaves/rig/releases/download/rigd/v%s/rigd-%s-%s.tar.gz",
		version, runtime.GOOS, runtime.GOARCH,
	)
}

// downloadBinary downloads a tar.gz archive from url, extracts the "rigd"
// binary, and writes it to destPath. Uses a temp file + rename for atomicity.
func downloadBinary(url, destPath string) error {
	if err := os.MkdirAll(filepath.Dir(destPath), 0o755); err != nil {
		return fmt.Errorf("create directory: %w", err)
	}

	resp, err := http.Get(url)
	if err != nil {
		return fmt.Errorf("download %s: %w", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download %s: HTTP %d", url, resp.StatusCode)
	}

	gz, err := gzip.NewReader(resp.Body)
	if err != nil {
		return fmt.Errorf("gzip reader: %w", err)
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("read tar: %w", err)
		}

		if filepath.Base(hdr.Name) != "rigd" || hdr.Typeflag != tar.TypeReg {
			continue
		}

		// Write to temp file then rename for atomicity.
		tmp, err := os.CreateTemp(filepath.Dir(destPath), "rigd-download-*")
		if err != nil {
			return fmt.Errorf("create temp file: %w", err)
		}
		tmpPath := tmp.Name()

		if _, err := io.Copy(tmp, tr); err != nil {
			tmp.Close()
			os.Remove(tmpPath)
			return fmt.Errorf("extract binary: %w", err)
		}
		if err := tmp.Chmod(0o755); err != nil {
			tmp.Close()
			os.Remove(tmpPath)
			return fmt.Errorf("chmod binary: %w", err)
		}
		if err := tmp.Close(); err != nil {
			os.Remove(tmpPath)
			return fmt.Errorf("close temp file: %w", err)
		}
		if err := os.Rename(tmpPath, destPath); err != nil {
			os.Remove(tmpPath)
			return fmt.Errorf("rename binary: %w", err)
		}
		return nil
	}

	return fmt.Errorf("rigd binary not found in archive %s", url)
}
