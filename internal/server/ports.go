package server

import (
	"fmt"
	"net"
	"sync"
)

// PortAllocator allocates random OS-assigned ports and tracks which ports
// belong to which environment instance. This prevents the same rigd from
// handing out a port that is already in use by another active environment.
type PortAllocator struct {
	mu        sync.Mutex
	allocated map[int]string   // port → instance ID
	byInstance map[string][]int // instance ID → ports (reverse index for O(k) release)
}

// NewPortAllocator creates an empty port allocator.
func NewPortAllocator() *PortAllocator {
	return &PortAllocator{
		allocated:  make(map[int]string),
		byInstance: make(map[string][]int),
	}
}

// Allocate reserves n random ports for the given instance. It binds to :0
// to let the OS assign free ports, records them, then closes the listeners
// and returns the ports.
//
// There is a small TOCTOU window between closing the listener and the
// service actually binding the port. In practice this is negligible.
func (a *PortAllocator) Allocate(instanceID string, n int) ([]int, error) {
	if n <= 0 {
		return nil, nil
	}

	listeners := make([]net.Listener, 0, n)
	ports := make([]int, 0, n)

	// Open all listeners first to get guaranteed-unique ports from the OS.
	for range n {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			// Clean up any listeners we already opened.
			for _, l := range listeners {
				l.Close()
			}
			return nil, fmt.Errorf("allocate port: %w", err)
		}
		listeners = append(listeners, ln)
		ports = append(ports, ln.Addr().(*net.TCPAddr).Port)
	}

	// Close all listeners so the ports are available for services.
	for _, ln := range listeners {
		ln.Close()
	}

	// Record the allocation. Check all ports before writing any to avoid
	// partial state if a collision is detected.
	a.mu.Lock()
	defer a.mu.Unlock()

	for _, port := range ports {
		if existingID, ok := a.allocated[port]; ok {
			// Extremely unlikely — the OS just gave us this port.
			// Defensive check in case the OS reuses a port from a
			// recently-released but not-yet-cleaned-up instance.
			return nil, fmt.Errorf("port %d already allocated to instance %q", port, existingID)
		}
	}
	for _, port := range ports {
		a.allocated[port] = instanceID
	}
	a.byInstance[instanceID] = append(a.byInstance[instanceID], ports...)

	return ports, nil
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
