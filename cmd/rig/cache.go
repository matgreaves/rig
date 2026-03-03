package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func runCache(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("expected subcommand: prune")
	}
	switch args[0] {
	case "prune":
		return runCachePrune(args[1:])
	default:
		return fmt.Errorf("unknown subcommand %q (available: prune)", args[0])
	}
}

func runCachePrune(args []string) error {
	fs := flag.NewFlagSet("rig cache prune", flag.ContinueOnError)

	var maxAgeStr string
	var dryRun bool
	fs.StringVar(&maxAgeStr, "m", "24h", "")
	fs.StringVar(&maxAgeStr, "max-age", "24h", "")
	fs.BoolVar(&dryRun, "n", false, "")
	fs.BoolVar(&dryRun, "dry-run", false, "")

	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, `Usage: rig cache prune [flags]

Remove cached artifacts not used within the given duration.

Flags:
  -m, --max-age <duration>   Max age for cache entries (default: 24h)
  -n, --dry-run              Print what would be removed without deleting
`)
	}

	if err := fs.Parse(args); err != nil {
		return err
	}

	maxAge, err := time.ParseDuration(maxAgeStr)
	if err != nil {
		return fmt.Errorf("invalid duration %q: %w", maxAgeStr, err)
	}

	cacheDir := filepath.Join(defaultRigDir(), "cache")
	cutoff := time.Now().Add(-maxAge)

	typeDirs, err := os.ReadDir(cacheDir)
	if err != nil {
		if os.IsNotExist(err) {
			fmt.Println("nothing to prune")
			return nil
		}
		return fmt.Errorf("reading cache dir: %w", err)
	}

	var totalPruned int
	var totalBytes int64

	for _, td := range typeDirs {
		if !td.IsDir() {
			continue
		}
		typeDir := filepath.Join(cacheDir, td.Name())

		entries, err := os.ReadDir(typeDir)
		if err != nil {
			continue
		}

		// Track which hash dirs exist (for orphaned lock cleanup).
		hashDirs := make(map[string]bool)
		// Track which hash dirs were removed.
		removedDirs := make(map[string]bool)

		// First pass: identify and remove stale hash dirs.
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			hashDirs[e.Name()] = true

			lastUsed := filepath.Join(typeDir, e.Name(), ".last-used")
			info, err := os.Stat(lastUsed)

			stale := err != nil || info.ModTime().Before(cutoff)
			if !stale {
				continue
			}

			entryDir := filepath.Join(typeDir, e.Name())
			size := dirSize(entryDir)

			if dryRun {
				age := formatAge(info, err)
				fmt.Printf("would remove %s/%s (last used %s, %s)\n",
					td.Name(), e.Name(), age, formatBytes(size))
			} else {
				if err := os.RemoveAll(entryDir); err != nil {
					fmt.Fprintf(os.Stderr, "warning: failed to remove %s/%s: %v\n", td.Name(), e.Name(), err)
					continue
				}
			}
			removedDirs[e.Name()] = true
			totalPruned++
			totalBytes += size
		}

		// Second pass: clean orphaned .lock files.
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			name := e.Name()
			if !strings.HasSuffix(name, ".lock") {
				continue
			}
			hashName := strings.TrimSuffix(name, ".lock")
			if hashDirs[hashName] && !removedDirs[hashName] {
				continue // hash dir still exists and wasn't removed
			}
			lockPath := filepath.Join(typeDir, name)
			if !dryRun {
				os.Remove(lockPath)
			}
		}
	}

	if totalPruned == 0 {
		fmt.Println("nothing to prune")
		return nil
	}

	if dryRun {
		fmt.Printf("would prune %d %s (would free ~%s)\n",
			totalPruned, plural(totalPruned, "entry", "entries"), formatBytes(totalBytes))
	} else {
		fmt.Printf("pruned %d %s (freed ~%s)\n",
			totalPruned, plural(totalPruned, "entry", "entries"), formatBytes(totalBytes))
	}
	return nil
}

func formatAge(info os.FileInfo, err error) string {
	if err != nil {
		return "unknown"
	}
	d := time.Since(info.ModTime())
	switch {
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}

func dirSize(path string) int64 {
	var total int64
	filepath.Walk(path, func(_ string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() {
			total += info.Size()
		}
		return nil
	})
	return total
}

func plural(n int, singular, p string) string {
	if n == 1 {
		return singular
	}
	return p
}
