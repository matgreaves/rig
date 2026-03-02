package explain

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// JSON writes the report as JSON to w.
func JSON(w io.Writer, r *Report) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(r)
}

// Pretty writes a human-readable report to w.
func Pretty(w io.Writer, r *Report) {
	// Header line.
	outcome := strings.ToUpper(r.Outcome)
	durStr := formatDurationMs(r.DurationMs)
	svcs := "[" + strings.Join(r.Services, ", ") + "]"
	fmt.Fprintf(w, "%s  %s  %s  %s\n", r.Test, outcome, durStr, svcs)

	if len(r.Assertions) > 0 {
		fmt.Fprintln(w)
		fmt.Fprintln(w, "  Assertions:")
		for _, a := range r.Assertions {
			if a.File != "" {
				fmt.Fprintf(w, "    %s:%d: %s\n", a.File, a.Line, a.Message)
			} else {
				fmt.Fprintf(w, "    %s\n", a.Message)
			}
		}
	}

	if len(r.ServiceFailures) > 0 {
		fmt.Fprintln(w)
		fmt.Fprintln(w, "  Service failures:")
		for _, sf := range r.ServiceFailures {
			fmt.Fprintf(w, "    %s: %s\n", sf.Service, sf.Error)
		}
	}

	if r.Stall != nil {
		fmt.Fprintln(w)
		fmt.Fprintf(w, "  Stall (no progress for %s):\n", r.Stall.StalledFor)
		for _, s := range r.Stall.Services {
			if len(s.WaitingOn) > 0 {
				fmt.Fprintf(w, "    %s: %s (waiting on %s)\n",
					s.Name, s.Phase, strings.Join(s.WaitingOn, ", "))
			} else {
				fmt.Fprintf(w, "    %s: %s\n", s.Name, s.Phase)
			}
		}
	}

	if len(r.Errors) > 0 {
		fmt.Fprintln(w)
		fmt.Fprintln(w, "  Errors:")
		for _, e := range r.Errors {
			target := "→ " + e.Target
			switch e.Type {
			case "http":
				fmt.Fprintf(w, "    %s %s %s %d (%.1fms)\n",
					e.Method, target, e.Path, e.Status, e.LatencyMs)
			case "grpc":
				fmt.Fprintf(w, "    gRPC %s %s status=%s (%.1fms)\n",
					target, e.Path, e.GRPCStatus, e.LatencyMs)
				if e.GRPCMessage != "" {
					fmt.Fprintf(w, "      %s\n", e.GRPCMessage)
				}
			}
			if e.ResponseBody != "" {
				fmt.Fprintf(w, "      %s\n", e.ResponseBody)
			}
		}
	}

	if len(r.ServiceErrors) > 0 {
		fmt.Fprintln(w)
		fmt.Fprintln(w, "  Service stderr:")
		for _, se := range r.ServiceErrors {
			fmt.Fprintf(w, "    %s: %s\n", se.Service, se.Data)
		}
	}
}

// Condensed returns a compact multi-line string suitable for t.Log() output.
// Only includes information the agent doesn't already have from Go's test
// output: response bodies, correlated stderr, service failures, and stalls.
// Assertions and test metadata are omitted — Go already printed those.
// Returns "" if the report has no new information to add.
//
// Priority order (root causes first, symptoms last):
//  1. Service failures — a crashed service explains everything downstream
//  2. Stall diagnostics — shows what's blocked and why
//  3. Traffic errors — HTTP 4xx/5xx / gRPC errors with response bodies
//  4. Correlated stderr — server-side logs matching error fingerprints
//
// Each section has a per-section cap so no single category can starve the
// others, even with 20+ traffic errors.
func Condensed(r *Report) string {
	if r.Outcome == "passed" {
		return ""
	}
	if len(r.Errors) == 0 && len(r.ServiceErrors) == 0 &&
		len(r.ServiceFailures) == 0 && r.Stall == nil {
		return ""
	}

	const maxBodyLen = 200

	// Per-section caps. These sum to 20 (the max). Unused budget from
	// earlier sections doesn't carry forward — keeps output predictable.
	const maxFailures = 5
	const maxStall = 5
	const maxTrafficErrors = 7
	const maxStderr = 3

	var b strings.Builder

	// 1. Service failures — root causes.
	n := 0
	for _, sf := range r.ServiceFailures {
		if n >= maxFailures {
			break
		}
		fmt.Fprintf(&b, "rig: %s failed: %s\n", sf.Service, sf.Error)
		n++
	}

	// 2. Stall diagnostics.
	if r.Stall != nil {
		n = 0
		fmt.Fprintf(&b, "rig: stall (%s):\n", r.Stall.StalledFor)
		n++
		for _, s := range r.Stall.Services {
			if n >= maxStall {
				break
			}
			if len(s.WaitingOn) > 0 {
				fmt.Fprintf(&b, "rig:   %s: %s (waiting on %s)\n",
					s.Name, s.Phase, strings.Join(s.WaitingOn, ", "))
			} else {
				fmt.Fprintf(&b, "rig:   %s: %s\n", s.Name, s.Phase)
			}
			n++
		}
	}

	// 3. Traffic errors with response bodies inlined.
	// Deduplicate by target+status+path — repeated identical errors add noise.
	n = 0
	seen := make(map[string]bool)
	for _, e := range r.Errors {
		if n >= maxTrafficErrors {
			break
		}
		key := fmt.Sprintf("%s:%s:%d:%s", e.Target, e.Path, e.Status, e.GRPCStatus)
		if seen[key] {
			continue
		}
		seen[key] = true

		target := "→ " + e.Target
		body := e.ResponseBody
		if len(body) > maxBodyLen {
			body = body[:maxBodyLen] + "..."
		}
		switch e.Type {
		case "http":
			if body != "" {
				fmt.Fprintf(&b, "rig: %s %s %s %d: %s\n",
					e.Method, target, e.Path, e.Status, body)
			} else {
				fmt.Fprintf(&b, "rig: %s %s %s %d\n",
					e.Method, target, e.Path, e.Status)
			}
		case "grpc":
			msg := e.GRPCMessage
			if body != "" {
				msg = body
			}
			if msg != "" {
				fmt.Fprintf(&b, "rig: gRPC %s %s status=%s: %s\n",
					target, e.Path, e.GRPCStatus, msg)
			} else {
				fmt.Fprintf(&b, "rig: gRPC %s %s status=%s\n",
					target, e.Path, e.GRPCStatus)
			}
		}
		n++
	}

	// 4. Correlated service stderr.
	n = 0
	for _, se := range r.ServiceErrors {
		if n >= maxStderr {
			break
		}
		fmt.Fprintf(&b, "rig: %s stderr: %s\n", se.Service, se.Data)
		n++
	}

	return strings.TrimRight(b.String(), "\n")
}

// CondensedFile is a convenience wrapper: opens a file, analyzes it, and
// returns the condensed output. Returns "" on any error.
func CondensedFile(path string) string {
	r, err := AnalyzeFile(path)
	if err != nil {
		return ""
	}
	return Condensed(r)
}

func formatDurationMs(ms float64) string {
	if ms < 1000 {
		return fmt.Sprintf("%.0fms", ms)
	}
	return fmt.Sprintf("%.2fs", ms/1000)
}
