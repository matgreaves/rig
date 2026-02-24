package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
	"time"
)

const (
	typeServiceLog = "service.log"
	typeTestNote   = "test.note"
)

type logEntry struct {
	Stream string `json:"stream"` // "stdout" or "stderr"
	Data   string `json:"data"`
}

// logEvent is the subset of a JSONL event we need for log display.
type logEvent struct {
	Seq       uint64    `json:"seq"`
	Type      string    `json:"type"`
	Service   string    `json:"service"`
	Log       *logEntry `json:"log,omitempty"`
	Error     string    `json:"error,omitempty"` // test.note assertion message
	Timestamp time.Time `json:"timestamp"`
}

// logRow is a parsed log line ready for display.
type logRow struct {
	Time    string
	Service string
	Stream  string // "stdout", "stderr", or "note"
	Data    string
}

func runLogs(args []string) error {
	filename, flagArgs := extractFile(args)

	fs := flag.NewFlagSet("logs", flag.ContinueOnError)
	var (
		service string
		stderr  bool
		stdout  bool
		grep    string
	)
	fs.StringVar(&service, "service", "", "filter to a specific service")
	fs.BoolVar(&stderr, "stderr", false, "only show stderr output")
	fs.BoolVar(&stdout, "stdout", false, "only show stdout output")
	fs.StringVar(&grep, "grep", "", "filter lines matching regex pattern")

	if err := fs.Parse(flagArgs); err != nil {
		return err
	}
	if filename == "" {
		if fs.NArg() > 0 {
			filename = fs.Arg(0)
		} else {
			return fmt.Errorf("missing JSONL file argument\n\nUsage: rig logs <file.jsonl> [flags]")
		}
	}

	var grepRe *regexp.Regexp
	if grep != "" {
		var err error
		grepRe, err = regexp.Compile(grep)
		if err != nil {
			return fmt.Errorf("invalid --grep pattern %q: %v", grep, err)
		}
	}

	f, err := os.Open(filename)
	if err != nil {
		return err
	}
	defer f.Close()

	events, err := parseLogEvents(f)
	if err != nil {
		return err
	}

	if len(events) == 0 {
		fmt.Fprintln(os.Stderr, "No log events found.")
		return nil
	}

	// Build service → color index map in order of first appearance.
	serviceIndex := map[string]int{}
	for _, ev := range events {
		if ev.Service == "" {
			continue
		}
		if _, ok := serviceIndex[ev.Service]; !ok {
			serviceIndex[ev.Service] = len(serviceIndex)
		}
	}

	// Find the longest service name for alignment (minimum "TEST" width for notes).
	maxName := 4 // len("TEST")
	for name := range serviceIndex {
		if len(name) > maxName {
			maxName = len(name)
		}
	}

	t0 := events[0].Timestamp
	rows := make([]logRow, 0, len(events))
	for _, ev := range events {
		var row logRow
		row.Time = formatDuration(ev.Timestamp.Sub(t0))

		if ev.Type == typeTestNote {
			row.Service = "TEST"
			row.Stream = "note"
			row.Data = ev.Error
		} else {
			row.Service = ev.Service
			row.Stream = ev.Log.Stream
			row.Data = ev.Log.Data
		}

		if service != "" && !strings.EqualFold(row.Service, service) {
			continue
		}
		if stderr && row.Stream != "stderr" && row.Stream != "note" {
			continue
		}
		if stdout && row.Stream != "stdout" {
			continue
		}
		if grepRe != nil && !grepRe.MatchString(row.Data) {
			continue
		}
		rows = append(rows, row)
	}

	if len(rows) == 0 {
		fmt.Fprintln(os.Stderr, "No matching log events.")
		return nil
	}

	serviceColorTotal = len(serviceIndex)
	renderLogs(os.Stdout, rows, serviceIndex, maxName)
	return nil
}

func parseLogEvents(r io.Reader) ([]logEvent, error) {
	var events []logEvent
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 256*1024), 1024*1024)
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var ev logEvent
		if err := json.Unmarshal(line, &ev); err != nil {
			return nil, fmt.Errorf("line %d: %w", lineNo, err)
		}
		switch {
		case ev.Type == typeServiceLog && ev.Log != nil:
			events = append(events, ev)
		case ev.Type == typeTestNote && ev.Error != "":
			events = append(events, ev)
		}
	}
	return events, scanner.Err()
}

func renderLogs(w io.Writer, rows []logRow, serviceIndex map[string]int, maxName int) {
	for _, r := range rows {
		name := fmt.Sprintf("%-*s", maxName, r.Service)
		ts := dim(r.Time)

		if r.Stream == "note" {
			data := bold(colorNote("✗ " + r.Data))
			fmt.Fprintf(w, "%s  %s  %s\n", ts, bold(colorNote(name)), data)
		} else {
			idx := serviceIndex[r.Service]
			fmt.Fprintf(w, "%s  %s  %s\n", ts, colorService(name, idx), r.Data)
		}
	}
}

func colorNote(s string) string {
	if !colorEnabled {
		return s
	}
	return ansiRed + s + ansiReset
}
