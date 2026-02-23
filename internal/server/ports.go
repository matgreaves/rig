package server

import (
	"fmt"
	"math/big"
	"math/rand/v2"
	"net"
	"sync"
)

const (
	portBase  = 0x2000 // 8192
	portCount = 0x8000 - portBase // 24576
)

// PortAllocator allocates ports using a prime-stepping strategy that spreads
// allocations across the port range, minimising collisions in parallel tests.
// Ports are returned as open net.Listeners — callers that need zero TOCTOU
// (proxies) can use the listener directly; callers that need a raw port number
// close the listener and use the port.
type PortAllocator struct {
	mu         sync.Mutex
	allocated  map[int]string   // port → instance ID
	byInstance map[string][]int // instance ID → ports (reverse index for O(k) release)
	offset     uint64
	step       uint64 // random prime
}

// NewPortAllocator creates an empty port allocator.
func NewPortAllocator() *PortAllocator {
	return &PortAllocator{
		allocated:  make(map[int]string),
		byInstance: make(map[string][]int),
		offset:     rand.Uint64N(portCount),
		step:       randomPrime(portCount),
	}
}

// Allocate reserves n ports for the given instance. It steps through the port
// range by a random prime, trying net.Listen on each candidate. Listeners are
// returned open — the caller decides whether to keep them (proxy) or close
// them (service port).
func (a *PortAllocator) Allocate(instanceID string, n int) ([]net.Listener, error) {
	if n <= 0 {
		return nil, nil
	}

	// Lock held for the full loop including net.Listen calls. This serialises
	// concurrent Allocate callers but n is small (1–5) and listens are on
	// localhost, so the hold time is negligible in practice.
	a.mu.Lock()
	defer a.mu.Unlock()

	listeners := make([]net.Listener, 0, n)
	ports := make([]int, 0, n)

	cleanup := func() {
		for _, ln := range listeners {
			ln.Close()
		}
	}

	for range n {
		found := false
		for range portCount {
			port := portBase + int(a.offset%uint64(portCount))
			a.offset += a.step

			if _, taken := a.allocated[port]; taken {
				continue
			}

			ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
			if err != nil {
				continue // port busy outside our tracking
			}

			listeners = append(listeners, ln)
			ports = append(ports, port)
			found = true
			break
		}
		if !found {
			cleanup()
			return nil, fmt.Errorf("allocate port: exhausted %d candidates", portCount)
		}
	}

	for _, port := range ports {
		a.allocated[port] = instanceID
	}
	a.byInstance[instanceID] = append(a.byInstance[instanceID], ports...)

	return listeners, nil
}

// Release removes all port tracking for the given instance.
func (a *PortAllocator) Release(instanceID string) {
	a.mu.Lock()
	defer a.mu.Unlock()

	for _, port := range a.byInstance[instanceID] {
		delete(a.allocated, port)
	}
	delete(a.byInstance, instanceID)
}

// Allocated returns the number of currently tracked ports.
func (a *PortAllocator) Allocated() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.allocated)
}

// randomPrime returns a random prime in [2, max).
func randomPrime(max uint64) uint64 {
	for {
		n := 2 + rand.Uint64N(max-2)
		if big.NewInt(int64(n)).ProbablyPrime(20) {
			return n
		}
	}
}
