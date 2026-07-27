package main

import (
	"errors"
	"strings"
	"testing"

	"github.com/BananaLabs-OSS/Fiber/pulp"
	"github.com/BananaLabs-OSS/Fiber/pulp/docker"
)

func TestExistingServerForRequestedIDReturnsRetryProjection(t *testing.T) {
	const serverID = "minecraft-stable-42"
	getCalls := 0
	server, found, err := existingServerForRequestedID(serverID, func(id string) (*docker.Server, error) {
		getCalls++
		if id != serverID {
			t.Fatalf("lookup id = %q, want %q", id, serverID)
		}
		return &docker.Server{
			ID:     "container-42",
			Name:   "/" + serverID,
			IP:     "172.18.0.42",
			Ports:  map[string]int{"java": 25565},
			Status: "running",
		}, nil
	})
	if err != nil || !found {
		t.Fatalf("lookup = (%v, %t, %v), want existing server", server, found, err)
	}
	if getCalls != 1 {
		t.Fatalf("get calls = %d, want 1", getCalls)
	}

	projected := orchestrationResponseServer(*server, serverID, "games.example.test")
	if projected.Name != serverID || projected.IP != "games.example.test" || projected.Ports["java"] != 25565 {
		t.Fatalf("retry projection = %#v, want stable name, external host, and Docker ports", projected)
	}
}

func TestExistingServerForRequestedIDOnlyProceedsOnNotFound(t *testing.T) {
	t.Run("not found", func(t *testing.T) {
		restore := replaceGetOwnedServer(t, func(string) (*docker.Server, error) { return nil, pulp.ErrNotFound })
		defer restore()
		server, found, err := existingServerForRequestedID("stable", func(string) (*docker.Server, error) {
			return nil, pulp.ErrNotFound
		})
		if err != nil || found || server != nil {
			t.Fatalf("lookup = (%v, %t, %v), want no existing server", server, found, err)
		}
	})

	t.Run("inspect failure", func(t *testing.T) {
		want := errors.New("docker unavailable")
		_, found, err := existingServerForRequestedID("stable", func(string) (*docker.Server, error) {
			return nil, want
		})
		if found || !errors.Is(err, want) {
			t.Fatalf("lookup = (found=%t, err=%v), want inspect failure", found, err)
		}
	})

	t.Run("different container", func(t *testing.T) {
		_, found, err := existingServerForRequestedID("stable", func(string) (*docker.Server, error) {
			return &docker.Server{ID: "stable", Name: "/another-server"}, nil
		})
		if found || err == nil || !strings.Contains(err.Error(), "another-server") {
			t.Fatalf("lookup = (found=%t, err=%v), want exact-name failure", found, err)
		}
	})
}

func TestExistingServerForRequestedIDUsesHostOwnedLookupAfterRawMiss(t *testing.T) {
	restore := replaceGetOwnedServer(t, func(logicalName string) (*docker.Server, error) {
		if logicalName != "minecraft-stable-42" {
			t.Fatalf("logical name = %q", logicalName)
		}
		return &docker.Server{ID: "scoped-container-42", Name: "/pulp-bananagine-default-bananagine-primary-minecraft-stable-42"}, nil
	})
	defer restore()
	server, found, err := existingServerForRequestedID("minecraft-stable-42", func(string) (*docker.Server, error) {
		return nil, pulp.ErrNotFound
	})
	if err != nil || !found || server == nil || server.ID != "scoped-container-42" {
		t.Fatalf("owned lookup = (%#v, %t, %v), want canonical scoped server", server, found, err)
	}
}

func replaceGetOwnedServer(t *testing.T, replacement dockerServerGet) func() {
	t.Helper()
	previous := getOwnedServer
	getOwnedServer = replacement
	return func() { getOwnedServer = previous }
}

func TestCreateWithSpeculativeResourcesReleasesOnNameConflictRace(t *testing.T) {
	const serverID = "minecraft-stable-42"
	capacity := newCapacityTracker(4, 8)
	if err := capacity.tryAllocate(serverID, 1, 1024*1024*1024); err != nil {
		t.Fatal(err)
	}
	ports := newPortPool(25565, 25565)
	if _, err := ports.allocate(serverID); err != nil {
		t.Fatal(err)
	}

	releases := 0
	server, existing, err := createWithSpeculativeResources(
		serverID,
		docker.CreateRequest{Name: serverID},
		func(id string) (*docker.Server, error) {
			if id != serverID {
				t.Fatalf("conflict lookup id = %q, want %q", id, serverID)
			}
			return &docker.Server{ID: "container-42", Name: "/" + serverID, Ports: map[string]int{"java": 25565}}, nil
		},
		func(req docker.CreateRequest) (*docker.Server, error) {
			if req.Name != serverID {
				t.Fatalf("create name = %q, want %q", req.Name, serverID)
			}
			return nil, errors.New("Conflict. The container name \"/minecraft-stable-42\" is already in use")
		},
		func() {
			releases++
			capacity.release(serverID)
			ports.releaseByServer(serverID)
		},
	)
	if err != nil || !existing || server == nil || server.ID != "container-42" {
		t.Fatalf("race result = (%#v, existing=%t, err=%v), want existing container", server, existing, err)
	}
	if releases != 1 {
		t.Fatalf("release calls = %d, want 1", releases)
	}
	if cpu, mem, count := capacity.snapshot(); cpu != 0 || mem != 0 || count != 0 {
		t.Fatalf("capacity leaked after conflict: cpu=%v mem=%v count=%d", cpu, mem, count)
	}
	if len(ports.allocated) != 0 {
		t.Fatalf("ports leaked after conflict: %#v", ports.allocated)
	}
}

func TestCreateWithSpeculativeResourcesFailsAndReleasesWhenConflictCannotResolve(t *testing.T) {
	const serverID = "minecraft-stable-42"
	releases := 0
	_, existing, err := createWithSpeculativeResources(
		serverID,
		docker.CreateRequest{Name: serverID},
		func(string) (*docker.Server, error) { return nil, errors.New("docker unavailable") },
		func(docker.CreateRequest) (*docker.Server, error) {
			return nil, errors.New("Conflict. The container name \"/minecraft-stable-42\" is already in use")
		},
		func() { releases++ },
	)
	if existing || err == nil || !strings.Contains(err.Error(), "docker unavailable") {
		t.Fatalf("race result = (existing=%t, err=%v), want lookup failure", existing, err)
	}
	if releases != 1 {
		t.Fatalf("release calls = %d, want 1", releases)
	}
}

func TestCreateWithSpeculativeResourcesDoesNotMaskGenuineCreateError(t *testing.T) {
	getCalls := 0
	releases := 0
	_, existing, err := createWithSpeculativeResources(
		"stable",
		docker.CreateRequest{Name: "stable"},
		func(string) (*docker.Server, error) {
			getCalls++
			return nil, nil
		},
		func(docker.CreateRequest) (*docker.Server, error) { return nil, errors.New("image pull failed") },
		func() { releases++ },
	)
	if existing || err == nil || err.Error() != "image pull failed" {
		t.Fatalf("create result = (existing=%t, err=%v), want genuine create error", existing, err)
	}
	if getCalls != 0 {
		t.Fatalf("get calls = %d, want 0 for a non-conflict create failure", getCalls)
	}
	if releases != 1 {
		t.Fatalf("release calls = %d, want 1", releases)
	}
}
