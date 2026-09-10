package main

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/BananaLabs-OSS/Fiber/pulp"
	"github.com/BananaLabs-OSS/Fiber/pulp/docker"
	"github.com/bananalabs-oss/bananagine/hostagent"
	"github.com/bananalabs-oss/bananagine/orchestration"
)

type pulpHostAgentTransport struct{}

func (pulpHostAgentTransport) Post(_ context.Context, endpoint string, headers map[string]string, body []byte) (int, []byte, error) {
	response, err := pulp.HTTP.Fetch(pulp.HTTPFetchRequest{Method: "POST", URL: endpoint, Headers: headers, Body: body, Timeout: 10 * time.Second})
	if err != nil {
		return 0, nil, err
	}
	return int(response.Status), response.Body, nil
}

type hostAgentEngine struct {
	localHost string
	create    creationCore
	capacity  *capacityTracker
	ports     *portPoolSet
	ips       *ipPool
}

func (e hostAgentEngine) validate(inv hostagent.Invocation) error {
	if inv.HostID != e.localHost || inv.WorkloadID == "" {
		return errors.New("cross-host or empty assignment")
	}
	return nil
}
func (e hostAgentEngine) Create(_ context.Context, inv hostagent.Invocation) (hostagent.Outcome, error) {
	if err := e.validate(inv); err != nil {
		return hostagent.Outcome{}, err
	}
	spec, err := hostagent.DecodeExecutable(inv)
	if err != nil {
		return hostagent.Outcome{}, permanentHostAgentError{err.Error()}
	}
	req := orchestration.CreateServerRequest{Template: spec.Template, ServerID: inv.WorkloadID, Env: spec.Environment, Resources: &orchestration.ResourceOverride{CPULimit: float64(spec.Resources.CPUMillicores) / 1000, MemoryLimit: spec.Resources.MemoryBytes}}
	if _, err = e.create.CreateFenced(req, inv.IdempotencyKey, inv.IdempotencyKey); err != nil {
		return hostagent.Outcome{}, err
	}
	return hostagent.Outcome{State: "running"}, nil
}
func (e hostAgentEngine) Start(_ context.Context, inv hostagent.Invocation) (hostagent.Outcome, error) {
	if err := e.validate(inv); err != nil {
		return hostagent.Outcome{}, err
	}
	server, found, err := existingServerForRequestedID(inv.WorkloadID, docker.Get)
	if err != nil || !found {
		return hostagent.Outcome{}, fmt.Errorf("resolve assigned runtime: %w", err)
	}
	if err = docker.Restart(server.ID); err != nil {
		return hostagent.Outcome{}, err
	}
	return hostagent.Outcome{State: "running"}, nil
}
func (e hostAgentEngine) Update(_ context.Context, inv hostagent.Invocation) (hostagent.Outcome, error) {
	if err := e.validate(inv); err != nil {
		return hostagent.Outcome{}, err
	}
	spec, err := hostagent.DecodeExecutable(inv)
	if err != nil {
		return hostagent.Outcome{}, permanentHostAgentError{err.Error()}
	}
	server, found, err := existingServerForRequestedID(inv.WorkloadID, docker.Get)
	if err != nil || !found {
		return hostagent.Outcome{}, fmt.Errorf("resolve assigned runtime: %w", err)
	}
	request := fleetLifecycleRequest{ServerID: inv.WorkloadID, NodeID: e.localHost, Env: spec.Environment}
	if err = executeFleetLifecycle("reconfigure", server.ID, request); err != nil {
		return hostagent.Outcome{}, err
	}
	return hostagent.Outcome{State: "running"}, nil
}
func (e hostAgentEngine) Stop(_ context.Context, inv hostagent.Invocation) (hostagent.Outcome, error) {
	if err := e.validate(inv); err != nil {
		return hostagent.Outcome{}, err
	}
	server, found, err := existingServerForRequestedID(inv.WorkloadID, docker.Get)
	if err != nil || !found {
		return hostagent.Outcome{}, fmt.Errorf("resolve assigned runtime: %w", err)
	}
	if err = executeFleetLifecycle("suspend", server.ID, fleetLifecycleRequest{ServerID: inv.WorkloadID, NodeID: e.localHost}); err != nil {
		return hostagent.Outcome{}, err
	}
	return hostagent.Outcome{State: "stopped"}, nil
}
func (e hostAgentEngine) Delete(_ context.Context, inv hostagent.Invocation) (hostagent.Outcome, error) {
	if err := e.validate(inv); err != nil {
		return hostagent.Outcome{}, err
	}
	server, found, err := existingServerForRequestedID(inv.WorkloadID, docker.Get)
	if err != nil {
		return hostagent.Outcome{}, err
	}
	if !found {
		return hostagent.Outcome{State: "deleted"}, nil
	}
	if err = retireServer(server.ID, false, inv.WorkloadID, docker.Destroy, e.capacity, e.ports, e.ips); err != nil {
		return hostagent.Outcome{}, err
	}
	return hostagent.Outcome{State: "deleted"}, nil
}

