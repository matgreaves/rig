package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/matgreaves/rig/cmd/rig/rigdata"
)

func runPrune(args []string) error {
	fs := flag.NewFlagSet("rig prune", flag.ContinueOnError)

	var (
		maxAgeStr string
		dryRun    bool
		logsOnly  bool
		cacheOnly bool
	)
	fs.StringVar(&maxAgeStr, "m", "24h", "")
	fs.StringVar(&maxAgeStr, "max-age", "24h", "")
	fs.BoolVar(&dryRun, "n", false, "")
	fs.BoolVar(&dryRun, "dry-run", false, "")
	fs.BoolVar(&logsOnly, "l", false, "")
	fs.BoolVar(&cacheOnly, "c", false, "")

	fs.Usage = printPruneUsage

	if err := fs.Parse(args); err != nil {
		return err
	}

	maxAge, err := time.ParseDuration(maxAgeStr)
	if err != nil {
		return fmt.Errorf("invalid duration %q: %w", maxAgeStr, err)
	}

	cutoff := time.Now().Add(-maxAge)

	// Default: both. If only one flag is set, limit to that scope.
	// -l -c together is equivalent to no flags (both).
	doLogs := logsOnly || !cacheOnly
	doCache := cacheOnly || !logsOnly

	var totalPruned int
	var totalBytes int64

	if doCache {
		n, b, err := pruneCache(filepath.Join(rigdata.DefaultRigDir(), "cache"), cutoff, dryRun)
		if err != nil {
			return err
		}
		totalPruned += n
		totalBytes += b
	}

	if doLogs {
		n, b, err := pruneLogs(rigdata.LogDir(), cutoff, dryRun)
		if err != nil {
			return err
		}
		totalPruned += n
		totalBytes += b
	}

	if totalPruned == 0 {
		fmt.Println("nothing to prune")
		return nil
	}

	if dryRun {
		fmt.Printf("would prune %d %s (would free ~%s)\n",
			totalPruned, plural(totalPruned, "entry", "entries"), rigdata.FormatBytes(totalBytes))
	} else {
		fmt.Printf("pruned %d %s (freed ~%s)\n",
			totalPruned, plural(totalPruned, "entry", "entries"), rigdata.FormatBytes(totalBytes))
	}
	return nil
}

func pruneCache(cacheDir string, cutoff time.Time, dryRun bool) (int, int64, error) {
	typeDirs, err := os.ReadDir(cacheDir)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, 0, nil
		}
		return 0, 0, fmt.Errorf("reading cache dir: %w", err)
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
				fmt.Printf("would remove cache/%s/%s (last used %s, %s)\n",
					td.Name(), e.Name(), age, rigdata.FormatBytes(size))
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

	return totalPruned, totalBytes, nil
}

func pruneLogs(dir string, cutoff time.Time, dryRun bool) (int, int64, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, 0, nil
		}
		return 0, 0, fmt.Errorf("reading log dir: %w", err)
	}

	var totalPruned int
	var totalBytes int64

	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}

		info, err := e.Info()
		if err != nil {
			continue
		}

		if !info.ModTime().Before(cutoff) {
			continue
		}

		size := info.Size()

		if dryRun {
			age := formatAge(info, nil)
			fmt.Printf("would remove logs/%s (%s, %s)\n",
				e.Name(), age, rigdata.FormatBytes(size))
		} else {
			if err := os.Remove(filepath.Join(dir, e.Name())); err != nil {
				fmt.Fprintf(os.Stderr, "warning: failed to remove logs/%s: %v\n", e.Name(), err)
				continue
			}
		}
		totalPruned++
		totalBytes += size

		// Remove companion .log file if present.
		companion := strings.TrimSuffix(e.Name(), ".jsonl") + ".log"
		companionPath := filepath.Join(dir, companion)
		if ci, err := os.Stat(companionPath); err == nil {
			if dryRun {
				fmt.Printf("would remove logs/%s (%s)\n", companion, rigdata.FormatBytes(ci.Size()))
			} else {
				os.Remove(companionPath)
			}
			totalBytes += ci.Size()
		}
	}

	return totalPruned, totalBytes, nil
}

func printPruneUsage() {
	fmt.Fprintf(os.Stderr, `Usage: rig prune [flags]

Remove stale cache entries and log files.

Flags:
  -l                       Logs only
  -c                       Cache only
  -m, --max-age <duration> Max age for entries (default: 24h)
  -n, --dry-run            Print what would be removed without deleting

By default both cache and logs are pruned. Use -l or -c to limit scope.
`)
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
