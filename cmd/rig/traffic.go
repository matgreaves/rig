package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"
)

// Event types we care about for traffic display.
const (
	typeRequestCompleted  = "request.completed"
	typeConnectionClosed  = "connection.closed"
	typeGRPCCallCompleted = "grpc.call.completed"
)

// event is the top-level JSONL event structure. Only traffic-relevant fields
// are included; lifecycle events are silently skipped.
type event struct {
	Seq        uint64          `json:"seq"`
	Type       string          `json:"type"`
	Timestamp  time.Time       `json:"timestamp"`
	Request    *requestInfo    `json:"request,omitempty"`
	Connection *connectionInfo `json:"connection,omitempty"`
	GRPCCall   *grpcCallInfo   `json:"grpc_call,omitempty"`
}

type requestInfo struct {
	Source                string              `json:"source"`
	Target                string              `json:"target"`
	Ingress               string              `json:"ingress"`
	Method                string              `json:"method"`
	Path                  string              `json:"path"`
	StatusCode            int                 `json:"status_code"`
	LatencyMs             float64             `json:"latency_ms"`
	RequestSize           int64               `json:"request_size"`
	ResponseSize          int64               `json:"response_size"`
	RequestHeaders        map[string][]string `json:"request_headers,omitempty"`
	RequestBody           []byte              `json:"request_body,omitempty"`
	RequestBodyTruncated  bool                `json:"request_body_truncated,omitempty"`
	ResponseHeaders       map[string][]string `json:"response_headers,omitempty"`
	ResponseBody          []byte              `json:"response_body,omitempty"`
	ResponseBodyTruncated bool                `json:"response_body_truncated,omitempty"`
}

type connectionInfo struct {
	Source     string  `json:"source"`
	Target     string  `json:"target"`
	Ingress    string  `json:"ingress"`
	BytesIn    int64   `json:"bytes_in"`
	BytesOut   int64   `json:"bytes_out"`
	DurationMs float64 `json:"duration_ms"`
}

type grpcCallInfo struct {
	Source                string              `json:"source"`
	Target                string              `json:"target"`
	Ingress               string              `json:"ingress"`
	Service               string              `json:"service"`
	Method                string              `json:"method"`
	GRPCStatus            string              `json:"grpc_status"`
	GRPCMessage           string              `json:"grpc_message"`
	LatencyMs             float64             `json:"latency_ms"`
	RequestSize           int64               `json:"request_size"`
	ResponseSize          int64               `json:"response_size"`
	RequestMetadata       map[string][]string `json:"request_metadata,omitempty"`
	ResponseMetadata      map[string][]string `json:"response_metadata,omitempty"`
	RequestBody           []byte              `json:"request_body,omitempty"`
	RequestBodyTruncated  bool                `json:"request_body_truncated,omitempty"`
	ResponseBody          []byte              `json:"response_body,omitempty"`
	ResponseBodyTruncated bool                `json:"response_body_truncated,omitempty"`
	RequestBodyDecoded    json.RawMessage     `json:"request_body_decoded,omitempty"`
	ResponseBodyDecoded   json.RawMessage     `json:"response_body_decoded,omitempty"`
}

// trafficRow is a normalized row ready for display.
type trafficRow struct {
	Index    int
	Time     string // relative to first event
	Source   string
	Target   string
	Protocol string // "HTTP", "gRPC", "TCP"
	Method   string
	Path     string // path for HTTP, service/method for gRPC, "—" for TCP
	Status   string
	Latency  string
	Extra    string // e.g. byte counts for TCP

	// Original event, kept for --detail rendering.
	Event event
}

type trafficFilter struct {
	edge     string
	slowMs   float64
	status   string
	protocol string // "http", "grpc", "tcp", or ""
}

func runTraffic(args []string) error {
	// Extract the file argument from any position so flags can appear
	// before or after the file: rig traffic file.jsonl --detail 1
	// or: rig traffic --detail 1 file.jsonl
	filename, flagArgs := extractFile(args)

	fs := flag.NewFlagSet("traffic", flag.ContinueOnError)
	var (
		detail   int
		edge     string
		slow     string
		status   string
		grpc     bool
		http     bool
		tcp      bool
	)
	fs.IntVar(&detail, "detail", 0, "show full detail for request #N")
	fs.StringVar(&edge, "edge", "", `filter by edge: "source→target", "source", or "→target"`)
	fs.StringVar(&slow, "slow", "", "only show requests slower than threshold (e.g. 5ms, 1s)")
	fs.StringVar(&status, "status", "", "filter by status code (e.g. 500) or class (e.g. 4xx)")
	fs.BoolVar(&grpc, "grpc", false, "only show gRPC calls")
	fs.BoolVar(&http, "http", false, "only show HTTP requests")
	fs.BoolVar(&tcp, "tcp", false, "only show TCP connections")

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

	var filter trafficFilter
	filter.edge = edge
	filter.status = status

	if slow != "" {
		d, err := time.ParseDuration(slow)
		if err != nil {
			return fmt.Errorf("invalid --slow value %q: %v", slow, err)
		}
		filter.slowMs = float64(d) / float64(time.Millisecond)
	}

	switch {
	case grpc:
		filter.protocol = "grpc"
	case http:
		filter.protocol = "http"
	case tcp:
		filter.protocol = "tcp"
	}

	f, err := os.Open(filename)
	if err != nil {
		return err
	}
	defer f.Close()

	events, err := parseTrafficEvents(f)
	if err != nil {
		return err
	}

	if len(events) == 0 {
		fmt.Fprintln(os.Stderr, "No traffic events found.")
		return nil
	}

	rows := buildRows(events)
	rows = applyFilter(rows, filter)

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

// parseTrafficEvents reads JSONL and returns only traffic-related events.
func parseTrafficEvents(r io.Reader) ([]event, error) {
	var events []event
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 256*1024), 1024*1024) // up to 1MB lines
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var ev event
		if err := json.Unmarshal(line, &ev); err != nil {
			return nil, fmt.Errorf("line %d: %w", lineNo, err)
		}
		switch ev.Type {
		case typeRequestCompleted, typeConnectionClosed, typeGRPCCallCompleted:
			events = append(events, ev)
		}
	}
	return events, scanner.Err()
}

