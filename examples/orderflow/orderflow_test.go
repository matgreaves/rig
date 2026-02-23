package orderflow_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	rig "github.com/matgreaves/rig/client"
	"github.com/matgreaves/rig/connect/httpx"
	"github.com/matgreaves/rig/examples/orderflow"
)

func setupEnv(t *testing.T) *rig.Environment {
	t.Helper()
	return rig.Up(t, rig.Services{
		"db":       rig.Postgres().InitSQLDir("./migrations"),
		"temporal": rig.Temporal(),
		"api": rig.Go("./cmd/orderflow").
			Egress("db").
			Egress("temporal"),
	})
}

func TestOrderFlow(t *testing.T) {
	t.Parallel()
	env := setupEnv(t)
	api := httpx.New(env.Endpoint("api"))

	// POST /orders — create a new order.
	resp, err := api.Post("/orders", "application/json",
		strings.NewReader(`{"customer":"alice@example.com","items":["widget","gadget"]}`))
	if err != nil {
		t.Fatalf("POST /orders: %v", err)
	}
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST /orders: status %d, want %d", resp.StatusCode, http.StatusCreated)
	}

	var order orderflow.Order
	if err := json.NewDecoder(resp.Body).Decode(&order); err != nil {
		t.Fatalf("decode order: %v", err)
	}
	resp.Body.Close()

	if order.Status != "pending" {
		t.Errorf("initial status = %q, want pending", order.Status)
	}
	if order.Customer != "alice@example.com" {
		t.Errorf("customer = %q, want alice@example.com", order.Customer)
	}
	if len(order.Items) != 2 {
		t.Errorf("items = %v, want [widget gadget]", order.Items)
	}

	// GET /orders/{id} — verify order exists.
	resp, err = api.Get("/orders/" + order.ID)
	if err != nil {
		t.Fatalf("GET /orders/%s: %v", order.ID, err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /orders/%s: status %d, want %d", order.ID, resp.StatusCode, http.StatusOK)
	}
	resp.Body.Close()

	// Poll until workflow completes and status becomes "completed".
	var final orderflow.Order
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		resp, err = api.Get("/orders/" + order.ID)
		if err != nil {
			t.Fatalf("GET /orders/%s: %v", order.ID, err)
		}
		json.NewDecoder(resp.Body).Decode(&final)
		resp.Body.Close()
		if final.Status == "completed" {
			break
		}
		time.Sleep(250 * time.Millisecond)
	}
	if final.Status != "completed" {
		t.Fatalf("order status = %q after polling, want completed", final.Status)
	}
}
