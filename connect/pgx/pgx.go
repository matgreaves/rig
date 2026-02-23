// Package pgx provides Postgres connectivity built on rig endpoints.
//
// In tests, construct from a resolved environment endpoint:
//
//	pool, err := pgx.Connect(ctx, env.Endpoint("db"))
//	defer pool.Close()
//
// In service code, construct from parsed wiring:
//
//	w, _ := connect.ParseWiring(ctx)
//	pool, err := pgx.Connect(ctx, w.Egress("db"))
package pgx

import (
	"context"
	"database/sql"

	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib" // register "pgx" database/sql driver
	"github.com/matgreaves/rig/connect"
)

// DSN builds a Postgres connection string from endpoint attributes.
// Uses PGHOST/PGPORT/PGUSER/PGPASSWORD/PGDATABASE with sslmode=disable.
func DSN(ep connect.Endpoint) string {
	return connect.PostgresDSN(ep)
}

// Connect returns a pgx connection pool from a rig Postgres endpoint.
func Connect(ctx context.Context, ep connect.Endpoint) (*pgxpool.Pool, error) {
	return pgxpool.New(ctx, DSN(ep))
}

// OpenDB returns a *sql.DB backed by the pgx driver.
func OpenDB(ep connect.Endpoint) (*sql.DB, error) {
	return sql.Open("pgx", DSN(ep))
}
