package main

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

func loadTestLogEvents(t *testing.T, path string) []logEvent {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer f.Close()
	events, err := parseLogEvents(f)
	if err != nil {
		t.Fatalf("parseLogEvents(%s): %v", path, err)
	}
	return events
}

func TestParseLogEvents(t *testing.T) {
	events := loadTestLogEvents(t, "testdata/service_logs.jsonl")
	// 9 service.log + 2 test.note = 11 events (skips environment.up and request.completed).
	if got := len(events); got != 11 {
		t.Fatalf("got %d events, want 11", got)
	}
	if events[0].Type != typeServiceLog {
		t.Errorf("events[0].Type = %q, want %q", events[0].Type, typeServiceLog)
	}
	if events[0].Service != "order" {
		t.Errorf("events[0].Service = %q, want order", events[0].Service)
	}
	if events[0].Log.Data != "starting order service on :8080" {
		t.Errorf("events[0].Log.Data = %q", events[0].Log.Data)
	}
}

func TestParseTestNotes(t *testing.T) {
	events := loadTestLogEvents(t, "testdata/service_logs.jsonl")

	var notes []logEvent
	for _, ev := range events {
		if ev.Type == typeTestNote {
			notes = append(notes, ev)
		}
	}
	if len(notes) != 2 {
		t.Fatalf("got %d test.note events, want 2", len(notes))
	}
	if !strings.Contains(notes[0].Error, "order_test.go:42") {
		t.Errorf("notes[0].Error = %q, want file:line prefix", notes[0].Error)
	}
	if !strings.Contains(notes[1].Error, "should have been fulfilled") {
		t.Errorf("notes[1].Error = %q", notes[1].Error)
	}
}

func TestRenderLogs(t *testing.T) {
	events := loadTestLogEvents(t, "testdata/service_logs.jsonl")

	serviceIndex := map[string]int{}
	maxName := 4 // "TEST"
	for _, ev := range events {
		if ev.Service == "" {
			continue
		}
		if _, ok := serviceIndex[ev.Service]; !ok {
			serviceIndex[ev.Service] = len(serviceIndex)
		}
		if len(ev.Service) > maxName {
			maxName = len(ev.Service)
		}
	}

	t0 := events[0].Timestamp
	var rows []logRow
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
		rows = append(rows, row)
	}

	var buf bytes.Buffer
	renderLogs(&buf, rows, serviceIndex, maxName)
	out := buf.String()

	lines := strings.Split(strings.TrimSpace(out), "\n")
	if got := len(lines); got != 11 {
		t.Fatalf("got %d lines, want 11", got)
	}
	// Service logs present.
	if !strings.Contains(out, "order") {
		t.Error("missing order service")
	}
	if !strings.Contains(out, "postgres") {
		t.Error("missing postgres service")
	}
	// Test notes present with marker.
	if !strings.Contains(out, "TEST") {
		t.Error("missing TEST label for notes")
	}
	if !strings.Contains(out, "order_test.go:42") {
		t.Error("missing test note content")
	}
}

func TestRenderNotesWithMarker(t *testing.T) {
	events := loadTestLogEvents(t, "testdata/service_logs.jsonl")

	t0 := events[0].Timestamp
	var rows []logRow
	for _, ev := range events {
		if ev.Type != typeTestNote {
			continue
		}
		rows = append(rows, logRow{
			Time:    formatDuration(ev.Timestamp.Sub(t0)),
			Service: "TEST",
			Stream:  "note",
			Data:    ev.Error,
		})
	}

	var buf bytes.Buffer
	renderLogs(&buf, rows, map[string]int{}, 8)
	out := buf.String()

	// Notes should have the ✗ marker.
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if !strings.Contains(line, "✗") {
			t.Errorf("note line missing ✗ marker: %s", line)
		}
	}
}

func TestFilterByService(t *testing.T) {
	events := loadTestLogEvents(t, "testdata/service_logs.jsonl")

	var count int
	for _, ev := range events {
		if ev.Type == typeServiceLog && strings.EqualFold(ev.Service, "order") {
			count++
		}
	}
	if count != 6 {
		t.Fatalf("got %d order log events, want 6", count)
	}
}

func TestFilterByStderr(t *testing.T) {
	events := loadTestLogEvents(t, "testdata/service_logs.jsonl")

	var count int
	for _, ev := range events {
		if ev.Type == typeServiceLog && ev.Log.Stream == "stderr" {
			count++
		}
	}
	// 2 stderr lines: order "failed to connect" + postgres "checkpoint complete".
	if count != 2 {
		t.Fatalf("got %d stderr events, want 2", count)
	}
}

func TestFilterByGrep(t *testing.T) {
	events := loadTestLogEvents(t, "testdata/service_logs.jsonl")

	var count int
	for _, ev := range events {
		data := ""
		if ev.Type == typeTestNote {
			data = ev.Error
		} else if ev.Log != nil {
			data = ev.Log.Data
		}
		if strings.Contains(data, "abc123") {
			count++
		}
	}
	// "processing order abc123", "order abc123 completed successfully",
	// and "order_test.go:51: order abc123 should have been fulfilled"
	if count != 3 {
		t.Fatalf("got %d rows matching abc123, want 3", count)
	}
}

func TestEmptyLogInput(t *testing.T) {
	events, err := parseLogEvents(strings.NewReader(""))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(events) != 0 {
		t.Errorf("got %d events, want 0", len(events))
	}
}
