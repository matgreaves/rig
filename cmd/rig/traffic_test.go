package main

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

func loadTestEvents(t *testing.T, path string) []event {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer f.Close()
	events, err := parseTrafficEvents(f)
	if err != nil {
		t.Fatalf("parseTrafficEvents(%s): %v", path, err)
	}
	return events
}

func TestParseTrafficEvents(t *testing.T) {
	events := loadTestEvents(t, "testdata/mixed_traffic.jsonl")
	// Should skip the environment.up event, keep 6 traffic events.
	if got := len(events); got != 6 {
		t.Fatalf("got %d events, want 6", got)
	}
	if events[0].Type != typeRequestCompleted {
		t.Errorf("events[0].Type = %q, want %q", events[0].Type, typeRequestCompleted)
	}
	if events[1].Type != typeGRPCCallCompleted {
		t.Errorf("events[1].Type = %q, want %q", events[1].Type, typeGRPCCallCompleted)
	}
	if events[4].Type != typeConnectionClosed {
		t.Errorf("events[4].Type = %q, want %q", events[4].Type, typeConnectionClosed)
	}
}

func TestBuildRows(t *testing.T) {
	events := loadTestEvents(t, "testdata/mixed_traffic.jsonl")
	rows := buildRows(events)

	if len(rows) != 6 {
		t.Fatalf("got %d rows, want 6", len(rows))
	}

	// First row: HTTP POST.
	r := rows[0]
	if r.Index != 1 {
		t.Errorf("rows[0].Index = %d, want 1", r.Index)
	}
	if r.Protocol != "HTTP" {
		t.Errorf("rows[0].Protocol = %q, want HTTP", r.Protocol)
	}
	if r.Method != "POST" {
		t.Errorf("rows[0].Method = %q, want POST", r.Method)
	}
	if r.Source != "order" || r.Target != "postgres" {
		t.Errorf("rows[0] edge = %s→%s, want order→postgres", r.Source, r.Target)
	}

	// Second row: gRPC.
	r = rows[1]
	if r.Protocol != "gRPC" {
		t.Errorf("rows[1].Protocol = %q, want gRPC", r.Protocol)
	}
	if r.Path != "WorkflowService/Start" {
		t.Errorf("rows[1].Path = %q, want WorkflowService/Start", r.Path)
	}
	if r.Status != "OK" {
		t.Errorf("rows[1].Status = %q, want OK", r.Status)
	}

	// TCP row.
	r = rows[4]
	if r.Protocol != "TCP" {
		t.Errorf("rows[4].Protocol = %q, want TCP", r.Protocol)
	}
	if !strings.Contains(r.Extra, "↑") || !strings.Contains(r.Extra, "↓") {
		t.Errorf("rows[4].Extra = %q, want byte counts with ↑ ↓", r.Extra)
	}
}

func TestRenderTable(t *testing.T) {
	events := loadTestEvents(t, "testdata/mixed_traffic.jsonl")
	rows := buildRows(events)
	var buf bytes.Buffer
	renderTable(&buf, rows)
	out := buf.String()

	// Header line.
	if !strings.Contains(out, "TIME") || !strings.Contains(out, "EDGE") || !strings.Contains(out, "STATUS") {
		t.Errorf("table missing headers: %s", out)
	}
	// Should have 6 data lines + 1 header.
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if got := len(lines); got != 7 {
		t.Errorf("got %d lines, want 7 (1 header + 6 rows)", got)
	}
	if !strings.Contains(out, "order → postgres") {
		t.Errorf("missing edge in output: %s", out)
	}
	if !strings.Contains(out, "/orders") {
		t.Errorf("missing path in output: %s", out)
	}
}

