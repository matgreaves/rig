package service

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/pkg/stdcopy"
	"github.com/docker/go-connections/nat"
	"github.com/matgreaves/rig/internal/server/artifact"
	"github.com/matgreaves/rig/internal/server/dockerutil"
	"github.com/matgreaves/rig/internal/spec"
	"github.com/matgreaves/run"
	"github.com/matgreaves/run/onexit"
)

const (
	// containerTempPath is the fixed mount point for the service's temp dir inside a container.
	containerTempPath = "/rig/temp"
	// containerEnvPath is the fixed mount point for the environment dir inside a container.
	containerEnvPath = "/rig/env"
)

// ContainerConfig is the type-specific config for "container" services.
type ContainerConfig struct {
	// Image is the Docker image reference (e.g. "postgres:16").
	Image string `json:"image"`

	// Cmd overrides the container's default command.
	Cmd []string `json:"cmd,omitempty"`

	// Env sets additional environment variables on the container.
	// These are merged with the standard RIG_* wiring env vars.
	Env map[string]string `json:"env,omitempty"`
}

// ContainerName returns the Docker container name for a service instance.
func ContainerName(instanceID, serviceName string) string {
	return fmt.Sprintf("rig-%s-%s", instanceID, serviceName)
}

// Container implements Type for the "container" service type.
// It runs a Docker container with host-mapped ports.
type Container struct{}

// ExecHookConfig is the Config payload for "exec" hooks.
type ExecHookConfig struct {
	Command []string `json:"command"`
}

// ExecInContainer runs a command inside a running container via docker exec.
// Output is written to stdout/stderr. Returns an error if the command exits
// with a non-zero status.
func ExecInContainer(ctx context.Context, containerName string, cmd []string, stdout, stderr io.Writer) error {
	cli, err := dockerutil.Client()
	if err != nil {
		return fmt.Errorf("exec: docker client: %w", err)
	}

	exec, err := cli.ContainerExecCreate(ctx, containerName, container.ExecOptions{
		Cmd:          cmd,
		AttachStdout: true,
		AttachStderr: true,
	})
	if err != nil {
		return fmt.Errorf("exec create: %w", err)
	}

	resp, err := cli.ContainerExecAttach(ctx, exec.ID, container.ExecAttachOptions{})
	if err != nil {
		return fmt.Errorf("exec attach: %w", err)
	}

	_, err = stdcopy.StdCopy(stdout, stderr, resp.Reader)
	resp.Close()
	if err != nil {
		return fmt.Errorf("exec read output: %w", err)
	}

	inspect, err := cli.ContainerExecInspect(ctx, exec.ID)
	if err != nil {
		return fmt.Errorf("exec inspect: %w", err)
	}
	if inspect.ExitCode != 0 {
		return fmt.Errorf("exec %v: exit code %d", cmd, inspect.ExitCode)
	}
	return nil
}

