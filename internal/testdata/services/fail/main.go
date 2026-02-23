// fail exits immediately with an error. Used for testing failure propagation.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if err := run(ctx, os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "fail: %v\n", err)
		os.Exit(1)
	}
}

func run(_ context.Context, _ []string) error {
	return errors.New("intentional failure")
}
