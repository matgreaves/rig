package rig

import (
	"fmt"
	"path/filepath"
	"runtime"
	"testing"
)

// rigTB wraps a testing.TB to intercept assertion failures and post them
// as test.note events to the rig server's event log. This creates a unified
// timeline of server-side events and client-side test assertions.
//
// Helper() is NOT overridden — calls pass through to the embedded TB,
// preserving correct file:line reporting even when assertion libraries
// (testify, is, require, etc.) call t.Helper() internally.
type rigTB struct {
	testing.TB
	serverURL string
	envID     string
}

func (tb *rigTB) Error(args ...any) {
	tb.Helper()
	msg := fmt.Sprint(args...)
	tb.postNote(msg)
	tb.TB.Error(args...)
}

func (tb *rigTB) Errorf(format string, args ...any) {
	tb.Helper()
	msg := fmt.Sprintf(format, args...)
	tb.postNote(msg)
	tb.TB.Errorf(format, args...)
}

func (tb *rigTB) Fatal(args ...any) {
	tb.Helper()
	msg := fmt.Sprint(args...)
	tb.postNote(msg)
	tb.TB.Fatal(args...)
}

func (tb *rigTB) Fatalf(format string, args ...any) {
	tb.Helper()
	msg := fmt.Sprintf(format, args...)
	tb.postNote(msg)
	tb.TB.Fatalf(format, args...)
}

func (tb *rigTB) postNote(msg string) {
	// Capture the caller's file:line. Skip postNote (0) and the
	// Error/Errorf/Fatal/Fatalf wrapper (1) to reach the call site.
	if _, file, line, ok := runtime.Caller(2); ok {
		msg = fmt.Sprintf("%s:%d: %s", filepath.Base(file), line, msg)
	}
	postClientEvent(tb.serverURL, tb.envID, struct {
		Type  string `json:"type"`
		Error string `json:"error"`
	}{
		Type:  "test.note",
		Error: msg,
	})
}
