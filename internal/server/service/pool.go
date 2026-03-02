package service

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// Backend defines the lifecycle operations for a pooled resource.
type Backend interface {
	// Start brings the backend up. Called once per key (e.g. per image).
	// Returns host, port for the running backend.
	Start(ctx context.Context) (host string, port int, err error)

	// Stop tears down the backend. Called when idle timer fires or on Close.
	Stop()

	// NewLease creates per-test isolation (e.g. CREATE DATABASE).
	// Returns a lease ID (e.g. "rig_3") and opaque data (e.g. container name).
	NewLease(ctx context.Context) (id string, data any, err error)

	// DropLease cleans up per-test isolation (e.g. DROP DATABASE).
	// Best-effort — called during Release, errors are not observed.
	DropLease(ctx context.Context, id string)
}

// Pool manages shared backend instances. Each unique key gets one instance;
// individual test environments get isolated leases within it.
type Pool struct {
	mu        sync.Mutex
	instances map[string]*instance
	factory   func(key string) Backend
	idleTime  time.Duration
}

// NewPool creates a new pool. The factory function creates a Backend for each
// unique key. idleTime controls how long an instance lingers after its last
// lease is released before being stopped.
func NewPool(factory func(key string) Backend, idleTime time.Duration) *Pool {
	return &Pool{
		instances: make(map[string]*instance),
		factory:   factory,
		idleTime:  idleTime,
	}
}

// instanceState represents the lifecycle state of an instance.
type instanceState int

const (
	instanceStateNew      instanceState = iota // not yet started
	instanceStateStarting                      // start in progress
	instanceStateReady                         // backend is healthy
	instanceStateFailed                        // start failed (retryable)
)

// instance represents a single shared backend for one key.
type instance struct {
	key     string
	backend Backend
	host    string
	port    int

	mu        sync.Mutex
	refCount  int
	idleTimer *time.Timer
	state     instanceState
	startErr  error
	ready     chan struct{} // closed when state transitions to Ready or Failed

	pool *Pool // back-reference for cleanup
}

// Lease represents a single test environment's claim on a shared instance.
type Lease struct {
	ID       string // e.g. "rig_3"
	Host     string // e.g. "127.0.0.1"
	Port     int    // e.g. 54321
	Data     any    // backend-specific (e.g. container name for Postgres)
	instance *instance
}

// Acquire returns a lease for the backend identified by key.
// The first call for a key starts the backend; subsequent calls reuse it.
// If the backend start fails, the instance resets to allow retry by the
// next caller — a transient failure or cancelled context doesn't poison the pool.
func (p *Pool) Acquire(ctx context.Context, key string) (*Lease, error) {
	p.mu.Lock()
	inst, ok := p.instances[key]
	if !ok {
		inst = &instance{
			key:     key,
			backend: p.factory(key),
			ready:   make(chan struct{}),
			pool:    p,
		}
		p.instances[key] = inst
	}
	p.mu.Unlock()

	// Ensure the backend is started. Only one goroutine drives the start;
	// others wait on the ready channel. On failure the state resets so the
	// next Acquire retries.
	inst.mu.Lock()
	switch inst.state {
	case instanceStateReady:
		// Already running — fall through to wait.
		inst.mu.Unlock()

	case instanceStateStarting:
		// Another goroutine is starting — fall through to wait.
		inst.mu.Unlock()

	case instanceStateNew, instanceStateFailed:
		// We drive the start. Use a detached context so a single
		// environment's cancellation doesn't kill the shared backend.
		inst.state = instanceStateStarting
		inst.startErr = nil
		readyCh := inst.ready
		inst.mu.Unlock()

		host, port, err := inst.backend.Start(context.Background())

		inst.mu.Lock()
		if err != nil {
			inst.state = instanceStateFailed
			inst.startErr = err
			// Replace the ready channel so future waiters get a fresh one
			// after we unblock current waiters below.
			inst.ready = make(chan struct{})
			inst.mu.Unlock()
			// Unblock anyone waiting on the old channel by closing it.
			// They'll check startErr and see the failure.
			close(readyCh)
			return nil, fmt.Errorf("pool: start backend for %s: %w", key, err)
		}
		inst.host = host
		inst.port = port
		inst.state = instanceStateReady
		inst.mu.Unlock()
		close(readyCh)

	default:
		inst.mu.Unlock()
	}

	// Wait for the backend to be ready (or fail).
	inst.mu.Lock()
	readyCh := inst.ready
	inst.mu.Unlock()

	select {
	case <-readyCh:
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	// Check if the start that just completed actually failed.
	inst.mu.Lock()
	if inst.state == instanceStateFailed {
		err := inst.startErr
		inst.mu.Unlock()
		return nil, fmt.Errorf("pool: start backend for %s: %w", key, err)
	}

	// Cancel any pending idle timer and increment refcount.
	if inst.idleTimer != nil {
		inst.idleTimer.Stop()
		inst.idleTimer = nil
	}
	inst.refCount++
	inst.mu.Unlock()

	id, data, err := inst.backend.NewLease(ctx)
	if err != nil {
		// NewLease failed — decrement refcount and possibly start idle timer.
		inst.mu.Lock()
		inst.refCount--
		remaining := inst.refCount
		inst.mu.Unlock()
		if remaining <= 0 {
			inst.mu.Lock()
			inst.idleTimer = time.AfterFunc(p.idleTime, func() {
				p.removeInstance(inst)
			})
			inst.mu.Unlock()
		}
		return nil, fmt.Errorf("pool: new lease for %s: %w", key, err)
	}

	return &Lease{
		ID:       id,
		Host:     inst.host,
		Port:     inst.port,
		Data:     data,
		instance: inst,
	}, nil
}

// Release drops a lease, cleaning up per-test isolation and decrementing
// the refcount. When refcount hits zero, an idle timer starts;
// if no new Acquire happens before it fires, the backend is stopped.
func (p *Pool) Release(lease *Lease) {
	inst := lease.instance

	// Best-effort DropLease — ignore errors (backend may be shutting down).
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	inst.backend.DropLease(ctx, lease.ID)

	inst.mu.Lock()
	inst.refCount--
	remaining := inst.refCount
	inst.mu.Unlock()

	if remaining <= 0 {
		inst.mu.Lock()
		inst.idleTimer = time.AfterFunc(p.idleTime, func() {
			p.removeInstance(inst)
		})
		inst.mu.Unlock()
	}
}

// Close stops all backend instances. Called on server shutdown.
func (p *Pool) Close() {
	p.mu.Lock()
	instances := make([]*instance, 0, len(p.instances))
	for _, inst := range p.instances {
		instances = append(instances, inst)
	}
	p.instances = make(map[string]*instance)
	p.mu.Unlock()

	for _, inst := range instances {
		inst.mu.Lock()
		if inst.idleTimer != nil {
			inst.idleTimer.Stop()
			inst.idleTimer = nil
		}
		inst.mu.Unlock()
		inst.backend.Stop()
	}
}

// removeInstance stops a backend and cleans up the pool entry.
func (p *Pool) removeInstance(inst *instance) {
	p.mu.Lock()
	// Only remove if refcount is still 0 (a new Acquire may have arrived).
	inst.mu.Lock()
	if inst.refCount > 0 {
		inst.mu.Unlock()
		p.mu.Unlock()
		return
	}
	inst.mu.Unlock()
	delete(p.instances, inst.key)
	p.mu.Unlock()

	inst.backend.Stop()
}
