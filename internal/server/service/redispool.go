package service

import (
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/go-connections/nat"
	"github.com/matgreaves/rig/internal/server/dockerutil"
	"github.com/matgreaves/run/onexit"
)

const (
	redisDefaultImage = "redis:7-alpine"
	redisMaxDatabases = 16 // Redis default: databases 0-15
)

// NewRedisPool creates a Pool backed by Redis containers. Each unique
// image key gets one shared container; individual test environments get
// isolated databases within it. The pid is embedded in container names so
// multiple rigd processes never collide.
func NewRedisPool(pid int) *Pool {
	return NewPool(func(key string) Backend {
		return &redisBackend{
			image:         key,
			containerName: redisContainerName(pid, key),
			freeDBs:       makeDBList(),
		}
	}, 2*time.Minute)
}

// redisContainerName builds a deterministic container name from the image.
func redisContainerName(pid int, image string) string {
	safe := strings.NewReplacer(":", "-", "/", "-", ".", "-").Replace(image)
	return fmt.Sprintf("rig-redis-%d-%s", pid, safe)
}

// makeDBList returns a slice of available Redis database numbers [0..15].
func makeDBList() []int {
	dbs := make([]int, redisMaxDatabases)
	for i := range dbs {
		dbs[i] = i
	}
	return dbs
}

// redisBackend implements Backend for Redis Docker containers.
type redisBackend struct {
	image         string
	containerName string
	containerID   string
	cancelOnexit  func() error

	mu      sync.Mutex
	freeDBs []int // available database numbers
}

// Start creates and starts a shared Redis container.
func (b *redisBackend) Start(ctx context.Context) (string, int, error) {
	cli, err := dockerutil.Client()
	if err != nil {
		return "", 0, fmt.Errorf("docker client: %w", err)
	}

	// If a same-name container exists (from a previous crash), remove it.
	cli.ContainerRemove(ctx, b.containerName, container.RemoveOptions{Force: true})

	containerPort := nat.Port("6379/tcp")

	config := &container.Config{
		Image:        b.image,
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
		return "", 0, fmt.Errorf("no port binding for 6379")
	}
	port, err := strconv.Atoi(bindings[0].HostPort)
	if err != nil {
		return "", 0, fmt.Errorf("parse host port: %w", err)
	}

	// Wait for redis-cli PING to succeed.
	if err := b.waitReady(ctx); err != nil {
		return "", 0, fmt.Errorf("wait for ready: %w", err)
	}

	return "127.0.0.1", port, nil
}

// Stop stops and removes the Docker container.
func (b *redisBackend) Stop() {
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

// NewLease allocates a database number from the free list.
// Returns the db number (as string) as ID and the container name as Data.
func (b *redisBackend) NewLease(_ context.Context) (string, any, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if len(b.freeDBs) == 0 {
		return "", nil, fmt.Errorf("all %d redis databases in use", redisMaxDatabases)
	}

	db := b.freeDBs[0]
	b.freeDBs = b.freeDBs[1:]

	return strconv.Itoa(db), b.containerName, nil
}

// DropLease flushes the database and returns its number to the free list.
func (b *redisBackend) DropLease(ctx context.Context, id string) {
	// Flush the database.
	b.flushDB(ctx, id)

	// Return the db number to the free list.
	db, err := strconv.Atoi(id)
	if err != nil {
		return
	}
	b.mu.Lock()
	b.freeDBs = append(b.freeDBs, db)
	b.mu.Unlock()
}

// flushDB runs FLUSHDB on the given database number inside the container.
func (b *redisBackend) flushDB(ctx context.Context, dbNum string) {
	cli, err := dockerutil.Client()
	if err != nil {
		return
	}

	cmd := []string{"redis-cli", "-n", dbNum, "FLUSHDB"}
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
	io.Copy(io.Discard, resp.Reader)
	resp.Close()
}

// waitReady polls redis-cli PING inside the container until it succeeds.
func (b *redisBackend) waitReady(ctx context.Context) error {
	cli, err := dockerutil.Client()
	if err != nil {
		return err
	}

	deadline := time.After(60 * time.Second)
	for {
		exec, err := cli.ContainerExecCreate(ctx, b.containerName, container.ExecOptions{
			Cmd:          []string{"redis-cli", "PING"},
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
			return fmt.Errorf("redis PING timed out after 60s")
		case <-time.After(100 * time.Millisecond):
		}
	}
}
