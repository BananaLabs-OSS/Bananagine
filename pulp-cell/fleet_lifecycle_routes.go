package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"unicode"

	"github.com/BananaLabs-OSS/Fiber/pulp"
	"github.com/BananaLabs-OSS/Fiber/pulp/docker"
	pulpgin "github.com/BananaLabs-OSS/Fiber/pulp/gin"
)

const (
	fleetIdempotencyHeader = "Idempotency-Key"
	fleetEffectIDHeader    = "X-Pulp-Effect-ID"
	fleetReceiptDir        = "fleet-lifecycle-receipts"
)

type fleetLifecycleRequest struct {
	ServerID  string            `json:"server_id"`
	NodeID    string            `json:"node_id"`
	Resources fleetResources    `json:"resources"`
	Env       map[string]string `json:"env"`
}

type fleetResources struct {
	CPU    float64 `json:"cpu_limit"`
	Memory int64   `json:"memory_limit"`
}

type fleetLifecycleReceipt struct {
	IdempotencyKey string          `json:"idempotency_key"`
	EffectID       string          `json:"effect_id"`
	Action         string          `json:"action"`
	ContainerID    string          `json:"container_id"`
	RequestSHA256  string          `json:"request_sha256"`
	Response       json.RawMessage `json:"response"`
}

func registerFleetLifecycleRoutes(group *pulpgin.RouterGroup) {
	group.POST("/servers/:id/reconfigure", fleetLifecycleHandler("reconfigure"))
	group.POST("/servers/:id/suspend", fleetLifecycleHandler("suspend"))
	group.POST("/servers/:id/resume", fleetLifecycleHandler("resume"))
	group.POST("/servers/:id/restart", fleetLifecycleHandler("restart"))
	group.POST("/servers/:id/regenerate", fleetLifecycleHandler("regenerate"))
	// Config apply is deliberately a distinct, typed route.  It reuses the
	// bounded reconfigure compiler below, but is not an alias for the generic
	// /exec surface: Fleet can request only approved settings and gamerules.
	group.POST("/servers/:id/config-apply", fleetLifecycleHandler("config-apply"))
	group.POST("/servers/:id/access-apply", fleetAccessApplyHandler)
	registerFleetWorldTransferRoutes(group)
}

const fleetAccessApplyMaxCommands = 100

var fleetMinecraftPlayerName = regexp.MustCompile(`^[A-Za-z0-9_]{1,16}$`)
var fleetCanonicalUUID = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// fleetAccessApplyRequest is the host-side DTO for Fleet's exact durable
// access/player intent. It is intentionally not a general Minecraft command
// request: every possible command is constructed below from this allowlist.
type fleetAccessApplyRequest struct {
	ServerID   string                  `json:"server_id"`
	NodeID     string                  `json:"node_id"`
	Operation  string                  `json:"operation"`
	Generation uint64                  `json:"generation"`
	Access     *fleetAccessMutationDTO `json:"access,omitempty"`
	Player     *fleetPlayerActionDTO   `json:"player,omitempty"`
}
type fleetAccessMutationDTO struct {
	Kind    string                   `json:"kind"`
	Action  string                   `json:"action"`
	Entries []fleetPlayerIdentityDTO `json:"entries"`
}
type fleetPlayerActionDTO struct {
	Action string                  `json:"action"`
	Target *fleetPlayerIdentityDTO `json:"target,omitempty"`
	Value  string                  `json:"value,omitempty"`
	Reason struct {
		Code    string `json:"code"`
		Message string `json:"message,omitempty"`
	} `json:"reason"`
}
type fleetPlayerIdentityDTO struct {
	Name     string `json:"name"`
	UUID     string `json:"uuid"`
	Platform string `json:"platform"`
}

