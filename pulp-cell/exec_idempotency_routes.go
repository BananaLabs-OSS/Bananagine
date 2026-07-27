package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"github.com/BananaLabs-OSS/Fiber/pulp"
	"github.com/BananaLabs-OSS/Fiber/pulp/docker"
	pulpgin "github.com/BananaLabs-OSS/Fiber/pulp/gin"
	"github.com/bananalabs-oss/bananagine/orchestration"
)

const fleetExecV2ReceiptDir = "fleet-exec-v2-receipts"

// fleetExecV2Request is deliberately separate from the legacy exec DTO. The
// extra identities make retries bind to one logical server, one node, and one
// scoped physical container; generic v1 callers cannot accidentally claim the
// stronger semantics by adding an idempotency header.
type fleetExecV2Request struct {
	ServerID string   `json:"server_id"`
	NodeID   string   `json:"node_id"`
	Cmd      []string `json:"cmd"`
}

type fleetExecV2Receipt struct {
	IdempotencyKey string          `json:"idempotency_key"`
	EffectID       string          `json:"effect_id"`
	ServerID       string          `json:"server_id"`
	NodeID         string          `json:"node_id"`
	ContainerID    string          `json:"container_id"`
	RequestSHA256  string          `json:"request_sha256"`
	Response       json.RawMessage `json:"response"`
}

// One Pulp cell instance owns one scoped durable receipt directory. The mutex
// closes the lost-response race in that instance; the receipt itself survives
// process restart through the scoped Pulp filesystem.
var fleetExecV2ReceiptMu sync.Mutex

func registerFleetExecV2Route(group *pulpgin.RouterGroup) {
	group.POST("/servers/:id/exec-v2", fleetExecV2Handler)
}

func fleetExecV2Handler(c *pulpgin.Context) {
	containerID := c.Param("id")
	if !validFleetIdentity(containerID) {
		c.JSON(400, pulpgin.H{"error": "invalid container identity"})
		return
	}
	request, err := decodeFleetExecV2Request(c)
	if err != nil {
		c.JSON(400, pulpgin.H{"error": err.Error()})
		return
	}
	idempotencyKey, effectID := c.GetHeader(fleetIdempotencyHeader), c.GetHeader(fleetEffectIDHeader)
	if !validFleetEffectIdentity(idempotencyKey) || !validFleetEffectIdentity(effectID) {
		c.JSON(400, pulpgin.H{"error": "valid idempotency and effect identities are required"})
		return
	}
	requestWire, err := json.Marshal(request)
	if err != nil {
		c.JSON(400, pulpgin.H{"error": "invalid exec-v2 request"})
		return
	}
	digest := sha256.Sum256(append([]byte("exec-v2\x00"+containerID+"\x00"+effectID+"\x00"), requestWire...))
	requestSHA := hex.EncodeToString(digest[:])
	receiptPath := fleetExecV2ReceiptPath(idempotencyKey)

	fleetExecV2ReceiptMu.Lock()
	defer fleetExecV2ReceiptMu.Unlock()
	if receipt, found, err := loadFleetExecV2Receipt(receiptPath); err != nil {
		c.JSON(500, pulpgin.H{"error": "exec-v2 idempotency receipt unavailable"})
		return
	} else if found {
		if receipt.IdempotencyKey != idempotencyKey || receipt.EffectID != effectID || receipt.ServerID != request.ServerID || receipt.NodeID != request.NodeID || receipt.ContainerID != containerID || receipt.RequestSHA256 != requestSHA {
			c.JSON(409, pulpgin.H{"error": "idempotency key already belongs to a different exec-v2 operation"})
			return
		}
		c.Data(200, "application/json; charset=utf-8", receipt.Response)
		return
	}

	server, err := docker.Get(containerID)
	if err != nil {
		if isDockerNotFound(err) {
			c.JSON(404, pulpgin.H{"error": "server not found"})
			return
		}
		c.JSON(500, pulpgin.H{"error": "scoped runtime lookup unavailable"})
		return
	}
	owned, err := getOwnedServer(request.ServerID)
	if err != nil {
		if isDockerNotFound(err) {
			c.JSON(404, pulpgin.H{"error": "server not found"})
			return
		}
		c.JSON(500, pulpgin.H{"error": "scoped server lookup unavailable"})
		return
	}
	if server == nil || owned == nil || server.ID != containerID || owned.ID != containerID {
		c.JSON(409, pulpgin.H{"error": "server identity does not match container"})
		return
	}
	output, err := docker.Exec(containerID, request.Cmd)
	if err != nil {
		if isDockerNotFound(err) {
			c.JSON(404, pulpgin.H{"error": "server not found"})
			return
		}
		c.JSON(500, pulpgin.H{"error": "runtime exec unavailable"})
		return
	}
	response, err := json.Marshal(orchestration.ExecResponse{Output: output})
	if err != nil {
		c.JSON(500, pulpgin.H{"error": "encode exec-v2 response"})
		return
	}
	receipt := fleetExecV2Receipt{IdempotencyKey: idempotencyKey, EffectID: effectID, ServerID: request.ServerID, NodeID: request.NodeID, ContainerID: containerID, RequestSHA256: requestSHA, Response: response}
	if err := storeFleetExecV2Receipt(receiptPath, effectID, receipt); err != nil {
		c.JSON(500, pulpgin.H{"error": "persist exec-v2 idempotency receipt"})
		return
	}
	c.Data(200, "application/json; charset=utf-8", response)
}

