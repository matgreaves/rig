package orderflow

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Order represents an order in the system.
type Order struct {
	ID        string    `json:"id"`
	Customer  string    `json:"customer"`
	Items     []string  `json:"items"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

func insertOrder(ctx context.Context, pool *pgxpool.Pool, o Order) error {
	items, err := json.Marshal(o.Items)
	if err != nil {
		return err
	}
	_, err = pool.Exec(ctx,
		`INSERT INTO orders (id, customer, items, status, created_at, updated_at)
		 VALUES ($1, $2, $3, $4, $5, $6)`,
		o.ID, o.Customer, items, o.Status, o.CreatedAt, o.UpdatedAt,
	)
	return err
}

func getOrder(ctx context.Context, pool *pgxpool.Pool, id string) (*Order, error) {
	var o Order
	var items []byte
	err := pool.QueryRow(ctx,
		`SELECT id, customer, items, status, created_at, updated_at
		 FROM orders WHERE id = $1`, id,
	).Scan(&o.ID, &o.Customer, &items, &o.Status, &o.CreatedAt, &o.UpdatedAt)
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(items, &o.Items); err != nil {
		return nil, err
	}
	return &o, nil
}

func updateOrderStatus(ctx context.Context, pool *pgxpool.Pool, id, status string) error {
	_, err := pool.Exec(ctx,
		`UPDATE orders SET status = $1, updated_at = now() WHERE id = $2`,
		status, id,
	)
	return err
}
