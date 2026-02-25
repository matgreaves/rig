package service

import (
	"context"
	"encoding/json"
	"fmt"
	"runtime"
	"strconv"

	"github.com/matgreaves/rig/connect"
	"github.com/matgreaves/rig/internal/server/artifact"
	"github.com/matgreaves/rig/internal/spec"
	"github.com/matgreaves/run"
)

const (
	temporalDefaultVersion = "1.5.1"
	temporalDefaultNS      = "default"
)

// TemporalConfig is the type-specific config for "temporal" services.
type TemporalConfig struct {
	Version   string `json:"version,omitempty"`
	Namespace string `json:"namespace,omitempty"`
}

// Temporal implements Type and ArtifactProvider for the "temporal" builtin
// service type. It downloads the Temporal CLI binary and runs
// `temporal server start-dev` with automatic port wiring.
type Temporal struct{}

// Artifacts returns a Download artifact for the Temporal CLI binary.
func (Temporal) Artifacts(params ArtifactParams) ([]artifact.Artifact, error) {
	cfg := temporalConfig(params.Spec.Config)
	url := temporalDownloadURL(cfg.Version)
	key := temporalArtifactKey(cfg.Version)
	return []artifact.Artifact{{
		Key:      key,
		Resolver: artifact.Download{URL: url, Binary: "temporal"},
	}}, nil
}

// Publish resolves ingress endpoints and injects Temporal connection attributes
// (TEMPORAL_ADDRESS, TEMPORAL_NAMESPACE) onto the default ingress.
func (Temporal) Publish(_ context.Context, params PublishParams) (map[string]spec.Endpoint, error) {
	endpoints, err := PublishLocalEndpoints(params)
	if err != nil {
		return nil, err
	}
	cfg := temporalConfig(params.Spec.Config)
	if ep, ok := endpoints["default"]; ok {
		if ep.Attributes == nil {
			ep.Attributes = make(map[string]any)
		}
		connect.TemporalAddress.Set(ep.Attributes, "${HOSTPORT}")
		connect.TemporalNamespace.Set(ep.Attributes, cfg.Namespace)
		endpoints["default"] = ep
	}
	return endpoints, nil
}

// Runner looks up the downloaded Temporal CLI binary and runs
// `temporal server start-dev` with the resolved port wiring.
func (Temporal) Runner(params StartParams) run.Runner {
	cfg := temporalConfig(params.Spec.Config)

	// Find the artifact.
	key := temporalArtifactKey(cfg.Version)
	out, ok := params.Artifacts[key]
	if !ok {
		return run.Func(func(context.Context) error {
			return fmt.Errorf("service %q: artifact %q not resolved", params.ServiceName, key)
		})
	}

	// Resolve gRPC and UI ports from ingresses.
	grpcPort := 0
	uiPort := 0
	if ep, ok := params.Ingresses["default"]; ok {
		grpcPort = ep.Port
	}
	if ep, ok := params.Ingresses["ui"]; ok {
		uiPort = ep.Port
	}

	args := []string{
		"server", "start-dev",
		"--ip", "127.0.0.1",
		"--port", strconv.Itoa(grpcPort),
		"--namespace", cfg.Namespace,
		"--log-format", "json",
	}
	if uiPort > 0 {
		args = append(args, "--ui-port", strconv.Itoa(uiPort))
	} else {
		args = append(args, "--headless")
	}

	return run.Process{
		Name:   params.ServiceName,
		Path:   out.Path,
		Args:   args,
		Env:    params.Env,
		Stdout: params.Stdout,
		Stderr: params.Stderr,
	}
}

func temporalConfig(raw json.RawMessage) TemporalConfig {
	cfg := TemporalConfig{
		Version:   temporalDefaultVersion,
		Namespace: temporalDefaultNS,
	}
	if raw != nil {
		json.Unmarshal(raw, &cfg)
	}
	if cfg.Version == "" {
		cfg.Version = temporalDefaultVersion
	}
	if cfg.Namespace == "" {
		cfg.Namespace = temporalDefaultNS
	}
	return cfg
}

func temporalArtifactKey(version string) string {
	return fmt.Sprintf("download:temporal-cli:%s:%s:%s", version, runtime.GOOS, runtime.GOARCH)
}

func temporalDownloadURL(version string) string {
	goos := runtime.GOOS
	goarch := runtime.GOARCH
	return fmt.Sprintf(
		"https://github.com/temporalio/cli/releases/download/v%s/temporal_cli_%s_%s_%s.tar.gz",
		version, version, goos, goarch,
	)
}
