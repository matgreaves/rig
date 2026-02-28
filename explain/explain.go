// Package explain analyzes rigd JSONL event logs and produces concise failure
// diagnoses. It is imported by client/ (for inline t.Log output) and cmd/rig/
// (for the CLI explain command).
//
// Zero external dependencies — stdlib only.
package explain

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
)

// Report is the structured analysis result from a JSONL event log.
type Report struct {
	Test            string           `json:"test"`
	Outcome         string           `json:"outcome"`
	DurationMs      float64          `json:"duration_ms"`
	Services        []string         `json:"services"`
	Assertions      []Assertion      `json:"assertions,omitempty"`
	Errors          []TrafficError   `json:"errors,omitempty"`
	ServiceErrors   []ServiceError   `json:"service_errors,omitempty"`
	ServiceFailures []ServiceFailure `json:"service_failures,omitempty"`
	Stall           *StallInfo       `json:"stall,omitempty"`
}

// Assertion is a parsed test.note assertion.
type Assertion struct {
	Message string `json:"message"`
	File    string `json:"file,omitempty"`
	Line    int    `json:"line,omitempty"`
}

// TrafficError is an HTTP 4xx/5xx or gRPC error captured by the proxy.
type TrafficError struct {
	Type         string  `json:"type"`                    // "http" or "grpc"
	Source       string  `json:"source"`                  // source service
	Target       string  `json:"target"`                  // target service
	Method       string  `json:"method,omitempty"`        // HTTP method or gRPC method
	Path         string  `json:"path,omitempty"`          // URL path (HTTP) or service/method (gRPC)
	Status       int     `json:"status,omitempty"`        // HTTP status code
	GRPCStatus   string  `json:"grpc_status,omitempty"`   // gRPC status code
	GRPCMessage  string  `json:"grpc_message,omitempty"`  // gRPC status message
	LatencyMs    float64 `json:"latency_ms"`              // request latency
	ResponseBody string  `json:"response_body,omitempty"` // response body (decoded)
}

// ServiceError is a stderr line correlated with a traffic error or service failure.
type ServiceError struct {
	Service string `json:"service"`
	Stream  string `json:"stream"` // "stderr"
	Data    string `json:"data"`
}

// ServiceFailure records a service that crashed or failed to start.
type ServiceFailure struct {
	Service string `json:"service"`
	Error   string `json:"error"`
}

// StallInfo captures the last progress.stall diagnostic snapshot.
type StallInfo struct {
	StalledFor string             `json:"stalled_for"`
	Services   []StallServiceInfo `json:"services"`
}

// StallServiceInfo is a per-service snapshot from a stall diagnostic.
type StallServiceInfo struct {
	Name      string   `json:"name"`
	Phase     string   `json:"phase"`
	WaitingOn []string `json:"waiting_on,omitempty"`
}

// --- Internal event types for JSONL parsing ---

type logHeader struct {
	Type       string   `json:"type"`
	Env        string   `json:"environment"`
	Outcome    string   `json:"outcome"`
	Services   []string `json:"services"`
	DurationMs float64  `json:"duration_ms"`
}

type rawEvent struct {
	Type       string          `json:"type"`
	Service    string          `json:"service,omitempty"`
	Error      string          `json:"error,omitempty"`
	Log        *logEntry       `json:"log,omitempty"`
	Request    *requestInfo    `json:"request,omitempty"`
	GRPCCall   *grpcCallInfo   `json:"grpc_call,omitempty"`
	Diagnostic *diagnosticSnap `json:"diagnostic,omitempty"`
}

type logEntry struct {
	Stream string `json:"stream"`
	Data   string `json:"data"`
}

type requestInfo struct {
	Source       string `json:"source"`
	Target       string `json:"target"`
	Method       string `json:"method"`
	Path         string `json:"path"`
	StatusCode   int    `json:"status_code"`
	LatencyMs    float64 `json:"latency_ms"`
	ResponseBody []byte `json:"response_body,omitempty"`
}

type grpcCallInfo struct {
	Source       string `json:"source"`
	Target       string `json:"target"`
	Service      string `json:"service"`
	Method       string `json:"method"`
	GRPCStatus   string `json:"grpc_status"`
	GRPCMessage  string `json:"grpc_message"`
	LatencyMs    float64 `json:"latency_ms"`
	ResponseBody []byte `json:"response_body,omitempty"`
	ResponseBodyDecoded json.RawMessage `json:"response_body_decoded,omitempty"`
}

type diagnosticSnap struct {
	StalledFor string            `json:"stalled_for"`
	Services   []diagnosticSvc   `json:"services"`
}

type diagnosticSvc struct {
	Name      string   `json:"name"`
	Phase     string   `json:"phase"`
	WaitingOn []string `json:"waiting_on"`
}

// Max stderr lines kept per service during analysis.
const maxStderrLines = 20

