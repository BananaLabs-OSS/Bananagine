package main

// This file deliberately exposes two narrow world-transfer operations instead
// of widening exec-v2.  The caller presents an immutable object generation and
// a short-lived download reference minted by its host capability; Bananagine
// never receives storage credentials or an object-store namespace capability.

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/BananaLabs-OSS/Fiber/pulp/docker"
	pulpgin "github.com/BananaLabs-OSS/Fiber/pulp/gin"
)

const fleetWorldTransferContract = "world-transfer.v1"

type fleetWorldTransferRequest struct {
	Version  string                      `json:"version"`
	ServerID string                      `json:"server_id"`
	NodeID   string                      `json:"node_id"`
	Object   fleetWorldTransferObject    `json:"object"`
	Transfer fleetWorldTransferReference `json:"transfer"`
}

type fleetWorldTransferObject struct {
	Namespace  string `json:"namespace"`
	Key        string `json:"key"`
	Generation uint64 `json:"generation"`
	SHA256     string `json:"sha256"`
	SizeBytes  int64  `json:"size_bytes"`
}

type fleetWorldTransferReference struct {
	URL           string `json:"url"`
	ExpiresAtUnix int64  `json:"expires_at_unix"`
}

func registerFleetWorldTransferRoutes(group *pulpgin.RouterGroup) {
	group.POST("/servers/:id/world-restore", fleetWorldTransferHandler("restore"))
	group.POST("/servers/:id/world-upload-apply", fleetWorldTransferHandler("upload-apply"))
}

func fleetWorldTransferHandler(action string) pulpgin.HandlerFunc {
	return func(c *pulpgin.Context) {
		containerID := strings.TrimSpace(c.Param("id"))
		if !validFleetIdentity(containerID) {
			c.JSON(400, pulpgin.H{"error": "invalid container identity"})
			return
		}
		request, err := decodeFleetWorldTransferRequest(c)
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
			c.JSON(400, pulpgin.H{"error": "invalid typed world transfer"})
			return
		}
		digest := sha256.Sum256(append([]byte("world-transfer\x00"+action+"\x00"+containerID+"\x00"), requestWire...))
		requestSHA := hex.EncodeToString(digest[:])
		receiptPath := fleetReceiptPath(idempotencyKey)
		if replay, found, err := loadFleetLifecycleReceipt(receiptPath); err != nil {
			c.JSON(500, pulpgin.H{"error": "idempotency receipt unavailable"})
			return
		} else if found {
			if replay.IdempotencyKey != idempotencyKey || replay.EffectID != effectID || replay.Action != "world-"+action || replay.ContainerID != containerID || replay.RequestSHA256 != requestSHA {
				c.JSON(409, pulpgin.H{"error": "idempotency key already belongs to a different world transfer"})
				return
			}
			c.Data(200, "application/json; charset=utf-8", replay.Response)
			return
		}
		server, err := docker.Get(containerID)
		if err != nil {
			if isDockerNotFound(err) {
				c.JSON(404, pulpgin.H{"error": "server not found"})
			} else {
				c.JSON(500, pulpgin.H{"error": "scoped runtime lookup unavailable"})
			}
			return
		}
		owned, err := getOwnedServer(request.ServerID)
		if err != nil {
			if isDockerNotFound(err) {
				c.JSON(404, pulpgin.H{"error": "server not found"})
			} else {
				c.JSON(500, pulpgin.H{"error": "scoped server lookup unavailable"})
			}
			return
		}
		if server == nil || owned == nil || server.ID != containerID || owned.ID != containerID {
			c.JSON(409, pulpgin.H{"error": "server identity does not match container"})
			return
		}
		flag := "/data/.sessions-restore"
		status := "restoring"
		if action == "upload-apply" {
			flag, status = "/data/.sessions-upload", "replacing"
		}
		// The URL is passed as a positional shell argument, never interpolated
		// into source. This is the only runtime materialization path exposed.
		if _, err := docker.Exec(containerID, []string{"sh", "-c", "printf '%s' \"$1\" > " + flag, "--", request.Transfer.URL}); err != nil {
			c.JSON(422, pulpgin.H{"error": "stage typed world transfer: " + err.Error()})
			return
		}
		if err := docker.Restart(containerID); err != nil {
			c.JSON(422, pulpgin.H{"error": "restart typed world transfer: " + err.Error()})
			return
		}
		responseWire, err := json.Marshal(pulpgin.H{"id": containerID, "server_id": request.ServerID, "node_id": request.NodeID, "container_id": containerID, "status": status})
		if err != nil {
			c.JSON(500, pulpgin.H{"error": "encode world transfer response"})
			return
		}
		if err := storeFleetLifecycleReceipt(receiptPath, effectID, fleetLifecycleReceipt{IdempotencyKey: idempotencyKey, EffectID: effectID, Action: "world-" + action, ContainerID: containerID, RequestSHA256: requestSHA, Response: responseWire}); err != nil {
			c.JSON(500, pulpgin.H{"error": "persist idempotency receipt"})
			return
		}
		c.Data(200, "application/json; charset=utf-8", responseWire)
	}
}

