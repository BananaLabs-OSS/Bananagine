package main

import (
	"testing"

	"github.com/BananaLabs-OSS/Fiber/pulp"
	"github.com/BananaLabs-OSS/Fiber/pulp/docker"
	"github.com/bananalabs-oss/bananagine/orchestration"
)

// This is deliberately a core-level parity test rather than an HTTP fixture:
// normal POST /servers is now a thin adapter over this exact operation, and
// replacement-first recreate will call the same operation as well.
func TestCreationCoreNormalCreatePreservesProvisioningBehavior(t *testing.T) {
	const logicalID = "minecraft-core-parity"
	ports := newPortPoolSet(newPortPool(30000, 30010))
	capacity := newCapacityTracker(8, 16)
	core := creationCore{
		templates: map[string]Template{
			"minecraft": {
				Name: "minecraft",
				Container: ContainerSpec{
					Image:       "example/minecraft",
					Environment: map[string]string{"TEMPLATE": "yes"},
					Volumes:     map[string]string{"/worlds/{{SERVER_ID}}": "/data"},
					Ports:       []PortSpec{{Name: "java", Container: 25565}, {Name: "bedrock", Container: 19132}},
					CPULimit:    1, MemoryLimit: 2 * 1024 * 1024 * 1024,
				},
				Server: map[string]string{"SERVER": "template"},
				Hooks:  Hooks{PreStart: "https://hook.example.test/pre-start"},
				Config: ConfigSchema{Engines: []EngineOption{{Value: "java", Platforms: []string{"java"}}}},
			},
		},
		externalHost: "games.example.test", runtimeNodeID: "node-create-1", capacity: capacity, ipp: newIPPool("10.99.0.10", "10.99.0.11"), portPools: ports,
		get: func(string) (*docker.Server, error) { return nil, pulp.ErrNotFound },
		hook: func(url string) (map[string]string, error) {
			if url != "https://hook.example.test/pre-start" {
				t.Fatalf("hook URL = %q", url)
			}
			return map[string]string{"HOOK": "yes"}, nil
		},
		create: func(request docker.CreateRequest) (*docker.Server, error) {
			if request.Name != logicalID || request.Image != "example/minecraft" {
				t.Fatalf("create identity = %#v", request)
			}
			if len(request.Ports) != 1 || request.Ports[0].Name != "java" || request.Ports[0].Host != 30000 || request.Ports[0].Container != 30000 {
				t.Fatalf("filtered/allocated ports = %#v", request.Ports)
			}
			if request.Volumes["/worlds/"+logicalID] != "/data" {
				t.Fatalf("expanded volumes = %#v", request.Volumes)
			}
			for key, want := range map[string]string{"TEMPLATE": "yes", "SERVER": "template", "HOOK": "yes", "REQUEST": "yes", "SERVER_ID": logicalID, "SERVER_PORT": "30000"} {
				if request.Environment[key] != want {
					t.Fatalf("environment[%q] = %q, want %q; all=%#v", key, request.Environment[key], want, request.Environment)
				}
			}
			return &docker.Server{ID: "container-1", Name: "/" + logicalID, Status: "created", Ports: map[string]int{}}, nil
		},
	}
	restore := replaceGetOwnedServer(t, func(string) (*docker.Server, error) { return nil, pulp.ErrNotFound })
	defer restore()
	result, err := core.Create(orchestration.CreateServerRequest{Template: "minecraft", ServerID: logicalID, Env: map[string]string{"ENGINE": "java", "REQUEST": "yes"}})
	if err != nil {
		t.Fatal(err)
	}
	if result.Existing || result.Server.ID != "container-1" || result.Server.Name != logicalID || result.Server.NodeID != "node-create-1" || result.Server.WorldName != logicalID || result.Server.IP != "games.example.test" || result.Server.Ports["java"] != 30000 {
		t.Fatalf("result = %#v", result)
	}
	if _, allocated, count := capacity.snapshot(); count != 1 || allocated != 2 {
		t.Fatalf("capacity = allocated=%v count=%d", allocated, count)
	}
	if owner := ports.fallback.allocated[30000]; owner != "container-1" {
		t.Fatalf("port owner = %q, want rekeyed container", owner)
	}
}

func TestCreationCorePreservesIdempotentCreateProjection(t *testing.T) {
	const logicalID = "minecraft-existing"
	core := creationCore{
		templates: map[string]Template{}, externalHost: "games.example.test", runtimeNodeID: "node-adopt-1",
		get: func(id string) (*docker.Server, error) {
			if id != logicalID {
				t.Fatalf("get id = %q", id)
			}
			return &docker.Server{ID: "container-existing", Name: "/" + logicalID, Ports: map[string]int{"java": 25565}}, nil
		},
	}
	result, err := core.Create(orchestration.CreateServerRequest{Template: "missing-is-okay-on-retry", ServerID: logicalID})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Existing || result.Server.ID != "container-existing" || result.Server.Name != logicalID || result.Server.NodeID != "node-adopt-1" || result.Server.WorldName != logicalID || result.Server.IP != "games.example.test" {
		t.Fatalf("result = %#v", result)
	}
}
