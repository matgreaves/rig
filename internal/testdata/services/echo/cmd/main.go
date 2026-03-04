package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"

	"github.com/matgreaves/rig/internal/testdata/services/echo"
)

func main() {
	ingress := flag.String("ingress", "", "ingress name (default: \"default\")")
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	run := echo.Run
	if *ingress != "" {
		run = echo.RunOn(*ingress)
	}
	if err := run(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "echo: %v\n", err)
		os.Exit(1)
	}
}
