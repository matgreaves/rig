package orderflow

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"
)

// ProcessOrder is a Temporal workflow that transitions an order through
// pending → processing → completed.
func ProcessOrder(ctx workflow.Context, orderID string) error {
	ao := workflow.ActivityOptions{
		StartToCloseTimeout: 10 * time.Second,
		RetryPolicy: &temporal.RetryPolicy{
			InitialInterval:    100 * time.Millisecond,
			BackoffCoefficient: 2.0,
			MaximumAttempts:    5,
		},
	}
	ctx = workflow.WithActivityOptions(ctx, ao)

	if err := workflow.ExecuteActivity(ctx, (*Activities).UpdateStatus, orderID, "processing").Get(ctx, nil); err != nil {
		return err
	}
	if err := workflow.ExecuteActivity(ctx, (*Activities).UpdateStatus, orderID, "completed").Get(ctx, nil); err != nil {
		return err
	}
	return nil
}

// Activities holds dependencies for workflow activities.
type Activities struct {
	Pool *pgxpool.Pool
}

// UpdateStatus updates the order's status in the database.
func (a *Activities) UpdateStatus(ctx context.Context, orderID, status string) error {
	return updateOrderStatus(ctx, a.Pool, orderID, status)
}
