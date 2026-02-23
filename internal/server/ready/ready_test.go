package ready_test

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/matgreaves/rig/internal/server/ready"
	"github.com/matgreaves/rig/internal/spec"
)

func TestTCPCheck_Success(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	port := ln.Addr().(*net.TCPAddr).Port
	checker := &ready.TCP{}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	if err := checker.Check(ctx, "127.0.0.1", port); err != nil {
		t.Errorf("expected success, got: %v", err)
	}
}

func TestTCPCheck_Failure(t *testing.T) {
	checker := &ready.TCP{}

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	// Port 1 is almost certainly not listening.
	err := checker.Check(ctx, "127.0.0.1", 1)
	if err == nil {
		t.Error("expected error for closed port")
	}
}

func TestHTTPCheck_Success(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port

	srv := &http.Server{Handler: mux}
	go srv.Serve(ln)
	defer srv.Close()

	checker := &ready.HTTP{Path: "/health"}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	if err := checker.Check(ctx, "127.0.0.1", port); err != nil {
		t.Errorf("expected success, got: %v", err)
	}
}

func TestHTTPCheck_ServerError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port

	srv := &http.Server{Handler: mux}
	go srv.Serve(ln)
	defer srv.Close()

	checker := &ready.HTTP{Path: "/"}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	err = checker.Check(ctx, "127.0.0.1", port)
	if err == nil {
		t.Error("expected error for 500 response")
	}
}

func TestPoll_Success(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	port := ln.Addr().(*net.TCPAddr).Port

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	err = ready.Poll(ctx, "127.0.0.1", port, &ready.TCP{}, nil, nil)
	if err != nil {
		t.Errorf("expected success, got: %v", err)
	}
}

func TestPoll_Timeout(t *testing.T) {
	// Use a port that's definitely not listening.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close() // Close immediately so nothing is listening.

	shortTimeout := spec.Duration{Duration: 100 * time.Millisecond}
	rs := &spec.ReadySpec{Timeout: shortTimeout}

	ctx := context.Background()
	err = ready.Poll(ctx, "127.0.0.1", port, &ready.TCP{}, rs, nil)
	if err == nil {
		t.Error("expected timeout error")
	}
	// Error should include the last check error, not just "context deadline exceeded".
	if !strings.Contains(err.Error(), "last error:") {
		t.Errorf("timeout error should include last check error, got: %v", err)
	}
}

func TestPoll_OnFailureCallback(t *testing.T) {
	// Port that's not listening.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()

	shortTimeout := spec.Duration{Duration: 100 * time.Millisecond}
	rs := &spec.ReadySpec{Timeout: shortTimeout}

	var failures []error
	onFailure := func(err error) {
		failures = append(failures, err)
	}

	ready.Poll(context.Background(), "127.0.0.1", port, &ready.TCP{}, rs, onFailure)
	if len(failures) == 0 {
		t.Error("expected onFailure to be called at least once")
	}
}

func TestPoll_DelayedReady(t *testing.T) {
	// Start a listener after a delay to simulate slow startup.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close() // Close first.

	// Re-open after 100ms.
	go func() {
		time.Sleep(100 * time.Millisecond)
		ln2, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
		if err != nil {
			return
		}
		defer ln2.Close()
		// Accept connections until test finishes.
		for {
			conn, err := ln2.Accept()
			if err != nil {
				return
			}
			conn.Close()
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err = ready.Poll(ctx, "127.0.0.1", port, &ready.TCP{}, nil, nil)
	if err != nil {
		t.Errorf("expected eventual success, got: %v", err)
	}
}

func TestForEndpoint_InfersFromProtocol(t *testing.T) {
	tests := []struct {
		protocol spec.Protocol
		want     string
	}{
		{spec.TCP, "*ready.TCP"},
		{spec.HTTP, "*ready.HTTP"},
		{spec.GRPC, "*ready.GRPC"},
	}

	for _, tt := range tests {
		ep := spec.Endpoint{Protocol: tt.protocol}
		checker := ready.ForEndpoint(ep, nil)
		got := fmt.Sprintf("%T", checker)
		if got != tt.want {
			t.Errorf("ForEndpoint(%s) = %s, want %s", tt.protocol, got, tt.want)
		}
	}
}

func TestForEndpoint_ReadySpecOverride(t *testing.T) {
	ep := spec.Endpoint{Protocol: spec.HTTP}
	rs := &spec.ReadySpec{Type: "tcp"}
	checker := ready.ForEndpoint(ep, rs)
	got := fmt.Sprintf("%T", checker)
	if got != "*ready.TCP" {
		t.Errorf("expected TCP checker from override, got %s", got)
	}
}
