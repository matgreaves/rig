package service

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sync"

	"github.com/docker/docker/api/types/container"
	"github.com/matgreaves/rig/connect"
	"github.com/matgreaves/rig/internal/server/artifact"
	"github.com/matgreaves/rig/internal/server/dockerutil"
	"github.com/matgreaves/rig/internal/server/ready"
	"github.com/matgreaves/rig/internal/spec"
	"github.com/matgreaves/run"
)

const (
	postgresDefaultImage    = "postgres:16-alpine"
	postgresDefaultUser     = "postgres"
	postgresDefaultPassword = "postgres"
)

// PostgresConfig is the type-specific config for "postgres" services.
type PostgresConfig struct {
	// Image overrides the default Postgres Docker image.
	Image string `json:"image,omitempty"`
}

// Postgres implements Type and ArtifactProvider for the "postgres" builtin
// service type. It uses a Pool to share containers across environments,
// providing per-test database isolation.
type Postgres struct {
	pool   *Pool
	leases sync.Map // "instanceID:serviceName" → *Lease
}

// NewPostgres creates a Postgres service type backed by the given pool.
func NewPostgres(pool *Pool) *Postgres {
	return &Postgres{pool: pool}
}

// leaseKey returns the map key for storing/retrieving a lease.
func leaseKey(instanceID, serviceName string) string {
	return instanceID + ":" + serviceName
}

// Artifacts returns a DockerPull artifact for the Postgres image.
// The pool manages containers, but the artifact phase still ensures the
// image is pulled before any Acquire call.
func (p *Postgres) Artifacts(params ArtifactParams) ([]artifact.Artifact, error) {
	image := postgresImage(params.Spec.Config)
	return []artifact.Artifact{{
		Key:      "docker:" + image,
		Resolver: artifact.DockerPull{Image: image},
	}}, nil
}

// Publish acquires a lease from the pool (which creates the per-test database)
// and returns an endpoint using the shared container's port and unique DB name.
func (p *Postgres) Publish(ctx context.Context, params PublishParams) (map[string]spec.Endpoint, error) {
	image := postgresImage(params.Spec.Config)

	lease, err := p.pool.Acquire(ctx, image)
	if err != nil {
		return nil, fmt.Errorf("postgres publish: %w", err)
	}

	// Store the lease for later phases.
	p.leases.Store(leaseKey(params.InstanceID, params.ServiceName), lease)

	// Build endpoints — one per ingress (typically just "default").
	endpoints := make(map[string]spec.Endpoint, len(params.Ingresses))
	for name, ingSpec := range params.Ingresses {
		endpoints[name] = spec.Endpoint{
			HostPort:   fmt.Sprintf("%s:%d", lease.Host, lease.Port),
			Protocol:   ingSpec.Protocol,
			Attributes: map[string]any{},
		}
	}

	// Inject standard PG attributes.
	for name, ep := range endpoints {
		connect.PGHost.Set(ep.Attributes, "${HOST}")
		connect.PGPort.Set(ep.Attributes, "${PORT}")
		connect.PGDatabase.Set(ep.Attributes, lease.ID)
		connect.PGUser.Set(ep.Attributes, postgresDefaultUser)
		connect.PGPassword.Set(ep.Attributes, postgresDefaultPassword)
		endpoints[name] = ep
	}

	return endpoints, nil
}

// ReadyCheck returns a checker that runs pg_isready against the shared container.
// Since the container is already healthy from the pool, this should pass quickly.
func (p *Postgres) ReadyCheck(params ReadyCheckParams) ready.Checker {
	// Look up the lease to get the container name.
	key := leaseKey(params.InstanceID, params.ServiceName)
	v, ok := p.leases.Load(key)
	if !ok {
		// Fallback — shouldn't happen in normal flow.
		return &pgReadyCheck{
			containerName: ContainerName(params.InstanceID, params.ServiceName),
			dbName:        params.ServiceName,
		}
	}
	lease := v.(*Lease)
	return &pgReadyCheck{
		containerName: lease.Data.(string),
		dbName:        "postgres", // check against default DB for stability
	}
}

// pgReadyCheck runs pg_isready inside the postgres container.
type pgReadyCheck struct {
	containerName string
	dbName        string
}

