package server

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"

	"github.com/matgreaves/rig/internal/server/artifact"
	"github.com/matgreaves/rig/internal/server/proxy"
	"github.com/matgreaves/rig/internal/server/ready"
	"github.com/matgreaves/rig/internal/server/service"
	"github.com/matgreaves/rig/internal/spec"
	"github.com/matgreaves/run"
)

// serviceContext holds the resolved state for a single service during its lifecycle.
type serviceContext struct {
	name       string
	spec       spec.Service
	svcType    service.Type
	ingresses  map[string]spec.Endpoint   // populated after publish
	egresses   map[string]spec.Endpoint   // populated after wiring
	artifacts  map[string]artifact.Output // populated by artifact phase (shared, read-only during service phase)
	tempDir    string
	envDir     string
	log        *EventLog
	envName    string
	instanceID string
	observe    bool                // when true, create proxy forwarders
	forwarders []*proxy.Forwarder // populated during publish/wiring when observing
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
	inner := run.Sequence{
		publishStep(sc, ports),
		waitForEgressesStep(sc, ports),
		prestartStep(sc),
		runWithLifecycle(sc),
	}
	// Wrap to emit stopping/stopped events during teardown.
	// Returns the stripped domain error (no run.Sequence/run.Group noise)
	// so callers never need to strip again.
	return run.Func(func(ctx context.Context) error {
		err := inner.Run(ctx)
		var domainErr string
		if err != nil {
			domainErr = stripRunPrefixes(err.Error())
		}
		if ctx.Err() != nil {
			// Context cancelled — service is stopping due to teardown.
			sc.log.Publish(Event{
				Type:        EventServiceStopping,
				Environment: sc.envName,
				Service:     sc.name,
			})
		} else if err != nil {
			// Service failed — mark as failed before stopped.
			sc.log.Publish(Event{
				Type:        EventServiceFailed,
				Environment: sc.envName,
				Service:     sc.name,
				Error:       domainErr,
			})
		}
		sc.log.Publish(Event{
			Type:        EventServiceStopped,
			Environment: sc.envName,
			Service:     sc.name,
		})
		if err != nil {
			return fmt.Errorf("%s", domainErr)
		}
		return nil
	})
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
			return fmt.Errorf("allocate ports: %w", err)
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
			return fmt.Errorf("publish: %w", err)
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

		// When observing, create one external proxy per ingress so that
		// env.Endpoint() returns a proxy address instead of the real one.
		if sc.observe {
			proxyPorts, err := ports.Allocate(sc.instanceID, len(endpoints))
			if err != nil {
				return fmt.Errorf("allocate external proxy ports: %w", err)
			}
			i := 0
			for ingressName, ep := range endpoints {
				fwd := &proxy.Forwarder{
					ListenPort: proxyPorts[i],
					Target:     ep,
					Source:     "external",
					TargetSvc:  sc.name,
					Ingress:    ingressName,
					Protocol:   string(ep.Protocol),
					Emit:       proxyEmitter(sc),
				}
				sc.forwarders = append(sc.forwarders, fwd)

				proxyEP := fwd.Endpoint()
				sc.log.Publish(Event{
					Type:        EventProxyPublished,
					Environment: sc.envName,
					Service:     sc.name,
					Ingress:     ingressName,
					Endpoint:    &proxyEP,
				})
				i++
			}
		}

		return nil
	})
}

// waitForEgressesStep blocks until every egress target is READY.
func waitForEgressesStep(sc *serviceContext, ports *PortAllocator) run.Runner {
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
				return fmt.Errorf("waiting for egress %q (service %q): %w",
					egressName, targetService, err)
			}

			// Find the published ingress endpoint for the target.
			ev, err := sc.log.WaitFor(ctx, func(e Event) bool {
				return e.Type == EventIngressPublished &&
					e.Environment == sc.envName &&
					e.Service == targetService &&
					e.Ingress == targetIngress
			})
			if err != nil {
				return fmt.Errorf("finding endpoint for egress %q: %w",
					egressName, err)
			}

			realEndpoint := *ev.Endpoint

			if sc.observe {
				// Create an egress proxy so the service talks through the proxy.
				proxyPorts, err := ports.Allocate(sc.instanceID, 1)
				if err != nil {
					return fmt.Errorf("allocate egress proxy port for %q: %w",
						egressName, err)
				}
				fwd := &proxy.Forwarder{
					ListenPort: proxyPorts[0],
					Target:     realEndpoint,
					Source:     sc.name,
					TargetSvc:  targetService,
					Ingress:    targetIngress,
					Protocol:   string(realEndpoint.Protocol),
					Emit:       proxyEmitter(sc),
				}
				sc.forwarders = append(sc.forwarders, fwd)
				sc.egresses[egressName] = fwd.Endpoint()
			} else {
				sc.egresses[egressName] = realEndpoint
			}
		}

		sc.log.Publish(Event{
			Type:        EventWiringResolved,
			Environment: sc.envName,
			Service:     sc.name,
		})

		return nil
	})
}

