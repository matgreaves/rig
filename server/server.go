package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/matgreaves/rig/server/service"
	"github.com/matgreaves/rig/spec"
)

// Server is the rig HTTP API server. It manages the lifecycle of one or more
// concurrent environments, each with its own event log and run.Runner.
type Server struct {
	mux      *http.ServeMux
	ports    *PortAllocator
	registry *service.Registry
	tempBase string
	cacheDir string // artifact cache directory; empty → Orchestrator default (~/.rig/cache/)

	mu   sync.Mutex
	envs map[string]*envInstance

	idle *IdleTimer
}

// envInstance holds the runtime state of a single active environment.
type envInstance struct {
	id   string
	spec *spec.Environment
	log  *EventLog

	cancel context.CancelFunc
	done   <-chan error // receives runner's terminal error (buffered 1)
}

// NewServer creates a Server and registers all HTTP routes.
// Pass idleTimeout = 0 to disable automatic shutdown.
// Pass cacheDir = "" to use the default artifact cache (~/.rig/cache/).
func NewServer(
	ports *PortAllocator,
	registry *service.Registry,
	tempBase string,
	idleTimeout time.Duration,
	cacheDir string,
) *Server {
	s := &Server{
		mux:      http.NewServeMux(),
		ports:    ports,
		registry: registry,
		tempBase: tempBase,
		cacheDir: cacheDir,
		envs:     make(map[string]*envInstance),
		idle:     NewIdleTimer(idleTimeout),
	}

	s.mux.HandleFunc("POST /environments", s.handleCreateEnvironment)
	s.mux.HandleFunc("GET /environments/{id}/events", s.handleSSE)
	s.mux.HandleFunc("POST /environments/{id}/events", s.handleClientEvent)
	s.mux.HandleFunc("DELETE /environments/{id}", s.handleDeleteEnvironment)
	s.mux.HandleFunc("GET /environments/{id}", s.handleGetEnvironment)

	return s
}

// ServeHTTP implements http.Handler.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mux.ServeHTTP(w, r)
}

// ShutdownCh returns a channel that is closed when the idle timer fires.
func (s *Server) ShutdownCh() <-chan struct{} {
	return s.idle.ShutdownCh()
}

// handleCreateEnvironment handles POST /environments.
//
// Validates the spec, orchestrates the environment, and returns the instance
// ID immediately. Orchestration runs asynchronously in the background.
func (s *Server) handleCreateEnvironment(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeError(w, http.StatusBadRequest, "read body: "+err.Error())
		return
	}

	env, err := spec.DecodeEnvironment(body)
	if err != nil {
		writeError(w, http.StatusBadRequest, "decode: "+err.Error())
		return
	}

	if errs := ValidateEnvironment(&env); len(errs) > 0 {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{
			"error":             "spec validation failed",
			"validation_errors": errs,
		})
		return
	}

	envLog := NewEventLog()
	orch := &Orchestrator{
		Ports:    s.ports,
		Registry: s.registry,
		Log:      envLog,
		TempBase: s.tempBase,
		CacheDir: s.cacheDir,
	}

	runner, id, err := orch.Orchestrate(&env)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "orchestrate: "+err.Error())
		return
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)

	inst := &envInstance{
		id:     id,
		spec:   &env,
		log:    envLog,
		cancel: cancel,
		done:   done,
	}

	s.mu.Lock()
	s.envs[id] = inst
	s.mu.Unlock()

	s.idle.EnvironmentCreated()

	go func() {
		// Watch for all services becoming ready then emit environment.up.
		// watchCtx is cancelled when the runner exits, preventing the watcher
		// from blocking forever if some services never become ready.
		watchCtx, watchCancel := context.WithCancel(ctx)
		defer watchCancel()

		go func() {
			need := len(env.Services)
			ch := envLog.Subscribe(watchCtx, 0, func(e Event) bool {
				return e.Type == EventServiceReady
			})
			count := 0
			for range ch {
				count++
				if count >= need {
					resolved := buildResolvedEnvironment(inst)
					ingresses := make(map[string]map[string]spec.Endpoint, len(resolved.Services))
					for svcName, svc := range resolved.Services {
						ingresses[svcName] = svc.Ingresses
					}
					envLog.Publish(Event{
						Type:        EventEnvironmentUp,
						Environment: env.Name,
						Ingresses:   ingresses,
					})
					return
				}
			}
		}()

		err := runner.Run(ctx)

		// Emit environment.down before signalling done so that SSE clients
		// see the terminal event before DELETE returns.
		envLog.Publish(Event{
			Type:        EventEnvironmentDown,
			Environment: env.Name,
		})

		done <- err
	}()

	writeJSON(w, http.StatusCreated, map[string]string{"id": id})
}

// handleGetEnvironment handles GET /environments/{id}.
func (s *Server) handleGetEnvironment(w http.ResponseWriter, r *http.Request) {
	inst, ok := s.getInstance(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, buildResolvedEnvironment(inst))
}

