// echo is a minimal HTTP server used for integration tests.
// It demonstrates the preferred pattern for rig-aware services:
// parse RIG_WIRING once at startup, pass typed config through to functions.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"time"

	"github.com/matgreaves/rig/spec"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if err := run(ctx, os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "echo: %v\n", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, _ []string) error {
	ep, err := ingressEndpoint()
	if err != nil {
		return err
	}
	return serve(ctx, ep)
}

func serve(ctx context.Context, ep spec.Endpoint) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "echo: %s %s", r.Method, r.URL.Path)
	})
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "ok")
	})

	srv := &http.Server{
		Addr:    fmt.Sprintf("%s:%d", ep.Host, ep.Port),
		Handler: mux,
	}

	errCh := make(chan error, 1)
	go func() {
		errCh <- srv.ListenAndServe()
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return srv.Shutdown(shutdownCtx)
}

// ingressEndpoint resolves an ingress endpoint from the environment.
// Defaults to the "default" ingress; pass a name to select another.
// Prefers RIG_WIRING (structured JSON) over the flat HOST/PORT env vars.
// In real code, use the rig client library instead of inlining this.
func ingressEndpoint(name ...string) (spec.Endpoint, error) {
	n := "default"
	if len(name) > 0 {
		n = name[0]
	}
	if raw := os.Getenv("RIG_WIRING"); raw != "" {
		var w struct {
			Ingresses map[string]spec.Endpoint `json:"ingresses"`
		}
		if err := json.Unmarshal([]byte(raw), &w); err != nil {
			return spec.Endpoint{}, fmt.Errorf("parse RIG_WIRING: %w", err)
		}
		ep, ok := w.Ingresses[n]
		if !ok {
			return spec.Endpoint{}, fmt.Errorf("RIG_WIRING: no ingress %q", n)
		}
		return ep, nil
	}

	// Fallback for non-rig-aware callers.
	host := os.Getenv("HOST")
	portStr := os.Getenv("PORT")
	if host == "" || portStr == "" {
		return spec.Endpoint{}, fmt.Errorf("HOST and PORT must be set (or RIG_WIRING)")
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return spec.Endpoint{}, fmt.Errorf("invalid PORT %q: %w", portStr, err)
	}
	return spec.Endpoint{Host: host, Port: port}, nil
}
