package server

import (
	"context"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/matgreaves/rig/spec"
)

// EventType identifies the kind of lifecycle event.
type EventType string

const (
	// Artifact phase.
	EventArtifactStarted   EventType = "artifact.started"
	EventArtifactCompleted EventType = "artifact.completed"
	EventArtifactFailed    EventType = "artifact.failed"
	EventArtifactCached    EventType = "artifact.cached"

	// Service lifecycle.
	EventIngressPublished EventType = "ingress.published"
	EventWiringResolved   EventType = "wiring.resolved"
	EventServicePrestart  EventType = "service.prestart"
	EventServiceStarting  EventType = "service.starting"
	EventServiceHealthy   EventType = "service.healthy"
	EventServiceInit      EventType = "service.init"
	EventServiceReady     EventType = "service.ready"
	EventServiceFailed    EventType = "service.failed"
	EventServiceStopping  EventType = "service.stopping"
	EventServiceStopped   EventType = "service.stopped"
	EventServiceLog       EventType = "service.log"

	// Client-side callbacks.
	EventCallbackRequest  EventType = "callback.request"
	EventCallbackResponse EventType = "callback.response"

	// Environment lifecycle.
	EventEnvironmentFailing    EventType = "environment.failing"
	EventEnvironmentDestroying EventType = "environment.destroying"
	EventEnvironmentUp         EventType = "environment.up"
	EventEnvironmentDown       EventType = "environment.down"

	// Client-side test events.
	EventTestNote EventType = "test.note"

	// Health checks.
	EventHealthCheckFailed EventType = "health.check_failed"

	// Traffic observation.
	EventRequestCompleted  EventType = "request.completed"
	EventConnectionOpened  EventType = "connection.opened"
	EventConnectionClosed  EventType = "connection.closed"
	EventProxyPublished    EventType = "proxy.published"
)

// LogEntry holds a line of service output.
type LogEntry struct {
	Stream string `json:"stream"` // "stdout" or "stderr"
	Data   string `json:"data"`
}

// CallbackRequest is published when the server needs the client to
// execute a function (hook or custom service type callback).
type CallbackRequest struct {
	RequestID string         `json:"request_id"`
	Name      string         `json:"name"`
	Type      string         `json:"type"` // "hook", "publish", "start", "ready"
	Wiring    *WiringContext `json:"wiring,omitempty"`
}

// WiringContext provides resolved endpoint information to callbacks.
// Contents vary by callback type:
//   - prestart hook: Ingresses + Egresses + TempDir + EnvDir
//   - init hook: Ingresses only + TempDir (no egresses)
//   - custom type callbacks: type-specific
type WiringContext struct {
	Ingresses  map[string]spec.Endpoint `json:"ingresses,omitempty"`
	Egresses   map[string]spec.Endpoint `json:"egresses,omitempty"`
	TempDir    string                   `json:"temp_dir,omitempty"`
	EnvDir     string                   `json:"env_dir,omitempty"`
	Attributes map[string]string        `json:"attributes,omitempty"`
}

// CallbackResponse is posted by the client after handling a callback request.
type CallbackResponse struct {
	RequestID string         `json:"request_id"`
	Error     string         `json:"error,omitempty"`
	Data      map[string]any `json:"data,omitempty"`
}

// RequestInfo captures an observed HTTP request/response pair.
type RequestInfo struct {
	Source       string  `json:"source"`
	Target       string  `json:"target"`
	Ingress      string  `json:"ingress"`
	Method       string  `json:"method"`
	Path         string  `json:"path"`
	StatusCode   int     `json:"status_code"`
	LatencyMs    float64 `json:"latency_ms"`
	RequestSize  int64   `json:"request_size"`
	ResponseSize int64   `json:"response_size"`

	RequestHeaders        map[string][]string `json:"request_headers,omitempty"`
	RequestBody           []byte              `json:"request_body,omitempty"`
	RequestBodyTruncated  bool                `json:"request_body_truncated,omitempty"`
	ResponseHeaders       map[string][]string `json:"response_headers,omitempty"`
	ResponseBody          []byte              `json:"response_body,omitempty"`
	ResponseBodyTruncated bool                `json:"response_body_truncated,omitempty"`
}

