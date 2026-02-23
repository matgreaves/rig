package main

import (
	"context"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/matgreaves/rig/server"
	"github.com/matgreaves/rig/server/service"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:0", "listen address")
	idle := flag.Duration("idle", 5*time.Minute, "idle shutdown timeout (0 to disable)")
	rigDir := flag.String("rig-dir", "", "rig directory (default ~/.rig)")
	flag.Parse()

	if *rigDir == "" {
		*rigDir = server.DefaultRigDir()
	}

	reg := service.NewRegistry()
	reg.Register("process", service.Process{})
	reg.Register("go", service.Go{})
	reg.Register("container", service.Container{})
	reg.Register("client", service.Client{})
	reg.Register("postgres", service.Postgres{})
	reg.Register("temporal", service.Temporal{})

	s := server.NewServer(
		server.NewPortAllocator(),
		reg,
		"",    // tempBase — use OS default
		*idle,
		*rigDir,
	)

	ln, err := net.Listen("tcp", *addr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "rigd: listen: %v\n", err)
		os.Exit(1)
	}

	// Write addr file atomically so clients never read a partial address.
	addrFile := filepath.Join(*rigDir, "rigd.addr")
	if err := os.MkdirAll(*rigDir, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "rigd: mkdir %s: %v\n", *rigDir, err)
		os.Exit(1)
	}
	tmpFile := addrFile + ".tmp"
	if err := os.WriteFile(tmpFile, []byte(ln.Addr().String()), 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "rigd: write addr file: %v\n", err)
		os.Exit(1)
	}
	if err := os.Rename(tmpFile, addrFile); err != nil {
		os.Remove(tmpFile)
		fmt.Fprintf(os.Stderr, "rigd: rename addr file: %v\n", err)
		os.Exit(1)
	}
	defer os.Remove(addrFile)

	fmt.Fprintf(os.Stderr, "rigd listening on %s\n", ln.Addr())

	httpSrv := &http.Server{Handler: s}

	// Serve in background.
	serveErr := make(chan error, 1)
	go func() { serveErr <- httpSrv.Serve(ln) }()

	// Wait for idle shutdown, signal, or serve error.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	select {
	case <-s.ShutdownCh():
		fmt.Fprintln(os.Stderr, "rigd: idle timeout, shutting down")
	case sig := <-sigCh:
		fmt.Fprintf(os.Stderr, "rigd: received %s, shutting down\n", sig)
	case err := <-serveErr:
		fmt.Fprintf(os.Stderr, "rigd: serve error: %v\n", err)
		os.Exit(1)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	httpSrv.Shutdown(ctx)
}
