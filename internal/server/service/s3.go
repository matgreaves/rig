package service

import (
	"context"
	"fmt"
	"sync"

	"github.com/matgreaves/rig/connect"
	"github.com/matgreaves/rig/internal/server/artifact"
	"github.com/matgreaves/rig/internal/spec"
	"github.com/matgreaves/run"
)

// S3 implements Type and ArtifactProvider for the "s3" builtin service type.
// It uses a Pool to share a single SeaweedFS container across environments,
// providing per-test bucket isolation.
type S3 struct {
	pool   *Pool
	leases sync.Map // "instanceID:serviceName" → *Lease
}

// NewS3 creates an S3 service type backed by the given pool.
func NewS3(pool *Pool) *S3 {
	return &S3{pool: pool}
}

// Artifacts returns a DockerPull artifact for the SeaweedFS image.
func (s *S3) Artifacts(params ArtifactParams) ([]artifact.Artifact, error) {
	return []artifact.Artifact{{
		Key:      "docker:" + s3DefaultImage,
		Resolver: artifact.DockerPull{Image: s3DefaultImage},
	}}, nil
}

// Publish acquires a lease from the pool (which creates a per-test bucket)
// and returns an endpoint with S3 connection attributes.
func (s *S3) Publish(ctx context.Context, params PublishParams) (map[string]spec.Endpoint, error) {
	lease, err := s.pool.Acquire(ctx, s3DefaultImage)
	if err != nil {
		return nil, fmt.Errorf("s3 publish: %w", err)
	}

	// Store the lease for later phases.
	s.leases.Store(leaseKey(params.InstanceID, params.ServiceName), lease)

	// Build endpoints — one per ingress (typically just "default").
	endpoints := make(map[string]spec.Endpoint, len(params.Ingresses))
	for name, ingSpec := range params.Ingresses {
		endpoints[name] = spec.Endpoint{
			HostPort:   fmt.Sprintf("%s:%d", lease.Host, lease.Port),
			Protocol:   ingSpec.Protocol,
			Attributes: map[string]any{},
		}
	}

	// Inject S3 attributes.
	for name, ep := range endpoints {
		connect.S3Endpoint.Set(ep.Attributes, "http://${HOST}:${PORT}")
		connect.S3Bucket.Set(ep.Attributes, lease.ID)
		connect.S3AccessKeyID.Set(ep.Attributes, "rig")
		connect.S3SecretAccessKey.Set(ep.Attributes, "rig")
		endpoints[name] = ep
	}

	return endpoints, nil
}

// Runner returns a runner that blocks on ctx and releases the lease on exit.
func (s *S3) Runner(params StartParams) run.Runner {
	return run.Func(func(ctx context.Context) error {
		key := leaseKey(params.InstanceID, params.ServiceName)
		v, ok := s.leases.Load(key)
		if !ok {
			return fmt.Errorf("s3 runner: no lease for %s", key)
		}
		lease := v.(*Lease)

		// Block until teardown.
		<-ctx.Done()

		// Release the lease (empties and deletes the per-test bucket).
		s.leases.Delete(key)
		s.pool.Release(lease)

		return ctx.Err()
	})
}