type permanentHostAgentError struct{ message string }

func (e permanentHostAgentError) Error() string { return e.message }
func (permanentHostAgentError) Retryable() bool { return false }

type hostAgentRuntime struct {
	client                               *hostagent.Client
	interval                             time.Duration
	next                                 time.Time
	lastGeneration                       uint64
	cpuMillis, memoryBytes, storageBytes int64
}

func (r *hostAgentRuntime) Step(now time.Time) error {
	if now.Before(r.next) {
		return nil
	}
	r.next = now.Add(r.interval)
	generation := uint64(now.UnixMilli())
	if generation <= r.lastGeneration {
		generation = r.lastGeneration + 1
	}
	r.lastGeneration = generation
	if err := r.client.Observe(context.Background(), hostagent.HostObservation{Observed: "ready", HeartbeatGeneration: generation, AtUnixMilli: now.UnixMilli(), CPUMillicores: r.cpuMillis, MemoryBytes: r.memoryBytes, StorageBytes: r.storageBytes}); err != nil {
		return err
	}
	_, err := r.client.Step(context.Background())
	return err
}

func parseHostAgentPrincipals(raw string) ([]hostagent.PrincipalCredential, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, errors.New("host agent principals are required")
	}
	var result []hostagent.PrincipalCredential
	for _, item := range strings.Split(raw, ",") {
		parts := strings.SplitN(strings.TrimSpace(item), "@", 2)
		if len(parts) != 2 {
			return nil, errors.New("principal must be token@unix-expiry")
		}
		expires, err := strconv.ParseInt(parts[1], 10, 64)
		if err != nil || expires <= 0 {
			return nil, errors.New("invalid principal expiry")
		}
		result = append(result, hostagent.PrincipalCredential{Token: parts[0], ExpiresAt: time.Unix(expires, 0)})
	}
	return result, nil
}

func newHostAgentRuntime(cfg appConfig, create creationCore, capacity *capacityTracker, ports *portPoolSet, ips *ipPool) (*hostAgentRuntime, error) {
	if !cfg.HostAgentEnabled {
		return nil, nil
	}
	if cfg.HostAgentURL == "" || cfg.HostAgentID == "" {
		return nil, errors.New("enabled host agent requires endpoint and agent identity")
	}
	principals, err := parseHostAgentPrincipals(cfg.HostAgentPrincipals)
	if err != nil {
		return nil, err
	}
	engine := hostAgentEngine{localHost: cfg.RuntimeNodeID, create: create, capacity: capacity, ports: ports, ips: ips}
	client, err := hostagent.NewWithTransport(hostagent.Config{BaseURL: cfg.HostAgentURL, HostID: cfg.RuntimeNodeID, AgentID: cfg.HostAgentID, Principals: principals, MaxActions: cfg.HostAgentMaxActions}, pulpHostAgentTransport{}, engine)
	if err != nil {
		return nil, err
	}
	interval := time.Duration(cfg.HostAgentIntervalMillis) * time.Millisecond
	if interval == 0 {
		interval = 5 * time.Second
	}
	if interval < time.Second || interval > time.Minute {
		return nil, errors.New("host agent interval must be between 1s and 1m")
	}
	return &hostAgentRuntime{client: client, interval: interval, cpuMillis: int64(cfg.CPUBudget * 1000), memoryBytes: int64(cfg.MemBudget * (1 << 30)), storageBytes: int64(cfg.NodeDiskTotal)}, nil
}