func decodeFleetExecV2Request(c *pulpgin.Context) (fleetExecV2Request, error) {
	var fields map[string]json.RawMessage
	if err := c.ShouldBindJSON(&fields); err != nil {
		return fleetExecV2Request{}, errors.New("valid JSON body is required")
	}
	if len(fields) != 3 || fields["server_id"] == nil || fields["node_id"] == nil || fields["cmd"] == nil {
		return fleetExecV2Request{}, errors.New("exec-v2 requires exactly server_id, node_id, and cmd")
	}
	for field := range fields {
		if field != "server_id" && field != "node_id" && field != "cmd" {
			return fleetExecV2Request{}, fmt.Errorf("payload field %q is not allowed", field)
		}
	}
	var request fleetExecV2Request
	if err := c.ShouldBindJSON(&request); err != nil {
		return fleetExecV2Request{}, errors.New("invalid exec-v2 payload")
	}
	if !validFleetIdentity(request.ServerID) || !validFleetIdentity(request.NodeID) || len(request.Cmd) == 0 || !execCommandAllowed(request.Cmd) {
		return fleetExecV2Request{}, errors.New("exec-v2 requires safe server, node, and allowlisted command")
	}
	return request, nil
}

// decodeLegacyExecRequest intentionally admits only the legacy `{cmd}` shape
// and rejects v2 headers/fields. This prevents a client from relying on a
// nondurable endpoint while believing it has exactly-once execution semantics.
func decodeLegacyExecRequest(c *pulpgin.Context) (orchestration.ExecRequest, error) {
	if c.GetHeader(fleetIdempotencyHeader) != "" || c.GetHeader(fleetEffectIDHeader) != "" {
		return orchestration.ExecRequest{}, errors.New("exec v1 does not accept idempotent execution headers; use exec-v2")
	}
	var fields map[string]json.RawMessage
	if err := c.ShouldBindJSON(&fields); err != nil {
		return orchestration.ExecRequest{}, errors.New("valid JSON body is required")
	}
	if len(fields) != 1 || fields["cmd"] == nil {
		return orchestration.ExecRequest{}, errors.New("exec v1 requires exactly cmd; use exec-v2 for idempotent execution")
	}
	var request orchestration.ExecRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		return orchestration.ExecRequest{}, errors.New("invalid exec payload")
	}
	if len(request.Cmd) == 0 || !execCommandAllowed(request.Cmd) {
		return orchestration.ExecRequest{}, errors.New("cmd must be allowlisted")
	}
	return request, nil
}

func loadFleetExecV2Receipt(path string) (fleetExecV2Receipt, bool, error) {
	wire, err := pulp.FS.Read(path)
	if err != nil {
		if errors.Is(err, pulp.ErrNotFound) {
			return fleetExecV2Receipt{}, false, nil
		}
		return fleetExecV2Receipt{}, false, err
	}
	var receipt fleetExecV2Receipt
	if err := json.Unmarshal(wire, &receipt); err != nil || len(receipt.Response) == 0 {
		return fleetExecV2Receipt{}, false, errors.New("invalid persisted exec-v2 receipt")
	}
	return receipt, true, nil
}

func storeFleetExecV2Receipt(path, effectID string, receipt fleetExecV2Receipt) error {
	wire, err := json.Marshal(receipt)
	if err != nil {
		return err
	}
	if err := pulp.FS.MkdirAll(fleetExecV2ReceiptDir, 0o700); err != nil {
		return err
	}
	temporary := path + "." + fleetReceiptName(effectID) + ".tmp"
	if err := pulp.FS.WriteMode(temporary, wire, 0o600); err != nil {
		return err
	}
	return pulp.FS.Rename(temporary, path)
}

func fleetExecV2ReceiptPath(idempotencyKey string) string {
	return fleetExecV2ReceiptDir + "/" + fleetReceiptName(idempotencyKey) + ".json"
}
