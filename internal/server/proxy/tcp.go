package proxy

import (
	"context"
	"fmt"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

// runTCP starts a TCP relay that captures connection metadata.
func (f *Forwarder) runTCP(ctx context.Context) error {
	ln, err := f.getListener()
	if err != nil {
		return fmt.Errorf("proxy %s→%s: listen: %w", f.Source, f.TargetSvc, err)
	}

	go func() {
		<-ctx.Done()
		ln.Close()
	}()

	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("proxy %s→%s: accept: %w", f.Source, f.TargetSvc, err)
		}
		go f.handleTCPConn(ctx, conn)
	}
}

func (f *Forwarder) handleTCPConn(ctx context.Context, client net.Conn) {
	start := time.Now()

	f.Emit(Event{
		Type: "connection.opened",
		Connection: &ConnectionInfo{
			Source:  f.Source,
			Target:  f.TargetSvc,
			Ingress: f.Ingress,
		},
	})

	target, err := net.DialTimeout("tcp", f.targetAddr(), 5*time.Second)
	if err != nil {
		client.Close()
		f.Emit(Event{
			Type: "connection.closed",
			Connection: &ConnectionInfo{
				Source:     f.Source,
				Target:     f.TargetSvc,
				Ingress:    f.Ingress,
				DurationMs: float64(time.Since(start).Microseconds()) / 1000.0,
			},
		})
		return
	}

	// Close both when context is cancelled.
	go func() {
		<-ctx.Done()
		client.Close()
		target.Close()
	}()

	var bytesIn, bytesOut atomic.Int64
	var wg sync.WaitGroup
	wg.Add(2)

	// client → target
	go func() {
		defer wg.Done()
		n, _ := io.Copy(target, client)
		bytesIn.Store(n)
		if tc, ok := target.(*net.TCPConn); ok {
			tc.CloseWrite()
		}
	}()

	// target → client
	go func() {
		defer wg.Done()
		n, _ := io.Copy(client, target)
		bytesOut.Store(n)
		if tc, ok := client.(*net.TCPConn); ok {
			tc.CloseWrite()
		}
	}()

	wg.Wait()
	client.Close()
	target.Close()

	f.Emit(Event{
		Type: "connection.closed",
		Connection: &ConnectionInfo{
			Source:     f.Source,
			Target:     f.TargetSvc,
			Ingress:    f.Ingress,
			BytesIn:    bytesIn.Load(),
			BytesOut:   bytesOut.Load(),
			DurationMs: float64(time.Since(start).Microseconds()) / 1000.0,
		},
	})
}
