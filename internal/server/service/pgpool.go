package service

import (
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/pkg/stdcopy"
	"github.com/docker/go-connections/nat"
	"github.com/matgreaves/rig/internal/server/dockerutil"
	"github.com/matgreaves/run/onexit"
)

// NewPostgresPool creates a Pool backed by Postgres containers. Each unique
// image key gets one shared container; individual test environments get
// isolated databases within it. The pid is embedded in container names so
// multiple rigd processes never collide.
func NewPostgresPool(pid int) *Pool {
	return NewPool(func(key string) Backend {
		return &pgBackend{
			image:         key,
			containerName: pgContainerName(pid, key),
		}
	}, 2*time.Minute)
}

// pgContainerName builds a deterministic container name from the image.
func pgContainerName(pid int, image string) string {
	safe := strings.NewReplacer(":", "-", "/", "-", ".", "-").Replace(image)
	return fmt.Sprintf("rig-pgpool-%d-%s", pid, safe)
}

// pgBackend implements Backend for Postgres Docker containers.
type pgBackend struct {
	image         string
	containerName string
	containerID   string
	dbCounter     atomic.Int64
	cancelOnexit  func() error
}

// Start creates and starts a shared Postgres container.
func (b *pgBackend) Start(ctx context.Context) (string, int, error) {
	cli, err := dockerutil.Client()
	if err != nil {
		return "", 0, fmt.Errorf("docker client: %w", err)
	}

	// If a same-name container exists (from a previous crash), remove it.
	cli.ContainerRemove(ctx, b.containerName, container.RemoveOptions{Force: true})

	containerPort := nat.Port("5432/tcp")

	config := &container.Config{
		Image: b.image,
		Env: []string{
			"POSTGRES_USER=" + postgresDefaultUser,
			"POSTGRES_PASSWORD=" + postgresDefaultPassword,
			"POSTGRES_DB=postgres",
		},
		Cmd:          []string{"-c", "max_connections=500"},
		ExposedPorts: nat.PortSet{containerPort: {}},
	}

	hostConfig := &container.HostConfig{
		PortBindings: nat.PortMap{
			containerPort: []nat.PortBinding{{
				HostIP:   "127.0.0.1",
				HostPort: "", // Docker auto-assigns
			}},
		},
	}

	resp, err := cli.ContainerCreate(ctx, config, hostConfig, nil, nil, b.containerName)
	if err != nil {
		return "", 0, fmt.Errorf("create container: %w", err)
	}
	b.containerID = resp.ID

	// Register onexit cleanup.
	cancelOnexit, _ := onexit.OnExitF("docker rm -f %s", b.containerID)
	b.cancelOnexit = cancelOnexit

	if err := cli.ContainerStart(ctx, b.containerID, container.StartOptions{}); err != nil {
		return "", 0, fmt.Errorf("start container: %w", err)
	}

	// Read back the mapped host port.
	inspect, err := cli.ContainerInspect(ctx, b.containerID)
	if err != nil {
		return "", 0, fmt.Errorf("inspect container: %w", err)
	}

	bindings, ok := inspect.NetworkSettings.Ports[containerPort]
	if !ok || len(bindings) == 0 {
		return "", 0, fmt.Errorf("no port binding for 5432")
	}
	port, err := strconv.Atoi(bindings[0].HostPort)
	if err != nil {
		return "", 0, fmt.Errorf("parse host port: %w", err)
	}

	// Wait for pg_isready.
	if err := b.waitReady(ctx); err != nil {
		return "", 0, fmt.Errorf("wait for ready: %w", err)
	}

	// Orphan cleanup: drop any rig_* databases from a previous crash.
	b.cleanOrphanDatabases(ctx)

	return "127.0.0.1", port, nil
}