// ConnectionInfo captures an observed TCP connection.
type ConnectionInfo struct {
	Source     string  `json:"source"`
	Target     string  `json:"target"`
	Ingress    string  `json:"ingress"`
	BytesIn    int64   `json:"bytes_in"`
	BytesOut   int64   `json:"bytes_out"`
	DurationMs float64 `json:"duration_ms"`
}

// Event is a single entry in the event log.
type Event struct {
	Seq         uint64            `json:"seq"`
	Type        EventType         `json:"type"`
	Environment string            `json:"environment,omitempty"`
	Service     string            `json:"service,omitempty"`
	Ingress     string            `json:"ingress,omitempty"`
	Endpoint    *spec.Endpoint    `json:"endpoint,omitempty"`
	Artifact    string            `json:"artifact,omitempty"`
	Log         *LogEntry         `json:"log,omitempty"`
	Callback    *CallbackRequest  `json:"callback,omitempty"`
	Result      *CallbackResponse `json:"result,omitempty"`
	Error       string            `json:"error,omitempty"`
	Request     *RequestInfo      `json:"request,omitempty"`
	Connection  *ConnectionInfo   `json:"connection,omitempty"`
	// Ingresses is populated on environment.up. It maps service name to a
	// map of ingress name to endpoint, giving clients everything they need
	// to connect to any service without a follow-up GET request.
	// It uses spec.Endpoint directly — the same type client libraries are
	// built against — so test and production wiring are handled identically.
	Ingresses map[string]map[string]spec.Endpoint `json:"ingresses,omitempty"`
	Timestamp time.Time                            `json:"timestamp"`
}

// EventLog is a persistent, ordered event log. Events are stored in two
// separate slices — lifecycle events and log events (service.log) — sharing
// a single monotonically increasing sequence counter. This keeps hot-path
// scans (WaitFor, buildResolvedEnvironment) fast by avoiding high-volume
// log output. When the full timeline is needed (Events, Subscribe, log dump),
// both slices are zip-merged by sequence number.
type EventLog struct {
	mu        sync.RWMutex
	lifecycle []Event // everything except service.log
	logEvents []Event // service.log only
	seq       uint64
	notify    chan struct{} // closed and replaced on each new event
}

// NewEventLog creates an empty event log.
func NewEventLog() *EventLog {
	return &EventLog{
		notify: make(chan struct{}),
	}
}

// Publish appends an event to the log with the next sequence number and
// the current timestamp, then wakes all waiters.
func (l *EventLog) Publish(event Event) {
	l.mu.Lock()
	l.seq++
	event.Seq = l.seq
	if event.Timestamp.IsZero() {
		event.Timestamp = time.Now()
	}
	if event.Type == EventServiceLog {
		l.logEvents = append(l.logEvents, event)
	} else {
		l.lifecycle = append(l.lifecycle, event)
	}
	ch := l.notify
	l.notify = make(chan struct{})
	l.mu.Unlock()

	close(ch) // wake all waiters
}

// Events returns a snapshot of all events (lifecycle + log) merged by
// sequence number.
func (l *EventLog) Events() []Event {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return mergeSorted(l.lifecycle, l.logEvents)
}

// LifecycleEvents returns a snapshot of lifecycle events only, excluding
// high-volume service.log events. Use this for building resolved state
// or scanning for specific lifecycle transitions.
func (l *EventLog) LifecycleEvents() []Event {
	l.mu.RLock()
	defer l.mu.RUnlock()
	out := make([]Event, len(l.lifecycle))
	copy(out, l.lifecycle)
	return out
}

// Since returns all events (lifecycle + log) with sequence number > seq,
// merged by sequence number.
func (l *EventLog) Since(seq uint64) []Event {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.mergedSince(seq)
}

// mergedSince returns all events from both slices with Seq > seq, merged
// in sequence order. Caller must hold at least l.mu.RLock.
func (l *EventLog) mergedSince(seq uint64) []Event {
	a := sliceSince(l.lifecycle, seq)
	b := sliceSince(l.logEvents, seq)
	return mergeSorted(a, b)
}

