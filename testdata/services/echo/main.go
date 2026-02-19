// echo is a minimal HTTP server used for integration tests.
package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"

	"github.com/matgreaves/rig/connect/httpx"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if err := run(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "echo: %v\n", err)
		os.Exit(1)
	}
}

func run(ctx context.Context) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "echo: %s %s", r.Method, r.URL.Path)
	})
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	return httpx.ListenAndServe(ctx, mux)
}
