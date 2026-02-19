// tcpecho is a minimal TCP echo server for integration tests.
package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"

	"github.com/matgreaves/rig/connect"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if err := run(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "tcpecho: %v\n", err)
		os.Exit(1)
	}
}

func run(ctx context.Context) error {
	w, err := connect.ParseWiring(ctx)
	if err != nil {
		return err
	}
	ep := w.Ingress()

	ln, err := net.Listen("tcp", ep.Addr())
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}

	fmt.Fprintf(os.Stdout, "tcpecho: listening on %s\n", ep.Addr())

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
			return err
		}
		go func() {
			defer conn.Close()
			io.Copy(conn, conn)
		}()
	}
}
