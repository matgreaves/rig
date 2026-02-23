package main

import (
	"context"
	"log"
	"os"
	"os/signal"

	"github.com/matgreaves/rig/examples/orderflow"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	if err := orderflow.Run(ctx); err != nil {
		log.Fatal(err)
	}
}
