package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/matgreaves/rig/internal/server/artifact"
	"github.com/matgreaves/rig/internal/server/service"
	"github.com/matgreaves/rig/internal/spec"
)

// Server is the rig HTTP API server. It manages the lifecycle of one or more
// concurrent environments, each with its own event log and run.Runner.
type Server struct {
	mux      *http.ServeMux
	ports    *PortAllocator
	registry *service.Registry
	tempBase string
	rigDir   string // base rig directory; cache/ and logs/ live under this

	mu   sync.Mutex
	envs map[string]*envInstance

	idle      *IdleTimer
	cache     *artifact.Cache
	refresher *artifact.Refresher
}

// envInstance holds the runtime state of a single active environment.
type envInstance struct {
	id       string
	spec     *spec.Environment
	log      *EventLog
	envDir   string
	preserve *bool // shared with Orchestrator; set to true to skip cleanup

	cancel context.CancelFunc
	done   <-chan error // receives runner's terminal error (buffered 1)
}

// NewServer creates a Server and registers all HTTP routes.
// Pass idleTimeout = 0 to disable automatic shutdown.
// Pass rigDir = "" to use the default (~/.rig via DefaultRigDir()).
// Cache lives at {rigDir}/cache/, event logs at {rigDir}/logs/.
func NewServer(
	ports *PortAllocator,
	registry *service.Registry,
	tempBase string,
	idleTimeout time.Duration,
	rigDir string,
) *Server {
	if rigDir == "" {
		rigDir = DefaultRigDir()
	}
	cache := artifact.NewCache(filepath.Join(rigDir, "cache"))
	s := &Server{
		mux:       http.NewServeMux(),
		ports:     ports,
		registry:  registry,
		tempBase:  tempBase,
		rigDir:    rigDir,
		envs:      make(map[string]*envInstance),
		idle:      NewIdleTimer(idleTimeout),
		cache:     cache,
		refresher: artifact.NewRefresher(cache, artifact.DefaultStaleAfter),
	}

	s.mux.HandleFunc("GET /health", s.handleHealth)
	s.mux.HandleFunc("POST /environments", s.handleCreateEnvironment)
	s.mux.HandleFunc("GET /environments/{id}/events", s.handleSSE)
	s.mux.HandleFunc("POST /environments/{id}/events", s.handleClientEvent)
	s.mux.HandleFunc("DELETE /environments/{id}", s.handleDeleteEnvironment)
	s.mux.HandleFunc("GET /environments/{id}", s.handleGetEnvironment)
	s.mux.HandleFunc("GET /environments/{id}/log", s.handleGetLog)

	return s
}

// ServeHTTP implements http.Handler.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mux.ServeHTTP(w, r)
}

// handleHealth handles GET /health. Returns 200 with {"status":"ok"}.
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// ShutdownCh returns a channel that is closed when the idle timer fires.
func (s *Server) ShutdownCh() <-chan struct{} {
	return s.idle.ShutdownCh()
}

// idleCheckInterval is how often the background loop checks whether the server
// is idle and runs maintenance tasks.
const idleCheckInterval = 30 * time.Second

// StartBackgroundTasks runs a polling loop that checks for server idleness
// every 30 seconds and triggers maintenance tasks (e.g. Docker image cache
// refresh) when no environments are active. Blocks until ctx is cancelled;
// call it in its own goroutine.
func (s *Server) StartBackgroundTasks(ctx context.Context) {
	ticker := time.NewTicker(idleCheckInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if !s.isIdle() {
				continue
			}
			s.refresher.RefreshOnce(ctx)
		}
	}
}