func fleetAccessApplyHandler(c *pulpgin.Context) {
	containerID := strings.TrimSpace(c.Param("id"))
	if !validFleetIdentity(containerID) {
		c.JSON(400, pulpgin.H{"error": "invalid container identity"})
		return
	}
	request, err := decodeFleetAccessApplyRequest(c)
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
		c.JSON(400, pulpgin.H{"error": "invalid access request"})
		return
	}
	digest := sha256.Sum256(append([]byte("access-apply\x00"+containerID+"\x00"), requestWire...))
	requestSHA := hex.EncodeToString(digest[:])
	receiptPath := fleetReceiptPath(idempotencyKey)
	if replay, found, err := loadFleetLifecycleReceipt(receiptPath); err != nil {
		c.JSON(500, pulpgin.H{"error": "idempotency receipt unavailable"})
		return
	} else if found {
		if replay.IdempotencyKey != idempotencyKey || replay.EffectID != effectID || replay.Action != "access-apply" || replay.ContainerID != containerID || replay.RequestSHA256 != requestSHA {
			c.JSON(409, pulpgin.H{"error": "idempotency key already belongs to a different operation"})
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
			c.JSON(500, pulpgin.H{"error": err.Error()})
		}
		return
	}
	if server == nil || server.ID != containerID {
		c.JSON(409, pulpgin.H{"error": "container identity mismatch"})
		return
	}
	owned, err := getOwnedServer(request.ServerID)
	if err != nil {
		c.JSON(500, pulpgin.H{"error": "scoped server lookup unavailable"})
		return
	}
	if owned == nil || owned.ID != containerID {
		c.JSON(409, pulpgin.H{"error": "server identity does not match container"})
		return
	}
	commands, err := fleetAccessApplyCommands(request)
	if err != nil {
		c.JSON(422, pulpgin.H{"error": err.Error()})
		return
	}
	for _, command := range commands {
		if _, err := docker.Exec(containerID, command); err != nil {
			c.JSON(422, pulpgin.H{"error": "apply typed access operation: " + err.Error()})
			return
		}
	}
	responseWire, err := json.Marshal(pulpgin.H{"id": containerID, "server_id": request.ServerID, "node_id": request.NodeID, "container_id": containerID, "status": "access_applied"})
	if err != nil {
		c.JSON(500, pulpgin.H{"error": "encode access response"})
		return
	}
	if err := storeFleetLifecycleReceipt(receiptPath, effectID, fleetLifecycleReceipt{IdempotencyKey: idempotencyKey, EffectID: effectID, Action: "access-apply", ContainerID: containerID, RequestSHA256: requestSHA, Response: responseWire}); err != nil {
		c.JSON(500, pulpgin.H{"error": "persist idempotency receipt"})
		return
	}
	c.Data(200, "application/json; charset=utf-8", responseWire)
}

func decodeFleetAccessApplyRequest(c *pulpgin.Context) (fleetAccessApplyRequest, error) {
	var fields map[string]json.RawMessage
	if err := c.ShouldBindJSON(&fields); err != nil {
		return fleetAccessApplyRequest{}, errors.New("valid JSON body is required")
	}
	for key := range fields {
		if key != "server_id" && key != "node_id" && key != "operation" && key != "generation" && key != "access" && key != "player" {
			return fleetAccessApplyRequest{}, fmt.Errorf("payload field %q is not allowed", key)
		}
	}
	requestWire, err := json.Marshal(fields)
	if err != nil {
		return fleetAccessApplyRequest{}, errors.New("invalid access payload")
	}
	var request fleetAccessApplyRequest
	decoder := json.NewDecoder(bytes.NewReader(requestWire))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		return fleetAccessApplyRequest{}, errors.New("invalid access payload")
	}
	if !validFleetIdentity(request.ServerID) || !validFleetIdentity(request.NodeID) || request.Generation == 0 {
		return fleetAccessApplyRequest{}, errors.New("valid server, node, and generation are required")
	}
	if (request.Operation == "access") == (request.Operation == "player_action") || (request.Access == nil) == (request.Player == nil) {
		return fleetAccessApplyRequest{}, errors.New("exactly one typed access or player operation is required")
	}
	if request.Operation == "access" {
		if err := validateFleetAccessMutation(*request.Access); err != nil {
			return fleetAccessApplyRequest{}, err
		}
	} else if err := validateFleetPlayerAction(*request.Player); err != nil {
		return fleetAccessApplyRequest{}, err
	}
	return request, nil
}

