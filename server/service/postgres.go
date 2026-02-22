package service

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strconv"

	"github.com/docker/docker/api/types/container"
	"github.com/matgreaves/rig/server/artifact"
	"github.com/matgreaves/rig/server/dockerutil"
	"github.com/matgreaves/rig/server/ready"
	"github.com/matgreaves/rig/spec"
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
// service type. It translates PostgresConfig into a ContainerConfig and
// delegates all Docker lifecycle to Container.
type Postgres struct{}

// Artifacts returns a DockerPull artifact for the Postgres image.
func (Postgres) Artifacts(params ArtifactParams) ([]artifact.Artifact, error) {
	image := postgresImage(params.Spec.Config)
	return []artifact.Artifact{{
		Key:      "docker:" + image,
		Resolver: artifact.DockerPull{Image: image},
	}}, nil
}

// Publish resolves ingress endpoints and injects standard PG attributes
// (PGHOST, PGPORT, PGDATABASE, PGUSER, PGPASSWORD) onto each endpoint.
func (Postgres) Publish(_ context.Context, params PublishParams) (map[string]spec.Endpoint, error) {
	endpoints, err := PublishLocalEndpoints(params)
	if err != nil {
		return nil, err
	}
	for name, ep := range endpoints {
		if ep.Attributes == nil {
			ep.Attributes = make(map[string]any)
		}
		ep.Attributes["PGHOST"] = ep.Host
		ep.Attributes["PGPORT"] = strconv.Itoa(ep.Port)
		ep.Attributes["PGDATABASE"] = params.ServiceName
		ep.Attributes["PGUSER"] = postgresDefaultUser
		ep.Attributes["PGPASSWORD"] = postgresDefaultPassword
		endpoints[name] = ep
	}
	return endpoints, nil
}

// ReadyCheck returns a checker that runs pg_isready inside the container
// via docker exec. This is more reliable than a TCP dial — the postgres
// entrypoint's initdb→restart cycle can make the port reachable before
// postgres is actually accepting connections.
func (Postgres) ReadyCheck(params ReadyCheckParams) ready.Checker {
	return &pgReadyCheck{
		containerName: ContainerName(params.InstanceID, params.ServiceName),
		dbName:        params.ServiceName,
	}
}

// pgReadyCheck runs pg_isready inside the postgres container.
type pgReadyCheck struct {
	containerName string
	dbName        string
}

func (c *pgReadyCheck) Check(ctx context.Context, host string, port int) error {
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

// Runner builds a ContainerConfig from the Postgres defaults and delegates
// to Container.Runner.
func (Postgres) Runner(params StartParams) run.Runner {
	image := postgresImage(params.Spec.Config)
	containerCfg := ContainerConfig{
		Image: image,
		Env: map[string]string{
			"POSTGRES_DB":       params.ServiceName,
			"POSTGRES_USER":     postgresDefaultUser,
			"POSTGRES_PASSWORD": postgresDefaultPassword,
		},
	}
	cfgJSON, err := json.Marshal(containerCfg)
	if err != nil {
		return run.Func(func(context.Context) error {
			return fmt.Errorf("service %q: marshal container config: %w", params.ServiceName, err)
		})
	}
	params.Spec.Config = cfgJSON
	return Container{}.Runner(params)
}

// sqlHookConfig is the Config payload for "sql" hooks.
type sqlHookConfig struct {
	Statements []string `json:"statements"`
}

// Init handles server-side hooks for the Postgres service type.
// Supports "sql" (runs each statement via psql) and "exec" (runs an arbitrary
// command inside the container).
func (Postgres) Init(ctx context.Context, params InitParams) error {
	switch params.Hook.Type {
	case "sql":
		return postgresInitSQL(ctx, params)
	case "exec":
		return Container{}.Init(ctx, params)
	default:
		return fmt.Errorf("postgres: unsupported hook type %q", params.Hook.Type)
	}
}

func postgresInitSQL(ctx context.Context, params InitParams) error {
	var cfg sqlHookConfig
	if err := json.Unmarshal(params.Hook.Config, &cfg); err != nil {
		return fmt.Errorf("postgres: invalid sql hook config: %w", err)
	}
	if len(cfg.Statements) == 0 {
		return nil
	}

	containerName := ContainerName(params.InstanceID, params.ServiceName)

	for _, stmt := range cfg.Statements {
		cmd := []string{"psql", "-h", "localhost", "-U", postgresDefaultUser, "-d", params.ServiceName, "-v", "ON_ERROR_STOP=1", "-c", stmt}
		if err := ExecInContainer(ctx, containerName, cmd, params.Stdout, params.Stderr); err != nil {
			return fmt.Errorf("postgres init: statement %q: %w", stmt, err)
		}
	}

	return nil
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
