package server

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/matgreaves/rig/internal/spec"
)

// progressWatchdog monitors the event log for progress stalls. If no new
// lifecycle events appear within stallTimeout, it publishes a progress.stall
// event with a diagnostic snapshot showing which services are stuck and why.
//
// The goroutine exits when ctx is cancelled (i.e. the service phase ends)
// or when all services have reached a terminal phase (ready/failed/stopped).
func progressWatchdog(ctx context.Context, log *EventLog, envName string, services map[string]spec.Service, stallTimeout time.Duration) {
	ticker := time.NewTicker(stallTimeout)
	defer ticker.Stop()

	// Track the max lifecycle seq seen on the previous tick.
	var lastMaxSeq uint64

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}

		events := log.LifecycleEvents()
		var maxSeq uint64
		for _, e := range events {
			if e.Seq > maxSeq {
				maxSeq = e.Seq
			}
		}

		if maxSeq == lastMaxSeq && len(events) > 0 {
			// No progress since last tick — build snapshot.
			snapshot := buildDiagnosticSnapshot(events, services, stallTimeout)
			if len(snapshot.Services) == 0 {
				// All services are in terminal phases — nothing is stuck.
				return
			}
			log.Publish(Event{
				Type:        EventProgressStall,
				Environment: envName,
				Diagnostic:  &snapshot,
				Message:     formatStallMessage(&snapshot),
			})
		}

		lastMaxSeq = maxSeq
	}
}

// phaseFromEvents returns the current phase string for a service based on
// the most recent lifecycle event for that service.
func phaseFromEvents(serviceName string, events []Event) string {
	phase := "pending"
	for _, e := range events {
		if e.Service != serviceName {
			continue
		}
		switch e.Type {
		case EventIngressPublished:
			phase = "published"
		case EventWiringResolved:
			phase = "wiring_resolved"
		case EventServicePrestart:
			phase = "prestart"
		case EventServiceStarting:
			phase = "starting"
		case EventServiceHealthy:
			phase = "healthy"
		case EventServiceInit:
			phase = "init"
		case EventServiceReady:
			phase = "ready"
		case EventServiceFailed:
			phase = "failed"
		case EventServiceStopping:
			phase = "stopping"
		case EventServiceStopped:
			phase = "stopped"
		}
	}
	return phase
}

// buildDiagnosticSnapshot scans lifecycle events to determine each service's
// current phase. For services stuck in early phases (pending/published), it
// checks their egress targets to populate WaitingOn.
func buildDiagnosticSnapshot(events []Event, services map[string]spec.Service, stalledFor time.Duration) DiagnosticSnapshot {
	// Build phase map for all services.
	phases := make(map[string]string, len(services))
	for name := range services {
		phases[name] = phaseFromEvents(name, events)
	}

	// Build snapshot — skip services that are already ready/stopped/failed.
	var snapshots []ServiceSnapshot
	names := sortedServiceNames(services)
	for _, name := range names {
		phase := phases[name]
		if phase == "ready" || phase == "failed" || phase == "stopped" || phase == "stopping" {
			continue
		}

		ss := ServiceSnapshot{
			Name:  name,
			Phase: phase,
		}

		// For services stuck in early phases, check what they're waiting on.
		if phase == "pending" || phase == "published" {
			svc := services[name]
			for _, egress := range svc.Egresses {
				targetPhase := phases[egress.Service]
				if targetPhase != "ready" {
					ss.WaitingOn = append(ss.WaitingOn, egress.Service)
				}
			}
			sort.Strings(ss.WaitingOn)
		}

		snapshots = append(snapshots, ss)
	}

	return DiagnosticSnapshot{
		StalledFor: stalledFor.String(),
		Services:   snapshots,
	}
}

// formatStallMessage renders a DiagnosticSnapshot as a human-readable string.
// This is included in the event's Message field so client SDKs can use it
// directly without reimplementing the formatting logic.
func formatStallMessage(d *DiagnosticSnapshot) string {
	var b strings.Builder
	fmt.Fprintf(&b, "no progress for %s:", d.StalledFor)
	for _, svc := range d.Services {
		fmt.Fprintf(&b, "\n  %s: %s", svc.Name, svc.Phase)
		if len(svc.WaitingOn) > 0 {
			b.WriteString(" — waiting on ")
			b.WriteString(strings.Join(svc.WaitingOn, ", "))
		}
	}
	return b.String()
}
