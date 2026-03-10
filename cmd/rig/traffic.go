package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/matgreaves/rig/cmd/rig/rigdata"
)

func runTraffic(args []string) error {
	// Extract the file argument from any position so flags can appear
	// before or after the file: rig traffic file.jsonl --detail 1
	// or: rig traffic --detail 1 file.jsonl
	filename, flagArgs := extractFile(args)

	fs := flag.NewFlagSet("traffic", flag.ContinueOnError)
	var (
		detail int
		edge   string
		slow   string
		status string
		grpc   bool
		http   bool
		tcp    bool
		kafka  bool
	)
	fs.IntVar(&detail, "detail", 0, "show full detail for request #N")
	fs.StringVar(&edge, "edge", "", `filter by edge: "source→target", "source", or "→target"`)
	fs.StringVar(&slow, "slow", "", "only show requests slower than threshold (e.g. 5ms, 1s)")
	fs.StringVar(&status, "status", "", "filter by status code (e.g. 500) or class (e.g. 4xx)")
	fs.BoolVar(&grpc, "grpc", false, "only show gRPC calls")
	fs.BoolVar(&http, "http", false, "only show HTTP requests")
	fs.BoolVar(&tcp, "tcp", false, "only show TCP connections")
	fs.BoolVar(&kafka, "kafka", false, "only show Kafka requests")

	if err := fs.Parse(flagArgs); err != nil {
		return err
	}
	// Also check remaining args after flag parse (handles: rig traffic --grpc file.jsonl)
	if filename == "" {
		if fs.NArg() > 0 {
			filename = fs.Arg(0)
		} else {
			return fmt.Errorf("missing JSONL file argument\n\nUsage: rig traffic <file.jsonl> [flags]")
		}
	}

	var filter rigdata.TrafficFilter
	filter.Edge = edge
	filter.Status = status

	if slow != "" {
		d, err := time.ParseDuration(slow)
		if err != nil {
			return fmt.Errorf("invalid --slow value %q: %v", slow, err)
		}
		filter.SlowMs = float64(d) / float64(time.Millisecond)
	}

	switch {
	case grpc:
		filter.Protocol = "grpc"
	case http:
		filter.Protocol = "http"
	case tcp:
		filter.Protocol = "tcp"
	case kafka:
		filter.Protocol = "kafka"
	}

	// Resolve glob pattern if the argument isn't a direct file path.
	resolved, err := rigdata.ResolveLogFile(filename)
	if err != nil {
		return err
	}
	filename = resolved

	f, err := os.Open(filename)
	if err != nil {
		return err
	}
	defer f.Close()

	events, err := rigdata.ParseTrafficEvents(f)
	if err != nil {
		return err
	}

	if len(events) == 0 {
		fmt.Fprintln(os.Stderr, "No traffic events found.")
		return nil
	}

	rows := rigdata.BuildRows(events)
	rows = rigdata.ApplyFilter(rows, filter)

	if len(rows) == 0 {
		fmt.Fprintln(os.Stderr, "No matching traffic events.")
		return nil
	}

	if detail > 0 {
		return renderDetail(os.Stdout, rows, detail)
	}

	renderTable(os.Stdout, rows)
	return nil
}

func renderTable(w io.Writer, rows []rigdata.TrafficRow) {
	// Build service → color index map in order of first appearance.
	serviceIndex := map[string]int{}
	for _, r := range rows {
		for _, name := range []string{r.Source, r.Target} {
			if _, ok := serviceIndex[name]; !ok {
				serviceIndex[name] = len(serviceIndex)
			}
		}
	}
	serviceColorTotal = len(serviceIndex)

	// Compute column widths.
	headers := []string{"#", "TIME", "EDGE", "METHOD", "PATH/SERVICE", "STATUS", "LATENCY", ""}
	widths := make([]int, len(headers))
	for i, h := range headers {
		widths[i] = len(h)
	}

	type formattedRow struct {
		cols [8]string
	}
	formatted := make([]formattedRow, len(rows))
	for i, r := range rows {
		edgeStr := r.Source + " → " + r.Target
		formatted[i] = formattedRow{cols: [8]string{
			strconv.Itoa(r.Index),
			r.Time,
			edgeStr,
			r.Method,
			r.Path,
			r.Status,
			r.Latency,
			r.Extra,
		}}
		for j, c := range formatted[i].cols {
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

	// Print rows with colored edge, method, and status.
	for ri, fr := range formatted {
		for i, c := range fr.cols {
			if i > 0 {
				fmt.Fprint(w, "  ")
			}
			padded := fmt.Sprintf("%-*s", widths[i], c)
			switch i {
			case 2: // EDGE — color source and target separately
				r := rows[ri]
				coloredEdge := colorService(r.Source, serviceIndex[r.Source]) +
					" → " +
					colorService(r.Target, serviceIndex[r.Target])
				// Pad to column width (edge plain text length is len(c))
				padding := widths[i] - len(c)
				if padding > 0 {
					coloredEdge += strings.Repeat(" ", padding)
				}
				fmt.Fprint(w, coloredEdge)
			case 3: // METHOD
				fmt.Fprint(w, colorMethod(padded))
			case 5: // STATUS
				fmt.Fprint(w, colorStatus(padded))
			default:
				fmt.Fprint(w, padded)
			}
		}
		fmt.Fprintln(w)
	}
}

// extractFile scans args for the first positional (non-flag) argument,
// returning it separately from the remaining flag args. This allows
// `rig traffic file.jsonl --detail 1` and `rig traffic --detail 1 file.jsonl`.
func extractFile(args []string) (string, []string) {
	var file string
	var flags []string
	skipNext := false
	for i, a := range args {
		if skipNext {
			skipNext = false
			continue
		}
		if strings.HasPrefix(a, "-") {
			flags = append(flags, a)
			// Check if this flag takes a value argument (not a bool flag).
			// Bool flags like --grpc don't consume the next arg.
			// We peek ahead: if next arg exists and doesn't start with -, it's the value.
			if strings.Contains(a, "=") {
				continue
			}
			if i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
				// Could be a flag value. We pass it through and let flag.Parse sort it out.
				flags = append(flags, args[i+1])
				skipNext = true
			}
			continue
		}
		if file == "" {
			file = a
		} else {
			flags = append(flags, a)
		}
	}
	return file, flags
}
