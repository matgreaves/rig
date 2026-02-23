package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"

	"github.com/matgreaves/rig/connect"
	"github.com/matgreaves/rig/connect/httpx"
	rigpgx "github.com/matgreaves/rig/connect/pgx"
	"github.com/matgreaves/rig/connect/temporalx"
	"github.com/matgreaves/rig/examples/orderflow"
	"go.temporal.io/sdk/worker"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	if err := run(ctx); err != nil {
		log.Fatal(err)
	}
}

func run(ctx context.Context) error {
	w, err := connect.ParseWiring(ctx)
	if err != nil {
		return err
	}

	pool, err := rigpgx.Connect(ctx, w.Egress("db"))
	if err != nil {
		return err
	}
	defer pool.Close()

	tc, err := temporalx.Dial(w.Egress("temporal"))
	if err != nil {
		return err
	}
	defer tc.Close()

	// Start Temporal worker.
	wkr := worker.New(tc, "orders", worker.Options{})
	wkr.RegisterWorkflow(orderflow.ProcessOrder)
	wkr.RegisterActivity(&orderflow.Activities{Pool: pool})

	if err := wkr.Start(); err != nil {
		return err
	}
	defer wkr.Stop()

	// Start HTTP server.
	h := &orderflow.Handler{Pool: pool, Temporal: tc}
	mux := http.NewServeMux()
	h.Routes(mux)

	return httpx.ListenAndServe(ctx, mux)
}