// Stop stops and removes the Docker container.
func (b *pgBackend) Stop() {
	if b.containerID == "" {
		return
	}

	cli, err := dockerutil.Client()
	if err != nil {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	timeout := 10
	cli.ContainerStop(ctx, b.containerID, container.StopOptions{Timeout: &timeout})
	cli.ContainerRemove(ctx, b.containerID, container.RemoveOptions{Force: true})

	if b.cancelOnexit != nil {
		b.cancelOnexit()
	}
}

// NewLease allocates a new per-test database in the shared container.
// Returns the database name as ID and the container name as Data.
func (b *pgBackend) NewLease(ctx context.Context) (string, any, error) {
	dbNum := b.dbCounter.Add(1)
	dbName := fmt.Sprintf("rig_%d", dbNum)

	createCmd := []string{
		"psql", "-h", "localhost", "-U", postgresDefaultUser,
		"-v", "ON_ERROR_STOP=1",
		"-c", fmt.Sprintf("CREATE DATABASE %s", dbName),
	}
	if err := ExecInContainer(ctx, b.containerName, createCmd, io.Discard, io.Discard); err != nil {
		return "", nil, fmt.Errorf("create database %s: %w", dbName, err)
	}

	return dbName, b.containerName, nil
}

// DropLease drops the per-test database. Best-effort — errors are ignored.
func (b *pgBackend) DropLease(ctx context.Context, id string) {
	cli, err := dockerutil.Client()
	if err != nil {
		return
	}

	// Terminate any remaining connections to the database before dropping.
	terminateCmd := []string{
		"psql", "-h", "localhost", "-U", postgresDefaultUser,
		"-c", fmt.Sprintf("SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = '%s' AND pid <> pg_backend_pid()", id),
	}
	exec, err := cli.ContainerExecCreate(ctx, b.containerName, container.ExecOptions{
		Cmd:          terminateCmd,
		AttachStdout: true,
		AttachStderr: true,
	})
	if err == nil {
		resp, err := cli.ContainerExecAttach(ctx, exec.ID, container.ExecAttachOptions{})
		if err == nil {
			io.Copy(io.Discard, resp.Reader)
			resp.Close()
		}
	}

	dropCmd := []string{
		"psql", "-h", "localhost", "-U", postgresDefaultUser,
		"-c", fmt.Sprintf("DROP DATABASE IF EXISTS %s", id),
	}
	exec, err = cli.ContainerExecCreate(ctx, b.containerName, container.ExecOptions{
		Cmd:          dropCmd,
		AttachStdout: true,
		AttachStderr: true,
	})
	if err != nil {
		return
	}
	resp, err := cli.ContainerExecAttach(ctx, exec.ID, container.ExecAttachOptions{})
	if err != nil {
		return
	}
	io.Copy(io.Discard, resp.Reader)
	resp.Close()
}

// waitReady polls pg_isready inside the container until it succeeds or ctx is cancelled.
func (b *pgBackend) waitReady(ctx context.Context) error {
	cli, err := dockerutil.Client()
	if err != nil {
		return err
	}

	deadline := time.After(120 * time.Second)
	for {
		exec, err := cli.ContainerExecCreate(ctx, b.containerName, container.ExecOptions{
			Cmd:          []string{"pg_isready", "-h", "localhost", "-U", postgresDefaultUser},
			AttachStdout: true,
			AttachStderr: true,
		})
		if err == nil {
			resp, err := cli.ContainerExecAttach(ctx, exec.ID, container.ExecAttachOptions{})
			if err == nil {
				io.Copy(io.Discard, resp.Reader)
				resp.Close()
				inspect, err := cli.ContainerExecInspect(ctx, exec.ID)
				if err == nil && inspect.ExitCode == 0 {
					return nil
				}
			}
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline:
			return fmt.Errorf("pg_isready timed out after 120s")
		case <-time.After(200 * time.Millisecond):
		}
	}
}

// cleanOrphanDatabases drops any rig_* databases left over from previous crashes.
func (b *pgBackend) cleanOrphanDatabases(ctx context.Context) {
	cli, err := dockerutil.Client()
	if err != nil {
		return
	}

	// List rig_* databases.
	cmd := []string{
		"psql", "-h", "localhost", "-U", postgresDefaultUser,
		"-t", "-A", "-c",
		"SELECT datname FROM pg_database WHERE datname LIKE 'rig_%'",
	}

	exec, err := cli.ContainerExecCreate(ctx, b.containerName, container.ExecOptions{
		Cmd:          cmd,
		AttachStdout: true,
		AttachStderr: true,
	})
	if err != nil {
		return
	}

	resp, err := cli.ContainerExecAttach(ctx, exec.ID, container.ExecAttachOptions{})
	if err != nil {
		return
	}

	var stdout strings.Builder
	stdcopy.StdCopy(&stdout, io.Discard, resp.Reader)
	resp.Close()

	for _, db := range strings.Split(strings.TrimSpace(stdout.String()), "\n") {
		db = strings.TrimSpace(db)
		if db == "" || !strings.HasPrefix(db, "rig_") {
			continue
		}
		dropCmd := []string{
			"psql", "-h", "localhost", "-U", postgresDefaultUser,
			"-c", fmt.Sprintf("DROP DATABASE IF EXISTS %s", db),
		}
		e, err := cli.ContainerExecCreate(ctx, b.containerName, container.ExecOptions{
			Cmd:          dropCmd,
			AttachStdout: true,
			AttachStderr: true,
		})
		if err != nil {
			continue
		}
		r, err := cli.ContainerExecAttach(ctx, e.ID, container.ExecAttachOptions{})
		if err != nil {
			continue
		}
		io.Copy(io.Discard, r.Reader)
		r.Close()
	}
}
