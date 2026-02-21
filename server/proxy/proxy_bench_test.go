package proxy_test

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/matgreaves/rig/server/proxy"
	"github.com/matgreaves/rig/spec"
)

// echoHandler returns a small body for every request.
func echoHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "echo: %s %s", r.Method, r.URL.Path)
	})
}

// BenchmarkHTTPDirect measures baseline HTTP request cost to an echo handler
// with no proxy in the path.
func BenchmarkHTTPDirect(b *testing.B) {
	ts := httptest.NewServer(echoHandler())
	b.Cleanup(ts.Close)

	client := ts.Client()
	url := ts.URL + "/bench"

	b.ResetTimer()
	b.ReportAllocs()
	for range b.N {
		resp, err := client.Get(url)
		if err != nil {
			b.Fatal(err)
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}
}

// BenchmarkHTTPProxied measures the same request routed through proxy.Forwarder.
// The diff isolates proxy overhead (header cloning, body teeing, cappedBuffer,
// event emission).
func BenchmarkHTTPProxied(b *testing.B) {
	// Start the real backend.
	backend := httptest.NewServer(echoHandler())
	b.Cleanup(backend.Close)

	backendHost, backendPortStr, _ := net.SplitHostPort(backend.Listener.Addr().String())
	var backendPort int
	fmt.Sscanf(backendPortStr, "%d", &backendPort)

	// Get a free port for the proxy.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		b.Fatal(err)
	}
	proxyPort := ln.Addr().(*net.TCPAddr).Port
	ln.Close()

	// Create and start the forwarder.
	fwd := &proxy.Forwarder{
		ListenPort: proxyPort,
		Target: spec.Endpoint{
			Host:     backendHost,
			Port:     backendPort,
			Protocol: "http",
		},
		Source:    "external",
		TargetSvc: "echo",
		Ingress:   "default",
		Protocol:  "http",
		Emit:      func(proxy.Event) {}, // discard events to isolate proxy overhead
	}

	ctx, cancel := context.WithCancel(context.Background())
	b.Cleanup(cancel)

	go fwd.Runner().Run(ctx)

	// Wait for proxy to be ready.
	waitForTCP(b, fmt.Sprintf("127.0.0.1:%d", proxyPort))

	client := &http.Client{}
	url := fmt.Sprintf("http://127.0.0.1:%d/bench", proxyPort)

	b.ResetTimer()
	b.ReportAllocs()
	for range b.N {
		resp, err := client.Get(url)
		if err != nil {
			b.Fatal(err)
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}
}

// waitForTCP polls until a TCP connection succeeds.
func waitForTCP(b *testing.B, addr string) {
	b.Helper()
	for range 100 {
		conn, err := net.DialTimeout("tcp", addr, 50*time.Millisecond)
		if err == nil {
			conn.Close()
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	b.Fatalf("proxy never became ready at %s", addr)
}
