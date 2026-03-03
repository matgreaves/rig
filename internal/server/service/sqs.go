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

// SQS implements Type and ArtifactProvider for the "sqs" builtin service type.
// It uses a Pool to share a single ElasticMQ container across environments,
// providing per-test queue isolation.
type SQS struct {
	pool   *Pool
	leases sync.Map // "instanceID:serviceName" → *Lease
}

// NewSQS creates an SQS service type backed by the given pool.
func NewSQS(pool *Pool) *SQS {
	return &SQS{pool: pool}
}

// Artifacts returns a DockerPull artifact for the ElasticMQ image.
func (s *SQS) Artifacts(params ArtifactParams) ([]artifact.Artifact, error) {
	return []artifact.Artifact{{
		Key:      "docker:" + sqsDefaultImage,
		Resolver: artifact.DockerPull{Image: sqsDefaultImage},
	}}, nil
}

// Publish acquires a lease from the pool (which creates a per-test queue)
// and returns an endpoint with SQS connection attributes.
func (s *SQS) Publish(ctx context.Context, params PublishParams) (map[string]spec.Endpoint, error) {
	lease, err := s.pool.Acquire(ctx, sqsDefaultImage)
	if err != nil {
		return nil, fmt.Errorf("sqs publish: %w", err)
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

	// Inject SQS attributes.
	for name, ep := range endpoints {
		connect.SQSEndpoint.Set(ep.Attributes, "http://${HOST}:${PORT}")
		connect.SQSQueueURL.Set(ep.Attributes, lease.ID)
		connect.S3AccessKeyID.Set(ep.Attributes, "rig")
		connect.S3SecretAccessKey.Set(ep.Attributes, "rig")
		endpoints[name] = ep
	}

	return endpoints, nil
}

// Runner returns a runner that blocks on ctx and releases the lease on exit.
func (s *SQS) Runner(params StartParams) run.Runner {
	return run.Func(func(ctx context.Context) error {
		key := leaseKey(params.InstanceID, params.ServiceName)
		v, ok := s.leases.Load(key)
		if !ok {
			return fmt.Errorf("sqs runner: no lease for %s", key)
		}
		lease := v.(*Lease)

		// Block until teardown.
		<-ctx.Done()

		// Release the lease (deletes the per-test queue).
		s.leases.Delete(key)
		s.pool.Release(lease)

		return ctx.Err()
	})
}
