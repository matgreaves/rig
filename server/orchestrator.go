package server

import (
	"context"
	"crypto/rand"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"

	"github.com/matgreaves/rig/server/artifact"
	"github.com/matgreaves/rig/server/service"
	"github.com/matgreaves/rig/spec"
	"github.com/matgreaves/run"
	"github.com/matgreaves/run/onexit"
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
// given environment. The runner executes two phases sequentially:
//
//  1. Artifact phase: resolves all required artifacts (compiled binaries, etc.)
//     in parallel, using a content-addressable cache.
//  2. Service phase: starts all services concurrently. Dependency ordering
//     emerges from services blocking on the event log until their egress
//     targets are ready. On first failure, the server cancels all remaining
//     services and emits environment.failing with the root cause.
//
// If either phase fails, the runner emits environment.failing with the root
// cause before returning. The results map is safe to share because the
// artifact phase completes before the service phase begins.
func (o *Orchestrator) Orchestrate(env *spec.Environment) (run.Runner, string, error) {
	// Generate instance ID.
	instanceID := generateID()

	// Create temp directories and register cleanup with onexit so they're
	// removed even if rigd is killed ungracefully.
	envDir := filepath.Join(o.tempBase(), instanceID)
	serviceNames := sortedServiceNames(env.Services)
	if err := createTempDirs(envDir, serviceNames); err != nil {
		return nil, "", fmt.Errorf("create temp dirs: %w", err)
	}
	cancelTempCleanup, _ := onexit.OnExitF("rm -rf %s", envDir)

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
	// Safe because the two phases run sequentially.
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
			return err
		}
		for k, v := range resolved {
			results[k] = v
		}
		return nil
	})

	// failedService is set by servicePhase to the name of the service
	// that caused the environment to fail. Read by lifecycle wrapper
	// to populate the structured Service field on environment.failing.
	var failedService string

	servicePhase := run.Func(func(ctx context.Context) error {
		ctx, cancel := context.WithCancel(ctx)
		defer cancel()

		type serviceErr struct {
			name string
			err  error
		}

		var wg sync.WaitGroup
		errs := make(chan serviceErr, len(serviceNames))

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

			wg.Add(1)
			go func() {
				defer wg.Done()
				if err := serviceLifecycle(sc, o.Ports).Run(ctx); err != nil {
					errs <- serviceErr{name: sc.name, err: err}
				}
			}()
		}

		// Close errs channel when all goroutines finish.
		go func() {
			wg.Wait()
			close(errs)
		}()

		var cause error
		for e := range errs {
			if cause == nil {
				failedService = e.name
				cause = fmt.Errorf("service %q: %s", e.name, e.err)
				cancel() // tear down all other services
			}
			// Subsequent errors are from services torn down by cancel —
			// only the first (root cause) is reported.
		}
		return cause
	})

	lifecycle := run.Func(func(ctx context.Context) error {
		// Clean up temp dirs when the lifecycle exits, regardless of outcome.
		defer func() {
			os.RemoveAll(envDir)
			if cancelTempCleanup != nil {
				cancelTempCleanup()
			}
		}()

		if err := artifactPhase.Run(ctx); err != nil {
			if ctx.Err() == nil {
				o.Log.Publish(Event{
					Type:        EventEnvironmentFailing,
					Environment: env.Name,
					Error:       err.Error(),
				})
			}
			return err
		}
		if err := servicePhase.Run(ctx); err != nil {
			if ctx.Err() == nil {
				o.Log.Publish(Event{
					Type:        EventEnvironmentFailing,
					Environment: env.Name,
					Service:     failedService,
					Error:       err.Error(),
				})
			}
			return err
		}
		return nil
	})

	return lifecycle, instanceID, nil
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
