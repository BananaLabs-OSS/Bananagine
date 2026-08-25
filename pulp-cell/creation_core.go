package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/BananaLabs-OSS/Fiber/pulp"
	"github.com/BananaLabs-OSS/Fiber/pulp/docker"
	"github.com/bananalabs-oss/bananagine/orchestration"

	"bananagine-cell/resources"
)

// creationCore is Bananagine's transport-neutral normal-create operation.
// HTTP is only an adapter: all template expansion, allocation, hook, capacity,
// idempotency, Docker creation, and resource re-keying lives here so a typed
// replacement operation can use the exact same safe creation path.
type creationCore struct {
	templates     map[string]Template
	externalHost  string
	runtimeNodeID string
	capacity      *capacityTracker
	ipp           *ipPool
	portPools     *portPoolSet
	get           dockerServerGet
	create        dockerServerCreate
	hook          func(string) (map[string]string, error)
}

type creationResult struct {
	Server   orchestration.Server
	Existing bool
}

// creationError preserves the legacy HTTP status classification without
// coupling the core to a transport.
type creationError struct {
	Status int
	Err    error
}

func (e *creationError) Error() string { return e.Err.Error() }

func createFailure(status int, format string, args ...any) error {
	return &creationError{Status: status, Err: fmt.Errorf(format, args...)}
}

func creationHTTPStatus(err error) int {
	if typed, ok := err.(*creationError); ok && typed.Status != 0 {
		return typed.Status
	}
	return 500
}

func defaultPreStartHook(url string) (map[string]string, error) {
	resp, err := pulp.HTTP.Fetch(pulp.HTTPFetchRequest{Method: "GET", URL: url})
	if err != nil {
		return nil, fmt.Errorf("hook failed: %w", err)
	}
	if resp.Status < 200 || resp.Status >= 300 {
		return nil, fmt.Errorf("hook returned %d", resp.Status)
	}
	var decoded struct {
		Env map[string]string `json:"env"`
	}
	if err := json.Unmarshal(resp.Body, &decoded); err != nil {
		return nil, fmt.Errorf("hook response decode failed: %w", err)
	}
	return decoded.Env, nil
}

func (core creationCore) Create(req orchestration.CreateServerRequest) (creationResult, error) {
	if req.ServerID != "" {
		existing, found, err := existingServerForRequestedID(req.ServerID, core.get)
		if err != nil {
			return creationResult{}, createFailure(500, "%v", err)
		}
		if found {
			return creationResult{Server: core.responseServer(*existing, req.ServerID), Existing: true}, nil
		}
	}

	tmpl, ok := core.templates[req.Template]
	if !ok {
		return creationResult{}, createFailure(404, "template not found")
	}
	container := deepCopyContainer(tmpl.Container)
	filterCreatePlatforms(&container, tmpl, req.Env)

	serverID := req.ServerID
	if serverID == "" {
		serverID = fmt.Sprintf("%s-%d", req.Template, time.Now().UnixNano())
	}
	expandCreateVolumes(&container, serverID)
	if container.Environment == nil {
		container.Environment = make(map[string]string)
	}
	for k, v := range tmpl.Server {
		container.Environment[k] = v
	}

	allocatedIP, allocatedPort, err := core.allocateCreateNetwork(serverID, &container)
	if err != nil {
		return creationResult{}, createFailure(503, "%v", err)
	}
	releaseResources := func() {
		if allocatedIP != "" {
			core.ipp.release(allocatedIP)
		} else {
			core.portPools.releaseByServer(serverID)
		}
	}

	if tmpl.Hooks.PreStart != "" {
		hook := core.hook
		if hook == nil {
			hook = defaultPreStartHook
		}
		env, hookErr := hook(tmpl.Hooks.PreStart)
		if hookErr != nil {
			releaseResources()
			return creationResult{}, createFailure(500, "%v", hookErr)
		}
		for k, v := range env {
			container.Environment[k] = v
		}
	}

	rc := resources.Container{MemoryLimit: container.MemoryLimit, CPULimit: container.CPULimit, MemorySwap: container.MemorySwap, Environment: container.Environment}
	var override orchestration.ResourceOverride
	if req.Resources != nil {
		override = *req.Resources
	}
	resources.Apply(&rc, resources.Override{MemoryLimit: override.MemoryLimit, CPULimit: override.CPULimit, MaxCpuCores: override.MaxCpuCores, MaxRamMb: override.MaxRamMb, JvmHeapMb: override.JvmHeapMb}, req.Env)
	container.MemoryLimit, container.CPULimit, container.MemorySwap, container.Environment = rc.MemoryLimit, rc.CPULimit, rc.MemorySwap, rc.Environment
	if err := core.capacity.tryAllocate(serverID, container.CPULimit, container.MemoryLimit); err != nil {
		releaseResources()
		return creationResult{}, createFailure(503, "%v", err)
	}

	container.Name = serverID
	server, existing, err := createWithSpeculativeResources(serverID, containerToCreateRequest(container), core.get, core.create, func() {
		core.capacity.release(serverID)
		releaseResources()
	})
	if err != nil {
		return creationResult{}, createFailure(500, "%v", err)
	}
	if existing {
		return creationResult{Server: core.responseServer(*server, serverID), Existing: true}, nil
	}
	core.capacity.commit(serverID, server.ID)
	if allocatedIP != "" {
		core.ipp.reKey(serverID, server.ID)
	} else {
		core.portPools.reKey(serverID, server.ID)
	}
	server.Name = serverID
	if server.Ports == nil {
		server.Ports = map[string]int{}
	}
	if len(container.Ports) > 0 {
		for _, p := range container.Ports {
			key := p.Name
			if key == "" {
				key = fmt.Sprintf("%d", p.Container)
			}
			if _, ok := server.Ports[key]; !ok {
				server.Ports[key] = p.Host
			}
		}
	} else {
		key := fmt.Sprintf("%d", allocatedPort)
		if _, ok := server.Ports[key]; !ok {
			server.Ports[key] = allocatedPort
		}
	}
	return creationResult{Server: core.responseServer(*server, serverID)}, nil
}

