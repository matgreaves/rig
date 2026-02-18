package server_test

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/matgreaves/rig/server"
)

func TestEventLog_PublishAndEvents(t *testing.T) {
	log := server.NewEventLog()

	log.Publish(server.Event{Type: server.EventServiceStarting, Service: "a"})
	log.Publish(server.Event{Type: server.EventServiceReady, Service: "a"})

	events := log.Events()
	if len(events) != 2 {
		t.Fatalf("expected 2 events, got %d", len(events))
	}
	if events[0].Seq != 1 || events[1].Seq != 2 {
		t.Errorf("sequence numbers: got %d, %d", events[0].Seq, events[1].Seq)
	}
	if events[0].Type != server.EventServiceStarting {
		t.Errorf("event 0 type: got %q", events[0].Type)
	}
	if events[1].Type != server.EventServiceReady {
		t.Errorf("event 1 type: got %q", events[1].Type)
	}
}

func TestEventLog_PublishSetsTimestamp(t *testing.T) {
	log := server.NewEventLog()

	before := time.Now()
	log.Publish(server.Event{Type: server.EventServiceStarting})
	after := time.Now()

	events := log.Events()
	if events[0].Timestamp.Before(before) || events[0].Timestamp.After(after) {
		t.Errorf("timestamp %v not between %v and %v", events[0].Timestamp, before, after)
	}
}

func TestEventLog_PublishPreservesExplicitTimestamp(t *testing.T) {
	log := server.NewEventLog()

	ts := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	log.Publish(server.Event{Type: server.EventServiceStarting, Timestamp: ts})

	events := log.Events()
	if !events[0].Timestamp.Equal(ts) {
		t.Errorf("expected preserved timestamp %v, got %v", ts, events[0].Timestamp)
	}
}

func TestEventLog_Since(t *testing.T) {
	log := server.NewEventLog()

	log.Publish(server.Event{Type: server.EventServiceStarting, Service: "a"})
	log.Publish(server.Event{Type: server.EventServiceReady, Service: "a"})
	log.Publish(server.Event{Type: server.EventServiceStarting, Service: "b"})

	events := log.Since(1) // events after seq 1
	if len(events) != 2 {
		t.Fatalf("expected 2 events after seq 1, got %d", len(events))
	}
	if events[0].Seq != 2 {
		t.Errorf("first event seq: got %d, want 2", events[0].Seq)
	}
	if events[1].Seq != 3 {
		t.Errorf("second event seq: got %d, want 3", events[1].Seq)
	}
}

func TestEventLog_SinceBeyondEnd(t *testing.T) {
	log := server.NewEventLog()

	log.Publish(server.Event{Type: server.EventServiceStarting})

	events := log.Since(5)
	if len(events) != 0 {
		t.Errorf("expected no events after seq 5, got %d", len(events))
	}
}

func TestEventLog_SinceZero(t *testing.T) {
	log := server.NewEventLog()

	log.Publish(server.Event{Type: server.EventServiceStarting})
	log.Publish(server.Event{Type: server.EventServiceReady})

	events := log.Since(0)
	if len(events) != 2 {
		t.Fatalf("expected all 2 events from seq 0, got %d", len(events))
	}
}

func TestEventLog_WaitFor_ExistingEvent(t *testing.T) {
	log := server.NewEventLog()

	log.Publish(server.Event{Type: server.EventServiceReady, Service: "a"})
	log.Publish(server.Event{Type: server.EventServiceStarting, Service: "b"})

	ctx := context.Background()
	event, err := log.WaitFor(ctx, func(e server.Event) bool {
		return e.Type == server.EventServiceReady && e.Service == "a"
	})
	if err != nil {
		t.Fatal(err)
	}
	if event.Service != "a" {
		t.Errorf("service: got %q", event.Service)
	}
}

func TestEventLog_WaitFor_FutureEvent(t *testing.T) {
	log := server.NewEventLog()

	var wg sync.WaitGroup
	wg.Add(1)

	var got server.Event
	var gotErr error

	go func() {
		defer wg.Done()
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		got, gotErr = log.WaitFor(ctx, func(e server.Event) bool {
			return e.Type == server.EventServiceReady && e.Service == "b"
		})
	}()

	// Give the goroutine time to start waiting.
	time.Sleep(10 * time.Millisecond)

	// Publish a non-matching event first.
	log.Publish(server.Event{Type: server.EventServiceStarting, Service: "a"})
	// Then the matching event.
	log.Publish(server.Event{Type: server.EventServiceReady, Service: "b"})

	wg.Wait()

	if gotErr != nil {
		t.Fatal(gotErr)
	}
	if got.Service != "b" || got.Type != server.EventServiceReady {
		t.Errorf("got event: type=%q service=%q", got.Type, got.Service)
	}
}

func TestEventLog_WaitFor_ContextCancelled(t *testing.T) {
	log := server.NewEventLog()

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	_, err := log.WaitFor(ctx, func(e server.Event) bool {
		return e.Type == server.EventServiceReady // never published
	})

	if err == nil {
		t.Fatal("expected error from cancelled context")
	}
	if err != context.DeadlineExceeded {
		t.Errorf("expected DeadlineExceeded, got %v", err)
	}
}