func validateFleetPlayerIdentity(value fleetPlayerIdentityDTO) error {
	if !fleetMinecraftPlayerName.MatchString(value.Name) || !fleetCanonicalUUID.MatchString(value.UUID) || value.Platform != "java" {
		return errors.New("typed player identity is invalid")
	}
	return nil
}
func validateFleetAccessMutation(value fleetAccessMutationDTO) error {
	if (value.Kind != "whitelist" && value.Kind != "operators" && value.Kind != "bans") || (value.Action != "add" && value.Action != "remove") || len(value.Entries) == 0 || len(value.Entries) > fleetAccessApplyMaxCommands {
		return errors.New("typed access mutation is invalid")
	}
	for _, entry := range value.Entries {
		if err := validateFleetPlayerIdentity(entry); err != nil {
			return err
		}
	}
	return nil
}
func validateFleetPlayerAction(value fleetPlayerActionDTO) error {
	if len(value.Reason.Code) == 0 || len(value.Reason.Code) > 64 || strings.IndexFunc(value.Reason.Code, unicode.IsControl) >= 0 || len(value.Reason.Message) > 256 || strings.IndexFunc(value.Reason.Message, unicode.IsControl) >= 0 {
		return errors.New("player reason is invalid")
	}
	switch value.Action {
	case "kick", "ban", "pardon", "op", "deop", "gamemode":
		if value.Target == nil {
			return errors.New("player action target is required")
		}
		if err := validateFleetPlayerIdentity(*value.Target); err != nil {
			return err
		}
	case "time", "weather", "announce":
		if value.Target != nil {
			return errors.New("global player action cannot include target")
		}
	default:
		return errors.New("player action is not allowlisted")
	}
	if value.Action == "gamemode" && !oneOf(value.Value, "survival", "creative", "adventure", "spectator") {
		return errors.New("gamemode is not allowed")
	}
	if value.Action == "time" && !oneOf(value.Value, "day", "noon", "night", "midnight") {
		return errors.New("time is not allowed")
	}
	if value.Action == "weather" && !oneOf(value.Value, "clear", "rain", "thunder") {
		return errors.New("weather is not allowed")
	}
	if value.Action == "announce" && (value.Value == "" || len(value.Value) > 256 || strings.IndexFunc(value.Value, unicode.IsControl) >= 0) {
		return errors.New("announcement is invalid")
	}
	if value.Action != "gamemode" && value.Action != "time" && value.Action != "weather" && value.Action != "announce" && value.Value != "" {
		return errors.New("player action value is not allowed")
	}
	return nil
}
func fleetAccessApplyCommands(request fleetAccessApplyRequest) ([][]string, error) {
	if request.Access != nil {
		commands := make([][]string, 0, len(request.Access.Entries))
		for _, entry := range request.Access.Entries {
			command := ""
			switch request.Access.Kind {
			case "whitelist":
				command = "whitelist "
			case "operators":
				command = ""
			case "bans":
				command = ""
			}
			switch request.Access.Kind {
			case "whitelist":
				if request.Access.Action == "add" {
					command += "add "
				} else {
					command += "remove "
				}
			case "operators":
				if request.Access.Action == "add" {
					command = "op "
				} else {
					command = "deop "
				}
			case "bans":
				if request.Access.Action == "add" {
					command = "ban "
				} else {
					command = "pardon "
				}
			}
			commands = append(commands, []string{"rcon", command + entry.Name})
		}
		return commands, nil
	}
	p := *request.Player
	target := ""
	if p.Target != nil {
		target = p.Target.Name
	}
	switch p.Action {
	case "kick":
		return [][]string{{"rcon", "kick " + target + fleetOptionalReason(p.Reason.Message)}}, nil
	case "ban":
		return [][]string{{"rcon", "ban " + target + fleetOptionalReason(p.Reason.Message)}}, nil
	case "pardon":
		return [][]string{{"rcon", "pardon " + target}}, nil
	case "op":
		return [][]string{{"rcon", "op " + target}}, nil
	case "deop":
		return [][]string{{"rcon", "deop " + target}}, nil
	case "gamemode":
		return [][]string{{"rcon", "gamemode " + p.Value + " " + target}}, nil
	case "time":
		return [][]string{{"rcon", "time set " + p.Value}}, nil
	case "weather":
		return [][]string{{"rcon", "weather " + p.Value}}, nil
	case "announce":
		wire, _ := json.Marshal(struct {
			Text string `json:"text"`
		}{Text: p.Value})
		return [][]string{{"rcon", "tellraw @a " + string(wire)}}, nil
	}
	return nil, errors.New("player action is not allowlisted")
}
func fleetOptionalReason(message string) string {
	if message == "" {
		return ""
	}
	return " " + message
}