// prestartStep runs the prestart hooks if configured.
func prestartStep(sc *serviceContext) run.Runner {
	return run.Func(func(ctx context.Context) error {
		if sc.spec.Hooks == nil || len(sc.spec.Hooks.Prestart) == 0 {
			return nil
		}

		sc.log.Publish(Event{
			Type:        EventServicePrestart,
			Environment: sc.envName,
			Service:     sc.name,
		})

		for _, hook := range sc.spec.Hooks.Prestart {
			if err := executeHook(ctx, sc, hook, true); err != nil {
				return err
			}
		}
		return nil
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
			Artifacts:   sc.artifacts,
			Env:         env,
			Args:        args,
			TempDir:     sc.tempDir,
			EnvDir:      sc.envDir,
			InstanceID:  sc.instanceID,
			Stdout:      &teeWriter{logWriter, "stdout"},
			Stderr:      &teeWriter{logWriter, "stderr"},
			BuildEnv: func(ingresses, egresses map[string]spec.Endpoint) map[string]string {
				return BuildServiceEnv(sc.name, ingresses, egresses, sc.tempDir, sc.envDir)
			},
			Callback: func(ctx context.Context, name, callbackType string) error {
				return dispatchCallback(ctx, sc, name, callbackType, true)
			},
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
		group := run.Group{
			"runner":    runner,
			"lifecycle": lifecycle,
		}

		// Start proxy forwarders alongside the service.
		for i, fwd := range sc.forwarders {
			group[fmt.Sprintf("proxy-%d", i)] = fwd.Runner()
		}

		return group.Run(ctx)
	})
}

// readyCheckRunner polls all ingresses until they're ready.
// If the service type implements ReadyChecker, its custom checker is used
// instead of the default protocol-based one.
func readyCheckRunner(sc *serviceContext) run.Runner {
	return run.Func(func(ctx context.Context) error {
		rc, hasCustom := sc.svcType.(service.ReadyChecker)

		for ingressName, ep := range sc.ingresses {
			var readySpec *spec.ReadySpec
			if ingSpec, ok := sc.spec.Ingresses[ingressName]; ok {
				readySpec = ingSpec.Ready
			}

			var checker ready.Checker
			if hasCustom {
				checker = rc.ReadyCheck(service.ReadyCheckParams{
					ServiceName: sc.name,
					InstanceID:  sc.instanceID,
					IngressName: ingressName,
					Endpoint:    ep,
					Spec:        sc.spec,
				})
			} else {
				checker = ready.ForEndpoint(ep, readySpec)
			}

			ingress := ingressName // capture for closure
			onFailure := func(err error) {
				sc.log.Publish(Event{
					Type:        EventHealthCheckFailed,
					Environment: sc.envName,
					Service:     sc.name,
					Ingress:     ingress,
					Error:       err.Error(),
				})
			}
			if err := ready.Poll(ctx, ep.Host, ep.Port, checker, readySpec, onFailure); err != nil {
				return fmt.Errorf("ingress %q: %w", ingressName, err)
			}
		}
		return nil
	})
}