// responseServer is the sole create/adopt projection. The logical server ID
// names the per-server world volume in this runtime, so Bananagine—not the
// caller—attests it as the canonical world identity.
func (core creationCore) responseServer(server docker.Server, serverID string) orchestration.Server {
	result := toOrchestrationServer(orchestrationResponseServer(server, serverID, core.externalHost))
	result.NodeID = core.runtimeNodeID
	result.WorldName = serverID
	return result
}

func filterCreatePlatforms(container *ContainerSpec, tmpl Template, env map[string]string) {
	if len(tmpl.Config.Engines) == 0 || len(container.Ports) == 0 {
		return
	}
	active := map[string]bool{}
	for _, engine := range tmpl.Config.Engines {
		if engine.Value == env["ENGINE"] {
			for _, platform := range engine.Platforms {
				active[platform] = true
			}
			break
		}
	}
	if env["_CROSSPLAY"] == "true" {
		active["bedrock"] = true
	}
	if len(active) == 0 {
		return
	}
	kept := container.Ports[:0]
	for _, port := range container.Ports {
		if (port.Name == "java" || port.Name == "bedrock") && !active[port.Name] {
			continue
		}
		kept = append(kept, port)
	}
	container.Ports = kept
}

func expandCreateVolumes(container *ContainerSpec, serverID string) {
	for hostPath, containerPath := range container.Volumes {
		if strings.Contains(hostPath, "{{SERVER_ID}}") {
			delete(container.Volumes, hostPath)
			container.Volumes[strings.ReplaceAll(hostPath, "{{SERVER_ID}}", serverID)] = containerPath
		}
	}
}

func (core creationCore) allocateCreateNetwork(serverID string, container *ContainerSpec) (string, int, error) {
	if container.Network != "" {
		ip, err := core.ipp.allocate(serverID)
		if err != nil {
			return "", 0, err
		}
		container.IP = ip
		port := 5520
		if len(container.Ports) > 0 {
			port = container.Ports[0].Container
		}
		container.Environment["SERVER_HOST"] = ip
		for _, p := range container.Ports {
			if p.Name != "" {
				container.Environment["PORT_"+strings.ToUpper(p.Name)] = fmt.Sprintf("%d", p.Container)
			}
		}
		container.Environment["SERVER_PORT"] = fmt.Sprintf("%d", port)
		container.Environment["SERVER_ID"] = serverID
		return ip, port, nil
	}
	allocated := make([]int, 0, len(container.Ports))
	for i := range container.Ports {
		port, err := core.portPools.allocate(container.Ports[i].Range, serverID)
		if err != nil {
			core.portPools.releaseByServer(serverID)
			return "", 0, err
		}
		allocated = append(allocated, port)
		container.Ports[i].Host, container.Ports[i].Container = port, port
	}
	if len(allocated) == 0 {
		port, err := core.portPools.allocate("", serverID)
		if err != nil {
			return "", 0, err
		}
		allocated = append(allocated, port)
	}
	container.Environment["SERVER_HOST"] = "0.0.0.0"
	for i, p := range container.Ports {
		if p.Name != "" {
			container.Environment["PORT_"+strings.ToUpper(p.Name)] = fmt.Sprintf("%d", allocated[i])
		}
	}
	container.Environment["SERVER_PORT"] = fmt.Sprintf("%d", allocated[0])
	container.Environment["SERVER_ID"] = serverID
	return "", allocated[0], nil
}