func fleetLifecycleHandler(action string) pulpgin.HandlerFunc {
	return func(c *pulpgin.Context) {
		containerID := strings.TrimSpace(c.Param("id"))
		if !validFleetIdentity(containerID) {
			c.JSON(400, pulpgin.H{"error": "invalid container identity"})
			return
		}
		request, err := decodeFleetLifecycleRequest(c, action)
		if err != nil {
			c.JSON(400, pulpgin.H{"error": err.Error()})
			return
		}
		idempotencyKey := c.GetHeader(fleetIdempotencyHeader)
		effectID := c.GetHeader(fleetEffectIDHeader)
		if !validFleetEffectIdentity(idempotencyKey) || !validFleetEffectIdentity(effectID) {
			c.JSON(400, pulpgin.H{"error": "valid idempotency and effect identities are required"})
			return
		}

		requestWire, err := json.Marshal(request)
		if err != nil {
			c.JSON(400, pulpgin.H{"error": "invalid lifecycle request"})
			return
		}
		requestDigest := sha256.Sum256(append([]byte(action+"\x00"+containerID+"\x00"), requestWire...))
		requestSHA := hex.EncodeToString(requestDigest[:])
		receiptPath := fleetReceiptPath(idempotencyKey)
		if replay, found, err := loadFleetLifecycleReceipt(receiptPath); err != nil {
			c.JSON(500, pulpgin.H{"error": "idempotency receipt unavailable"})
			return
		} else if found {
			if replay.IdempotencyKey != idempotencyKey || replay.EffectID != effectID || replay.Action != action || replay.ContainerID != containerID || replay.RequestSHA256 != requestSHA {
				c.JSON(409, pulpgin.H{"error": "idempotency key already belongs to a different operation"})
				return
			}
			c.Data(200, "application/json; charset=utf-8", replay.Response)
			return
		}

		server, err := docker.Get(containerID)
		if err != nil {
			if isDockerNotFound(err) {
				c.JSON(404, pulpgin.H{"error": "server not found"})
				return
			}
			c.JSON(500, pulpgin.H{"error": err.Error()})
			return
		}
		if server == nil || server.ID != containerID {
			c.JSON(409, pulpgin.H{"error": "container identity mismatch"})
			return
		}
		if action == "restart" || action == "regenerate" || action == "config-apply" {
			// The path may be a physical, scoped container ID. Resolve the
			// caller's logical server ID through the host-owned scope and require
			// it to be the exact same runtime object before any privileged work.
			owned, err := getOwnedServer(request.ServerID)
			if err != nil {
				if isDockerNotFound(err) {
					c.JSON(404, pulpgin.H{"error": "server not found"})
					return
				}
				c.JSON(500, pulpgin.H{"error": "scoped server lookup unavailable"})
				return
			}
			if owned == nil || owned.ID != containerID {
				c.JSON(409, pulpgin.H{"error": "server identity does not match container"})
				return
			}
		}

		if err := executeFleetLifecycle(action, containerID, request); err != nil {
			c.JSON(422, pulpgin.H{"error": err.Error()})
			return
		}
		response := pulpgin.H{
			"id":           containerID,
			"server_id":    request.ServerID,
			"node_id":      request.NodeID,
			"container_id": containerID,
			"status":       fleetLifecycleStatus(action),
		}
		responseWire, err := json.Marshal(response)
		if err != nil {
			c.JSON(500, pulpgin.H{"error": "encode lifecycle response"})
			return
		}
		receipt := fleetLifecycleReceipt{
			IdempotencyKey: idempotencyKey, EffectID: effectID, Action: action,
			ContainerID: containerID, RequestSHA256: requestSHA, Response: responseWire,
		}
		if err := storeFleetLifecycleReceipt(receiptPath, effectID, receipt); err != nil {
			c.JSON(500, pulpgin.H{"error": "persist idempotency receipt"})
			return
		}
		c.Data(200, "application/json; charset=utf-8", responseWire)
	}
}