// lifecycleSince returns lifecycle events with Seq > seq. Caller must hold
// at least l.mu.RLock.
func (l *EventLog) lifecycleSince(seq uint64) []Event {
	return sliceSince(l.lifecycle, seq)
}

// sliceSince returns events from a sorted slice with Seq > seq.
// Uses binary search since sequence numbers may have gaps.
func sliceSince(events []Event, seq uint64) []Event {
	i := sort.Search(len(events), func(i int) bool {
		return events[i].Seq > seq
	})
	if i >= len(events) {
		return nil
	}
	out := make([]Event, len(events)-i)
	copy(out, events[i:])
	return out
}

// mergeSorted merges two slices that are each sorted by Seq into a single
// sorted slice.
func mergeSorted(a, b []Event) []Event {
	if len(a) == 0 && len(b) == 0 {
		return nil
	}
	out := make([]Event, 0, len(a)+len(b))
	i, j := 0, 0
	for i < len(a) && j < len(b) {
		if a[i].Seq < b[j].Seq {
			out = append(out, a[i])
			i++
		} else {
			out = append(out, b[j])
			j++
		}
	}
	out = append(out, a[i:]...)
	out = append(out, b[j:]...)
	return out
}

// Subscribe returns a channel that receives events starting from fromSeq.
// It replays all existing events with Seq > fromSeq, then streams new events
// as they arrive. The channel is closed when ctx is cancelled.
//
// The channel is buffered (256). If a subscriber falls behind and the buffer
// fills, new events are dropped for that subscriber (publishers never block).
func (l *EventLog) Subscribe(ctx context.Context, fromSeq uint64, filter func(Event) bool) <-chan Event {
	ch := make(chan Event, 256)

	go func() {
		defer close(ch)

		cursor := fromSeq

		for {
			// Grab current state under lock.
			l.mu.RLock()
			batch := l.mergedSince(cursor)
			notify := l.notify
			l.mu.RUnlock()

			// Deliver buffered events.
			for _, e := range batch {
				if filter != nil && !filter(e) {
					cursor = e.Seq
					continue
				}
				select {
				case ch <- e:
				case <-ctx.Done():
					return
				default:
					// subscriber fell behind — drop event
				}
				cursor = e.Seq
			}

			// Wait for new events or cancellation.
			select {
			case <-notify:
				// new event published, loop again
			case <-ctx.Done():
				return
			}
		}
	}()

	return ch
}

// WaitFor scans lifecycle events for a matching event. If found, returns it
// immediately. Otherwise blocks until a matching lifecycle event is published
// or the context is cancelled. Log events (service.log) are not scanned.
func (l *EventLog) WaitFor(ctx context.Context, match func(Event) bool) (Event, error) {
	// First, scan existing lifecycle events under read lock.
	l.mu.RLock()
	for _, e := range l.lifecycle {
		if match(e) {
			l.mu.RUnlock()
			return e, nil
		}
	}
	cursor := l.seq
	notify := l.notify
	l.mu.RUnlock()

	// Not found in existing log — wait for new lifecycle events.
	for {
		select {
		case <-notify:
			l.mu.RLock()
			batch := l.lifecycleSince(cursor)
			notify = l.notify
			l.mu.RUnlock()

			for _, e := range batch {
				if match(e) {
					return e, nil
				}
				cursor = e.Seq
			}
		case <-ctx.Done():
			return Event{}, ctx.Err()
		}
	}
}

// ServiceLogTail returns the last n log lines for the named service,
// formatted with "  | " prefixes. Returns "" if there are no log events.
func (l *EventLog) ServiceLogTail(service string, n int) string {
	l.mu.RLock()
	defer l.mu.RUnlock()

	var lines []string
	for _, e := range l.logEvents {
		if e.Service != service || e.Log == nil {
			continue
		}
		for _, line := range strings.Split(strings.TrimRight(e.Log.Data, "\n"), "\n") {
			lines = append(lines, line)
		}
	}
	if len(lines) == 0 {
		return ""
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	var b strings.Builder
	for i, line := range lines {
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString("  | ")
		b.WriteString(line)
	}
	return b.String()
}
