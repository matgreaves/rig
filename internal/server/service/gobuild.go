package service

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/matgreaves/rig/internal/server/artifact"
	"github.com/matgreaves/rig/internal/spec"
	"github.com/matgreaves/run"
)

// GoServiceConfig is the type-specific config for "go" services.
type GoServiceConfig struct {
	// Module is an absolute local path ("/abs/path/cmd/server"), a relative
	// path ("./cmd/server") resolved against the environment's Dir, or a
	// remote module reference ("github.com/myorg/tool@v1.2.3").
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
	if !filepath.IsAbs(cfg.Module) && !strings.Contains(cfg.Module, "@") && params.Dir == "" {
		return nil, fmt.Errorf("service %q: relative module path %q requires environment dir (SDK must send \"dir\" field)", params.ServiceName, cfg.Module)
	}
	module := resolveModule(cfg.Module, params.Dir)
	key := artifactKey(module)
	return []artifact.Artifact{{
		Key:      key,
		Resolver: artifact.GoBuild{Module: module, HostEnv: params.HostEnv},
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

	module := resolveModule(cfg.Module, params.Dir)
	key := artifactKey(module)
	out, ok := params.Artifacts[key]
	if !ok {
		return run.Func(func(context.Context) error {
			return fmt.Errorf("service %q: artifact %q not resolved", params.ServiceName, key)
		})
	}

	return run.Process{
		Name:   params.ServiceName,
		Path:   out.Path,
		Dir:    params.Dir,
		Args:   expandAll(params.Args, params.Env),
		Env:    params.Env,
		Stdout: params.Stdout,
		Stderr: params.Stderr,
	}
}

// resolveModule resolves a relative module path against the environment dir.
func resolveModule(module, dir string) string {
	if dir != "" && !filepath.IsAbs(module) {
		return filepath.Clean(filepath.Join(dir, module))
	}
	return module
}

// artifactKey returns the dedup key for a GoBuild artifact.
func artifactKey(module string) string {
	return "gobuild:" + module
}
