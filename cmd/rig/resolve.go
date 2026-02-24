package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// defaultRigDir returns the base rig directory. Mirrors the server's
// DefaultRigDir logic without importing the server package.
func defaultRigDir() string {
	if dir := os.Getenv("RIG_DIR"); dir != "" {
		return dir
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(os.TempDir(), "rig")
	}
	return filepath.Join(home, ".rig")
}

// scanLogDir returns all .jsonl file paths in {rigDir}/logs/ whose base
// filename (without extension) matches the given glob pattern. Pass "" to
// match all files. Results are sorted lexicographically (chronological
// since IDs are time-prefixed).
func scanLogDir(pattern string) ([]string, error) {
	logDir := filepath.Join(defaultRigDir(), "logs")
	entries, err := os.ReadDir(logDir)
	if err != nil {
		return nil, err
	}

	glob := pattern
	if glob != "" && !hasGlobMeta(glob) {
		glob = "*" + glob + "*"
	}

	var paths []string
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".jsonl" {
			continue
		}
		if glob != "" {
			base := e.Name()[:len(e.Name())-len(".jsonl")]
			ok, _ := filepath.Match(glob, base)
			if !ok {
				continue
			}
		}
		paths = append(paths, filepath.Join(logDir, e.Name()))
	}
	sort.Strings(paths)
	return paths, nil
}

// resolveLogFile resolves a user-provided argument to a JSONL log file path.
// If the argument is an existing file or contains a path separator, it is
// returned as-is. Otherwise it is treated as a glob pattern and matched
// against filenames (without .jsonl extension) in {rigDir}/logs/. If multiple
// files match, the most recent (last lexicographically, since IDs are
// time-prefixed) is returned.
func resolveLogFile(arg string) (string, error) {
	// Direct file path.
	if _, err := os.Stat(arg); err == nil {
		return arg, nil
	}
	// If it contains a path separator, it was meant as a path — don't glob.
	if filepath.Base(arg) != arg {
		return "", fmt.Errorf("file not found: %s", arg)
	}

	matches, err := scanLogDir(arg)
	if err != nil {
		return "", fmt.Errorf("cannot read log directory: %w", err)
	}
	if len(matches) == 0 {
		return "", fmt.Errorf("no log files matching %q in %s",
			arg, filepath.Join(defaultRigDir(), "logs"))
	}
	return matches[len(matches)-1], nil
}

// hasGlobMeta reports whether s contains glob metacharacters.
func hasGlobMeta(s string) bool {
	for _, c := range s {
		switch c {
		case '*', '?', '[':
			return true
		}
	}
	return false
}
