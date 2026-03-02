package explain

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestAnalyzeAssertionFailure(t *testing.T) {
	r, err := AnalyzeFile("testdata/assertion_failure.jsonl")
	if err != nil {
		t.Fatal(err)
	}

	if r.Outcome != "failed" {
		t.Errorf("outcome = %q, want %q", r.Outcome, "failed")
	}
	if !contains(r.Services, "api") {
		t.Errorf("services = %v, want to contain 'api'", r.Services)
	}

	// Should have at least one assertion.
	if len(r.Assertions) == 0 {
		t.Fatal("expected at least one assertion")
	}
	found := false
	for _, a := range r.Assertions {
		if strings.Contains(a.Message, "expected 200, got 500") {
			found = true
			if a.File == "" {
				t.Error("assertion should have file set")
			}
			if a.Line == 0 {
				t.Error("assertion should have line set")
			}
		}
	}
	if !found {
		t.Errorf("expected assertion about '200, got 500', got %+v", r.Assertions)
	}

	// Should have at least one error traffic event (the 500).
	if len(r.Errors) == 0 {
		t.Fatal("expected at least one traffic error")
	}
	found500 := false
	for _, e := range r.Errors {
		if e.Status == 500 {
			found500 = true
			if e.Path != "/bad" {
				t.Errorf("error path = %q, want /bad", e.Path)
			}
			if !strings.Contains(e.ResponseBody, "something went wrong") {
				t.Errorf("error body = %q, want to contain 'something went wrong'", e.ResponseBody)
			}
		}
	}
	if !found500 {
		t.Errorf("expected a 500 error, got %+v", r.Errors)
	}

	// No service failures (services started fine).
	if len(r.ServiceFailures) != 0 {
		t.Errorf("expected no service failures, got %+v", r.ServiceFailures)
	}
}

func TestAnalyzeServiceCrash(t *testing.T) {
	r, err := AnalyzeFile("testdata/service_crash.jsonl")
	if err != nil {
		t.Fatal(err)
	}

	if r.Outcome != "crashed" {
		t.Errorf("outcome = %q, want %q", r.Outcome, "crashed")
	}
	if !contains(r.Services, "crasher") {
		t.Errorf("services = %v, want to contain 'crasher'", r.Services)
	}

	// Should have exactly one service failure per service — deduplicated.
	// The fixture has two service.failed events for "crasher" (the crash
	// itself and the subsequent health check timeout). Only the first
	// (root cause) should be kept.
	if len(r.ServiceFailures) != 1 {
		t.Errorf("expected 1 service failure (deduplicated), got %d: %+v",
			len(r.ServiceFailures), r.ServiceFailures)
	}
	if r.ServiceFailures[0].Service != "crasher" {
		t.Errorf("expected 'crasher' service failure, got %q", r.ServiceFailures[0].Service)
	}
	// The first failure should be the root cause, not the health check timeout.
	if strings.Contains(r.ServiceFailures[0].Error, "readiness check") {
		t.Errorf("first service failure should be root cause, not health check timeout: %s",
			r.ServiceFailures[0].Error)
	}
}

func TestAnalyzePassed(t *testing.T) {
	r, err := AnalyzeFile("testdata/passed.jsonl")
	if err != nil {
		t.Fatal(err)
	}

	if r.Outcome != "passed" {
		t.Errorf("outcome = %q, want %q", r.Outcome, "passed")
	}
	if len(r.Assertions) != 0 {
		t.Errorf("expected no assertions, got %+v", r.Assertions)
	}
	if len(r.Errors) != 0 {
		t.Errorf("expected no traffic errors, got %+v", r.Errors)
	}
	if len(r.ServiceFailures) != 0 {
		t.Errorf("expected no service failures, got %+v", r.ServiceFailures)
	}
}

func TestPrettyFormat(t *testing.T) {
	r, err := AnalyzeFile("testdata/assertion_failure.jsonl")
	if err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	Pretty(&buf, r)
	out := buf.String()

	// Should contain the test name and outcome.
	if !strings.Contains(out, "TestGenerate/assertion_failure") {
		t.Errorf("pretty output missing test name")
	}
	if !strings.Contains(out, "FAILED") {
		t.Errorf("pretty output missing FAILED")
	}

	// Should contain assertion info.
	if !strings.Contains(out, "Assertions:") {
		t.Errorf("pretty output missing Assertions section")
	}
	if !strings.Contains(out, "expected 200, got 500") {
		t.Errorf("pretty output missing assertion message")
	}

	// Should contain error traffic info with target (not source).
	if !strings.Contains(out, "Errors:") {
		t.Errorf("pretty output missing Errors section")
	}
	if !strings.Contains(out, "500") {
		t.Errorf("pretty output missing 500 status")
	}
	if !strings.Contains(out, "→ api") {
		t.Errorf("pretty output should show → target")
	}

	t.Logf("Pretty output:\n%s", out)
}

