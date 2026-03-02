package service

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/matgreaves/rig/connect"
	"github.com/matgreaves/rig/internal/server/artifact"
	"github.com/matgreaves/rig/internal/spec"
	"github.com/matgreaves/run"
)

// RedisConfig is the type-specific config for "redis" services.
type RedisConfig struct {
	Image string `json:"image,omitempty"`
}

// Redis implements Type and ArtifactProvider for the "redis" builtin
// service type. It uses a Pool to share containers across environments,
// providing per-test database isolation.
type Redis struct {
	pool   *Pool
	leases sync.Map // "instanceID:serviceName" → *Lease
}

// NewRedis creates a Redis service type backed by the given pool.
func NewRedis(pool *Pool) *Redis {
	return &Redis{pool: pool}
}

// Artifacts returns a DockerPull artifact for the Redis image.
// The pool manages containers, but the artifact phase still ensures the
// image is pulled before any Acquire call.
func (r *Redis) Artifacts(params ArtifactParams) ([]artifact.Artifact, error) {
	image := redisImage(params.Spec.Config)
	return []artifact.Artifact{{
		Key:      "docker:" + image,
		Resolver: artifact.DockerPull{Image: image},
	}}, nil
}

// Publish acquires a lease from the pool (which allocates a per-test database)
// and returns an endpoint using the shared container's port and unique db number.
func (r *Redis) Publish(ctx context.Context, params PublishParams) (map[string]spec.Endpoint, error) {
	image := redisImage(params.Spec.Config)

	lease, err := r.pool.Acquire(ctx, image)
	if err != nil {
		return nil, fmt.Errorf("redis publish: %w", err)
	}

	// Store the lease for later phases.
	r.leases.Store(leaseKey(params.InstanceID, params.ServiceName), lease)

	// Build endpoints — one per ingress (typically just "default").
	endpoints := make(map[string]spec.Endpoint, len(params.Ingresses))
	for name, ingSpec := range params.Ingresses {
		endpoints[name] = spec.Endpoint{
			HostPort:   fmt.Sprintf("%s:%d", lease.Host, lease.Port),
			Protocol:   ingSpec.Protocol,
			Attributes: map[string]any{},
		}
	}

	// Inject REDIS_URL on all ingresses.
	for name, ep := range endpoints {
		connect.RedisURL.Set(ep.Attributes, fmt.Sprintf("redis://${HOST}:${PORT}/%s", lease.ID))
		endpoints[name] = ep
	}

	return endpoints, nil
}

// Runner returns a runner that blocks on ctx and releases the lease on exit.
// The shared container is managed by the pool — no per-test container start.
func (r *Redis) Runner(params StartParams) run.Runner {
	return run.Func(func(ctx context.Context) error {
		key := leaseKey(params.InstanceID, params.ServiceName)
		v, ok := r.leases.Load(key)
		if !ok {
			return fmt.Errorf("redis runner: no lease for %s", key)
		}
		lease := v.(*Lease)

		// Block until teardown.
		<-ctx.Done()

		// Release the lease (flushes the per-test database).
		r.leases.Delete(key)
		r.pool.Release(lease)

		return ctx.Err()
	})
}

// redisImage returns the configured image or the default.
func redisImage(raw json.RawMessage) string {
	if raw != nil {
		var cfg RedisConfig
		if err := json.Unmarshal(raw, &cfg); err == nil && cfg.Image != "" {
			return cfg.Image
		}
	}
	return redisDefaultImage
}
