package service

import (
	"context"
	"encoding/json"
	"fmt"
	"runtime"
	"sync"

	"github.com/matgreaves/rig/connect"
	"github.com/matgreaves/rig/internal/server/artifact"
	"github.com/matgreaves/rig/internal/spec"
	"github.com/matgreaves/run"
)

const (
	temporalDefaultVersion = "1.5.1"
)

// TemporalConfig is the type-specific config for "temporal" services.
type TemporalConfig struct {
	Version string `json:"version,omitempty"`
}

// Temporal implements Type and ArtifactProvider for the "temporal" builtin
// service type. It uses a Pool to share dev server processes across
// environments, providing per-test namespace isolation.
type Temporal struct {
	pool   *Pool
	leases sync.Map // "instanceID:serviceName" → *Lease
}

// NewTemporal creates a Temporal service type backed by the given pool.
func NewTemporal(pool *Pool) *Temporal {
	return &Temporal{pool: pool}
}

// Artifacts returns a Download artifact for the Temporal CLI binary.
// The pool manages processes, but the artifact phase still ensures the
// binary is downloaded before any Acquire call.
func (t *Temporal) Artifacts(params ArtifactParams) ([]artifact.Artifact, error) {
	cfg := temporalConfig(params.Spec.Config)
	url := temporalDownloadURL(cfg.Version)
	key := temporalArtifactKey(cfg.Version)
	return []artifact.Artifact{{
		Key:      key,
		Resolver: artifact.Download{URL: url, Binary: "temporal"},
	}}, nil
}

// Publish acquires a lease from the pool (which creates a per-test namespace)
// and returns endpoints using the shared process's ports.
func (t *Temporal) Publish(ctx context.Context, params PublishParams) (map[string]spec.Endpoint, error) {
	cfg := temporalConfig(params.Spec.Config)

	lease, err := t.pool.Acquire(ctx, cfg.Version)
	if err != nil {
		return nil, fmt.Errorf("temporal publish: %w", err)
	}

	// Store the lease for later phases.
	t.leases.Store(leaseKey(params.InstanceID, params.ServiceName), lease)

	data := lease.Data.(temporalLeaseData)

	// Build endpoints for each ingress.
	endpoints := make(map[string]spec.Endpoint, len(params.Ingresses))
	for name, ingSpec := range params.Ingresses {
		port := data.GRPCPort
		if name == "ui" {
			port = data.UIPort
		}
		endpoints[name] = spec.Endpoint{
			HostPort:   fmt.Sprintf("%s:%d", lease.Host, port),
			Protocol:   ingSpec.Protocol,
			Attributes: map[string]any{},
		}
	}

	// Inject Temporal connection attributes on the default ingress.
	if ep, ok := endpoints["default"]; ok {
		connect.TemporalAddress.Set(ep.Attributes, "${HOSTPORT}")
		connect.TemporalNamespace.Set(ep.Attributes, lease.ID)
		endpoints["default"] = ep
	}

	return endpoints, nil
}

// Runner returns a runner that blocks on ctx and releases the lease on exit.
// The shared process is managed by the pool — no per-test subprocess.
func (t *Temporal) Runner(params StartParams) run.Runner {
	return run.Func(func(ctx context.Context) error {
		key := leaseKey(params.InstanceID, params.ServiceName)
		v, ok := t.leases.Load(key)
		if !ok {
			return fmt.Errorf("temporal runner: no lease for %s", key)
		}
		lease := v.(*Lease)

		// Block until teardown.
		<-ctx.Done()

		// Release the lease (drops the per-test namespace).
		t.leases.Delete(key)
		t.pool.Release(lease)

		return ctx.Err()
	})
}

func temporalConfig(raw json.RawMessage) TemporalConfig {
	cfg := TemporalConfig{
		Version: temporalDefaultVersion,
	}
	if raw != nil {
		json.Unmarshal(raw, &cfg)
	}
	if cfg.Version == "" {
		cfg.Version = temporalDefaultVersion
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