// waitForContainer polls until the named Docker container exists and is
// running. This is needed when exec hooks race with container creation —
// for example, a no-ingress service has no health check, so the lifecycle
// can fire init hooks before Container.Runner has created the container.
func waitForContainer(ctx context.Context, containerName string) error {
	cli, err := dockerutil.Client()
	if err != nil {
		return err
	}
	for {
		inspect, err := cli.ContainerInspect(ctx, containerName)
		if err == nil && inspect.State.Running {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
}

// Init handles server-side init hooks for the container service type.
// Supports the "exec" hook type — runs a command inside the running container.
func (Container) Init(ctx context.Context, params InitParams) error {
	if params.Hook.Type != "exec" {
		return fmt.Errorf("container: unsupported hook type %q", params.Hook.Type)
	}

	var cfg ExecHookConfig
	if err := json.Unmarshal(params.Hook.Config, &cfg); err != nil {
		return fmt.Errorf("container init: invalid exec hook config: %w", err)
	}
	if len(cfg.Command) == 0 {
		return fmt.Errorf("container init: exec hook command is empty")
	}

	containerName := ContainerName(params.InstanceID, params.ServiceName)
	if err := waitForContainer(ctx, containerName); err != nil {
		return fmt.Errorf("container init: waiting for container: %w", err)
	}
	return ExecInContainer(ctx, containerName, cfg.Command, params.Stdout, params.Stderr)
}

// Artifacts returns a DockerPull artifact for the configured image.
func (Container) Artifacts(params ArtifactParams) ([]artifact.Artifact, error) {
	var cfg ContainerConfig
	if params.Spec.Config == nil {
		return nil, fmt.Errorf("service %q: missing config", params.ServiceName)
	}
	if err := json.Unmarshal(params.Spec.Config, &cfg); err != nil {
		return nil, fmt.Errorf("service %q: invalid container config: %w", params.ServiceName, err)
	}
	if cfg.Image == "" {
		return nil, fmt.Errorf("service %q: container config missing required \"image\" field", params.ServiceName)
	}
	return []artifact.Artifact{{
		Key:      "docker:" + cfg.Image,
		Resolver: artifact.DockerPull{Image: cfg.Image},
	}}, nil
}

// Publish resolves ingress endpoints using host-allocated ports.
func (Container) Publish(_ context.Context, params PublishParams) (map[string]spec.Endpoint, error) {
	return PublishLocalEndpoints(params)
}

// Runner returns a run.Runner that creates, starts, and manages a Docker
// container. The container is stopped and removed when ctx is cancelled.
func (Container) Runner(params StartParams) run.Runner {
	var cfg ContainerConfig
	if params.Spec.Config != nil {
		if err := json.Unmarshal(params.Spec.Config, &cfg); err != nil {
			return run.Func(func(context.Context) error {
				return fmt.Errorf("service %q: invalid container config: %w", params.ServiceName, err)
			})
		}
	}

	return run.Func(func(ctx context.Context) error {
		cli, err := dockerutil.Client()
		if err != nil {
			return fmt.Errorf("service %q: docker client: %w", params.ServiceName, err)
		}

		// Verify Docker is reachable.
		if _, err := cli.Ping(ctx); err != nil {
			return fmt.Errorf("service %q: cannot connect to Docker daemon (is Docker running?): %w", params.ServiceName, err)
		}

		// Adjust endpoints for the container's network namespace, then
		// rebuild the env vars from scratch via BuildEnv. This avoids
		// reverse-engineering the wiring layer's env var naming convention.
		hostIP := dockerHostIP()
		adjustedIngresses := adjustIngressEndpoints(params.Ingresses, params.Spec.Ingresses)
		adjustedEgresses := adjustEgressEndpoints(params.Egresses, hostIP)
		adjustedEnv := params.BuildEnv(adjustedIngresses, adjustedEgresses)

		// Replace host temp/env dir paths with the fixed container mount points.
		// The host dirs are bind-mounted into the container below.
		adjustedEnv["RIG_TEMP_DIR"] = containerTempPath
		adjustedEnv["RIG_ENV_DIR"] = containerEnvPath
		adjustTempDirsInWiring(adjustedEnv)

		// Merge user-specified env vars (from container config) on top.
		for k, v := range cfg.Env {
			adjustedEnv[k] = v
		}
		env := envMapToSlice(adjustedEnv)

		// Build port bindings: host port → container port.
		portBindings, exposedPorts := buildPortBindings(params.Ingresses, params.Spec.Ingresses)

		containerName := ContainerName(params.InstanceID, params.ServiceName)

		config := &container.Config{
			Image:        cfg.Image,
			Env:          env,
			ExposedPorts: exposedPorts,
		}

		// Expand command and arg templates against the container-adjusted env
		// so that ${RIG_TEMP_DIR}, host addresses, etc. resolve correctly.
		cmd := expandAll(cfg.Cmd, adjustedEnv)
		args := expandAll(params.Args, adjustedEnv)
		switch {
		case len(cmd) > 0 && len(args) > 0:
			config.Cmd = append(cmd, args...)
		case len(cmd) > 0:
			config.Cmd = cmd
		case len(args) > 0:
			config.Cmd = args
		}

		hostConfig := &container.HostConfig{
			PortBindings: portBindings,
			Mounts: []mount.Mount{
				{
					Type:   mount.TypeBind,
					Source: params.TempDir,
					Target: containerTempPath,
				},
				{
					Type:   mount.TypeBind,
					Source: params.EnvDir,
					Target: containerEnvPath,
				},
			},
		}
		// On Linux, ensure host.docker.internal resolves to the host.
		if runtime.GOOS == "linux" {
			hostConfig.ExtraHosts = []string{"host.docker.internal:host-gateway"}
		}

		resp, err := cli.ContainerCreate(ctx, config, hostConfig, nil, nil, containerName)
		if err != nil {
			return fmt.Errorf("service %q: create container: %w", params.ServiceName, err)
		}
		containerID := resp.ID

		// Register backup cleanup with onexit so the container is removed
		// even if rigd is killed (SIGKILL, OOM, CI timeout, etc.).
		cancelOnexit, _ := onexit.OnExitF("docker rm -f %s", containerID)

		// Ensure cleanup: stop + remove on exit.
		defer func() {
			// Use a background context for cleanup — the original ctx may already be cancelled.
			cleanCtx := context.Background()
			timeout := 10 // seconds
			cli.ContainerStop(cleanCtx, containerID, container.StopOptions{Timeout: &timeout})
			cli.ContainerRemove(cleanCtx, containerID, container.RemoveOptions{Force: true})
			// Graceful cleanup succeeded — cancel the onexit backup.
			if cancelOnexit != nil {
				cancelOnexit()
			}
		}()

		if err := cli.ContainerStart(ctx, containerID, container.StartOptions{}); err != nil {
			return fmt.Errorf("service %q: start container: %w", params.ServiceName, err)
		}

		// Stream container logs to the service's stdout/stderr writers.
		logReader, err := cli.ContainerLogs(ctx, containerID, container.LogsOptions{
			ShowStdout: true,
			ShowStderr: true,
			Follow:     true,
		})
		if err != nil {
			return fmt.Errorf("service %q: attach logs: %w", params.ServiceName, err)
		}

		// Copy logs in the background.
		logDone := make(chan struct{})
		go func() {
			defer close(logDone)
			stdcopy.StdCopy(params.Stdout, params.Stderr, logReader)
			logReader.Close()
		}()

		// Wait for the container to exit or the context to be cancelled.
		waitCh, errCh := cli.ContainerWait(ctx, containerID, container.WaitConditionNotRunning)

		select {
		case result := <-waitCh:
			<-logDone // drain remaining logs
			if result.StatusCode != 0 {
				return fmt.Errorf("service %q: container exited with code %d", params.ServiceName, result.StatusCode)
			}
			return nil
		case err := <-errCh:
			<-logDone
			if ctx.Err() != nil {
				// Context cancelled — teardown path. Not an error.
				return ctx.Err()
			}
			return fmt.Errorf("service %q: container wait: %w", params.ServiceName, err)
		case <-ctx.Done():
			<-logDone
			return ctx.Err()
		}
	})
}

// dockerHostIP returns the IP address containers should use to reach the host.
// On macOS (Docker Desktop), host.docker.internal resolves to the host.
// On Linux, we could detect the bridge gateway, but host.docker.internal
// is also supported on modern Docker versions.
func dockerHostIP() string {
	if runtime.GOOS == "darwin" || runtime.GOOS == "windows" {
		return "host.docker.internal"
	}
	// Linux: host.docker.internal works on Docker 20.10+ with
	// --add-host=host.docker.internal:host-gateway, but it's not
	// guaranteed. Default to it and let users override if needed.
	return "host.docker.internal"
}

// adjustIngressEndpoints returns a copy of the ingress endpoints adjusted for
// a container's network namespace:
//   - Host → 0.0.0.0 (must listen on all interfaces for Docker port forwarding)
//   - Port → ContainerPort if set (the port inside the container)
//
// Attributes that carried the original host or port values are updated to
// match the adjusted values so that env vars derived from attributes (e.g.
// PGHOST, PGPORT) are correct inside the container.
func adjustIngressEndpoints(ingresses map[string]spec.Endpoint, specs map[string]spec.IngressSpec) map[string]spec.Endpoint {
	adjusted := make(map[string]spec.Endpoint, len(ingresses))
	for name, ep := range ingresses {
		origHost := ep.Host
		origPort := strconv.Itoa(ep.Port)

		ep.Host = "0.0.0.0"
		if is, ok := specs[name]; ok && is.ContainerPort != 0 {
			ep.Port = is.ContainerPort
		}

		// Rewrite declared address-derived attrs first, then fall back
		// to convention-based adjustAttrs for user-specified attributes.
		ep.Attributes = spec.RewriteAddressAttrs(ep, ep.Host, ep.Port)
		ep.Attributes = adjustAttrs(ep.Attributes, origHost, ep.Host, origPort, strconv.Itoa(ep.Port))
		adjusted[name] = ep
	}
	return adjusted
}

// adjustEgressEndpoints returns a copy of the egress endpoints with host
// addresses replaced so containers can reach host services through Docker's
// bridge network. Attributes that carried 127.0.0.1 are updated to match.
func adjustEgressEndpoints(egresses map[string]spec.Endpoint, hostIP string) map[string]spec.Endpoint {
	adjusted := make(map[string]spec.Endpoint, len(egresses))
	for name, ep := range egresses {
		origHost := ep.Host
		ep.Host = strings.ReplaceAll(ep.Host, "127.0.0.1", hostIP)
		// Rewrite declared address-derived attrs first, then fall back
		// to convention-based adjustAttrs for user-specified attributes.
		ep.Attributes = spec.RewriteAddressAttrs(ep, ep.Host, ep.Port)
		ep.Attributes = adjustAttrs(ep.Attributes, origHost, ep.Host, "", "")
		adjusted[name] = ep
	}
	return adjusted
}

// adjustAttrs returns a copy of attrs with values matching oldHost or oldPort
// replaced by newHost or newPort respectively. If oldPort is empty, port
// replacement is skipped.
func adjustAttrs(attrs map[string]any, oldHost, newHost, oldPort, newPort string) map[string]any {
	if len(attrs) == 0 {
		return attrs
	}
	out := make(map[string]any, len(attrs))
	for k, v := range attrs {
		s := fmt.Sprintf("%v", v)
		switch s {
		case oldHost:
			out[k] = newHost
		case oldPort:
			if oldPort != "" {
				out[k] = newPort
			} else {
				out[k] = v
			}
		default:
			out[k] = v
		}
	}
	return out
}

// envMapToSlice converts a map of env vars to a slice of "KEY=VALUE" strings.
func envMapToSlice(env map[string]string) []string {
	out := make([]string, 0, len(env))
	for k, v := range env {
		out = append(out, k+"="+v)
	}
	return out
}

// expandAll expands ${VAR} references in each string against the env map.
func expandAll(templates []string, env map[string]string) []string {
	if len(templates) == 0 {
		return nil
	}
	out := make([]string, len(templates))
	for i, t := range templates {
		out[i] = expand(t, env)
	}
	return out
}

// expand expands ${VAR} and $VAR references in s against the env map.
func expand(s string, env map[string]string) string {
	return os.Expand(s, func(key string) string {
		return env[key]
	})
}

// adjustTempDirsInWiring updates temp_dir and env_dir inside the RIG_WIRING
// JSON value to use container mount paths instead of host paths.
func adjustTempDirsInWiring(env map[string]string) {
	raw, ok := env["RIG_WIRING"]
	if !ok {
		return
	}
	var wiring map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &wiring); err != nil {
		return
	}
	if b, err := json.Marshal(containerTempPath); err == nil {
		wiring["temp_dir"] = b
	}
	if b, err := json.Marshal(containerEnvPath); err == nil {
		wiring["env_dir"] = b
	}
	if b, err := json.Marshal(wiring); err == nil {
		env["RIG_WIRING"] = string(b)
	}
}

// buildPortBindings creates Docker port bindings from resolved ingresses.
// Each ingress has a host port (from the port allocator) and a container port
// (from the ingress spec). If ContainerPort is 0 (rig-native apps that read
// RIG_DEFAULT_PORT), the host port is used as the container port too — the app
// listens on whatever port rig assigns and Docker maps it through.
func buildPortBindings(ingresses map[string]spec.Endpoint, ingressSpecs map[string]spec.IngressSpec) (nat.PortMap, nat.PortSet) {
	portBindings := make(nat.PortMap)
	exposedPorts := make(nat.PortSet)

	for name, ep := range ingresses {
		if _, ok := ingressSpecs[name]; !ok {
			continue
		}
		containerPort := ingressSpecs[name].ContainerPort
		if containerPort == 0 {
			// Rig-native app: listen on the same port rig allocated.
			containerPort = ep.Port
		}

		proto := "tcp"
		containerPortStr := nat.Port(fmt.Sprintf("%d/%s", containerPort, proto))
		exposedPorts[containerPortStr] = struct{}{}
		portBindings[containerPortStr] = []nat.PortBinding{{
			HostIP:   "127.0.0.1",
			HostPort: strconv.Itoa(ep.Port),
		}}
	}

	return portBindings, exposedPorts
}
