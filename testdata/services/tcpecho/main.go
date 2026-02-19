// tcpecho is a minimal TCP echo server for integration tests.
// It demonstrates the preferred pattern for rig-aware services:
// parse RIG_WIRING once at startup, pass typed config through to functions.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"strconv"

	"github.com/matgreaves/rig/spec"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if err := run(ctx, os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "tcpecho: %v\n", err)
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
	ln, err := net.Listen("tcp", fmt.Sprintf("%s:%d", ep.Host, ep.Port))
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}

	fmt.Fprintf(os.Stdout, "tcpecho: listening on %s:%d\n", ep.Host, ep.Port)

	// Close listener when context is cancelled to unblock Accept.
	go func() {
		<-ctx.Done()
		ln.Close()
	}()

	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil // clean shutdown
			}
			return err
		}
		go func() {
			defer conn.Close()
			io.Copy(conn, conn)
		}()
	}
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
