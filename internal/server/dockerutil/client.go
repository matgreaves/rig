// Package dockerutil provides a shared Docker client with automatic socket
// discovery for common Docker Desktop installations.
package dockerutil

import (
	"os"
	"path/filepath"
	"sync"

	"github.com/docker/docker/client"
)

var (
	sharedClient *client.Client
	clientOnce   sync.Once
	clientErr    error
)

// Client returns a process-wide shared Docker client. The client is
// thread-safe and reuses connections to the Docker daemon. Callers must
// NOT call Close on the returned client.
func Client() (*client.Client, error) {
	clientOnce.Do(func() {
		sharedClient, clientErr = newClient()
	})
	return sharedClient, clientErr
}

// newClient creates a Docker client. If DOCKER_HOST is not set, it probes
// common socket paths so the SDK finds Docker Desktop on macOS without
// extra configuration.
func newClient() (*client.Client, error) {
	opts := []client.Opt{client.FromEnv, client.WithAPIVersionNegotiation()}

	// If DOCKER_HOST is not set, probe common socket paths and pass the
	// host directly via client options (not os.Setenv, which is not
	// concurrent-safe).
	if os.Getenv("DOCKER_HOST") == "" {
		if sock := findSocket(); sock != "" {
			opts = append(opts, client.WithHost("unix://"+sock))
		}
	}

	return client.NewClientWithOpts(opts...)
}

// findSocket returns the first existing Docker socket path, or "".
func findSocket() string {
	home, err := os.UserHomeDir()
	if err != nil {
		home = ""
	}

	candidates := []string{
		"/var/run/docker.sock",
	}
	if home != "" {
		candidates = append(candidates,
			filepath.Join(home, ".docker", "run", "docker.sock"),
			filepath.Join(home, ".colima", "default", "docker.sock"),
		)
	}

	for _, p := range candidates {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}