// isIdle returns true when there are no active environments.
func (s *Server) isIdle() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.envs) == 0
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
	preserve := false
	orch := &Orchestrator{
		Ports:    s.ports,
		Registry: s.registry,
		Log:      envLog,
		TempBase: s.tempBase,
		Cache:    s.cache,
		Preserve: &preserve,
	}

	runner, id, envDir, err := orch.Orchestrate(&env)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "orchestrate: "+err.Error())
		return
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)

	inst := &envInstance{
		id:       id,
		spec:     &env,
		log:      envLog,
		envDir:   envDir,
		preserve: &preserve,
		cancel:   cancel,
		done:     done,
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
						EnvDir:      envDir,
					})
					return
				}
			}
		}()

		err := runner.Run(ctx)

		// Emit environment.down before signalling done so that SSE clients
		// see the terminal event before DELETE returns. Include a pre-formatted
		// summary so client SDKs can use it directly as an error message.
		envLog.Publish(Event{
			Type:        EventEnvironmentDown,
			Environment: env.Name,
			Message:     buildDownSummary(envLog),
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

	// service.error / service.log fields
	Service string `json:"service,omitempty"`

	// service.log fields
	Stream  string `json:"stream,omitempty"`   // "stdout" or "stderr"
	LogData string `json:"log_data,omitempty"` // log line content
}

// handleClientEvent handles POST /environments/{id}/events.
//
// A single endpoint for all client→server communication. The payload's type
// field determines how the event is processed:
//   - "callback.response": unblocks a waiting lifecycle step
//   - "service.error": marks a client-side service as failed
//   - "service.log": captures a log line from a client-side (Func) service
//   - "test.note": records a test assertion or diagnostic message
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
			Service:     ev.Service,
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

	case "service.log":
		stream := ev.Stream
		if stream == "" {
			stream = "stdout"
		}
		inst.log.Publish(Event{
			Type:        EventServiceLog,
			Environment: inst.spec.Name,
			Service:     ev.Service,
			Log: &LogEntry{
				Stream: stream,
				Data:   ev.LogData,
			},
		})

	case "test.note":
		inst.log.Publish(Event{
			Type:        EventTestNote,
			Environment: inst.spec.Name,
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

	// Only emit environment.destroying if the environment is still running.
	// If a service crash already brought it down, destroying doesn't apply —
	// nobody requested teardown, it just died.
	alreadyDown := false
	for _, e := range inst.log.LifecycleEvents() {
		if e.Type == EventEnvironmentDown {
			alreadyDown = true
			break
		}
	}
	if !alreadyDown {
		inst.log.Publish(Event{
			Type:        EventEnvironmentDestroying,
			Environment: inst.spec.Name,
		})
	}

	// Set preserve flag before cancelling so the orchestrator's cleanup
	// defer sees it. Supports both query param and server-wide env var.
	if r.URL.Query().Get("preserve") == "true" || os.Getenv("RIG_PRESERVE") == "true" {
		if inst.preserve != nil {
			*inst.preserve = true
		}
	}

	inst.cancel()
	<-inst.done

	s.ports.Release(id)
	s.idle.EnvironmentDestroyed()

	result := map[string]any{
		"id":      id,
		"status":  "destroyed",
		"env_dir": inst.envDir,
	}
	if r.URL.Query().Get("log") == "true" {
		if path, err := s.writeEventLog(inst); err == nil {
			result["log_file"] = path
		}
	}
	writeJSON(w, http.StatusOK, result)
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
	events := inst.log.LifecycleEvents()

	services := make(map[string]spec.ResolvedService, len(inst.spec.Services))
	for name := range inst.spec.Services {
		services[name] = spec.ResolvedService{
			Ingresses: make(map[string]spec.Endpoint),
			Egresses:  make(map[string]spec.Endpoint),
			Status:    spec.StatusPending,
		}
	}

	// Track proxy endpoints separately; they override ingresses for env.Endpoint().
	proxyEndpoints := make(map[string]map[string]spec.Endpoint) // service → ingress → proxy endpoint

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
		case EventProxyPublished:
			if e.Endpoint != nil && e.Ingress != "" {
				if proxyEndpoints[e.Service] == nil {
					proxyEndpoints[e.Service] = make(map[string]spec.Endpoint)
				}
				proxyEndpoints[e.Service][e.Ingress] = *e.Endpoint
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

	// When observing, replace ingress endpoints with proxy endpoints so
	// env.Endpoint() returns the proxy address.
	for svcName, proxyMap := range proxyEndpoints {
		svc := services[svcName]
		for ingressName, proxyEP := range proxyMap {
			svc.Ingresses[ingressName] = proxyEP
		}
		services[svcName] = svc
	}

	return spec.ResolvedEnvironment{
		ID:       inst.id,
		Name:     inst.spec.Name,
		Services: services,
	}
}

// contextEvents is the number of lifecycle events shown before the trigger
// in the failure summary.
const contextEvents = 5

// buildDownSummary scans the event log and builds a human-readable failure
// summary for the environment.down event. Client SDKs use this directly as
// their error message, avoiding the need to reimplement timeline formatting.
// Returns "" for normal (non-failure) shutdowns.
func buildDownSummary(log *EventLog) string {
	events := log.LifecycleEvents()
	if len(events) == 0 {
		return ""
	}

	// Collect failure causes and find the trigger event.
	var failures []string
	triggerIdx := -1
	for i, e := range events {
		if e.Type == EventEnvironmentFailing {
			failures = append(failures, e.Error)
			if triggerIdx == -1 {
				triggerIdx = i
			}
		}
	}
	if len(failures) == 0 {
		return "" // normal shutdown
	}

	var b strings.Builder
	b.WriteString("environment failed:\n  ")
	b.WriteString(strings.Join(failures, "\n  "))

	// Build a short timeline of events leading up to the failure.
	// Filter to the same set of events the full timeline uses.
	type timelineEntry struct {
		elapsed float64
		text    string
	}
	start := events[0].Timestamp
	var timeline []timelineEntry
	for i, e := range events {
		switch e.Type {
		case EventServiceLog, EventHealthCheckFailed,
			EventCallbackRequest, EventCallbackResponse,
			EventRequestCompleted, EventConnectionOpened, EventConnectionClosed,
			EventGRPCCallCompleted, EventProxyPublished:
			continue
		}
		elapsed := e.Timestamp.Sub(start).Seconds()

		if e.Type == EventProgressStall && e.Message != "" {
			lines := strings.Split(e.Message, "\n")
			first := fmt.Sprintf("  %5.2fs  %-22s %s", elapsed, e.Type, lines[0])
			timeline = append(timeline, timelineEntry{elapsed, first})
			for _, line := range lines[1:] {
				timeline = append(timeline, timelineEntry{elapsed, "          " + line})
			}
			continue
		}

		subject := e.Service
		if subject == "" {
			subject = e.Artifact
		}
		detail := e.Error

		var line string
		if subject != "" && detail != "" && e.Type != EventEnvironmentFailing {
			line = fmt.Sprintf("  %5.2fs  %-22s %-12s %s", elapsed, e.Type, subject, detail)
		} else if subject != "" {
			line = fmt.Sprintf("  %5.2fs  %-22s %s", elapsed, e.Type, subject)
		} else if detail != "" && e.Type != EventEnvironmentFailing {
			line = fmt.Sprintf("  %5.2fs  %-22s %s", elapsed, e.Type, detail)
		} else {
			line = fmt.Sprintf("  %5.2fs  %s", elapsed, e.Type)
		}

		timeline = append(timeline, timelineEntry{elapsed, line})

		// If this is the trigger, record where we are in timeline.
		if i == triggerIdx {
			triggerIdx = len(timeline) - 1
		}
	}

	if len(timeline) > 0 {
		// Show context events before the trigger.
		end := triggerIdx + 1
		if end > len(timeline) {
			end = len(timeline)
		}
		startIdx := end - contextEvents - 1
		if startIdx < 0 {
			startIdx = 0
		}

		b.WriteString("\n\n")
		for i := startIdx; i < end; i++ {
			if i > startIdx {
				b.WriteByte('\n')
			}
			b.WriteString(timeline[i].text)
		}
	}

	return b.String()
}

// handleGetLog handles GET /environments/{id}/log.
//
// Returns the full event log as a JSON array, suitable for diagnostics
// when a test fails. Events are ordered by sequence number.
func (s *Server) handleGetLog(w http.ResponseWriter, r *http.Request) {
	inst, ok := s.getInstance(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, inst.log.Events())
}

// logMaxAge is how long event log files are kept before pruning.
const logMaxAge = 72 * time.Hour

// writeEventLog writes both a structured JSONL event log and a human-readable
// timeline summary to {rigDir}/logs/. The JSONL file (one event per line) is
// the source of truth for tooling; the .log file is a convenience rendering
// for quick scanning. Returns the JSONL file path on success.
func (s *Server) writeEventLog(inst *envInstance) (string, error) {
	logDir := filepath.Join(s.rigDir, "logs")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		return "", err
	}

	pruneOldLogs(logDir, logMaxAge)

	events := inst.log.Events()
	if len(events) == 0 {
		return "", fmt.Errorf("no events")
	}

	safe := strings.NewReplacer("/", "_", "\\", "_", " ", "_").Replace(inst.spec.Name)
	base := filepath.Join(logDir, safe+"-"+inst.id)

	// Write structured JSONL — one event per line for streaming parsers.
	jsonlPath := base + ".jsonl"
	var jb strings.Builder
	enc := json.NewEncoder(&jb)
	enc.SetEscapeHTML(false)
	for _, e := range events {
		if err := enc.Encode(e); err != nil {
			return "", err
		}
	}
	if err := os.WriteFile(jsonlPath, []byte(jb.String()), 0o644); err != nil {
		return "", err
	}

	// Collect the last few log lines per service so we can include them
	// in the timeline when a service fails.
	const tailLines = 10
	serviceLogs := make(map[string][]string)
	for _, e := range events {
		if e.Type == EventServiceLog && e.Log != nil {
			// A single log event may contain multiple newline-separated lines.
			for _, line := range strings.Split(strings.TrimRight(e.Log.Data, "\n"), "\n") {
				serviceLogs[e.Service] = append(serviceLogs[e.Service], line)
			}
			if len(serviceLogs[e.Service]) > tailLines {
				sl := serviceLogs[e.Service]
				serviceLogs[e.Service] = sl[len(sl)-tailLines:]
			}
		}
	}

	// Traffic summary accumulators.
	type edgeKey struct{ source, target string }
	type edgeStats struct {
		requests    int
		connections int
		grpcCalls   int
		totalLatMs  float64
		grpcLatMs   float64
		bytesIn     int64
		bytesOut    int64
	}
	edges := make(map[edgeKey]*edgeStats)
	getEdge := func(source, target string) *edgeStats {
		k := edgeKey{source, target}
		if s, ok := edges[k]; ok {
			return s
		}
		s := &edgeStats{}
		edges[k] = s
		return s
	}

	// Write human-readable timeline summary alongside.
	var b strings.Builder
	start := events[0].Timestamp
	fmt.Fprintf(&b, "rig: event timeline for environment %s:", inst.id)
	for _, e := range events {
		// Skip noisy per-line events — the timeline is a structural overview.
		// Health check probes and service log lines are in the JSONL for detail.
		if e.Type == EventServiceLog || e.Type == EventHealthCheckFailed {
			continue
		}
		elapsed := e.Timestamp.Sub(start).Seconds()

		// Render observed traffic events with source→target detail.
		if e.Type == EventRequestCompleted && e.Request != nil {
			r := e.Request
			fmt.Fprintf(&b, "\n  %5.2fs  %-22s %-10s → %-10s %-6s %-14s %3d  %.1fms",
				elapsed, e.Type, r.Source, r.Target, r.Method, r.Path, r.StatusCode, r.LatencyMs)
			s := getEdge(r.Source, r.Target)
			s.requests++
			s.totalLatMs += r.LatencyMs
			continue
		}
		if e.Type == EventConnectionClosed && e.Connection != nil {
			c := e.Connection
			fmt.Fprintf(&b, "\n  %5.2fs  %-22s %-10s → %-10s %.1fms  %dB↑ %dB↓",
				elapsed, e.Type, c.Source, c.Target, c.DurationMs, c.BytesIn, c.BytesOut)
			s := getEdge(c.Source, c.Target)
			s.connections++
			s.bytesIn += c.BytesIn
			s.bytesOut += c.BytesOut
			continue
		}
		if e.Type == EventGRPCCallCompleted && e.GRPCCall != nil {
			g := e.GRPCCall
			fmt.Fprintf(&b, "\n  %5.2fs  %-22s %-10s → %-10s %s/%s  %s  %.1fms",
				elapsed, e.Type, g.Source, g.Target, g.Service, g.Method, g.GRPCStatus, g.LatencyMs)
			s := getEdge(g.Source, g.Target)
			s.grpcCalls++
			s.grpcLatMs += g.LatencyMs
			continue
		}
		if e.Type == EventConnectionOpened || e.Type == EventProxyPublished {
			// Skip noisy per-open events.
			continue
		}
		if e.Type == EventProgressStall && e.Diagnostic != nil {
			fmt.Fprintf(&b, "\n  %5.2fs  %-22s no progress for %s", elapsed, e.Type, e.Diagnostic.StalledFor)
			for _, svc := range e.Diagnostic.Services {
				fmt.Fprintf(&b, "\n           %s  %s: %s", strings.Repeat(" ", 22), svc.Name, svc.Phase)
				if len(svc.WaitingOn) > 0 {
					fmt.Fprintf(&b, " — waiting on %s", strings.Join(svc.WaitingOn, ", "))
				}
			}
			continue
		}

		subject := e.Service
		if subject == "" {
			subject = e.Artifact
		}
		detail := e.Error
		if subject != "" && detail != "" {
			fmt.Fprintf(&b, "\n  %5.2fs  %-22s %-12s %s", elapsed, e.Type, subject, detail)
		} else if subject != "" {
			fmt.Fprintf(&b, "\n  %5.2fs  %-22s %s", elapsed, e.Type, subject)
		} else if detail != "" {
			fmt.Fprintf(&b, "\n  %5.2fs  %-22s %s", elapsed, e.Type, detail)
		} else {
			fmt.Fprintf(&b, "\n  %5.2fs  %s", elapsed, e.Type)
		}

		// After a service.failed event, include the tail of that service's output.
		if e.Type == EventServiceFailed {
			if logs, ok := serviceLogs[e.Service]; ok && len(logs) > 0 {
				for _, line := range logs {
					fmt.Fprintf(&b, "\n          | %s", line)
				}
			}
		}
	}
	// Append traffic summary if any traffic was observed.
	if len(edges) > 0 {
		// Sort edges for deterministic output.
		type edgeEntry struct {
			key   edgeKey
			stats *edgeStats
		}
		sorted := make([]edgeEntry, 0, len(edges))
		for k, s := range edges {
			sorted = append(sorted, edgeEntry{k, s})
		}
		sort.Slice(sorted, func(i, j int) bool {
			if sorted[i].key.source != sorted[j].key.source {
				return sorted[i].key.source < sorted[j].key.source
			}
			return sorted[i].key.target < sorted[j].key.target
		})

		fmt.Fprintf(&b, "\n\n  Traffic:")
		for _, e := range sorted {
			if e.stats.requests > 0 {
				avg := e.stats.totalLatMs / float64(e.stats.requests)
				fmt.Fprintf(&b, "\n    %-10s → %-10s %d requests   avg %.1fms",
					e.key.source, e.key.target, e.stats.requests, avg)
			}
			if e.stats.grpcCalls > 0 {
				avg := e.stats.grpcLatMs / float64(e.stats.grpcCalls)
				fmt.Fprintf(&b, "\n    %-10s → %-10s %d gRPC calls  avg %.1fms",
					e.key.source, e.key.target, e.stats.grpcCalls, avg)
			}
			if e.stats.connections > 0 {
				totalBytes := e.stats.bytesIn + e.stats.bytesOut
				fmt.Fprintf(&b, "\n    %-10s → %-10s %d connections  %s total",
					e.key.source, e.key.target, e.stats.connections, formatBytes(totalBytes))
			}
		}
	}

	// Write human-readable timeline — this is what we surface to users.
	logPath := base + ".log"
	os.WriteFile(logPath, []byte(b.String()+"\n"), 0o644)

	return logPath, nil
}

// pruneOldLogs removes .jsonl and .log files older than maxAge from dir.
// Best-effort — errors are silently ignored.
func pruneOldLogs(dir string, maxAge time.Duration) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-maxAge)
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(name, ".jsonl") && !strings.HasSuffix(name, ".log") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if info.ModTime().Before(cutoff) {
			os.Remove(filepath.Join(dir, name))
		}
	}
}

func formatBytes(b int64) string {
	switch {
	case b >= 1<<20:
		return fmt.Sprintf("%.1fMB", float64(b)/float64(1<<20))
	case b >= 1<<10:
		return fmt.Sprintf("%.1fKB", float64(b)/float64(1<<10))
	default:
		return fmt.Sprintf("%dB", b)
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
