package service

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/matgreaves/rig/internal/spec"
	"github.com/matgreaves/run"
)

// ClientConfig is the type-specific config for "client" services.
type ClientConfig struct {
	// StartHandler is the name of the client-side start callback.
	StartHandler string `json:"start_handler"`
}

// Client implements Type for the "client" service type. A client service
// delegates its start phase to a function in the client SDK via the callback
// protocol. The server allocates ports and health-checks normally; only the
// "start" step is handled client-side.
type Client struct{}

// Publish resolves ingress endpoints for a client service.
// Client services run locally, so they use the standard local publish.
func (Client) Publish(_ context.Context, params PublishParams) (map[string]spec.Endpoint, error) {
	return PublishLocalEndpoints(params)
}

// Runner returns a runner that dispatches a start callback to the client,
// then idles until ctx is cancelled.
func (Client) Runner(params StartParams) run.Runner {
	var cfg ClientConfig
	if params.Spec.Config != nil {
		if err := json.Unmarshal(params.Spec.Config, &cfg); err != nil {
			return run.Func(func(context.Context) error {
				return fmt.Errorf("service %q: invalid client config: %w", params.ServiceName, err)
			})
		}
	}

	if params.Callback == nil {
		return run.Func(func(context.Context) error {
			return fmt.Errorf("service %q: client type requires callback support", params.ServiceName)
		})
	}

	return run.Func(func(ctx context.Context) error {
		// Dispatch the start callback — the client launches the function
		// and responds immediately. The function runs until its context
		// is cancelled during cleanup.
		if err := params.Callback(ctx, cfg.StartHandler, "start"); err != nil {
			return fmt.Errorf("service %q: start callback: %w", params.ServiceName, err)
		}

		// Idle until teardown. The service function is running in the
		// client process; health checks will validate it's ready.
		<-ctx.Done()
		return ctx.Err()
	})
}
