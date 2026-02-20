package server

import (
	"context"
	"crypto/rand"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/matgreaves/rig/server/artifact"
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
	CacheDir string // artifact cache directory (default ~/.rig/cache/)
}

// Orchestrate builds a run.Runner that manages the full lifecycle of the
// given environment. The runner is a run.Sequence of two phases:
//
//  1. Artifact phase: resolves all required artifacts (compiled binaries, etc.)
//     in parallel, using a content-addressable cache.
//  2. Service phase: starts all services concurrently (run.Group). Dependency
//     ordering emerges from services blocking on the event log until their
//     egress targets are ready.
//
// The results map is safe to share because run.Sequence guarantees sequential
// execution: the artifact phase writes to it, the service phase reads from it.
func (o *Orchestrator) Orchestrate(env *spec.Environment) (run.Runner, string, error) {
	// Generate instance ID.
	instanceID := generateID()

	// Create temp directories.
	envDir := filepath.Join(o.tempBase(), instanceID)
	serviceNames := sortedServiceNames(env.Services)
	if err := createTempDirs(envDir, serviceNames); err != nil {
		return nil, "", fmt.Errorf("create temp dirs: %w", err)
	}

	// Collect artifacts from all ArtifactProvider service types.
	var allArtifacts []artifact.Artifact
	for _, name := range serviceNames {
		svc := env.Services[name]
		svcType, err := o.Registry.Get(svc.Type)
		if err != nil {
			return nil, "", fmt.Errorf("service %q: %w", name, err)
		}
		if provider, ok := svcType.(service.ArtifactProvider); ok {
			arts, err := provider.Artifacts(service.ArtifactParams{
				ServiceName: name,
				Spec:        svc,
			})
			if err != nil {
				return nil, "", fmt.Errorf("service %q: artifacts: %w", name, err)
			}
			allArtifacts = append(allArtifacts, arts...)
		}
	}

	// results is populated by artifactPhase and read by servicePhase.
	// Safe because run.Sequence is sequential.
	results := make(map[string]artifact.Output)

	cache := artifact.NewCache(o.cacheDir())

	emit := func(kind artifact.EventKind, key string, err error) {
		evt := Event{
			Environment: env.Name,
			Artifact:    key,
		}
		switch kind {
		case artifact.EventStarted:
			evt.Type = EventArtifactStarted
		case artifact.EventCompleted:
			evt.Type = EventArtifactCompleted
		case artifact.EventCached:
			evt.Type = EventArtifactCached
		case artifact.EventFailed:
			evt.Type = EventArtifactFailed
			if err != nil {
				evt.Error = err.Error()
			}
		}
		o.Log.Publish(evt)
	}

	artifactPhase := run.Func(func(ctx context.Context) error {
		resolved, err := artifact.Resolve(ctx, allArtifacts, cache, emit)
		if err != nil {
			return fmt.Errorf("artifact phase: %w", err)
		}
		for k, v := range resolved {
			results[k] = v
		}
		return nil
	})

	servicePhase := run.Func(func(ctx context.Context) error {
		group := run.Group{}
		for _, name := range serviceNames {
			svc := env.Services[name]
			svcType, err := o.Registry.Get(svc.Type)
			if err != nil {
				return fmt.Errorf("service %q: %w", name, err)
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
				artifacts:  results,
				observe:    env.Observe,
			}

			group[name] = serviceLifecycle(sc, o.Ports)
		}
		return group.Run(ctx)
	})

	return run.Sequence{artifactPhase, servicePhase}, instanceID, nil
}

func (o *Orchestrator) tempBase() string {
	if o.TempBase != "" {
		return o.TempBase
	}
	return filepath.Join(os.TempDir(), "rig")
}

func (o *Orchestrator) cacheDir() string {
	if o.CacheDir != "" {
		return o.CacheDir
	}
	return filepath.Join(DefaultRigDir(), "cache")
}

// DefaultRigDir returns the base rig directory. It checks RIG_DIR first,
// then falls back to ~/.rig, then $TMPDIR/rig.
func DefaultRigDir() string {
	if dir := os.Getenv("RIG_DIR"); dir != "" {
		return dir
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(os.TempDir(), "rig")
	}
	return filepath.Join(home, ".rig")
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
