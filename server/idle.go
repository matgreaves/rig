package server

import (
	"sync"
	"time"
)

// IdleTimer fires a shutdown signal after a configurable period with no active
// environments. EnvironmentCreated/EnvironmentDestroyed keep the count; once
// the count returns to zero the countdown restarts.
type IdleTimer struct {
	mu       sync.Mutex
	active   int
	timeout  time.Duration
	timer    *time.Timer
	shutdown chan struct{}
	once     sync.Once
}

// NewIdleTimer creates an IdleTimer that will fire after timeout if no
// environments are created first. Pass zero to disable (the timer never fires).
func NewIdleTimer(timeout time.Duration) *IdleTimer {
	t := &IdleTimer{
		timeout:  timeout,
		shutdown: make(chan struct{}),
	}
	if timeout > 0 {
		t.timer = time.AfterFunc(timeout, t.fire)
	}
	return t
}

func (t *IdleTimer) fire() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.active == 0 {
		t.once.Do(func() { close(t.shutdown) })
	}
}

// EnvironmentCreated records a new active environment and stops the countdown.
func (t *IdleTimer) EnvironmentCreated() {
	if t.timeout == 0 {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.active++
	t.timer.Stop()
}

// EnvironmentDestroyed records an environment teardown. If no environments
// remain the countdown restarts.
func (t *IdleTimer) EnvironmentDestroyed() {
	if t.timeout == 0 {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.active--
	if t.active == 0 {
		t.timer.Reset(t.timeout)
	}
}

// ShutdownCh returns a channel that is closed when the idle timeout fires.
func (t *IdleTimer) ShutdownCh() <-chan struct{} {
	return t.shutdown
}
