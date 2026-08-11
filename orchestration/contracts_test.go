package orchestration

import (
	"encoding/json"
	"testing"
)

func TestCreateServerRequestJSONContract(t *testing.T) {
	body, err := json.Marshal(CreateServerRequest{
		Template: "minecraft",
		ServerID: "server-1",
		Env:      map[string]string{"ENGINE": "paper"},
		Resources: &ResourceOverride{
			MaxCpuCores: 1.5,
			MaxRamMb:    4096,
			JvmHeapMb:   2560,
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	var wire map[string]any
	if err := json.Unmarshal(body, &wire); err != nil {
		t.Fatal(err)
	}
	if wire["template"] != "minecraft" || wire["server_id"] != "server-1" {
		t.Fatalf("identity fields changed: %s", body)
	}
	resources, ok := wire["resources"].(map[string]any)
	if !ok {
		t.Fatalf("resources missing: %s", body)
	}
	if resources["max_cpu_cores"] != 1.5 || resources["max_ram_mb"] != float64(4096) || resources["jvm_heap_mb"] != float64(2560) {
		t.Fatalf("resource fields changed: %s", body)
	}
}

func TestServerRuntimeAttestationJSONContract(t *testing.T) {
	body, err := json.Marshal(Server{
		ID: "container-1", Name: "server-1", NodeID: "node-a", WorldName: "server-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	var wire map[string]any
	if err := json.Unmarshal(body, &wire); err != nil {
		t.Fatal(err)
	}
	if wire["node_id"] != "node-a" || wire["world_name"] != "server-1" {
		t.Fatalf("runtime attestation fields changed: %s", body)
	}
}

func TestStatsResponseJSONContract(t *testing.T) {
	body, err := json.Marshal(StatsResponse{
		Containers: []ContainerStats{},
		Node: NodeStats{
			CPUBudget:    8,
			MemoryBudget: 32,
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	var wire struct {
		Containers []ContainerStats `json:"containers"`
		Node       map[string]any   `json:"node"`
	}
	if err := json.Unmarshal(body, &wire); err != nil {
		t.Fatal(err)
	}
	if wire.Containers == nil {
		t.Fatalf("empty containers must encode as [], got %s", body)
	}
	if wire.Node["cpu_budget"] != float64(8) || wire.Node["memory_budget"] != float64(32) {
		t.Fatalf("node budget fields changed: %s", body)
	}
}