func TestEventLog_Subscribe_Replay(t *testing.T) {
	log := server.NewEventLog()

	log.Publish(server.Event{Type: server.EventServiceStarting, Service: "a"})
	log.Publish(server.Event{Type: server.EventServiceReady, Service: "a"})

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	ch := log.Subscribe(ctx, 0, nil) // replay all

	var events []server.Event
	for i := 0; i < 2; i++ {
		select {
		case e := <-ch:
			events = append(events, e)
		case <-ctx.Done():
			t.Fatal("timed out waiting for events")
		}
	}

	if len(events) != 2 {
		t.Fatalf("expected 2 events, got %d", len(events))
	}
	if events[0].Seq != 1 || events[1].Seq != 2 {
		t.Errorf("sequences: got %d, %d", events[0].Seq, events[1].Seq)
	}
}

func TestEventLog_Subscribe_ReplayFromMiddle(t *testing.T) {
	log := server.NewEventLog()

	log.Publish(server.Event{Type: server.EventServiceStarting, Service: "a"})
	log.Publish(server.Event{Type: server.EventServiceReady, Service: "a"})
	log.Publish(server.Event{Type: server.EventServiceStarting, Service: "b"})

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	ch := log.Subscribe(ctx, 1, nil) // events after seq 1

	var events []server.Event
	for i := 0; i < 2; i++ {
		select {
		case e := <-ch:
			events = append(events, e)
		case <-ctx.Done():
			t.Fatal("timed out waiting for events")
		}
	}

	if events[0].Seq != 2 {
		t.Errorf("first event seq: got %d, want 2", events[0].Seq)
	}
}

func TestEventLog_Subscribe_LiveEvents(t *testing.T) {
	log := server.NewEventLog()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	ch := log.Subscribe(ctx, 0, nil)

	// Publish after subscribing.
	go func() {
		time.Sleep(10 * time.Millisecond)
		log.Publish(server.Event{Type: server.EventServiceReady, Service: "a"})
	}()

	select {
	case e := <-ch:
		if e.Service != "a" {
			t.Errorf("service: got %q", e.Service)
		}
	case <-ctx.Done():
		t.Fatal("timed out waiting for live event")
	}
}

func TestEventLog_Subscribe_Filter(t *testing.T) {
	log := server.NewEventLog()

	log.Publish(server.Event{Type: server.EventServiceStarting, Service: "a"})
	log.Publish(server.Event{Type: server.EventServiceReady, Service: "a"})
	log.Publish(server.Event{Type: server.EventServiceStarting, Service: "b"})

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	ch := log.Subscribe(ctx, 0, func(e server.Event) bool {
		return e.Type == server.EventServiceReady
	})

	select {
	case e := <-ch:
		if e.Type != server.EventServiceReady {
			t.Errorf("type: got %q", e.Type)
		}
	case <-ctx.Done():
		t.Fatal("timed out waiting for filtered event")
	}
}

func TestEventLog_Subscribe_ClosedOnCancel(t *testing.T) {
	log := server.NewEventLog()

	ctx, cancel := context.WithCancel(context.Background())
	ch := log.Subscribe(ctx, 0, nil)

	cancel()

	// Channel should eventually close.
	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	select {
	case _, ok := <-ch:
		if ok {
			// Got an event, that's fine — drain and check for close.
			select {
			case _, ok := <-ch:
				if ok {
					t.Error("expected channel to close after context cancellation")
				}
			case <-timer.C:
				t.Error("channel not closed after cancel")
			}
		}
	case <-timer.C:
		t.Error("channel not closed after cancel")
	}
}

func TestEventLog_EventsSnapshotIsIndependent(t *testing.T) {
	log := server.NewEventLog()

	log.Publish(server.Event{Type: server.EventServiceStarting})

	snapshot := log.Events()

	log.Publish(server.Event{Type: server.EventServiceReady})

	if len(snapshot) != 1 {
		t.Errorf("snapshot should not grow: got %d", len(snapshot))
	}

	all := log.Events()
	if len(all) != 2 {
		t.Errorf("full log should have 2 events: got %d", len(all))
	}
}

func TestEventLog_ConcurrentPublish(t *testing.T) {
	log := server.NewEventLog()

	const n = 100
	var wg sync.WaitGroup
	wg.Add(n)

	for i := range n {
		go func(i int) {
			defer wg.Done()
			log.Publish(server.Event{
				Type:    server.EventServiceStarting,
				Service: fmt.Sprintf("svc-%d", i),
			})
		}(i)
	}

	wg.Wait()

	events := log.Events()
	if len(events) != n {
		t.Fatalf("expected %d events, got %d", n, len(events))
	}

	// Verify sequence numbers are unique and monotonic.
	seen := make(map[uint64]bool)
	for _, e := range events {
		if seen[e.Seq] {
			t.Errorf("duplicate seq: %d", e.Seq)
		}
		seen[e.Seq] = true
	}
	for i := 1; i <= n; i++ {
		if !seen[uint64(i)] {
			t.Errorf("missing seq: %d", i)
		}
	}
}
