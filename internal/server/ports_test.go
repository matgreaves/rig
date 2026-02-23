package server_test

import (
	"fmt"
	"net"
	"testing"

	"github.com/matgreaves/rig/internal/server"
)

func TestPortAllocator_AllocateReturnsUniquePorts(t *testing.T) {
	alloc := server.NewPortAllocator()

	ports, err := alloc.Allocate("inst-1", 3)
	if err != nil {
		t.Fatal(err)
	}
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

	ports, err := alloc.Allocate("inst-1", 0)
	if err != nil {
		t.Fatal(err)
	}
	if ports != nil {
		t.Errorf("expected nil for 0 ports, got %v", ports)
	}
}

func TestPortAllocator_PortsAreBindable(t *testing.T) {
	alloc := server.NewPortAllocator()

	ports, err := alloc.Allocate("inst-1", 2)
	if err != nil {
		t.Fatal(err)
	}

	// Verify the ports are actually available by binding to them.
	for _, port := range ports {
		ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
		if err != nil {
			t.Errorf("port %d not bindable: %v", port, err)
			continue
		}
		ln.Close()
	}
}

func TestPortAllocator_TracksAllocations(t *testing.T) {
	alloc := server.NewPortAllocator()

	_, err := alloc.Allocate("inst-1", 2)
	if err != nil {
		t.Fatal(err)
	}
	_, err = alloc.Allocate("inst-2", 3)
	if err != nil {
		t.Fatal(err)
	}

	if alloc.Allocated() != 5 {
		t.Errorf("expected 5 tracked ports, got %d", alloc.Allocated())
	}
}

func TestPortAllocator_Release(t *testing.T) {
	alloc := server.NewPortAllocator()

	_, err := alloc.Allocate("inst-1", 2)
	if err != nil {
		t.Fatal(err)
	}
	_, err = alloc.Allocate("inst-2", 3)
	if err != nil {
		t.Fatal(err)
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

	ports1, err := alloc.Allocate("inst-1", 5)
	if err != nil {
		t.Fatal(err)
	}

	ports2, err := alloc.Allocate("inst-2", 5)
	if err != nil {
		t.Fatal(err)
	}

	// All 10 ports should be unique (OS guarantees this, but verify).
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