// assertionRe matches "file.go:42: message" patterns in test.note error fields.
var assertionRe = regexp.MustCompile(`^(.+\.go):(\d+):\s*(.*)$`)

// AnalyzeFile opens a JSONL file and runs Analyze.
func AnalyzeFile(path string) (*Report, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return Analyze(f)
}

// Analyze performs a single-pass analysis over a JSONL event log and returns
// a structured Report.
func Analyze(r io.Reader) (*Report, error) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 256*1024), 1024*1024)

	// Parse header (first line).
	if !scanner.Scan() {
		if err := scanner.Err(); err != nil {
			return nil, fmt.Errorf("read header: %w", err)
		}
		return nil, fmt.Errorf("empty log file")
	}

	var hdr logHeader
	if err := json.Unmarshal(scanner.Bytes(), &hdr); err != nil {
		return nil, fmt.Errorf("parse header: %w", err)
	}
	if hdr.Type != "log.header" {
		return nil, fmt.Errorf("expected log.header, got %q", hdr.Type)
	}

	report := &Report{
		Test:       hdr.Env,
		Outcome:    hdr.Outcome,
		DurationMs: hdr.DurationMs,
		Services:   hdr.Services,
	}

	// Accumulators for single-pass analysis.
	var (
		assertions      []Assertion
		trafficErrors   []TrafficError
		serviceFailures []ServiceFailure
		stall           *StallInfo
		// stderr lines per service, capped at maxStderrLines.
		stderr = make(map[string][]string)
		// Set of services that appear in service.failed events.
		failedServices = make(map[string]bool)
		// Services that reached the healthy state.
		healthyServices = make(map[string]bool)
		// Traffic/stderr collected before envDown. After the scan, if
		// envUp fired we trim everything before it (startup probes).
		// If envUp never fired (crash), we keep pre-down traffic but
		// filter out errors targeting services that eventually became
		// healthy (transient startup noise).
		envUp      bool
		envDown    bool
		envUpIndex int // number of traffic errors when envUp fired
	)

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var ev rawEvent
		if err := json.Unmarshal(line, &ev); err != nil {
			continue // skip malformed lines
		}

		switch ev.Type {
		case "test.note":
			assertions = append(assertions, parseAssertion(ev.Error))

		case "environment.up":
			envUp = true
			envUpIndex = len(trafficErrors)

		case "environment.destroying":
			envDown = true

		case "request.completed":
			if !envDown && ev.Request != nil && ev.Request.StatusCode >= 400 {
				te := TrafficError{
					Type:      "http",
					Source:    ev.Request.Source,
					Target:   ev.Request.Target,
					Method:   ev.Request.Method,
					Path:     ev.Request.Path,
					Status:   ev.Request.StatusCode,
					LatencyMs: ev.Request.LatencyMs,
				}
				te.ResponseBody = string(ev.Request.ResponseBody)
				trafficErrors = append(trafficErrors, te)
			}

		case "grpc.call.completed":
			if !envDown && ev.GRPCCall != nil && ev.GRPCCall.GRPCStatus != "0" && ev.GRPCCall.GRPCStatus != "OK" {
				te := TrafficError{
					Type:        "grpc",
					Source:      ev.GRPCCall.Source,
					Target:      ev.GRPCCall.Target,
					Method:      ev.GRPCCall.Method,
					Path:        ev.GRPCCall.Service + "/" + ev.GRPCCall.Method,
					GRPCStatus:  ev.GRPCCall.GRPCStatus,
					GRPCMessage: ev.GRPCCall.GRPCMessage,
					LatencyMs:   ev.GRPCCall.LatencyMs,
				}
				if ev.GRPCCall.ResponseBodyDecoded != nil {
					te.ResponseBody = string(ev.GRPCCall.ResponseBodyDecoded)
				} else {
					te.ResponseBody = string(ev.GRPCCall.ResponseBody)
				}
				trafficErrors = append(trafficErrors, te)
			}

		case "service.log":
			if !envDown && ev.Log != nil && ev.Log.Stream == "stderr" {
				svc := ev.Service
				data := strings.TrimRight(ev.Log.Data, "\n")
				if data != "" {
					lines := stderr[svc]
					if len(lines) < maxStderrLines {
						stderr[svc] = append(lines, data)
					} else {
						// Keep last maxStderrLines by shifting.
						copy(lines, lines[1:])
						lines[len(lines)-1] = data
					}
				}
			}

		case "service.healthy":
			healthyServices[ev.Service] = true

		case "service.failed":
			// Keep only the first failure per service — the root cause.
			// Subsequent failures (e.g. health check timeout after crash)
			// are consequences, not causes.
			if !failedServices[ev.Service] {
				serviceFailures = append(serviceFailures, ServiceFailure{
					Service: ev.Service,
					Error:   ev.Error,
				})
			}
			failedServices[ev.Service] = true

		case "environment.failing":
			if ev.Service != "" {
				failedServices[ev.Service] = true
			}

		case "progress.stall":
			if ev.Diagnostic != nil {
				stall = &StallInfo{
					StalledFor: ev.Diagnostic.StalledFor,
				}
				for _, s := range ev.Diagnostic.Services {
					stall.Services = append(stall.Services, StallServiceInfo{
						Name:      s.Name,
						Phase:     s.Phase,
						WaitingOn: s.WaitingOn,
					})
				}
			}
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read events: %w", err)
	}

	// If the test passed, all non-2xx responses were expected behavior
	// (e.g. testing that GET /users/1 returns 404). Don't report them.
	if report.Outcome == "passed" {
		trafficErrors = nil
	} else {
		// If environment.up fired, drop all pre-up traffic (startup probes).
		// If it never fired (crash), keep pre-up traffic but filter out errors
		// targeting services that eventually became healthy — those are just
		// transient startup probe failures, not real errors.
		if envUp && envUpIndex > 0 {
			trafficErrors = trafficErrors[envUpIndex:]
		} else if !envUp && len(healthyServices) > 0 {
			filtered := trafficErrors[:0]
			for _, te := range trafficErrors {
				if !healthyServices[te.Target] {
					filtered = append(filtered, te)
				}
			}
			trafficErrors = filtered
		}

		// Reverse traffic errors: most recent first. The last error before the
		// test assertion is usually the one that caused it.
		for i, j := 0, len(trafficErrors)-1; i < j; i, j = i+1, j-1 {
			trafficErrors[i], trafficErrors[j] = trafficErrors[j], trafficErrors[i]
		}
	}

	report.Assertions = assertions
	report.Errors = trafficErrors
	report.ServiceFailures = serviceFailures
	report.Stall = stall

	// Correlate stderr with traffic errors and failed services.
	report.ServiceErrors = correlateServiceErrors(trafficErrors, stderr, failedServices)

	return report, nil
}