// buildRows converts events to display rows, assigning 1-based indices.
func buildRows(events []event) []trafficRow {
	if len(events) == 0 {
		return nil
	}
	t0 := events[0].Timestamp
	rows := make([]trafficRow, len(events))
	for i, ev := range events {
		rel := ev.Timestamp.Sub(t0)
		row := trafficRow{
			Index: i + 1,
			Time:  formatDuration(rel),
			Event: ev,
		}
		switch ev.Type {
		case typeRequestCompleted:
			r := ev.Request
			row.Source = r.Source
			row.Target = r.Target
			row.Protocol = "HTTP"
			row.Method = r.Method
			row.Path = r.Path
			row.Status = strconv.Itoa(r.StatusCode)
			row.Latency = formatLatency(r.LatencyMs)
		case typeGRPCCallCompleted:
			g := ev.GRPCCall
			row.Source = g.Source
			row.Target = g.Target
			row.Protocol = "gRPC"
			row.Method = "gRPC"
			row.Path = g.Service + "/" + g.Method
			row.Status = g.GRPCStatus
			row.Latency = formatLatency(g.LatencyMs)
		case typeConnectionClosed:
			c := ev.Connection
			row.Source = c.Source
			row.Target = c.Target
			row.Protocol = "TCP"
			row.Method = "TCP"
			row.Path = "—"
			row.Status = "—"
			row.Latency = formatLatency(c.DurationMs)
			row.Extra = fmt.Sprintf("%s↑ %s↓", formatBytes(c.BytesIn), formatBytes(c.BytesOut))
		}
		rows[i] = row
	}
	return rows
}

func applyFilter(rows []trafficRow, f trafficFilter) []trafficRow {
	if f.edge == "" && f.slowMs == 0 && f.status == "" && f.protocol == "" {
		return rows
	}
	var out []trafficRow
	for _, r := range rows {
		if !matchEdge(r, f.edge) {
			continue
		}
		if !matchSlow(r, f.slowMs) {
			continue
		}
		if !matchStatus(r, f.status) {
			continue
		}
		if !matchProtocol(r, f.protocol) {
			continue
		}
		out = append(out, r)
	}
	return out
}

func matchEdge(r trafficRow, edge string) bool {
	if edge == "" {
		return true
	}
	// Normalize arrow: accept both → and ->
	edge = strings.ReplaceAll(edge, "->", "→")
	if strings.Contains(edge, "→") {
		parts := strings.SplitN(edge, "→", 2)
		src := strings.TrimSpace(parts[0])
		tgt := strings.TrimSpace(parts[1])
		if src != "" && !strings.EqualFold(r.Source, src) {
			return false
		}
		if tgt != "" && !strings.EqualFold(r.Target, tgt) {
			return false
		}
		return true
	}
	// No arrow: match either source or target.
	return strings.EqualFold(r.Source, edge) || strings.EqualFold(r.Target, edge)
}

func matchSlow(r trafficRow, thresholdMs float64) bool {
	if thresholdMs == 0 {
		return true
	}
	var latencyMs float64
	switch r.Event.Type {
	case typeRequestCompleted:
		latencyMs = r.Event.Request.LatencyMs
	case typeGRPCCallCompleted:
		latencyMs = r.Event.GRPCCall.LatencyMs
	case typeConnectionClosed:
		latencyMs = r.Event.Connection.DurationMs
	}
	return latencyMs >= thresholdMs
}

func matchStatus(r trafficRow, status string) bool {
	if status == "" {
		return true
	}
	// Class match: "4xx", "5xx", etc.
	if len(status) == 3 && status[1] == 'x' && status[2] == 'x' {
		classDigit := status[0]
		if r.Event.Type == typeRequestCompleted {
			actual := strconv.Itoa(r.Event.Request.StatusCode)
			return len(actual) == 3 && actual[0] == classDigit
		}
		return false // gRPC/TCP don't have HTTP status classes
	}
	// Exact match.
	return strings.EqualFold(r.Status, status)
}

func matchProtocol(r trafficRow, protocol string) bool {
	if protocol == "" {
		return true
	}
	return strings.EqualFold(r.Protocol, protocol)
}

func renderTable(w io.Writer, rows []trafficRow) {
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

// formatDuration formats a duration as seconds with 3 decimal places.
func formatDuration(d time.Duration) string {
	secs := d.Seconds()
	return fmt.Sprintf("%.3fs", secs)
}

// formatLatency formats milliseconds into a human-readable string.
func formatLatency(ms float64) string {
	if ms < 1 {
		return fmt.Sprintf("%.0fµs", ms*1000)
	}
	if ms < 1000 {
		return fmt.Sprintf("%.1fms", ms)
	}
	return fmt.Sprintf("%.2fs", ms/1000)
}

// formatBytes formats byte counts into a compact human-readable string.
func formatBytes(b int64) string {
	switch {
	case b < 1024:
		return fmt.Sprintf("%dB", b)
	case b < 1024*1024:
		return fmt.Sprintf("%.1fKB", float64(b)/1024)
	default:
		return fmt.Sprintf("%.1fMB", float64(b)/(1024*1024))
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
