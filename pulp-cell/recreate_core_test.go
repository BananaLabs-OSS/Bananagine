package main

import (
	"errors"
	"testing"

	"github.com/BananaLabs-OSS/Fiber/pulp"
	"github.com/BananaLabs-OSS/Fiber/pulp/docker"
	"github.com/bananalabs-oss/bananagine/orchestration"
)

func recreateRequest(t *testing.T, replacement string) orchestration.RecreateServerRequestV1 {
	t.Helper()
	request := orchestration.RecreateServerRequestV1{
		Version: recreateContractV1, IdempotencyKey: "recreate-key-1", ReceiptID: "receipt-1",
		Replacement: orchestration.CreateServerRequest{Template: "minecraft", ServerID: replacement, Env: map[string]string{"ENGINE": "java"}},
	}
	digest, err := recreateImmutableDigest(request)
	if err != nil {
		t.Fatal(err)
	}
	request.ImmutableSpecSHA256 = digest
	return request
}

func TestRecreateCoreCreatesReplacementBeforeRetiringOldAndReplaysReceipt(t *testing.T) {
	steps := []string{}
	create := creationCore{
		templates: map[string]Template{"minecraft": {Name: "minecraft", Container: ContainerSpec{Image: "example/minecraft", Ports: []PortSpec{{Name: "java", Container: 25565}}}}},
		capacity:  newCapacityTracker(8, 16), ipp: newIPPool("10.99.0.10", "10.99.0.11"), portPools: newPortPoolSet(newPortPool(30000, 30001)),
		get: func(string) (*docker.Server, error) { return nil, pulp.ErrNotFound },
		create: func(req docker.CreateRequest) (*docker.Server, error) {
			steps = append(steps, "create:"+req.Name)
			return &docker.Server{ID: "replacement-container", Name: "/" + req.Name, Ports: map[string]int{}}, nil
		},
	}
	recreate := newRecreateCore(create, func(old string) error { steps = append(steps, "destroy:"+old); return nil })
	restore := replaceGetOwnedServer(t, func(string) (*docker.Server, error) { return nil, pulp.ErrNotFound })
	defer restore()
	request := recreateRequest(t, "minecraft-replacement-1")
	receipt, err := recreate.Recreate("old-container-1", request)
	if err != nil {
		t.Fatal(err)
	}
	if !receipt.ReplacementReady || !receipt.OldRetired || receipt.Replacement.ID != "replacement-container" {
		t.Fatalf("receipt = %#v", receipt)
	}
	if got, want := len(steps), 2; got != want || steps[0] != "create:minecraft-replacement-1" || steps[1] != "destroy:old-container-1" {
		t.Fatalf("operation order = %#v", steps)
	}
	if _, err := recreate.Recreate("old-container-1", request); err != nil {
		t.Fatal(err)
	}
	if len(steps) != 2 {
		t.Fatalf("idempotent replay repeated side effects: %#v", steps)
	}
}

func TestRecreateCoreDoesNotRetireOldWhenReplacementFails(t *testing.T) {
	destroyed := false
	create := creationCore{
		templates: map[string]Template{"minecraft": {Name: "minecraft", Container: ContainerSpec{Image: "example/minecraft"}}},
		capacity:  newCapacityTracker(8, 16), ipp: newIPPool("10.99.0.10", "10.99.0.11"), portPools: newPortPoolSet(newPortPool(30000, 30001)),
		get:    func(string) (*docker.Server, error) { return nil, pulp.ErrNotFound },
		create: func(docker.CreateRequest) (*docker.Server, error) { return nil, errors.New("image pull failed") },
	}
	recreate := newRecreateCore(create, func(string) error { destroyed = true; return nil })
	restore := replaceGetOwnedServer(t, func(string) (*docker.Server, error) { return nil, pulp.ErrNotFound })
	defer restore()
	_, err := recreate.Recreate("old-container-1", recreateRequest(t, "minecraft-replacement-1"))
	if err == nil || destroyed {
		t.Fatalf("recreate error=%v destroyed=%t, want replacement failure without old retirement", err, destroyed)
	}
}

func TestRecreateCoreRejectsIdempotencyKeySpecMutation(t *testing.T) {
	core := newRecreateCore(creationCore{}, func(string) error { return nil })
	request := recreateRequest(t, "minecraft-replacement-1")
	core.replays[request.IdempotencyKey] = recreateReplay{specDigest: "different"}
	_, err := core.Recreate("old-container-1", request)
	if err == nil || creationHTTPStatus(err) != 409 {
		t.Fatalf("error=%v status=%d, want conflict", err, creationHTTPStatus(err))
	}
}