// parseAssertion parses a test.note error string into an Assertion.
// Tries to extract file:line from the message.
func parseAssertion(errMsg string) Assertion {
	a := Assertion{Message: errMsg}
	m := assertionRe.FindStringSubmatch(errMsg)
	if m != nil {
		a.File = m[1]
		fmt.Sscanf(m[2], "%d", &a.Line)
		a.Message = m[3]
	}
	return a
}

// correlateServiceErrors matches traffic error response bodies against service
// stderr lines. Also includes all stderr from services that appear in
// service.failed events.
func correlateServiceErrors(
	errors []TrafficError,
	stderr map[string][]string,
	failedServices map[string]bool,
) []ServiceError {
	// Collect fingerprints from traffic errors: error messages to match.
	type fingerprint struct {
		text    string
		target  string
	}
	var fingerprints []fingerprint
	for _, te := range errors {
		fp := extractErrorFingerprint(te.ResponseBody)
		if fp != "" {
			fingerprints = append(fingerprints, fingerprint{text: fp, target: te.Target})
		}
	}

	seen := make(map[string]bool) // "service:data" dedup key
	var result []ServiceError

	// Match fingerprints against stderr from target services.
	for _, fp := range fingerprints {
		lines, ok := stderr[fp.target]
		if !ok {
			continue
		}
		fpLower := strings.ToLower(fp.text)
		for _, line := range lines {
			if strings.Contains(strings.ToLower(line), fpLower) {
				key := fp.target + ":" + line
				if !seen[key] {
					seen[key] = true
					result = append(result, ServiceError{
						Service: fp.target,
						Stream:  "stderr",
						Data:    line,
					})
				}
			}
		}
	}

	// Include all stderr from services that failed.
	for svc := range failedServices {
		lines, ok := stderr[svc]
		if !ok {
			continue
		}
		for _, line := range lines {
			key := svc + ":" + line
			if !seen[key] {
				seen[key] = true
				result = append(result, ServiceError{
					Service: svc,
					Stream:  "stderr",
					Data:    line,
				})
			}
		}
	}

	return result
}

// extractErrorFingerprint tries to pull out a meaningful error string from
// a response body. If the body is JSON with an "error" field, use that.
// Otherwise use the first non-empty line.
func extractErrorFingerprint(body string) string {
	body = strings.TrimSpace(body)
	if body == "" {
		return ""
	}

	// Try JSON with "error" field.
	if body[0] == '{' {
		var obj map[string]any
		if json.Unmarshal([]byte(body), &obj) == nil {
			if errVal, ok := obj["error"]; ok {
				if s, ok := errVal.(string); ok {
					return s // may be "" if the field exists but is empty
				}
			}
		}
	}

	// Fall back to first line.
	line, _, _ := strings.Cut(body, "\n")
	return strings.TrimSpace(line)
}
