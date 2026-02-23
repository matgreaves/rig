package server

import (
	"context"
	"testing"
	"time"

	"github.com/matgreaves/rig/internal/spec"
)

func TestProgressWatchdog_EmitsStallEvent(t *testing.T) {
	log := NewEventLog()
	services := map[string]spec.Service{
		"db":  {Type: "postgres"},
		"app": {Type: "go", Egresses: map[string]spec.EgressSpec{"db": {Service: "db"}}},
	}

	// Publish some initial events but don't reach ready.
	log.Publish(Event{Type: EventIngressPublished, Service: "db", Environment: "test"})
	log.Publish(Event{Type: EventServiceStarting, Service: "db", Environment: "test"})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go progressWatchdog(ctx, log, "test", services, 100*time.Millisecond)

	// Wait for the watchdog to fire.
	ev, err := log.WaitFor(ctx, func(e Event) bool {
		return e.Type == EventProgressStall
	})
	if err != nil {
		t.Fatalf("waiting for progress.stall: %v", err)
	}

	if ev.Diagnostic == nil {
		t.Fatal("expected diagnostic snapshot, got nil")
	}

	// app should be pending and waiting on db.
	found := false
	for _, s := range ev.Diagnostic.Services {
		if s.Name == "app" {
			found = true
			if s.Phase != "pending" {
				t.Errorf("app phase: got %q, want pending", s.Phase)
			}
			if len(s.WaitingOn) == 0 {
				t.Error("app should be waiting on db")
			}
		}
	}
	if !found {
		t.Error("app not found in diagnostic snapshot")
	}

	// db should be in starting phase.
	for _, s := range ev.Diagnostic.Services {
		if s.Name == "db" {
			if s.Phase != "starting" {
				t.Errorf("db phase: got %q, want starting", s.Phase)
			}
		}
	}
}

func TestProgressWatchdog_NoStallOnSteadyProgress(t *testing.T) {
	log := NewEventLog()
	services := map[string]spec.Service{
		"svc": {Type: "process"},
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	stallTimeout := 100 * time.Millisecond
	go progressWatchdog(ctx, log, "test", services, stallTimeout)

	// Keep publishing events faster than the stall timeout.
	for i := 0; i < 5; i++ {
		time.Sleep(stallTimeout / 3)
		log.Publish(Event{Type: EventServiceStarting, Service: "svc", Environment: "test"})
	}

	// Give one extra tick window then check.
	time.Sleep(stallTimeout / 2)

	events := log.LifecycleEvents()
	for _, e := range events {
		if e.Type == EventProgressStall {
			t.Error("unexpected progress.stall event during steady progress")
		}
	}
}

func TestProgressWatchdog_TwoServiceDependency(t *testing.T) {
	log := NewEventLog()
	services := map[string]spec.Service{
		"b": {Type: "postgres"},
		"a": {Type: "go", Egresses: map[string]spec.EgressSpec{"b": {Service: "b"}}},
	}

	// Only B publishes events — A stays pending.
	log.Publish(Event{Type: EventIngressPublished, Service: "b", Environment: "test"})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go progressWatchdog(ctx, log, "test", services, 100*time.Millisecond)

	ev, err := log.WaitFor(ctx, func(e Event) bool {
		return e.Type == EventProgressStall
	})
	if err != nil {
		t.Fatalf("waiting for progress.stall: %v", err)
	}

	if ev.Diagnostic == nil {
		t.Fatal("expected diagnostic, got nil")
	}

	// Verify A is waiting on B.
	for _, s := range ev.Diagnostic.Services {
		if s.Name == "a" {
			if s.Phase != "pending" {
				t.Errorf("a phase: got %q, want pending", s.Phase)
			}
			if len(s.WaitingOn) != 1 {
				t.Fatalf("a waiting on: got %d, want 1", len(s.WaitingOn))
			}
			if s.WaitingOn[0] != "b" {
				t.Errorf("a waiting on: got %q, want %q", s.WaitingOn[0], "b")
			}
		}
	}
}