func decodeFleetWorldTransferRequest(c *pulpgin.Context) (fleetWorldTransferRequest, error) {
	var fields map[string]json.RawMessage
	if err := c.ShouldBindJSON(&fields); err != nil {
		return fleetWorldTransferRequest{}, errors.New("valid JSON body is required")
	}
	if len(fields) != 5 {
		return fleetWorldTransferRequest{}, errors.New("world transfer requires exactly version, server_id, node_id, object, and transfer")
	}
	for key := range fields {
		if key != "version" && key != "server_id" && key != "node_id" && key != "object" && key != "transfer" {
			return fleetWorldTransferRequest{}, fmt.Errorf("payload field %q is not allowed", key)
		}
	}
	wire, err := json.Marshal(fields)
	if err != nil {
		return fleetWorldTransferRequest{}, errors.New("invalid world transfer payload")
	}
	var request fleetWorldTransferRequest
	decoder := json.NewDecoder(bytes.NewReader(wire))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		return fleetWorldTransferRequest{}, errors.New("invalid world transfer payload")
	}
	if err := validateFleetWorldTransferRequest(request); err != nil {
		return fleetWorldTransferRequest{}, err
	}
	return request, nil
}

func validateFleetWorldTransferRequest(request fleetWorldTransferRequest) error {
	if request.Version != fleetWorldTransferContract || !validFleetIdentity(request.ServerID) || !validFleetIdentity(request.NodeID) ||
		!validFleetToken(request.Object.Namespace, 256) || !validFleetObjectKey(request.Object.Key) || request.Object.Generation == 0 || request.Object.SizeBytes <= 0 ||
		len(request.Object.SHA256) != 64 || request.Object.SHA256 != strings.ToLower(request.Object.SHA256) || request.Transfer.ExpiresAtUnix <= time.Now().Unix() || len(request.Transfer.URL) == 0 || len(request.Transfer.URL) > 8192 {
		return errors.New("typed world transfer identity is invalid")
	}
	if _, err := hex.DecodeString(request.Object.SHA256); err != nil {
		return errors.New("typed world transfer digest is invalid")
	}
	reference, err := url.Parse(request.Transfer.URL)
	if err != nil || reference.Scheme != "https" || reference.Host == "" || reference.User != nil || reference.Fragment != "" {
		return errors.New("typed world transfer reference is invalid")
	}
	return nil
}

func validFleetObjectKey(value string) bool {
	if value == "" || len(value) > 1024 || strings.HasPrefix(value, "/") ||
		strings.HasSuffix(value, "/") || strings.Contains(value, "..") ||
		strings.ContainsAny(value, "\\\r\n\x00") {
		return false
	}
	for _, r := range value {
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '.' || r == ':' || r == '-' || r == '/') {
			return false
		}
	}
	return true
}
