// Package echo is a minimal HTTP echo server for integration tests.
//
// It can be used as a standalone binary via the cmd/ subdirectory,
// or in-process via rig.Func(echo.Run).
package echo

import (
	"context"
	"fmt"
	"net/http"

	"github.com/matgreaves/rig/connect/httpx"
)

// Run starts the echo HTTP server. It reads wiring from ctx (via
// connect.ParseWiring) and blocks until ctx is cancelled.
func Run(ctx context.Context) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "echo: %s %s", r.Method, r.URL.Path)
	})
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	return httpx.ListenAndServe(ctx, mux)
}
