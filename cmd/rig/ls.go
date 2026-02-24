package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"
)

// lsHeader mirrors the log.header struct written by the server.
type lsHeader struct {
	Type        string   `json:"type"`
	Environment string   `json:"environment"`
	Outcome     string   `json:"outcome"`
	Services    []string `json:"services"`
	DurationMs  float64  `json:"duration_ms"`
	Timestamp   time.Time `json:"timestamp"`
}

// lsEntry is a parsed log file summary ready for display.
type lsEntry struct {
	Path   string
	Header lsHeader
}

// errNoResults is returned when ls finds no matching files.
// main uses this to exit non-zero without printing an extra error.
var errNoResults = fmt.Errorf("no results")

func runLs(args []string) error {
	// Extract positional glob pattern before flags.
	pattern, flagArgs := extractFile(args)

	fs := flag.NewFlagSet("ls", flag.ContinueOnError)
	var (
		failed bool
		passed bool
		quiet  bool
		limit  int
	)
	fs.BoolVar(&failed, "failed", false, "only show failed/crashed logs")
	fs.BoolVar(&passed, "passed", false, "only show passed logs")
	fs.BoolVar(&quiet, "q", false, "output file paths only, one per line")
	fs.IntVar(&limit, "n", 0, "limit to the N most recent results")
	if err := fs.Parse(flagArgs); err != nil {
		return err
	}
	if pattern == "" && fs.NArg() > 0 {
		pattern = fs.Arg(0)
	}

	paths, err := scanLogDir(pattern)
	if err != nil {
		if os.IsNotExist(err) {
			fmt.Fprintln(os.Stderr, "No log files found.")
			return errNoResults
		}
		return fmt.Errorf("read log directory: %w", err)
	}

	var entries []lsEntry
	for _, path := range paths {
		hdr, err := readHeader(path)
		if err != nil {
			continue // skip files without a valid log.header
		}

		// Filter by outcome.
		if failed && hdr.Outcome != "failed" && hdr.Outcome != "crashed" {
			continue
		}
		if passed && hdr.Outcome != "passed" {
			continue
		}

		entries = append(entries, lsEntry{Path: path, Header: hdr})
	}

	if len(entries) == 0 {
		fmt.Fprintln(os.Stderr, "No matching log files.")
		return errNoResults
	}

	// Sort by timestamp descending (newest first).
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Header.Timestamp.After(entries[j].Header.Timestamp)
	})

	if limit > 0 && limit < len(entries) {
		entries = entries[:limit]
	}

	if quiet {
		for _, e := range entries {
			fmt.Println(e.Path)
		}
		return nil
	}

	renderLsTable(os.Stdout, entries)
	return nil
}

// readHeader reads only the first line of a JSONL file and parses it as a
// log.header event. Returns an error if the first line is not a log.header.
func readHeader(path string) (lsHeader, error) {
	f, err := os.Open(path)
	if err != nil {
		return lsHeader{}, err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	if !scanner.Scan() {
		return lsHeader{}, fmt.Errorf("empty file")
	}

	var hdr lsHeader
	if err := json.Unmarshal(scanner.Bytes(), &hdr); err != nil {
		return lsHeader{}, err
	}
	if hdr.Type != "log.header" {
		return lsHeader{}, fmt.Errorf("not a log.header")
	}
	return hdr, nil
}

func renderLsTable(w io.Writer, entries []lsEntry) {
	// Column headers and widths.
	headers := []string{"TIME", "OUTCOME", "NAME", "DURATION", "SERVICES"}
	widths := make([]int, len(headers))
	for i, h := range headers {
		widths[i] = len(h)
	}

	type row struct {
		cols [5]string
	}
	rows := make([]row, len(entries))
	for i, e := range entries {
		timeStr := e.Header.Timestamp.Local().Format("2006-01-02 15:04:05")
		outcome := e.Header.Outcome
		if outcome == "" {
			outcome = "unknown"
		}
		durStr := formatLsDuration(e.Header.DurationMs)
		svcs := strings.Join(e.Header.Services, ", ")

		rows[i] = row{cols: [5]string{
			timeStr,
			outcome,
			e.Header.Environment,
			durStr,
			svcs,
		}}
		for j, c := range rows[i].cols {
			if len(c) > widths[j] {
				widths[j] = len(c)
			}
		}
	}

	// Print header.
	for i, h := range headers {
		if i > 0 {
			fmt.Fprint(w, "  ")
		}
		fmt.Fprintf(w, "%-*s", widths[i], bold(h))
	}
	fmt.Fprintln(w)

	// Print rows.
	for _, r := range rows {
		for i, c := range r.cols {
			if i > 0 {
				fmt.Fprint(w, "  ")
			}
			padded := fmt.Sprintf("%-*s", widths[i], c)
			if i == 1 { // OUTCOME column
				fmt.Fprint(w, colorOutcome(padded))
			} else {
				fmt.Fprint(w, padded)
			}
		}
		fmt.Fprintln(w)
	}
}

func colorOutcome(s string) string {
	if !colorEnabled {
		return s
	}
	trimmed := strings.TrimSpace(s)
	switch trimmed {
	case "passed":
		return ansiGreen + s + ansiReset
	case "failed", "crashed":
		return ansiRed + s + ansiReset
	}
	return s
}

func formatLsDuration(ms float64) string {
	if ms < 1000 {
		return fmt.Sprintf("%.0fms", ms)
	}
	return fmt.Sprintf("%.2fs", ms/1000)
}
