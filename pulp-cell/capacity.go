package main

import "fmt"

// capacityTracker is NOT protected by a mutex.
//
// WASM cells are single-threaded: pulp_step is invoked serially by the
// host on one cell goroutine. All HTTP handlers, SSE emissions, and
// host-capability callbacks run on that same goroutine, so there is no
// concurrent access to mutate. Adding a mutex here would just add
// overhead without any safety benefit. Same comment applies to ipPool,
// portPool, and registry in sibling files.
type capacityTracker struct {
	cpuBudget  float64
	memBudget  float64 // GiB
	allocCPU   float64
	allocMem   float64 // GiB
	containers map[string]struct{ cpu, memGiB float64 }
}

func newCapacityTracker(cpuBudget, memBudget float64) *capacityTracker {
	return &capacityTracker{
		cpuBudget:  cpuBudget,
		memBudget:  memBudget,
		containers: make(map[string]struct{ cpu, memGiB float64 }),
	}
}

func (ct *capacityTracker) tryAllocate(containerID string, cpuLimit float64, memLimitBytes int64) error {
	memGiB := float64(memLimitBytes) / (1024 * 1024 * 1024)
	if ct.cpuBudget > 0 && ct.allocCPU+cpuLimit > ct.cpuBudget {
		return fmt.Errorf("CPU capacity exceeded (%.2f + %.2f > %.2f)", ct.allocCPU, cpuLimit, ct.cpuBudget)
	}
	if ct.memBudget > 0 && ct.allocMem+memGiB > ct.memBudget {
		return fmt.Errorf("memory capacity exceeded (%.2f + %.2f > %.2f GiB)", ct.allocMem, memGiB, ct.memBudget)
	}
	ct.allocCPU += cpuLimit
	ct.allocMem += memGiB
	ct.containers[containerID] = struct{ cpu, memGiB float64 }{cpuLimit, memGiB}
	return nil
}

func (ct *capacityTracker) commit(tempID, realID string) {
	if res, ok := ct.containers[tempID]; ok {
		delete(ct.containers, tempID)
		ct.containers[realID] = res
	}
}

func (ct *capacityTracker) release(containerID string) {
	if res, ok := ct.containers[containerID]; ok {
		ct.allocCPU -= res.cpu
		ct.allocMem -= res.memGiB
		delete(ct.containers, containerID)
	}
}

func (ct *capacityTracker) snapshot() (allocCPU, allocMem float64, count int) {
	return ct.allocCPU, ct.allocMem, len(ct.containers)
}
