package service

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/matgreaves/rig/server/artifact"
	"github.com/matgreaves/rig/spec"
	"github.com/matgreaves/run"
)

// GoServiceConfig is the type-specific config for "go" services.
type GoServiceConfig struct {
	// Module is an absolute local path ("/abs/path/cmd/server") or a remote
	// module reference ("github.com/myorg/tool@v1.2.3"). The SDK resolves
	// relative paths to absolute before sending the spec.
	Module string `json:"module"`
}

// Go implements Type for the "go" service type. It compiles a Go module during
// the artifact phase and runs the resulting binary during the service phase.
type Go struct{}

// Artifacts returns the GoBuild artifact for this service. Implements ArtifactProvider.
func (Go) Artifacts(params ArtifactParams) ([]artifact.Artifact, error) {
	var cfg GoServiceConfig
	if params.Spec.Config == nil {
		return nil, fmt.Errorf("service %q: missing config", params.ServiceName)
	}
	if err := json.Unmarshal(params.Spec.Config, &cfg); err != nil {
		return nil, fmt.Errorf("service %q: invalid go config: %w", params.ServiceName, err)
	}
	if cfg.Module == "" {
		return nil, fmt.Errorf("service %q: go config missing required \"module\" field", params.ServiceName)
	}
	key := artifactKey(cfg.Module)
	return []artifact.Artifact{{
		Key:      key,
		Resolver: artifact.GoBuild{Module: cfg.Module},
	}}, nil
}

// Publish resolves ingress endpoints for a go service.
func (Go) Publish(_ context.Context, params PublishParams) (map[string]spec.Endpoint, error) {
	return PublishLocalEndpoints(params)
}

// Runner looks up the compiled binary from the artifact results and returns a
// run.Process that executes it with the resolved wiring.
func (Go) Runner(params StartParams) run.Runner {
	var cfg GoServiceConfig
	if params.Spec.Config != nil {
		if err := json.Unmarshal(params.Spec.Config, &cfg); err != nil {
			return run.Func(func(context.Context) error {
				return fmt.Errorf("service %q: invalid go config: %w", params.ServiceName, err)
			})
		}
	}

	key := artifactKey(cfg.Module)
	out, ok := params.Artifacts[key]
	if !ok {
		return run.Func(func(context.Context) error {
			return fmt.Errorf("service %q: artifact %q not resolved", params.ServiceName, key)
		})
	}

	return run.Process{
		Name:   params.ServiceName,
		Path:   out.Path,
		Args:   params.Args,
		Env:    params.Env,
		Stdout: params.Stdout,
		Stderr: params.Stderr,
	}
}

// artifactKey returns the dedup key for a GoBuild artifact.
func artifactKey(module string) string {
	return "gobuild:" + module
}