// clientEvent is the wire format for events posted by the client SDK.
// The Type field determines how the event is handled.
type clientEvent struct {
	Type string `json:"type"`

	// callback.response fields
	RequestID string         `json:"request_id,omitempty"`
	Error     string         `json:"error,omitempty"`
	Data      map[string]any `json:"data,omitempty"`

	// service.error fields
	Service string `json:"service,omitempty"`
}

// handleClientEvent handles POST /environments/{id}/events.
//
// A single endpoint for all client→server communication. The payload's type
// field determines how the event is processed:
//   - "callback.response": unblocks a waiting lifecycle step
//   - "service.error": marks a client-side service as failed
func (s *Server) handleClientEvent(w http.ResponseWriter, r *http.Request) {
	inst, ok := s.getInstance(w, r)
	if !ok {
		return
	}

	var ev clientEvent
	if err := json.NewDecoder(r.Body).Decode(&ev); err != nil {
		writeError(w, http.StatusBadRequest, "decode body: "+err.Error())
		return
	}

	switch ev.Type {
	case "callback.response":
		inst.log.Publish(Event{
			Type:        EventCallbackResponse,
			Environment: inst.spec.Name,
			Result: &CallbackResponse{
				RequestID: ev.RequestID,
				Error:     ev.Error,
				Data:      ev.Data,
			},
		})

	case "service.error":
		inst.log.Publish(Event{
			Type:        EventServiceFailed,
			Environment: inst.spec.Name,
			Service:     ev.Service,
			Error:       ev.Error,
		})

	default:
		writeError(w, http.StatusBadRequest, "unknown client event type: "+ev.Type)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// handleDeleteEnvironment handles DELETE /environments/{id}.
//
// Cancels the runner, blocks until it exits, releases ports, then removes the
// environment from the active set. Returns once teardown is complete.
func (s *Server) handleDeleteEnvironment(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	// Remove from map immediately so concurrent DELETEs get 404.
	s.mu.Lock()
	inst, ok := s.envs[id]
	if ok {
		delete(s.envs, id)
	}
	s.mu.Unlock()

	if !ok {
		writeError(w, http.StatusNotFound, "environment not found")
		return
	}

	inst.cancel()
	<-inst.done

	s.ports.Release(id)
	s.idle.EnvironmentDestroyed()

	writeJSON(w, http.StatusOK, map[string]string{"id": id, "status": "destroyed"})
}

// getInstance looks up an environment by the {id} path value, writing a 404
// and returning false if not found.
func (s *Server) getInstance(w http.ResponseWriter, r *http.Request) (*envInstance, bool) {
	id := r.PathValue("id")
	s.mu.Lock()
	inst, ok := s.envs[id]
	s.mu.Unlock()
	if !ok {
		writeError(w, http.StatusNotFound, "environment not found")
		return nil, false
	}
	return inst, true
}

// buildResolvedEnvironment scans the event log to construct a point-in-time
// snapshot of the environment: resolved ingress/egress endpoints and service
// statuses.
func buildResolvedEnvironment(inst *envInstance) spec.ResolvedEnvironment {
	events := inst.log.Events()

	services := make(map[string]spec.ResolvedService, len(inst.spec.Services))
	for name := range inst.spec.Services {
		services[name] = spec.ResolvedService{
			Ingresses: make(map[string]spec.Endpoint),
			Egresses:  make(map[string]spec.Endpoint),
			Status:    spec.StatusPending,
		}
	}

	for _, e := range events {
		svc, ok := services[e.Service]
		if !ok {
			continue
		}
		switch e.Type {
		case EventIngressPublished:
			if e.Endpoint != nil && e.Ingress != "" {
				svc.Ingresses[e.Ingress] = *e.Endpoint
			}
		case EventServiceStarting:
			svc.Status = spec.StatusStarting
		case EventServiceHealthy:
			svc.Status = spec.StatusHealthy
		case EventServiceReady:
			svc.Status = spec.StatusReady
		case EventServiceFailed:
			svc.Status = spec.StatusFailed
		case EventServiceStopping:
			svc.Status = spec.StatusStopping
		case EventServiceStopped:
			svc.Status = spec.StatusStopped
		default:
			continue
		}
		services[e.Service] = svc
	}

	// Reconstruct egresses: for each service's egress spec, look up the
	// already-resolved ingress of the target service.
	for name, svcSpec := range inst.spec.Services {
		svc := services[name]
		for egressName, egressSpec := range svcSpec.Egresses {
			if target, ok := services[egressSpec.Service]; ok {
				if ep, ok := target.Ingresses[egressSpec.Ingress]; ok {
					svc.Egresses[egressName] = ep
				}
			}
		}
		services[name] = svc
	}

	return spec.ResolvedEnvironment{
		ID:       inst.id,
		Name:     inst.spec.Name,
		Services: services,
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
