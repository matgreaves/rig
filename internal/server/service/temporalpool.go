package service

import (
	"context"
	"fmt"
	"net"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/matgreaves/rig/internal/server/artifact"
	"github.com/matgreaves/run"
)

// temporalLeaseData carries the ports associated with a temporal lease.
type temporalLeaseData struct {
	GRPCPort int
	UIPort   int
}

// NewTemporalPool creates a Pool backed by Temporal dev server processes.
// Each unique version key gets one shared process; individual test environments
// get isolated namespaces within it. cacheDir is the artifact cache root
// (e.g. {rigDir}/cache) used to resolve/locate the Temporal CLI binary.
func NewTemporalPool(cacheDir string) *Pool {
	return NewPool(func(key string) Backend {
		return &temporalBackend{
			version:  key,
			cacheDir: cacheDir,
		}
	}, 2*time.Minute)
}

// temporalBackend implements Backend for Temporal dev server processes.
type temporalBackend struct {
	version  string
	cacheDir string

	binaryPath string
	grpcPort   int
	uiPort     int
	cancel     context.CancelFunc
	done       chan struct{}
	nsCounter  atomic.Int64
}

// Start resolves the Temporal CLI binary and launches `temporal server start-dev`.
func (b *temporalBackend) Start(ctx context.Context) (string, int, error) {
	// Resolve binary via artifact cache.
	binaryPath, err := b.resolveBinary(ctx)
	if err != nil {
		return "", 0, fmt.Errorf("resolve temporal binary: %w", err)
	}
	b.binaryPath = binaryPath

	// Allocate two free ports.
	grpcPort, err := freePort()
	if err != nil {
		return "", 0, fmt.Errorf("allocate gRPC port: %w", err)
	}
	uiPort, err := freePort()
	if err != nil {
		return "", 0, fmt.Errorf("allocate UI port: %w", err)
	}
	b.grpcPort = grpcPort
	b.uiPort = uiPort

	// Launch the dev server.
	procCtx, cancel := context.WithCancel(context.Background())
	b.cancel = cancel
	b.done = make(chan struct{})

	proc := run.Process{
		Name: "temporal-pool",
		Path: binaryPath,
		Args: []string{
			"server", "start-dev",
			"--ip", "127.0.0.1",
			"--port", strconv.Itoa(grpcPort),
			"--ui-port", strconv.Itoa(uiPort),
			"--namespace", "default",
			"--log-format", "json",
		},
	}

	go func() {
		defer close(b.done)
		proc.Run(procCtx)
	}()

	// Wait for the gRPC port to accept connections.
	if err := b.waitReady(ctx); err != nil {
		cancel()
		<-b.done
		return "", 0, fmt.Errorf("wait for temporal ready: %w", err)
	}

	return "127.0.0.1", grpcPort, nil
}

// Stop cancels the process context and waits for the goroutine to finish.
func (b *temporalBackend) Stop() {
	if b.cancel != nil {
		b.cancel()
		<-b.done
	}
}

// NewLease creates an isolated namespace in the running dev server.
func (b *temporalBackend) NewLease(ctx context.Context) (string, any, error) {
	n := b.nsCounter.Add(1)
	ns := fmt.Sprintf("rig_ns_%d", n)

	cmd := exec.CommandContext(ctx, b.binaryPath,
		"operator", "namespace", "create", ns,
		"--address", fmt.Sprintf("127.0.0.1:%d", b.grpcPort),
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", nil, fmt.Errorf("create namespace %s: %w\n%s", ns, err, out)
	}

	return ns, temporalLeaseData{
		GRPCPort: b.grpcPort,
		UIPort:   b.uiPort,
	}, nil
}

// DropLease deletes the namespace. Best-effort — errors are ignored.
func (b *temporalBackend) DropLease(ctx context.Context, id string) {
	cmd := exec.CommandContext(ctx, b.binaryPath,
		"operator", "namespace", "delete", "--yes", id,
		"--address", fmt.Sprintf("127.0.0.1:%d", b.grpcPort),
	)
	cmd.Run()
}

// resolveBinary downloads or locates the cached Temporal CLI binary.
func (b *temporalBackend) resolveBinary(ctx context.Context) (string, error) {
	url := temporalDownloadURL(b.version)
	dl := artifact.Download{URL: url, Binary: "temporal"}

	cacheKey, err := dl.CacheKey()
	if err != nil {
		return "", err
	}
	outputDir := filepath.Join(b.cacheDir, cacheKey)

	// Fast path: already cached.
	if out, ok := dl.Cached(outputDir); ok {
		return out.Path, nil
	}

	// Download and extract.
	out, err := dl.Resolve(ctx, outputDir)
	if err != nil {
		return "", err
	}
	return out.Path, nil
}

// waitReady polls the gRPC port until it accepts TCP connections.
func (b *temporalBackend) waitReady(ctx context.Context) error {
	deadline := time.After(120 * time.Second)
	addr := fmt.Sprintf("127.0.0.1:%d", b.grpcPort)
	for {
		conn, err := net.DialTimeout("tcp", addr, 500*time.Millisecond)
		if err == nil {
			conn.Close()
			return nil
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline:
			return fmt.Errorf("temporal gRPC port %d not ready after 120s", b.grpcPort)
		case <-time.After(200 * time.Millisecond):
		}
	}
}

// freePort binds to :0 to get a free port, then closes the listener.
func freePort() (int, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	return port, nil
}