// initRunner runs the init hooks if configured.
func initRunner(sc *serviceContext) run.Runner {
	return run.Func(func(ctx context.Context) error {
		if sc.spec.Hooks == nil || len(sc.spec.Hooks.Init) == 0 {
			return nil
		}

		sc.log.Publish(Event{
			Type:        EventServiceInit,
			Environment: sc.envName,
			Service:     sc.name,
		})

		for _, hook := range sc.spec.Hooks.Init {
			if err := executeHook(ctx, sc, hook, false); err != nil {
				return err
			}
		}
		return nil
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

// dispatchCallback sends a callback request to the client SDK via the event
// log and blocks until the response arrives. This is used both for hooks and
// for client service type start callbacks.
func dispatchCallback(ctx context.Context, sc *serviceContext, name, callbackType string, includeEgresses bool) error {
	wiring := &WiringContext{
		Ingresses: sc.ingresses,
		TempDir:   sc.tempDir,
		EnvDir:    sc.envDir,
	}
	if includeEgresses {
		wiring.Egresses = sc.egresses
	}

	requestID := fmt.Sprintf("%s-%s-%s", sc.instanceID, sc.name, name)

	sc.log.Publish(Event{
		Type:        EventCallbackRequest,
		Environment: sc.envName,
		Service:     sc.name,
		Callback: &CallbackRequest{
			RequestID: requestID,
			Name:      name,
			Type:      callbackType,
			Wiring:    wiring,
		},
	})

	ev, err := sc.log.WaitFor(ctx, func(e Event) bool {
		return e.Type == EventCallbackResponse &&
			e.Result != nil &&
			e.Result.RequestID == requestID
	})
	if err != nil {
		return fmt.Errorf("callback %q: waiting for response: %w", name, err)
	}

	if ev.Result.Error != "" {
		return fmt.Errorf("callback %q: error: %s", name, ev.Result.Error)
	}

	return nil
}

// executeHook dispatches a hook to the appropriate executor.
// client_func hooks are dispatched via callback events to the client SDK.
// All other hook types are delegated to the service type's Initializer
// and are only permitted during the init phase (includeEgresses=false).
func executeHook(ctx context.Context, sc *serviceContext, hook *spec.HookSpec, includeEgresses bool) error {
	if hook.Type == "client_func" {
		if hook.ClientFunc == nil {
			return fmt.Errorf("client_func hook missing client_func spec")
		}
		return dispatchCallback(ctx, sc, hook.ClientFunc.Name, "hook", includeEgresses)
	}

	// Server-side hooks only run during init — the service must be running
	// for the server to exec into it. Prestart hooks (includeEgresses=true)
	// must be client_func.
	if includeEgresses {
		return fmt.Errorf("server-side hook type %q is not supported in prestart phase (only client_func hooks allowed)", hook.Type)
	}

	initializer, ok := sc.svcType.(service.Initializer)
	if !ok {
		return fmt.Errorf("unsupported hook type %q for service type %T", hook.Type, sc.svcType)
	}

	logWriter := &eventLogWriter{
		log:     sc.log,
		envName: sc.envName,
		service: sc.name,
	}

	return initializer.Init(ctx, service.InitParams{
		ServiceName: sc.name,
		InstanceID:  sc.instanceID,
		Spec:        sc.spec,
		Ingresses:   sc.ingresses,
		Egresses:    sc.egresses,
		Hook:        hook,
		Stdout:      &teeWriter{logWriter, "stdout"},
		Stderr:      &teeWriter{logWriter, "stderr"},
	})
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

// proxyEmitter returns a function that converts proxy events into server events
// and publishes them to the event log.
func proxyEmitter(sc *serviceContext) func(proxy.Event) {
	return func(pe proxy.Event) {
		ev := Event{
			Type:        EventType(pe.Type),
			Environment: sc.envName,
		}
		if pe.Request != nil {
			ev.Request = &RequestInfo{
				Source:                pe.Request.Source,
				Target:                pe.Request.Target,
				Ingress:               pe.Request.Ingress,
				Method:                pe.Request.Method,
				Path:                  pe.Request.Path,
				StatusCode:            pe.Request.StatusCode,
				LatencyMs:             pe.Request.LatencyMs,
				RequestSize:           pe.Request.RequestSize,
				ResponseSize:          pe.Request.ResponseSize,
				RequestHeaders:        pe.Request.RequestHeaders,
				RequestBody:           pe.Request.RequestBody,
				RequestBodyTruncated:  pe.Request.RequestBodyTruncated,
				ResponseHeaders:       pe.Request.ResponseHeaders,
				ResponseBody:          pe.Request.ResponseBody,
				ResponseBodyTruncated: pe.Request.ResponseBodyTruncated,
			}
		}
		if pe.Connection != nil {
			ev.Connection = &ConnectionInfo{
				Source:     pe.Connection.Source,
				Target:     pe.Connection.Target,
				Ingress:    pe.Connection.Ingress,
				BytesIn:    pe.Connection.BytesIn,
				BytesOut:   pe.Connection.BytesOut,
				DurationMs: pe.Connection.DurationMs,
			}
		}
		if pe.GRPCCall != nil {
			ev.GRPCCall = &GRPCCallInfo{
				Source:           pe.GRPCCall.Source,
				Target:           pe.GRPCCall.Target,
				Ingress:          pe.GRPCCall.Ingress,
				Service:          pe.GRPCCall.Service,
				Method:           pe.GRPCCall.Method,
				GRPCStatus:       pe.GRPCCall.GRPCStatus,
				GRPCMessage:      pe.GRPCCall.GRPCMessage,
				LatencyMs:        pe.GRPCCall.LatencyMs,
				RequestSize:      pe.GRPCCall.RequestSize,
				ResponseSize:     pe.GRPCCall.ResponseSize,
				RequestMetadata:  pe.GRPCCall.RequestMetadata,
				ResponseMetadata: pe.GRPCCall.ResponseMetadata,
			}
		}
		sc.log.Publish(ev)
	}
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

// runPrefixRE matches the error prefixes added by run.Sequence and run.Group.
// These are orchestration details (step indices, group names) that obscure the
// actual failure cause in user-facing output.
var runPrefixRE = regexp.MustCompile(`^(sequence \[\d+:\d+\]: |run\.Group\[[^\]]+\]: )+`)

// stripRunPrefixes removes leading run.Sequence/run.Group error prefixes,
// leaving only the domain error message.
func stripRunPrefixes(s string) string {
	return runPrefixRE.ReplaceAllString(s, "")
}