func (c *pgReadyCheck) Check(ctx context.Context, addr string) error {
	cli, err := dockerutil.Client()
	if err != nil {
		return fmt.Errorf("pg_isready: docker client: %w", err)
	}

	exec, err := cli.ContainerExecCreate(ctx, c.containerName, container.ExecOptions{
		Cmd:          []string{"pg_isready", "-h", "localhost", "-U", postgresDefaultUser, "-d", c.dbName},
		AttachStdout: true,
		AttachStderr: true,
	})
	if err != nil {
		return fmt.Errorf("pg_isready: exec create: %w", err)
	}

	resp, err := cli.ContainerExecAttach(ctx, exec.ID, container.ExecAttachOptions{})
	if err != nil {
		return fmt.Errorf("pg_isready: exec attach: %w", err)
	}
	io.Copy(io.Discard, resp.Reader)
	resp.Close()

	inspect, err := cli.ContainerExecInspect(ctx, exec.ID)
	if err != nil {
		return fmt.Errorf("pg_isready: exec inspect: %w", err)
	}
	if inspect.ExitCode != 0 {
		return fmt.Errorf("pg_isready: exit code %d (not ready)", inspect.ExitCode)
	}
	return nil
}

// Runner returns a runner that blocks on ctx and releases the lease on exit.
// The shared container is managed by the pool — no per-test container start.
func (p *Postgres) Runner(params StartParams) run.Runner {
	return run.Func(func(ctx context.Context) error {
		key := leaseKey(params.InstanceID, params.ServiceName)
		v, ok := p.leases.Load(key)
		if !ok {
			return fmt.Errorf("postgres runner: no lease for %s", key)
		}
		lease := v.(*Lease)

		// Block until teardown.
		<-ctx.Done()

		// Release the lease (drops the per-test database).
		p.leases.Delete(key)
		p.pool.Release(lease)

		return ctx.Err()
	})
}

// sqlHookConfig is the Config payload for "sql" hooks.
type sqlHookConfig struct {
	Statements []string `json:"statements"`
}

// Init handles server-side hooks for the Postgres service type.
// Supports "sql" (runs each statement via psql against the per-test DB)
// and "exec" (runs an arbitrary command inside the shared container).
func (p *Postgres) Init(ctx context.Context, params InitParams) error {
	switch params.Hook.Type {
	case "sql":
		return p.initSQL(ctx, params)
	case "exec":
		return p.initExec(ctx, params)
	default:
		return fmt.Errorf("postgres: unsupported hook type %q", params.Hook.Type)
	}
}

func (p *Postgres) initSQL(ctx context.Context, params InitParams) error {
	var cfg sqlHookConfig
	if err := json.Unmarshal(params.Hook.Config, &cfg); err != nil {
		return fmt.Errorf("postgres: invalid sql hook config: %w", err)
	}
	if len(cfg.Statements) == 0 {
		return nil
	}

	key := leaseKey(params.InstanceID, params.ServiceName)
	v, ok := p.leases.Load(key)
	if !ok {
		return fmt.Errorf("postgres init: no lease for %s", key)
	}
	lease := v.(*Lease)

	// The per-test database was already created by the pool's NewLease.
	// Run each statement against it.
	for _, stmt := range cfg.Statements {
		cmd := []string{
			"psql", "-h", "localhost", "-U", postgresDefaultUser,
			"-d", lease.ID,
			"-v", "ON_ERROR_STOP=1",
			"-c", stmt,
		}
		if err := ExecInContainer(ctx, lease.Data.(string), cmd, params.Stdout, params.Stderr); err != nil {
			return fmt.Errorf("postgres init: statement %q: %w", stmt, err)
		}
	}

	return nil
}

func (p *Postgres) initExec(ctx context.Context, params InitParams) error {
	var cfg ExecHookConfig
	if err := json.Unmarshal(params.Hook.Config, &cfg); err != nil {
		return fmt.Errorf("postgres init: invalid exec hook config: %w", err)
	}
	if len(cfg.Command) == 0 {
		return fmt.Errorf("postgres init: exec hook command is empty")
	}

	key := leaseKey(params.InstanceID, params.ServiceName)
	v, ok := p.leases.Load(key)
	if !ok {
		return fmt.Errorf("postgres init exec: no lease for %s", key)
	}
	lease := v.(*Lease)

	return ExecInContainer(ctx, lease.Data.(string), cfg.Command, params.Stdout, params.Stderr)
}

// postgresImage returns the configured image or the default.
func postgresImage(raw json.RawMessage) string {
	if raw != nil {
		var cfg PostgresConfig
		if err := json.Unmarshal(raw, &cfg); err == nil && cfg.Image != "" {
			return cfg.Image
		}
	}
	return postgresDefaultImage
}

