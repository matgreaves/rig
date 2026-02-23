package server_test

import (
	"net"
	"testing"

	"github.com/matgreaves/rig/internal/server"
)

func listenersToPortsAndClose(t *testing.T, lns []net.Listener) []int {
	t.Helper()
	ports := make([]int, len(lns))
	for i, ln := range lns {
		ports[i] = ln.Addr().(*net.TCPAddr).Port
		ln.Close()
	}
	return ports
}

func TestPortAllocator_AllocateReturnsUniquePorts(t *testing.T) {
	alloc := server.NewPortAllocator()

	lns, err := alloc.Allocate("inst-1", 3)
	if err != nil {
		t.Fatal(err)
	}
	ports := listenersToPortsAndClose(t, lns)
	if len(ports) != 3 {
		t.Fatalf("expected 3 ports, got %d", len(ports))
	}

	seen := make(map[int]bool)
	for _, p := range ports {
		if p <= 0 {
			t.Errorf("invalid port: %d", p)
		}
		if seen[p] {
			t.Errorf("duplicate port: %d", p)
		}
		seen[p] = true
	}
}

func TestPortAllocator_AllocateZero(t *testing.T) {
	alloc := server.NewPortAllocator()

	lns, err := alloc.Allocate("inst-1", 0)
	if err != nil {
		t.Fatal(err)
	}
	if lns != nil {
		t.Errorf("expected nil for 0 ports, got %v", lns)
	}
}

func TestPortAllocator_ListenersAreOpen(t *testing.T) {
	alloc := server.NewPortAllocator()

	lns, err := alloc.Allocate("inst-1", 2)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		for _, ln := range lns {
			ln.Close()
		}
	}()

	// Verify the listeners are actually open by accepting (with timeout).
	for _, ln := range lns {
		addr := ln.Addr().String()
		conn, err := net.Dial("tcp", addr)
		if err != nil {
			t.Errorf("listener at %s not connectable: %v", addr, err)
			continue
		}
		conn.Close()
	}
}

func TestPortAllocator_TracksAllocations(t *testing.T) {
	alloc := server.NewPortAllocator()

	lns1, err := alloc.Allocate("inst-1", 2)
	if err != nil {
		t.Fatal(err)
	}
	for _, ln := range lns1 {
		ln.Close()
	}

	lns2, err := alloc.Allocate("inst-2", 3)
	if err != nil {
		t.Fatal(err)
	}
	for _, ln := range lns2 {
		ln.Close()
	}

	if alloc.Allocated() != 5 {
		t.Errorf("expected 5 tracked ports, got %d", alloc.Allocated())
	}
}

func TestPortAllocator_Release(t *testing.T) {
	alloc := server.NewPortAllocator()

	lns1, err := alloc.Allocate("inst-1", 2)
	if err != nil {
		t.Fatal(err)
	}
	for _, ln := range lns1 {
		ln.Close()
	}

	lns2, err := alloc.Allocate("inst-2", 3)
	if err != nil {
		t.Fatal(err)
	}
	for _, ln := range lns2 {
		ln.Close()
	}

	alloc.Release("inst-1")

	if alloc.Allocated() != 3 {
		t.Errorf("after releasing inst-1: expected 3 tracked ports, got %d", alloc.Allocated())
	}

	alloc.Release("inst-2")

	if alloc.Allocated() != 0 {
		t.Errorf("after releasing inst-2: expected 0 tracked ports, got %d", alloc.Allocated())
	}
}

func TestPortAllocator_ReleaseNonexistent(t *testing.T) {
	alloc := server.NewPortAllocator()

	// Should not panic.
	alloc.Release("nonexistent")

	if alloc.Allocated() != 0 {
		t.Errorf("expected 0 tracked ports, got %d", alloc.Allocated())
	}
}

func TestPortAllocator_MultipleInstancesGetDifferentPorts(t *testing.T) {
	alloc := server.NewPortAllocator()

	lns1, err := alloc.Allocate("inst-1", 5)
	if err != nil {
		t.Fatal(err)
	}
	ports1 := listenersToPortsAndClose(t, lns1)

	lns2, err := alloc.Allocate("inst-2", 5)
	if err != nil {
		t.Fatal(err)
	}
	ports2 := listenersToPortsAndClose(t, lns2)

	// All 10 ports should be unique.
	seen := make(map[int]bool)
	for _, p := range ports1 {
		seen[p] = true
	}
	for _, p := range ports2 {
		if seen[p] {
			t.Errorf("port %d allocated to both instances", p)
		}
		seen[p] = true
	}
}
