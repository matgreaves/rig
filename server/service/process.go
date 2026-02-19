package service

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/matgreaves/rig/spec"
	"github.com/matgreaves/run"
)

// ProcessConfig is the type-specific config for "process" services.
type ProcessConfig struct {
	// Command is the path to the executable.
	Command string `json:"command"`

	// Dir is the working directory. Optional.
	Dir string `json:"dir,omitempty"`
}

// Process implements Type for the "process" service type.
// It runs an external binary with arguments and environment variables.
type Process struct{}

// Publish resolves ingress endpoints for a process service.
func (Process) Publish(_ context.Context, params PublishParams) (map[string]spec.Endpoint, error) {
	return PublishLocalEndpoints(params)
}

// Runner returns a run.Process that executes the configured binary.
func (Process) Runner(params StartParams) run.Runner {
	var cfg ProcessConfig
	if params.Spec.Config != nil {
		if err := json.Unmarshal(params.Spec.Config, &cfg); err != nil {
			return run.Func(func(context.Context) error {
				return fmt.Errorf("service %q: invalid process config: %w", params.ServiceName, err)
			})
		}
	}

	return run.Process{
		Name:   params.ServiceName,
		Path:   cfg.Command,
		Dir:    cfg.Dir,
		Args:   params.Args,
		Env:    params.Env,
		Stdout: params.Stdout,
		Stderr: params.Stderr,
	}
}
