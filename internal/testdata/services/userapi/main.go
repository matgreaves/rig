// Command userapi is a minimal CRUD API backed by Postgres, used as an
// integration test fixture for rig.
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/signal"

	_ "github.com/lib/pq"

	"github.com/matgreaves/rig/connect"
	"github.com/matgreaves/rig/connect/httpx"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if err := run(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "userapi: %v\n", err)
		os.Exit(1)
	}
}

func run(ctx context.Context) error {
	w, err := connect.ParseWiring(ctx)
	if err != nil {
		return err
	}

	pg := w.Egress("db")
	dsn := fmt.Sprintf("host=%s port=%d user=%s password=%s dbname=%s sslmode=disable",
		pg.Host, pg.Port, pg.Attr("PGUSER"), pg.Attr("PGPASSWORD"), pg.Attr("PGDATABASE"))

	db, err := sql.Open("postgres", dsn)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer db.Close()

	if err := db.PingContext(ctx); err != nil {
		return fmt.Errorf("ping database: %w", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("POST /users", createUser(db))
	mux.HandleFunc("GET /users/{id}", getUser(db))
	mux.HandleFunc("DELETE /users/{id}", deleteUser(db))

	return httpx.ListenAndServe(ctx, mux)
}

type user struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

func createUser(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Name string `json:"name"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		var u user
		err := db.QueryRowContext(r.Context(),
			"INSERT INTO users (name) VALUES ($1) RETURNING id, name", req.Name,
		).Scan(&u.ID, &u.Name)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(u)
	}
}

func getUser(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		var u user
		err := db.QueryRowContext(r.Context(),
			"SELECT id, name FROM users WHERE id = $1", id,
		).Scan(&u.ID, &u.Name)
		if err == sql.ErrNoRows {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(u)
	}
}

func deleteUser(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		result, err := db.ExecContext(r.Context(),
			"DELETE FROM users WHERE id = $1", id)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		n, _ := result.RowsAffected()
		if n == 0 {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}
