package pgx_test

import (
	"context"
	"testing"

	rig "github.com/matgreaves/rig/client"
	"github.com/matgreaves/rig/connect"
	rigpgx "github.com/matgreaves/rig/connect/pgx"
)

func TestDSN(t *testing.T) {
	ep := connect.Endpoint{
		Host:     "127.0.0.1",
		Port:     5432,
		Protocol: connect.TCP,
		Attributes: map[string]any{
			"PGHOST":     "127.0.0.1",
			"PGPORT":     "5432",
			"PGUSER":     "postgres",
			"PGPASSWORD": "postgres",
			"PGDATABASE": "testdb",
		},
	}
	want := "postgres://postgres:postgres@127.0.0.1:5432/testdb?sslmode=disable"
	if got := rigpgx.DSN(ep); got != want {
		t.Errorf("DSN = %q, want %q", got, want)
	}
}

func TestDSN_Missing(t *testing.T) {
	ep := connect.Endpoint{Host: "127.0.0.1", Port: 5432}
	want := "postgres://:@:/?sslmode=disable"
	if got := rigpgx.DSN(ep); got != want {
		t.Errorf("DSN = %q, want %q", got, want)
	}
}

func TestConnect(t *testing.T) {
	t.Parallel()

	env := rig.Up(t, rig.Services{
		"db": rig.Postgres(),
	})

	pool, err := rigpgx.Connect(context.Background(), env.Endpoint("db"))
	if err != nil {
		t.Fatalf("pgx.Connect: %v", err)
	}
	defer pool.Close()

	// Verify the pool works by running a simple query.
	var result int
	err = pool.QueryRow(context.Background(), "SELECT 1").Scan(&result)
	if err != nil {
		t.Fatalf("SELECT 1: %v", err)
	}
	if result != 1 {
		t.Errorf("SELECT 1 = %d, want 1", result)
	}
}
