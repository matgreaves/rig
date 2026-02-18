package server

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/matgreaves/rig/server/ready"
	"github.com/matgreaves/rig/server/service"
	"github.com/matgreaves/rig/spec"
	"github.com/matgreaves/run"
)

// serviceContext holds the resolved state for a single service during its lifecycle.
type serviceContext struct {
	name       string
	spec       spec.Service
	svcType    service.Type
	ingresses  map[string]spec.Endpoint // populated after publish
	egresses   map[string]spec.Endpoint // populated after wiring
	tempDir    string
	envDir     string
	log        *EventLog
	envName    string
	instanceID string
}

// serviceLifecycle builds the full lifecycle sequence for a single service.
//
// The structure is:
//
//	Sequence{
//	    publish, waitForEgresses, prestart,
//	    Group{
//	        "runner":    the service process,
//	        "lifecycle": Sequence{ readyCheck, init, markReady, Idle },
//	    },
//	}
//
// The final Group runs the service process and lifecycle continuation in
// parallel. If the process fails, the Group cancels the lifecycle. If the
// lifecycle fails (e.g. ready check timeout), the Group kills the process.
// The lifecycle ends with Idle so the Group stays alive until teardown.
func serviceLifecycle(sc *serviceContext, ports *PortAllocator) run.Runner {
	return run.Sequence{
		publishStep(sc, ports),
		waitForEgressesStep(sc),
		prestartStep(sc),
		runWithLifecycle(sc),
	}
}

// publishStep allocates ports and lets the service type resolve endpoints.
func publishStep(sc *serviceContext, ports *PortAllocator) run.Runner {
	return run.Func(func(ctx context.Context) error {
		n := len(sc.spec.Ingresses)
		if n == 0 {
			return nil
		}

		allocated, err := ports.Allocate(sc.instanceID, n)
		if err != nil {
			return fmt.Errorf("service %q: allocate ports: %w", sc.name, err)
		}

		// Sort ingress names for deterministic port assignment.
		ingressNames := make([]string, 0, n)
		for name := range sc.spec.Ingresses {
			ingressNames = append(ingressNames, name)
		}
		sort.Strings(ingressNames)

		portMap := make(map[string]int, n)
		for i, name := range ingressNames {
			portMap[name] = allocated[i]
		}

		endpoints, err := sc.svcType.Publish(ctx, service.PublishParams{
			ServiceName: sc.name,
			Spec:        sc.spec,
			Ingresses:   sc.spec.Ingresses,
			Ports:       portMap,
		})
		if err != nil {
			return fmt.Errorf("service %q: publish: %w", sc.name, err)
		}

		sc.ingresses = endpoints

		for ingressName, ep := range endpoints {
			epCopy := ep
			sc.log.Publish(Event{
				Type:        EventIngressPublished,
				Environment: sc.envName,
				Service:     sc.name,
				Ingress:     ingressName,
				Endpoint:    &epCopy,
			})
		}

		return nil
	})
}

// waitForEgressesStep blocks until every egress target is READY.
func waitForEgressesStep(sc *serviceContext) run.Runner {
	return run.Func(func(ctx context.Context) error {
		if len(sc.spec.Egresses) == 0 {
			return nil
		}

		sc.egresses = make(map[string]spec.Endpoint, len(sc.spec.Egresses))

		for egressName, egressSpec := range sc.spec.Egresses {
			targetService := egressSpec.Service
			targetIngress := egressSpec.Ingress

			// Wait for the target service to be READY.
			_, err := sc.log.WaitFor(ctx, func(e Event) bool {
				return e.Type == EventServiceReady &&
					e.Environment == sc.envName &&
					e.Service == targetService
			})
			if err != nil {
				return fmt.Errorf("service %q: waiting for egress %q (service %q): %w",
					sc.name, egressName, targetService, err)
			}

			// Find the published ingress endpoint for the target.
			ev, err := sc.log.WaitFor(ctx, func(e Event) bool {
				return e.Type == EventIngressPublished &&
					e.Environment == sc.envName &&
					e.Service == targetService &&
					e.Ingress == targetIngress
			})
			if err != nil {
				return fmt.Errorf("service %q: finding endpoint for egress %q: %w",
					sc.name, egressName, err)
			}

			sc.egresses[egressName] = *ev.Endpoint
		}

		sc.log.Publish(Event{
			Type:        EventWiringResolved,
			Environment: sc.envName,
			Service:     sc.name,
		})

		return nil
	})
}

// prestartStep runs the prestart hook if configured.
func prestartStep(sc *serviceContext) run.Runner {
	return run.Func(func(ctx context.Context) error {
		if sc.spec.Hooks == nil || sc.spec.Hooks.Prestart == nil {
			return nil
		}

		sc.log.Publish(Event{
			Type:        EventServicePrestart,
			Environment: sc.envName,
			Service:     sc.name,
		})

		hook := sc.spec.Hooks.Prestart
		return executeHook(ctx, sc, hook, true)
	})
}

