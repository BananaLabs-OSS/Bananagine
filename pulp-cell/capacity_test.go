package main

import (
	"strings"
	"testing"
)

const gib = int64(1024 * 1024 * 1024)

func TestCapacityTrackerLifecycle(t *testing.T) {
	tracker := newCapacityTracker(2, 4)

	if err := tracker.tryAllocate("pending", 1, 2*gib); err != nil {
		t.Fatalf("first allocation: %v", err)
	}
	tracker.commit("pending", "server-1")

	cpu, memory, count := tracker.snapshot()
	if cpu != 1 || memory != 2 || count != 1 {
		t.Fatalf("snapshot = (%v, %v, %v), want (1, 2, 1)", cpu, memory, count)
	}

	tracker.release("server-1")
	cpu, memory, count = tracker.snapshot()
	if cpu != 0 || memory != 0 || count != 0 {
		t.Fatalf("released snapshot = (%v, %v, %v), want zero values", cpu, memory, count)
	}
}

func TestCapacityTrackerRejectsOverBudgetWithoutMutation(t *testing.T) {
	tests := []struct {
		name      string
		cpu       float64
		memory    int64
		wantError string
	}{
		{name: "cpu", cpu: 2.1, memory: gib, wantError: "CPU capacity exceeded"},
		{name: "memory", cpu: 1, memory: 5 * gib, wantError: "memory capacity exceeded"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tracker := newCapacityTracker(2, 4)
			err := tracker.tryAllocate("server", tt.cpu, tt.memory)
			if err == nil || !strings.Contains(err.Error(), tt.wantError) {
				t.Fatalf("tryAllocate() error = %v, want %q", err, tt.wantError)
			}
			cpu, memory, count := tracker.snapshot()
			if cpu != 0 || memory != 0 || count != 0 {
				t.Fatalf("rejected allocation mutated tracker: (%v, %v, %v)", cpu, memory, count)
			}
		})
	}
}

func TestCapacityTrackerTreatsZeroBudgetsAsUnlimited(t *testing.T) {
	tracker := newCapacityTracker(0, 0)
	if err := tracker.tryAllocate("server", 100, 200*gib); err != nil {
		t.Fatalf("unlimited allocation: %v", err)
	}
}
