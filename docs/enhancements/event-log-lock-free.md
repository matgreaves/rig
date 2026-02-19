# Event Log: Lock-Free Resizable Array

## Context

`server/EventLog` is an append-only, monotonically sequenced log. The current
implementation uses a `sync.RWMutex`: multiple readers hold `RLock`
concurrently, and `Publish` holds the exclusive `Lock` for the duration of the
append. This is correct and performs well at the scale rig currently targets.

Two alternative designs eliminate `RLock` entirely by exploiting the
append-only invariant. Both are documented here should contention on the read
path ever become measurable.

---

## Option A — Mutex-on-write, atomic publication

The simplest path to lock-free reads. Writers still serialise with a mutex, but
readers never touch it.

### Structure

```go
const (
    segSize  = 1024
    maxSegs  = 1024          // upper bound: ~1 M events per log
)

type EventLog struct {
    mu       sync.Mutex                    // serialises writers only
    segments [maxSegs]*[segSize]Event      // stable addresses; never move
    len      atomic.Uint64                 // count of fully-committed events
    notify   atomic.Pointer[chan struct{}]  // closed on each new event
}
```

Chunks, once allocated, are never reallocated or moved. Element `i` is always
at `segments[i/segSize][i%segSize]`.

### Write path

```go
func (l *EventLog) Publish(event Event) {
    l.mu.Lock()

    n := l.len.Load()
    seg, off := n/segSize, n%segSize
    if l.segments[seg] == nil {
        l.segments[seg] = new([segSize]Event)
    }
    event.Seq = n + 1
    l.segments[seg][off] = event

    // Atomic store publishes the write. Go's memory model guarantees
    // everything written before this store is visible to any goroutine
    // that observes the new value via an atomic load.
    l.len.Store(n + 1)

    l.mu.Unlock()

    // Swap notify channel and wake waiters (outside lock to avoid
    // waking goroutines while the lock is still held).
    ch := make(chan struct{})
    old := l.notify.Swap(&ch)
    close(*old)
}
```

### Read path (fully lock-free)

```go
func (l *EventLog) get(i uint64) Event {
    // Safe to call without any lock if i < l.len.Load().
    return l.segments[i/segSize][i%segSize]
}

func (l *EventLog) Events() []Event {
    n := int(l.len.Load())          // atomic load — no lock
    out := make([]Event, n)
    for i := range n {
        out[i] = l.get(uint64(i))
    }
    return out
}
```

### Why this is safe

Go's memory model (go.dev/ref/mem) specifies that an `atomic.Store`
*synchronises-before* any `atomic.Load` that observes the stored value.
Because the writer stores the complete `Event` struct into the segment
**before** atomically advancing `len`, any reader that observes the new `len`
is guaranteed to see the fully-written event — no additional synchronisation
required on the read path.

### Trade-offs

| | Current (RWMutex) | Option A |
|---|---|---|
| Read overhead | RLock + RUnlock | Single atomic load |
| Write overhead | Mutex lock/unlock | Mutex lock/unlock + atomic store |
| Max events | Unlimited (slice grows) | `maxSegs × segSize` (e.g. ~1 M) |
| Code complexity | Low | Low–medium |

The fixed upper bound is the primary cost. For a test orchestrator ~1 M events
per environment is far beyond any realistic workload, so this is a practical
ceiling rather than a real constraint.

---

## Option B — Lock-free writes (multi-producer segmented vector)

Eliminates the writer mutex entirely. Multiple goroutines can publish
concurrently without serialising. This is the approach described in Dechev et
al., "Lock-Free Dynamically Resizable Arrays" (OPODIS 2006).

### Core idea

Each writer atomically claims a slot by doing a fetch-and-add on the sequence
counter, then writes into that slot and sets a per-slot `ready` flag when done.
Readers spin (or back off) on the `ready` flag for the specific slot they want.

```go
type slot struct {
    event Event
    ready atomic.Bool
}

type EventLog struct {
    segments [maxSegs]*[segSize]slot
    seq      atomic.Uint64   // next slot to claim
    // ... notify mechanism
}

func (l *EventLog) Publish(event Event) {
    i := l.seq.Add(1) - 1           // claim slot i
    seg, off := i/segSize, i%segSize
    // allocate segment if needed — requires a CAS loop
    l.segments[seg][off].event = event
    l.segments[seg][off].ready.Store(true)  // publish
    // wake waiters
}

func (l *EventLog) waitReady(i uint64) Event {
    seg, off := i/segSize, i%segSize
    for !l.segments[seg][off].ready.Load() {
        runtime.Gosched()   // or exponential back-off
    }
    return l.segments[seg][off].event
}
```

### Why this is more complex

- **Segment allocation** requires a CAS loop so two concurrent writers racing
  to allocate the same segment don't both succeed.
- **Readers cannot simply load `seq`** and assume all prior slots are ready —
  a slow writer may have claimed slot 5 but not yet written it while slot 6 is
  already ready. `WaitFor` and `Subscribe` must wait on the `ready` flag for
  each slot in order, or maintain a separate "committed watermark" that
  advances only when all preceding slots are also ready (reintroducing some
  coordination).
- **Spinning** on `ready` flags burns CPU; back-off strategies add latency.

### When this is appropriate

Option B makes sense when the write path is the bottleneck — i.e., many
goroutines publishing events at high frequency and a single mutex causing
measurable queue depth. In rig's current architecture each environment has at
most one event in-flight per service lifecycle step, so write contention is
negligible.

---

## Recommendation

Stick with `sync.RWMutex` until profiling shows read-path contention. If that
point arrives, **Option A** (mutex-on-write, atomic publication) is the right
first step: the change is small, the reasoning is straightforward, and it
eliminates all reader locking with a fixed capacity that is not a real
constraint in practice.

Option B is only warranted if write-side serialisation becomes a bottleneck,
which would require a very different usage pattern than rig currently has.