func fleetLifecycleStatus(action string) string {
	switch action {
	case "reconfigure":
		return "reconfigured"
	case "suspend":
		return "suspended"
	case "resume":
		return "resumed"
	case "restart":
		return "restarted"
	case "regenerate":
		return "regenerated"
	case "config-apply":
		return "config_applied"
	default:
		return ""
	}
}

func decodeFleetLifecycleRequest(c *pulpgin.Context, action string) (fleetLifecycleRequest, error) {
	var fields map[string]json.RawMessage
	if err := c.ShouldBindJSON(&fields); err != nil {
		return fleetLifecycleRequest{}, errors.New("valid JSON body is required")
	}
	if action == "restart" || action == "regenerate" {
		if len(fields) != 2 || fields["server_id"] == nil || fields["node_id"] == nil {
			return fleetLifecycleRequest{}, errors.New("restart and regenerate require exactly server_id and node_id")
		}
		for field := range fields {
			if field != "server_id" && field != "node_id" {
				return fleetLifecycleRequest{}, fmt.Errorf("payload field %q is not allowed", field)
			}
		}
		var request fleetLifecycleRequest
		if err := c.ShouldBindJSON(&request); err != nil {
			return fleetLifecycleRequest{}, errors.New("invalid lifecycle payload")
		}
		if !validFleetIdentity(request.ServerID) || !validFleetIdentity(request.NodeID) {
			return fleetLifecycleRequest{}, errors.New("valid server and node identities are required")
		}
		return request, nil
	}
	for field := range fields {
		switch field {
		case "server_id", "node_id", "resources", "env":
		default:
			return fleetLifecycleRequest{}, fmt.Errorf("payload field %q is not allowed", field)
		}
	}
	var request fleetLifecycleRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		return fleetLifecycleRequest{}, errors.New("invalid lifecycle payload")
	}
	if !validFleetIdentity(request.ServerID) || !validFleetIdentity(request.NodeID) {
		return fleetLifecycleRequest{}, errors.New("valid server and node identities are required")
	}
	if request.Resources.CPU < 0 || request.Resources.Memory < 0 {
		return fleetLifecycleRequest{}, errors.New("resource limits cannot be negative")
	}
	return request, nil
}

func executeFleetLifecycle(action, containerID string, request fleetLifecycleRequest) error {
	switch action {
	case "reconfigure", "config-apply":
		if request.Resources.CPU != 0 || request.Resources.Memory != 0 {
			return errors.New("live resource reconfiguration is not supported by this runtime")
		}
		commands, err := fleetReconfigureCommands(request.Env)
		if err != nil {
			return err
		}
		for _, command := range commands {
			if _, err := docker.Exec(containerID, command); err != nil {
				return fmt.Errorf("apply runtime configuration: %w", err)
			}
		}
		return nil
	case "suspend":
		if request.Resources.CPU != 0 || request.Resources.Memory != 0 || len(request.Env) != 0 {
			return errors.New("suspend does not accept resource or environment changes")
		}
		if _, err := docker.Exec(containerID, []string{"rcon", "save-all flush"}); err != nil {
			return fmt.Errorf("flush before suspend: %w", err)
		}
		if _, err := docker.Exec(containerID, []string{"rcon", "stop"}); err != nil {
			return fmt.Errorf("suspend runtime: %w", err)
		}
		return nil
	case "resume":
		if request.Resources.CPU != 0 || request.Resources.Memory != 0 || len(request.Env) != 0 {
			return errors.New("resume does not accept resource or environment changes")
		}
		return docker.Restart(containerID)
	case "restart":
		if request.Resources.CPU != 0 || request.Resources.Memory != 0 || len(request.Env) != 0 {
			return errors.New("restart does not accept resource or environment changes")
		}
		return docker.Restart(containerID)
	case "regenerate":
		if request.Resources.CPU != 0 || request.Resources.Memory != 0 || len(request.Env) != 0 {
			return errors.New("regenerate does not accept resource or environment changes")
		}
		for _, command := range [][]string{
			{"rcon", "save-off"}, {"rcon", "save-all flush"},
			{"sh", "-c", "touch /data/.sessions-regen"},
		} {
			if _, err := docker.Exec(containerID, command); err != nil {
				return fmt.Errorf("prepare world regeneration: %w", err)
			}
		}
		return docker.Restart(containerID)
	default:
		return errors.New("unsupported lifecycle action")
	}
}