// runWithLifecycle returns a Group that runs the service process alongside
// the lifecycle continuation (ready check → init → mark ready → idle).
// If either side fails, the other is cancelled.
func runWithLifecycle(sc *serviceContext) run.Runner {
	return run.Func(func(ctx context.Context) error {
		sc.log.Publish(Event{
			Type:        EventServiceStarting,
			Environment: sc.envName,
			Service:     sc.name,
		})

		logWriter := &eventLogWriter{
			log:     sc.log,
			envName: sc.envName,
			service: sc.name,
		}

		env := BuildServiceEnv(sc.name, sc.ingresses, sc.egresses, sc.tempDir, sc.envDir)
		args := ExpandTemplates(sc.spec.Args, env)

		runner := sc.svcType.Runner(service.StartParams{
			ServiceName: sc.name,
			Spec:        sc.spec,
			Ingresses:   sc.ingresses,
			Egresses:    sc.egresses,
			Env:         env,
			Args:        args,
			TempDir:     sc.tempDir,
			EnvDir:      sc.envDir,
			Stdout:      &teeWriter{logWriter, "stdout"},
			Stderr:      &teeWriter{logWriter, "stderr"},
		})

		// Build the lifecycle continuation that runs alongside the service.
		lifecycle := run.Sequence{
			readyCheckRunner(sc),
			emitEvent(sc, EventServiceHealthy),
			initRunner(sc),
			emitEvent(sc, EventServiceReady),
			run.Idle,
		}

		// Run the service and lifecycle in parallel.
		return run.Group{
			"runner":    runner,
			"lifecycle": lifecycle,
		}.Run(ctx)
	})
}

// readyCheckRunner polls all ingresses until they're ready.
func readyCheckRunner(sc *serviceContext) run.Runner {
	return run.Func(func(ctx context.Context) error {
		for ingressName, ep := range sc.ingresses {
			var readySpec *spec.ReadySpec
			if ingSpec, ok := sc.spec.Ingresses[ingressName]; ok {
				readySpec = ingSpec.Ready
			}
			checker := ready.ForEndpoint(ep, readySpec)
			if err := ready.Poll(ctx, ep.Host, ep.Port, checker, readySpec); err != nil {
				return fmt.Errorf("service %q, ingress %q: %w", sc.name, ingressName, err)
			}
		}
		return nil
	})
}

// initRunner runs the init hook if configured.
func initRunner(sc *serviceContext) run.Runner {
	return run.Func(func(ctx context.Context) error {
		if sc.spec.Hooks == nil || sc.spec.Hooks.Init == nil {
			return nil
		}

		sc.log.Publish(Event{
			Type:        EventServiceInit,
			Environment: sc.envName,
			Service:     sc.name,
		})

		hook := sc.spec.Hooks.Init
		return executeHook(ctx, sc, hook, false)
	})
}

// emitEvent returns a Runner that publishes a lifecycle event.
func emitEvent(sc *serviceContext, eventType EventType) run.Runner {
	return run.Func(func(ctx context.Context) error {
		sc.log.Publish(Event{
			Type:        eventType,
			Environment: sc.envName,
			Service:     sc.name,
		})
		return nil
	})
}

// executeHook dispatches a hook to the appropriate executor.
// For Phase 3, only client_func hooks are supported (via callback events).
func executeHook(ctx context.Context, sc *serviceContext, hook *spec.HookSpec, includeEgresses bool) error {
	if hook.Type != "client_func" || hook.ClientFunc == nil {
		return fmt.Errorf("unsupported hook type %q (only client_func supported in this phase)", hook.Type)
	}

	wiring := &WiringContext{
		Ingresses: sc.ingresses,
		TempDir:   sc.tempDir,
		EnvDir:    sc.envDir,
	}
	if includeEgresses {
		wiring.Egresses = sc.egresses
	}

	requestID := fmt.Sprintf("%s-%s-%s", sc.instanceID, sc.name, hook.ClientFunc.Name)

	sc.log.Publish(Event{
		Type:        EventCallbackRequest,
		Environment: sc.envName,
		Service:     sc.name,
		Callback: &CallbackRequest{
			RequestID: requestID,
			Name:      hook.ClientFunc.Name,
			Type:      "hook",
			Wiring:    wiring,
		},
	})

	ev, err := sc.log.WaitFor(ctx, func(e Event) bool {
		return e.Type == EventCallbackResponse &&
			e.Result != nil &&
			e.Result.RequestID == requestID
	})
	if err != nil {
		return fmt.Errorf("hook %q: waiting for callback response: %w", hook.ClientFunc.Name, err)
	}

	if ev.Result.Error != "" {
		return fmt.Errorf("hook %q: callback error: %s", hook.ClientFunc.Name, ev.Result.Error)
	}

	return nil
}

// teeWriter writes service output to the event log.
type teeWriter struct {
	logWriter *eventLogWriter
	stream    string // "stdout" or "stderr"
}

func (w *teeWriter) Write(p []byte) (int, error) {
	w.logWriter.log.Publish(Event{
		Type:        EventServiceLog,
		Environment: w.logWriter.envName,
		Service:     w.logWriter.service,
		Log: &LogEntry{
			Stream: w.stream,
			Data:   string(p),
		},
	})
	return len(p), nil
}

// eventLogWriter provides context for writing to the event log.
type eventLogWriter struct {
	log     *EventLog
	envName string
	service string
}

// createTempDirs creates temp directories for an environment instance.
func createTempDirs(envDir string, serviceNames []string) error {
	if err := os.MkdirAll(envDir, 0o755); err != nil {
		return fmt.Errorf("create env dir: %w", err)
	}
	for _, name := range serviceNames {
		dir := filepath.Join(envDir, name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("create temp dir for %q: %w", name, err)
		}
	}
	return nil
}