func TestJSONFormat(t *testing.T) {
	r, err := AnalyzeFile("testdata/assertion_failure.jsonl")
	if err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	if err := JSON(&buf, r); err != nil {
		t.Fatal(err)
	}

	// Verify it's valid JSON.
	var decoded Report
	if err := json.Unmarshal(buf.Bytes(), &decoded); err != nil {
		t.Fatalf("JSON output is not valid: %v\n%s", err, buf.String())
	}

	if decoded.Test != r.Test {
		t.Errorf("JSON test = %q, want %q", decoded.Test, r.Test)
	}
	if decoded.Outcome != r.Outcome {
		t.Errorf("JSON outcome = %q, want %q", decoded.Outcome, r.Outcome)
	}
}

func TestCondensedNonEmpty(t *testing.T) {
	r, err := AnalyzeFile("testdata/assertion_failure.jsonl")
	if err != nil {
		t.Fatal(err)
	}

	out := Condensed(r)
	if out == "" {
		t.Fatal("expected non-empty condensed output for failed test")
	}

	// Should be capped at ~20 lines.
	lines := strings.Split(out, "\n")
	if len(lines) > 20 {
		t.Errorf("condensed output has %d lines, want <= 20", len(lines))
	}

	// Should contain response body (new information the agent needs).
	if !strings.Contains(out, "something went wrong") {
		t.Error("condensed output should contain response body")
	}

	// Should NOT contain the assertion — Go already printed it.
	if strings.Contains(out, "expected 200, got 500") {
		t.Error("condensed output should not repeat assertions")
	}

	// Should NOT contain a header line with test name/outcome.
	if strings.Contains(out, "FAILED") {
		t.Error("condensed output should not contain header metadata")
	}

	// Should show the target service, not ~test source.
	if strings.Contains(out, "~test") {
		t.Error("condensed output should not show ~test source")
	}
	if !strings.Contains(out, "→ api") {
		t.Error("condensed output should show → target")
	}

	t.Logf("Condensed output:\n%s", out)
}

func TestCondensedPassed(t *testing.T) {
	r, err := AnalyzeFile("testdata/passed.jsonl")
	if err != nil {
		t.Fatal(err)
	}

	out := Condensed(r)
	if out != "" {
		t.Errorf("expected empty condensed output for passed test, got: %s", out)
	}
}

func TestCondensedFile(t *testing.T) {
	out := CondensedFile("testdata/assertion_failure.jsonl")
	if out == "" {
		t.Fatal("expected non-empty output from CondensedFile")
	}

	// Non-existent file should return empty string.
	out = CondensedFile("testdata/nonexistent.jsonl")
	if out != "" {
		t.Errorf("expected empty output for nonexistent file, got: %s", out)
	}
}

func TestParseAssertion(t *testing.T) {
	tests := []struct {
		input   string
		file    string
		line    int
		message string
	}{
		{"order_test.go:42: expected 200, got 500", "order_test.go", 42, "expected 200, got 500"},
		{"some error without file:line", "", 0, "some error without file:line"},
		{"file.go:1: short", "file.go", 1, "short"},
	}
	for _, tt := range tests {
		a := parseAssertion(tt.input)
		if a.File != tt.file {
			t.Errorf("parseAssertion(%q).File = %q, want %q", tt.input, a.File, tt.file)
		}
		if a.Line != tt.line {
			t.Errorf("parseAssertion(%q).Line = %d, want %d", tt.input, a.Line, tt.line)
		}
		if a.Message != tt.message {
			t.Errorf("parseAssertion(%q).Message = %q, want %q", tt.input, a.Message, tt.message)
		}
	}
}

func TestExtractErrorFingerprint(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{`{"error":"column does not exist"}`, "column does not exist"},
		{`{"status":"error","error":"db timeout"}`, "db timeout"},
		{`plain text error`, "plain text error"},
		{`{"error":""}`, ""},
		{``, ""},
		{"line one\nline two", "line one"},
	}
	for _, tt := range tests {
		got := extractErrorFingerprint(tt.input)
		if got != tt.want {
			t.Errorf("extractErrorFingerprint(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func contains(ss []string, s string) bool {
	for _, v := range ss {
		if v == s {
			return true
		}
	}
	return false
}
