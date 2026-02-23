package orderflow

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.temporal.io/sdk/client"
)

// Handler provides HTTP handlers for the order API.
type Handler struct {
	Pool     *pgxpool.Pool
	Temporal client.Client
}

// Routes registers all HTTP routes on the given mux.
func (h *Handler) Routes(mux *http.ServeMux) {
	mux.HandleFunc("POST /orders", h.createOrder)
	mux.HandleFunc("GET /orders/{id}", h.getOrder)
	mux.HandleFunc("GET /health", h.health)
}

func (h *Handler) createOrder(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Customer string   `json:"customer"`
		Items    []string `json:"items"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	now := time.Now()
	order := Order{
		ID:        uuid.NewString(),
		Customer:  req.Customer,
		Items:     req.Items,
		Status:    "pending",
		CreatedAt: now,
		UpdatedAt: now,
	}

	if err := insertOrder(r.Context(), h.Pool, order); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	_, err := h.Temporal.ExecuteWorkflow(r.Context(), client.StartWorkflowOptions{
		ID:        "order-" + order.ID,
		TaskQueue: "orders",
	}, ProcessOrder, order.ID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(order)
}

func (h *Handler) getOrder(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	order, err := getOrder(r.Context(), h.Pool, id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(order)
}

func (h *Handler) health(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
}