func TestFilterEdge(t *testing.T) {
	events := loadTestEvents(t, "testdata/mixed_traffic.jsonl")
	rows := buildRows(events)

	// Filter by name (matches source or target).
	filtered := applyFilter(rows, trafficFilter{edge: "temporal"})
	for _, r := range filtered {
		if r.Source != "temporal" && r.Target != "temporal" {
			t.Errorf("expected temporal in edge, got %s→%s", r.Source, r.Target)
		}
	}
	if len(filtered) != 2 { // order→temporal, temporal→order
		t.Errorf("got %d rows for edge=temporal, want 2", len(filtered))
	}

	// Filter source→target with arrow.
	filtered = applyFilter(rows, trafficFilter{edge: "order→postgres"})
	for _, r := range filtered {
		if r.Source != "order" || r.Target != "postgres" {
			t.Errorf("expected order→postgres, got %s→%s", r.Source, r.Target)
		}
	}

	// Filter with -> syntax.
	filtered2 := applyFilter(rows, trafficFilter{edge: "order->postgres"})
	if len(filtered2) != len(filtered) {
		t.Errorf("-> filter got %d, → filter got %d", len(filtered2), len(filtered))
	}
}

func TestFilterSlow(t *testing.T) {
	events := loadTestEvents(t, "testdata/mixed_traffic.jsonl")
	rows := buildRows(events)

	filtered := applyFilter(rows, trafficFilter{slowMs: 5})
	for _, r := range filtered {
		switch r.Event.Type {
		case typeRequestCompleted:
			if r.Event.Request.LatencyMs < 5 {
				t.Errorf("got latency %.1fms, want >= 5ms", r.Event.Request.LatencyMs)
			}
		case typeGRPCCallCompleted:
			if r.Event.GRPCCall.LatencyMs < 5 {
				t.Errorf("got latency %.1fms, want >= 5ms", r.Event.GRPCCall.LatencyMs)
			}
		case typeConnectionClosed:
			if r.Event.Connection.DurationMs < 5 {
				t.Errorf("got duration %.1fms, want >= 5ms", r.Event.Connection.DurationMs)
			}
		}
	}
	// 8.3ms gRPC + 12.4ms TCP + 15.7ms HTTP = 3 results.
	if len(filtered) != 3 {
		t.Errorf("got %d slow rows, want 3", len(filtered))
	}
}

func TestFilterStatus(t *testing.T) {
	events := loadTestEvents(t, "testdata/mixed_traffic.jsonl")
	rows := buildRows(events)

	// Exact status.
	filtered := applyFilter(rows, trafficFilter{status: "500"})
	if len(filtered) != 1 {
		t.Errorf("got %d rows for status=500, want 1", len(filtered))
	}

	// Class match.
	filtered = applyFilter(rows, trafficFilter{status: "2xx"})
	for _, r := range filtered {
		if r.Event.Type == typeRequestCompleted && r.Event.Request.StatusCode/100 != 2 {
			t.Errorf("got status %d, want 2xx", r.Event.Request.StatusCode)
		}
	}
	if len(filtered) != 3 { // 201, 200, 200
		t.Errorf("got %d rows for status=2xx, want 3", len(filtered))
	}
}

func TestFilterProtocol(t *testing.T) {
	events := loadTestEvents(t, "testdata/mixed_traffic.jsonl")
	rows := buildRows(events)

	filtered := applyFilter(rows, trafficFilter{protocol: "grpc"})
	if len(filtered) != 1 {
		t.Errorf("got %d rows for protocol=grpc, want 1", len(filtered))
	}
	if filtered[0].Protocol != "gRPC" {
		t.Errorf("got protocol %q, want gRPC", filtered[0].Protocol)
	}

	filtered = applyFilter(rows, trafficFilter{protocol: "tcp"})
	if len(filtered) != 1 {
		t.Errorf("got %d rows for protocol=tcp, want 1", len(filtered))
	}

	filtered = applyFilter(rows, trafficFilter{protocol: "http"})
	if len(filtered) != 4 {
		t.Errorf("got %d rows for protocol=http, want 4", len(filtered))
	}
}

