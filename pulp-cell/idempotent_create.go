package main

import (
	"fmt"
	"strings"

	"github.com/BananaLabs-OSS/Fiber/pulp/docker"
)

// dockerServerGet and dockerServerCreate make the narrow create boundary
// host-testable without changing the Pulp docker capability's public API.
type dockerServerGet func(string) (*docker.Server, error)
type dockerServerCreate func(docker.CreateRequest) (*docker.Server, error)

// existingServerForRequestedID resolves the container created for serverID.
// Docker returns names with a leading slash, but the orchestration contract
// uses the unprefixed ServerID as the container name.
func existingServerForRequestedID(serverID string, get dockerServerGet) (*docker.Server, bool, error) {
	server, err := get(serverID)
	if err != nil {
		if isDockerNotFound(err) {
			return nil, false, nil
		}
		return nil, false, err
	}
	if server == nil {
		return nil, false, fmt.Errorf("docker get %q returned no container", serverID)
	}
	if strings.TrimPrefix(server.Name, "/") != serverID {
		return nil, false, fmt.Errorf("docker get %q resolved container named %q", serverID, server.Name)
	}
	return server, true, nil
}

// createOrResolveNameConflict handles the narrow race between an idempotent
// preflight lookup and Docker create. Only Docker's name-conflict response is
// retried as a lookup; every other create failure remains a failure.
func createOrResolveNameConflict(serverID string, req docker.CreateRequest, get dockerServerGet, create dockerServerCreate) (*docker.Server, bool, error) {
	server, err := create(req)
	if err == nil {
		return server, false, nil
	}
	if !isDockerNameConflict(err) {
		return nil, false, err
	}

	existing, found, lookupErr := existingServerForRequestedID(serverID, get)
	if lookupErr != nil {
		return nil, false, fmt.Errorf("resolve name conflict for %q: %w", serverID, lookupErr)
	}
	if !found {
		return nil, false, err
	}
	return existing, true, nil
}

// createWithSpeculativeResources makes cleanup inseparable from a failed
// create or a race won by another request. The caller passes its temporary
// capacity and port/IP releases as one idempotent operation.
func createWithSpeculativeResources(serverID string, req docker.CreateRequest, get dockerServerGet, create dockerServerCreate, release func()) (*docker.Server, bool, error) {
	server, existing, err := createOrResolveNameConflict(serverID, req, get, create)
	if err != nil || existing {
		release()
	}
	return server, existing, err
}

func isDockerNameConflict(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "container name") && strings.Contains(msg, "already in use")
}

// orchestrationResponseServer applies the public server projection shared by
// newly-created containers and successful idempotent retries.
func orchestrationResponseServer(server docker.Server, serverID, externalHost string) docker.Server {
	server.Name = serverID
	if externalHost != "" {
		server.IP = externalHost
	}
	return server
}
