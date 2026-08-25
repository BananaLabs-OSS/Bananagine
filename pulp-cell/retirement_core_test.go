package main

import (
	"errors"
	"testing"
)

func retirementFixtures(t *testing.T) (*capacityTracker, *portPoolSet, *ipPool) {
	t.Helper()
	capacity := newCapacityTracker(4, 8)
	if err := capacity.tryAllocate("server-1", 1, 1024*1024*1024); err != nil {
		t.Fatal(err)
	}
	ports := newPortPoolSet(newPortPool(30000, 30001))
	if _, err := ports.allocate("", "server-1"); err != nil {
		t.Fatal(err)
	}
	ips := newIPPool("10.99.0.10", "10.99.0.11")
	if _, err := ips.allocate("server-1"); err != nil {
		t.Fatal(err)
	}
	return capacity, ports, ips
}

func TestRetireServerRetainsOwnershipWhenDestroyFails(t *testing.T) {
	capacity, ports, ips := retirementFixtures(t)
	err := retireServer("server-1", false, "", func(string) error {
		return errors.New("docker unavailable")
	}, capacity, ports, ips)
	if err == nil {
		t.Fatal("retireServer succeeded despite Docker failure")
	}
	if _, _, count := capacity.snapshot(); count != 1 {
		t.Fatalf("capacity count = %d, want 1", count)
	}
	if ports.fallback.allocated[30000] != "server-1" {
		t.Fatalf("port ownership = %#v, want server-1", ports.fallback.allocated)
	}
	if ips.allocated["10.99.0.10"] != "server-1" {
		t.Fatalf("IP ownership = %#v, want server-1", ips.allocated)
	}
}

func TestRetireServerRekeysPreservedAllocationsAfterDestroy(t *testing.T) {
	capacity, ports, ips := retirementFixtures(t)
	if err := retireServer("server-1", true, "server-2", func(string) error { return nil }, capacity, ports, ips); err != nil {
		t.Fatal(err)
	}
	if _, _, count := capacity.snapshot(); count != 0 {
		t.Fatalf("capacity count = %d, want 0", count)
	}
	if ports.fallback.allocated[30000] != "server-2" {
		t.Fatalf("port ownership = %#v, want server-2", ports.fallback.allocated)
	}
	if ips.allocated["10.99.0.10"] != "server-2" {
		t.Fatalf("IP ownership = %#v, want server-2", ips.allocated)
	}
}