func fleetReconfigureCommands(env map[string]string) ([][]string, error) {
	keys := make([]string, 0, len(env))
	for key := range env {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	commands := make([][]string, 0, len(keys))
	for _, key := range keys {
		value := env[key]
		switch {
		case key == "FLEET_SETTING_difficulty":
			if !oneOf(value, "peaceful", "easy", "normal", "hard") {
				return nil, errors.New("difficulty is not allowed")
			}
			commands = append(commands, []string{"rcon", "difficulty " + value})
		case key == "FLEET_SETTING_gamemode" || key == "FLEET_SETTING_default_gamemode":
			if !oneOf(value, "survival", "creative", "adventure", "spectator") {
				return nil, errors.New("gamemode is not allowed")
			}
			commands = append(commands, []string{"rcon", "defaultgamemode " + value})
		case strings.HasPrefix(key, "FLEET_GAMERULE_"):
			rule := strings.TrimPrefix(key, "FLEET_GAMERULE_")
			if !validFleetToken(rule, 64) || !validFleetRuleValue(value) {
				return nil, errors.New("gamerule is not allowed")
			}
			commands = append(commands, []string{"rcon", "gamerule " + rule + " " + value})
		default:
			return nil, fmt.Errorf("runtime setting %q is not allowed", key)
		}
	}
	return commands, nil
}

func loadFleetLifecycleReceipt(path string) (fleetLifecycleReceipt, bool, error) {
	wire, err := pulp.FS.Read(path)
	if err != nil {
		if errors.Is(err, pulp.ErrNotFound) {
			return fleetLifecycleReceipt{}, false, nil
		}
		return fleetLifecycleReceipt{}, false, err
	}
	var receipt fleetLifecycleReceipt
	if err := json.Unmarshal(wire, &receipt); err != nil || len(receipt.Response) == 0 {
		return fleetLifecycleReceipt{}, false, errors.New("invalid persisted lifecycle receipt")
	}
	return receipt, true, nil
}

func storeFleetLifecycleReceipt(path, effectID string, receipt fleetLifecycleReceipt) error {
	wire, err := json.Marshal(receipt)
	if err != nil {
		return err
	}
	if err := pulp.FS.MkdirAll(fleetReceiptDir, 0o700); err != nil {
		return err
	}
	temp := path + "." + fleetReceiptName(effectID) + ".tmp"
	if err := pulp.FS.WriteMode(temp, wire, 0o600); err != nil {
		return err
	}
	return pulp.FS.Rename(temp, path)
}

func fleetReceiptPath(key string) string {
	return fleetReceiptDir + "/" + fleetReceiptName(key) + ".json"
}

func fleetReceiptName(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}

func validFleetIdentity(value string) bool {
	return len(value) > 0 &&
		len(value) <= 256 &&
		value == strings.TrimSpace(value) &&
		!strings.Contains(value, "..") &&
		!strings.ContainsAny(value, "/\\\r\n\x00") &&
		strings.IndexFunc(value, unicode.IsControl) < 0
}

func validFleetEffectIdentity(value string) bool {
	return len(value) > 0 &&
		len(value) <= 256 &&
		value == strings.TrimSpace(value) &&
		strings.IndexFunc(value, unicode.IsControl) < 0
}

func validFleetToken(value string, max int) bool {
	if value == "" || len(value) > max {
		return false
	}
	for _, r := range value {
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '.' || r == ':' || r == '-') {
			return false
		}
	}
	return true
}

func validFleetRuleValue(value string) bool {
	if oneOf(value, "true", "false") {
		return true
	}
	if value == "" || len(value) > 11 {
		return false
	}
	for index, r := range value {
		if index == 0 && r == '-' {
			continue
		}
		if r < '0' || r > '9' {
			return false
		}
	}
	return value != "-"
}

func oneOf(value string, allowed ...string) bool {
	for _, candidate := range allowed {
		if value == candidate {
			return true
		}
	}
	return false
}
