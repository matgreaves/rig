// tcpecho is a minimal TCP echo server for integration tests.
// It reads HOST and PORT from environment variables.
package main

import (
	"fmt"
	"io"
	"net"
	"os"
)

func main() {
	host := os.Getenv("HOST")
	if host == "" {
		host = "127.0.0.1"
	}
	port := os.Getenv("PORT")
	if port == "" {
		port = "9090"
	}

	addr := fmt.Sprintf("%s:%s", host, port)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "tcpecho: listen: %v\n", err)
		os.Exit(1)
	}
	defer ln.Close()

	fmt.Fprintf(os.Stdout, "tcpecho: listening on %s\n", addr)

	for {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		go func() {
			defer conn.Close()
			io.Copy(conn, conn)
		}()
	}
}
