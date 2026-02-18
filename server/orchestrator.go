package server

import (
	"crypto/rand"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/matgreaves/rig/server/service"
	"github.com/matgreaves/rig/spec"
	"github.com/matgreaves/run"
)

// Orchestrator coordinates the lifecycle of all services in an environment.
type Orchestrator struct {
	Ports    *PortAllocator
	Registry *service.Registry
	Log      *EventLog
	TempBase string // base directory for temp dirs (default os.TempDir()/rig)
}

// Orchestrate builds a run.Runner that manages the full lifecycle of the
// given environment. The runner starts all services concurrently using
// run.Group. Dependency ordering emerges from services blocking on the
// event log until their egress targets are ready.
func (o *Orchestrator) Orchestrate(env *spec.Environment) (run.Runner, string, error) {
	// Generate instance ID.
	instanceID := generateID()

	// Create temp directories.
	envDir := filepath.Join(o.tempBase(), instanceID)
	serviceNames := sortedServiceNames(env.Services)
	if err := createTempDirs(envDir, serviceNames); err != nil {
		return nil, "", fmt.Errorf("create temp dirs: %w", err)
	}

	// Build a run.Group with one lifecycle sequence per service.
	group := run.Group{}

	for _, name := range serviceNames {
		svc := env.Services[name]

		svcType, err := o.Registry.Get(svc.Type)
		if err != nil {
			return nil, "", fmt.Errorf("service %q: %w", name, err)
		}

		sc := &serviceContext{
			name:       name,
			spec:       svc,
			svcType:    svcType,
			tempDir:    filepath.Join(envDir, name),
			envDir:     envDir,
			log:        o.Log,
			envName:    env.Name,
			instanceID: instanceID,
		}

		group[name] = serviceLifecycle(sc, o.Ports)
	}

	return group, instanceID, nil
}

func (o *Orchestrator) tempBase() string {
	if o.TempBase != "" {
		return o.TempBase
	}
	return filepath.Join(os.TempDir(), "rig")
}

func sortedServiceNames(services map[string]spec.Service) []string {
	names := make([]string, 0, len(services))
	for name := range services {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func generateID() string {
	b := make([]byte, 8)
	rand.Read(b)
	return fmt.Sprintf("%x", b)
}
