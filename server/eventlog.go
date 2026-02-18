package server

import (
	"context"
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
	EventEnvironmentUp   EventType = "environment.up"
	EventEnvironmentDown EventType = "environment.down"
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
	Timestamp   time.Time         `json:"timestamp"`
}

// EventLog is a persistent, ordered event log. Events are appended with
// monotonically increasing sequence numbers. Subscribers can replay from
// any point. WaitFor scans the existing log before blocking.
type EventLog struct {
	mu     sync.Mutex
	events []Event
	seq    uint64
	notify chan struct{} // closed and replaced on each new event
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
	l.events = append(l.events, event)
	ch := l.notify
	l.notify = make(chan struct{})
	l.mu.Unlock()

	close(ch) // wake all waiters
}

// Events returns a snapshot of all events in the log.
func (l *EventLog) Events() []Event {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]Event, len(l.events))
	copy(out, l.events)
	return out
}

// Since returns all events with sequence number > seq.
func (l *EventLog) Since(seq uint64) []Event {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.eventsSince(seq)
}

// eventsSince returns events with Seq > seq. Caller must hold l.mu.
// Seq numbers are 1-indexed and contiguous, so events after seq start
// at slice index seq.
func (l *EventLog) eventsSince(seq uint64) []Event {
	start := int(seq)
	if start >= len(l.events) {
		return nil
	}
	out := make([]Event, len(l.events)-start)
	copy(out, l.events[start:])
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
			l.mu.Lock()
			batch := l.eventsSince(cursor)
			notify := l.notify
			l.mu.Unlock()

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

// WaitFor scans the existing log for a matching event. If found, returns it
// immediately. Otherwise blocks until a matching event is published or the
// context is cancelled.
func (l *EventLog) WaitFor(ctx context.Context, match func(Event) bool) (Event, error) {
	// First, scan existing events under lock.
	l.mu.Lock()
	for _, e := range l.events {
		if match(e) {
			l.mu.Unlock()
			return e, nil
		}
	}
	cursor := l.seq
	notify := l.notify
	l.mu.Unlock()

	// Not found in existing log — wait for new events.
	for {
		select {
		case <-notify:
			l.mu.Lock()
			batch := l.eventsSince(cursor)
			notify = l.notify
			l.mu.Unlock()

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