func TestRenderDetailHTTP(t *testing.T) {
	events := loadTestEvents(t, "testdata/mixed_traffic.jsonl")
	rows := buildRows(events)

	var buf bytes.Buffer
	if err := renderDetail(&buf, rows, 1); err != nil {
		t.Fatalf("renderDetail: %v", err)
	}
	out := buf.String()

	if !strings.Contains(out, "Request #1") {
		t.Errorf("missing request number in output")
	}
	if !strings.Contains(out, "order → postgres") {
		t.Errorf("missing edge in detail")
	}
	if !strings.Contains(out, "Request Headers") {
		t.Errorf("missing request headers section")
	}
	if !strings.Contains(out, "Content-Type: application/json") {
		t.Errorf("missing content-type header")
	}
	// Body should be decoded from base64 and pretty-printed as JSON.
	if !strings.Contains(out, `"name"`) {
		t.Errorf("missing decoded request body in output: %s", out)
	}
	if !strings.Contains(out, `"id"`) {
		t.Errorf("missing decoded response body in output: %s", out)
	}
}

func TestRenderDetailGRPC(t *testing.T) {
	events := loadTestEvents(t, "testdata/mixed_traffic.jsonl")
	rows := buildRows(events)

	var buf bytes.Buffer
	if err := renderDetail(&buf, rows, 2); err != nil {
		t.Fatalf("renderDetail: %v", err)
	}
	out := buf.String()

	if !strings.Contains(out, "Request #2") {
		t.Errorf("missing request number")
	}
	if !strings.Contains(out, "gRPC") {
		t.Errorf("missing gRPC label")
	}
	if !strings.Contains(out, "Request Metadata") {
		t.Errorf("missing request metadata section")
	}
	// Decoded response body should show run_id.
	if !strings.Contains(out, "run_id") {
		t.Errorf("missing decoded response body: %s", out)
	}
}

func TestRenderDetailTCP(t *testing.T) {
	events := loadTestEvents(t, "testdata/mixed_traffic.jsonl")
	rows := buildRows(events)

	var buf bytes.Buffer
	if err := renderDetail(&buf, rows, 5); err != nil {
		t.Fatalf("renderDetail: %v", err)
	}
	out := buf.String()

	if !strings.Contains(out, "Request #5") {
		t.Errorf("missing request number")
	}
	if !strings.Contains(out, "Bytes In") {
		t.Errorf("missing bytes in section")
	}
	if !strings.Contains(out, "1.2KB") {
		t.Errorf("missing bytes in value: %s", out)
	}
}

func TestRenderDetailNotFound(t *testing.T) {
	events := loadTestEvents(t, "testdata/mixed_traffic.jsonl")
	rows := buildRows(events)

	var buf bytes.Buffer
	err := renderDetail(&buf, rows, 99)
	if err == nil {
		t.Fatal("expected error for invalid index")
	}
	if !strings.Contains(err.Error(), "#99 not found") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestFormatLatency(t *testing.T) {
	tests := []struct {
		ms   float64
		want string
	}{
		{0.5, "500µs"},
		{2.1, "2.1ms"},
		{123.4, "123.4ms"},
		{1500, "1.50s"},
	}
	for _, tt := range tests {
		got := formatLatency(tt.ms)
		if got != tt.want {
			t.Errorf("formatLatency(%v) = %q, want %q", tt.ms, got, tt.want)
		}
	}
}

func TestFormatBytes(t *testing.T) {
	tests := []struct {
		b    int64
		want string
	}{
		{0, "0B"},
		{100, "100B"},
		{1024, "1.0KB"},
		{1536, "1.5KB"},
		{1048576, "1.0MB"},
	}
	for _, tt := range tests {
		got := formatBytes(tt.b)
		if got != tt.want {
			t.Errorf("formatBytes(%d) = %q, want %q", tt.b, got, tt.want)
		}
	}
}

func TestEmptyInput(t *testing.T) {
	events, err := parseTrafficEvents(strings.NewReader(""))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(events) != 0 {
		t.Errorf("got %d events, want 0", len(events))
	}
}

func TestParseInvalidJSON(t *testing.T) {
	f, err := os.Open("testdata/invalid.jsonl")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer f.Close()
	_, err = parseTrafficEvents(f)
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
	if !strings.Contains(err.Error(), "line 2") {
		t.Errorf("error should mention line 2: %v", err)
	}
}
