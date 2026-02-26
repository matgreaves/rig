package service

import (
	"context"

	"github.com/matgreaves/rig/internal/spec"
	"github.com/matgreaves/run"
)

// Test implements service.Type for the virtual ~test node. It has no
// ingresses and blocks until context cancellation. Its only purpose is
// to participate in the service lifecycle so that waitForEgressesStep
// gates on all real services being READY, and emitEnvironmentUp fires
// from its lifecycle.
type Test struct{}

// Publish returns nil — the ~test node has no ingresses.
func (Test) Publish(_ context.Context, _ PublishParams) (map[string]spec.Endpoint, error) {
	return nil, nil
}

// Runner returns run.Idle — blocks until context is cancelled.
func (Test) Runner(_ StartParams) run.Runner {
	return run.Idle
}
