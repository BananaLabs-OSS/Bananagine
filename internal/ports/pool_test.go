package ports

import (
	"testing"
)

func TestAllocate(t *testing.T) {
	p := NewPool(5000, 5002)

	port, err := p.Allocate("server-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if port != 5000 {
		t.Fatalf("expected port 5000, got %d", port)
	}

	port, err = p.Allocate("server-2")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if port != 5001 {
		t.Fatalf("expected port 5001, got %d", port)
	}

	port, err = p.Allocate("server-3")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if port != 5002 {
		t.Fatalf("expected port 5002, got %d", port)
	}

	// Pool exhausted
	_, err = p.Allocate("server-4")
	if err == nil {
		t.Fatal("expected error when pool exhausted")
	}
}

func TestAllocateN(t *testing.T) {
	t.Run("allocate multiple ports", func(t *testing.T) {
		p := NewPool(5000, 5009)

		ports, err := p.AllocateN(3, "server-1")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(ports) != 3 {
			t.Fatalf("expected 3 ports, got %d", len(ports))
		}
		if ports[0] != 5000 || ports[1] != 5001 || ports[2] != 5002 {
			t.Fatalf("expected [5000, 5001, 5002], got %v", ports)
		}

		// All 3 should be tracked under server-1
		for _, port := range ports {
			if p.allocated[port] != "server-1" {
				t.Fatalf("port %d not allocated to server-1", port)
			}
		}
	})

	t.Run("allocate single port", func(t *testing.T) {
		p := NewPool(5000, 5009)

		ports, err := p.AllocateN(1, "server-1")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(ports) != 1 || ports[0] != 5000 {
			t.Fatalf("expected [5000], got %v", ports)
		}
	})

	t.Run("insufficient ports", func(t *testing.T) {
		p := NewPool(5000, 5001) // only 2 available

		ports, err := p.AllocateN(3, "server-1")
		if err == nil {
			t.Fatal("expected error when not enough ports")
		}
		if ports != nil {
			t.Fatalf("expected nil ports on error, got %v", ports)
		}

		// Should NOT have allocated any ports (atomic)
		if len(p.allocated) != 0 {
			t.Fatalf("expected no allocations on failure, got %v", p.allocated)
		}
	})

	t.Run("sequential allocations don't overlap", func(t *testing.T) {
		p := NewPool(5000, 5005)

		ports1, _ := p.AllocateN(2, "server-1")
		ports2, _ := p.AllocateN(2, "server-2")

		if ports1[0] == ports2[0] || ports1[1] == ports2[1] {
			t.Fatalf("ports overlap: %v and %v", ports1, ports2)
		}
		if ports2[0] != 5002 || ports2[1] != 5003 {
			t.Fatalf("expected [5002, 5003], got %v", ports2)
		}
	})

	t.Run("pool exhausted after partial allocation", func(t *testing.T) {
		p := NewPool(5000, 5003) // 4 ports

		_, err := p.AllocateN(3, "server-1")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		// Only 1 left, asking for 2
		_, err = p.AllocateN(2, "server-2")
		if err == nil {
			t.Fatal("expected error when not enough ports remain")
		}

		// The 3 from server-1 should still be allocated
		if len(p.allocated) != 3 {
			t.Fatalf("expected 3 allocations, got %d", len(p.allocated))
		}
	})
}

func TestRelease(t *testing.T) {
	p := NewPool(5000, 5009)
	p.Allocate("server-1")

	p.Release(5000)

	if _, exists := p.allocated[5000]; exists {
		t.Fatal("port 5000 should have been released")
	}

	// Re-allocate the released port
	port, _ := p.Allocate("server-2")
	if port != 5000 {
		t.Fatalf("expected recycled port 5000, got %d", port)
	}
}

func TestReleaseByServer_MultiPort(t *testing.T) {
	p := NewPool(5000, 5009)

	// Allocate multiple ports for one server
	p.AllocateN(3, "server-1")
	p.Allocate("server-2")

	// Release all of server-1's ports
	p.ReleaseByServer("server-1")

	// Only server-2's port should remain
	if len(p.allocated) != 1 {
		t.Fatalf("expected 1 allocation remaining, got %d", len(p.allocated))
	}
	if p.allocated[5003] != "server-2" {
		t.Fatal("server-2's port should still be allocated")
	}

	// server-1's ports should be free
	ports, err := p.AllocateN(3, "server-3")
	if err != nil {
		t.Fatalf("unexpected error re-allocating released ports: %v", err)
	}
	if ports[0] != 5000 {
		t.Fatalf("expected recycled port 5000, got %d", ports[0])
	}
}

func TestReKey_MultiPort(t *testing.T) {
	p := NewPool(5000, 5009)

	p.AllocateN(3, "temp-id")

	p.ReKey("temp-id", "real-id")

	// All 3 ports should now belong to real-id
	for port := 5000; port <= 5002; port++ {
		if p.allocated[port] != "real-id" {
			t.Fatalf("port %d should be re-keyed to real-id, got %s", port, p.allocated[port])
		}
	}
}

func TestReserve(t *testing.T) {
	p := NewPool(5000, 5009)

	p.Reserve(5005, "existing-server")

	// 5005 should be taken
	if p.allocated[5005] != "existing-server" {
		t.Fatal("port 5005 should be reserved for existing-server")
	}

	// Allocate should skip reserved port
	port, _ := p.Allocate("new-server")
	if port != 5000 {
		t.Fatalf("expected 5000 (skipping reserved 5005), got %d", port)
	}
}

func TestReleaseByServer_NoMatch(t *testing.T) {
	p := NewPool(5000, 5009)
	p.Allocate("server-1")

	// Should not panic or affect anything
	p.ReleaseByServer("nonexistent")

	if len(p.allocated) != 1 {
		t.Fatalf("expected 1 allocation unchanged, got %d", len(p.allocated))
	}
}

func TestReKey_NoMatch(t *testing.T) {
	p := NewPool(5000, 5009)
	p.Allocate("server-1")

	// Should not panic or affect anything
	p.ReKey("nonexistent", "new-id")

	if p.allocated[5000] != "server-1" {
		t.Fatal("server-1's allocation should be unchanged")
	}
}
