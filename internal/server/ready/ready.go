package ready

import (
	"context"
	"fmt"
	"time"

	"github.com/matgreaves/rig/internal/spec"
)

const (
	// DefaultInitialInterval is the starting poll interval.
	DefaultInitialInterval = 10 * time.Millisecond

	// DefaultMaxInterval is the maximum poll interval after backoff.
	DefaultMaxInterval = 1 * time.Second

	// DefaultTimeout is the default maximum wait for readiness.
	DefaultTimeout = 30 * time.Second
)

// Checker performs a single readiness probe against an endpoint.
type Checker interface {
	Check(ctx context.Context, host string, port int) error
}

// ForEndpoint returns a Checker appropriate for the given endpoint,
// taking into account any ReadySpec override.
func ForEndpoint(ep spec.Endpoint, readySpec *spec.ReadySpec) Checker {
	checkType := string(ep.Protocol)
	if readySpec != nil && readySpec.Type != "" {
		checkType = readySpec.Type
	}

	switch checkType {
	case "http":
		path := "/"
		if readySpec != nil && readySpec.Path != "" {
			path = readySpec.Path
		}
		return &HTTP{Path: path}
	case "grpc":
		return &GRPC{}
	default:
		return &TCP{}
	}
}

// Poll repeatedly calls checker.Check with exponential backoff until
// the check succeeds or the context is cancelled/timed out.
//
// If onFailure is non-nil it is called after each failed probe with the
// check error, giving the caller an opportunity to log or emit events.
func Poll(ctx context.Context, host string, port int, checker Checker, readySpec *spec.ReadySpec, onFailure func(err error)) error {
	timeout := DefaultTimeout
	interval := DefaultInitialInterval

	if readySpec != nil {
		if readySpec.Timeout.Duration > 0 {
			timeout = readySpec.Timeout.Duration
		}
		if readySpec.Interval.Duration > 0 {
			interval = readySpec.Interval.Duration
		}
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var lastErr error

	for {
		if err := checker.Check(ctx, host, port); err == nil {
			return nil
		} else {
			lastErr = err
			if onFailure != nil {
				onFailure(err)
			}
		}

		select {
		case <-ctx.Done():
			if lastErr != nil {
				return fmt.Errorf("readiness check failed after %s (last error: %v)", timeout, lastErr)
			}
			return fmt.Errorf("readiness check failed: %w", ctx.Err())
		case <-time.After(interval):
		}

		// Exponential backoff, capped at max (but never below the configured interval).
		interval = interval * 2
		maxInterval := DefaultMaxInterval
		if readySpec != nil && readySpec.Interval.Duration > maxInterval {
			maxInterval = readySpec.Interval.Duration
		}
		if interval > maxInterval {
			interval = maxInterval
		}
	}
}
