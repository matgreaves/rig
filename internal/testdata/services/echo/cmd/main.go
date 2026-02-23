package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"

	"github.com/matgreaves/rig/internal/testdata/services/echo"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if err := echo.Run(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "echo: %v\n", err)
		os.Exit(1)
	}
}
