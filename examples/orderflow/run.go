package orderflow

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/matgreaves/rig/connect"
	"github.com/matgreaves/rig/connect/httpx"
	rigpgx "github.com/matgreaves/rig/connect/pgx"
	"github.com/matgreaves/rig/connect/temporalx"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/log"
	"go.temporal.io/sdk/worker"
)

// Run is the entrypoint for the orderflow service. It reads wiring from the
// context, connects to Postgres and Temporal, starts a worker and HTTP server.
//
// Works identically as a rig.Func or compiled binary — the only difference
// is how the context is provided.
func Run(ctx context.Context) error {
	w, err := connect.ParseWiring(ctx)
	if err != nil {
		return err
	}

	// Set up structured logging — in rig tests, logs appear in the event
	// timeline; in production, they go to stdout.
	logger := slog.New(slog.NewTextHandler(connect.LogWriter(ctx), nil))

	pool, err := rigpgx.Connect(ctx, w.Egress("db"))
	if err != nil {
		return err
	}
	defer pool.Close()

	tc, err := temporalx.Dial(w.Egress("temporal"), client.Options{
		Logger: log.NewStructuredLogger(logger),
	})
	if err != nil {
		return err
	}
	defer tc.Close()

	logger.Info("connected", "db", w.Egress("db").Addr(), "temporal", w.Egress("temporal").Addr())

	// Start Temporal worker — inherits the client's logger.
	wkr := worker.New(tc, "orders", worker.Options{})
	wkr.RegisterWorkflow(ProcessOrder)
	wkr.RegisterActivity(&Activities{Pool: pool})

	if err := wkr.Start(); err != nil {
		return err
	}
	defer wkr.Stop()

	// Start HTTP server.
	h := &Handler{Pool: pool, Temporal: tc, Log: logger}
	mux := http.NewServeMux()
	h.Routes(mux)

	return httpx.ListenAndServe(ctx, mux)
}
