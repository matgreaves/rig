package server_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/matgreaves/rig/server"
)

// BenchmarkPublish measures append throughput for lifecycle events.
func BenchmarkPublish(b *testing.B) {
	log := server.NewEventLog()
	e := server.Event{Type: server.EventServiceStarting, Service: "svc"}

	b.ResetTimer()
	for range b.N {
		log.Publish(e)
	}
}

// BenchmarkPublishLog measures append throughput for service.log events.
func BenchmarkPublishLog(b *testing.B) {
	log := server.NewEventLog()
	e := server.Event{
		Type:    server.EventServiceLog,
		Service: "svc",
		Log:     &server.LogEntry{Stream: "stdout", Data: "some log line output"},
	}

	b.ResetTimer()
	for range b.N {
		log.Publish(e)
	}
}

// BenchmarkPublishMixed measures append throughput with interleaved lifecycle
// and log events (3:1 log-to-lifecycle ratio).
func BenchmarkPublishMixed(b *testing.B) {
	log := server.NewEventLog()
	lifecycle := server.Event{Type: server.EventServiceStarting, Service: "svc"}
	logEvt := server.Event{
		Type:    server.EventServiceLog,
		Service: "svc",
		Log:     &server.LogEntry{Stream: "stdout", Data: "line"},
	}

	b.ResetTimer()
	for i := range b.N {
		if i%4 == 0 {
			log.Publish(lifecycle)
		} else {
			log.Publish(logEvt)
		}
	}
}

// BenchmarkEvents measures the cost of merging lifecycle + log slices at
// various sizes.
func BenchmarkEvents(b *testing.B) {
	for _, size := range []struct {
		lifecycle int
		logs      int
	}{
		{100, 0},
		{100, 1000},
		{100, 10000},
		{1000, 10000},
	} {
		name := fmt.Sprintf("lifecycle=%d/logs=%d", size.lifecycle, size.logs)
		b.Run(name, func(b *testing.B) {
			log := server.NewEventLog()
			for range size.lifecycle {
				log.Publish(server.Event{Type: server.EventServiceStarting, Service: "svc"})
			}
			for range size.logs {
				log.Publish(server.Event{
					Type:    server.EventServiceLog,
					Service: "svc",
					Log:     &server.LogEntry{Stream: "stdout", Data: "line"},
				})
			}

			b.ResetTimer()
			for range b.N {
				_ = log.Events()
			}
		})
	}
}

// BenchmarkLifecycleEvents measures the cost of the lifecycle-only snapshot
// (no merge needed).
func BenchmarkLifecycleEvents(b *testing.B) {
	for _, size := range []struct {
		lifecycle int
		logs      int
	}{
		{100, 0},
		{100, 10000},
	} {
		name := fmt.Sprintf("lifecycle=%d/logs=%d", size.lifecycle, size.logs)
		b.Run(name, func(b *testing.B) {
			log := server.NewEventLog()
			for range size.lifecycle {
				log.Publish(server.Event{Type: server.EventServiceStarting, Service: "svc"})
			}
			for range size.logs {
				log.Publish(server.Event{
					Type:    server.EventServiceLog,
					Service: "svc",
					Log:     &server.LogEntry{Stream: "stdout", Data: "line"},
				})
			}

			b.ResetTimer()
			for range b.N {
				_ = log.LifecycleEvents()
			}
		})
	}
}

// BenchmarkWaitFor measures the cost of scanning lifecycle events for a match,
// with varying amounts of log noise. The split design means log volume should
// not affect WaitFor performance.
func BenchmarkWaitFor(b *testing.B) {
	for _, size := range []struct {
		lifecycle int
		logs      int
	}{
		{100, 0},
		{100, 10000},
	} {
		name := fmt.Sprintf("lifecycle=%d/logs=%d", size.lifecycle, size.logs)
		b.Run(name, func(b *testing.B) {
			log := server.NewEventLog()
			for i := range size.lifecycle - 1 {
				log.Publish(server.Event{
					Type:    server.EventServiceStarting,
					Service: fmt.Sprintf("svc-%d", i),
				})
			}
			for range size.logs {
				log.Publish(server.Event{
					Type:    server.EventServiceLog,
					Service: "svc",
					Log:     &server.LogEntry{Stream: "stdout", Data: "line"},
				})
			}
			// Publish the target event last so WaitFor scans the full lifecycle slice.
			log.Publish(server.Event{
				Type:    server.EventServiceReady,
				Service: "target",
			})

			ctx := context.Background()
			match := func(e server.Event) bool {
				return e.Type == server.EventServiceReady && e.Service == "target"
			}

			b.ResetTimer()
			for range b.N {
				_, _ = log.WaitFor(ctx, match)
			}
		})
	}
}

// BenchmarkSubscribe measures delivery throughput through the subscriber channel.
// Uses 200 preloaded events (under the 256-entry channel buffer) so none are
// dropped by the non-blocking send in Subscribe.
func BenchmarkSubscribe(b *testing.B) {
	log := server.NewEventLog()

	const preload = 200
	for range preload {
		log.Publish(server.Event{Type: server.EventServiceStarting, Service: "svc"})
	}

	b.ResetTimer()
	for range b.N {
		ctx, cancel := context.WithCancel(context.Background())
		ch := log.Subscribe(ctx, 0, nil)

		count := 0
		for range ch {
			count++
			if count >= preload {
				break
			}
		}
		cancel()
	}
}
