package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"unicode"

	"github.com/BananaLabs-OSS/Fiber/pulp/docker"
	pulpgin "github.com/BananaLabs-OSS/Fiber/pulp/gin"
)

const (
	fleetObservationMaxBytes      = 64 << 10
	fleetObservationMaxPlayers    = 256
	fleetObservationMaxIdentities = 512
	fleetObservationMaxArtifacts  = 256
)

var fleetObservedSettings = map[string]string{
	"difficulty":          "difficulty",
	"gamemode":            "gamemode",
	"pvp":                 "pvp",
	"hardcore":            "hardcore",
	"allow-nether":        "allow_nether",
	"spawn-monsters":      "spawn_monsters",
	"spawn-animals":       "spawn_animals",
	"view-distance":       "view_distance",
	"simulation-distance": "simulation_distance",
	"motd":                "motd",
}

var fleetObservedGameRules = []string{
	"announceAdvancements",
	"commandBlockOutput",
	"doDaylightCycle",
	"doImmediateRespawn",
	"doInsomnia",
	"doMobSpawning",
	"doWeatherCycle",
	"drowningDamage",
	"fallDamage",
	"fireDamage",
	"forgiveDeadPlayers",
	"keepInventory",
	"mobGriefing",
	"naturalRegeneration",
	"playersSleepingPercentage",
	"randomTickSpeed",
	"showDeathMessages",
	"spawnRadius",
	"universalAnger",
}

type fleetStatusObservation struct {
	Status string `json:"status"`
}

type fleetSettingsObservation struct {
	Settings map[string]string `json:"settings"`
}

type fleetGameRulesObservation struct {
	GameRules map[string]string `json:"gamerules"`
}

type fleetPlayersObservation struct {
	Players []string `json:"players"`
}

// fleetPlayerHistoryEntry is a bounded identity record from Minecraft's
// usercache.json. It is intentionally distinct from Players, which remains a
// name-only online-presence probe.
type fleetPlayerHistoryEntry struct {
	UUID      string `json:"uuid"`
	Name      string `json:"name"`
	ExpiresOn string `json:"expires_on"`
}

type fleetPlayerHistoryObservation struct {
	PlayerHistory []fleetPlayerHistoryEntry `json:"player_history"`
}

type fleetAccessIdentity struct {
	UUID string `json:"uuid,omitempty"`
	Name string `json:"name"`
}

type fleetAccessState struct {
	WhitelistEnabled bool                  `json:"whitelist_enabled"`
	Whitelist        []fleetAccessIdentity `json:"whitelist"`
	Ops              []fleetAccessIdentity `json:"ops"`
	Bans             []fleetAccessIdentity `json:"bans"`
}

type fleetAccessObservation struct {
	Access fleetAccessState `json:"access"`
}

// fleetAccessSnapshot* preserves the fixed on-disk access evidence without
// exposing an arbitrary file or command surface. The existing Access response
// remains the stable name-only projection for callers that do not need this
// detailed runtime evidence.
type fleetAccessSnapshotIdentity struct {
	UUID string `json:"uuid"`
	Name string `json:"name"`
}

type fleetAccessSnapshotOperator struct {
	UUID                string `json:"uuid"`
	Name                string `json:"name"`
	Level               int    `json:"level"`
	BypassesPlayerLimit bool   `json:"bypasses_player_limit"`
}

type fleetAccessSnapshotBan struct {
	UUID    string `json:"uuid"`
	Name    string `json:"name"`
	Created string `json:"created"`
	Source  string `json:"source"`
	Expires string `json:"expires"`
	Reason  string `json:"reason"`
}

type fleetAccessSnapshotState struct {
	Whitelist []fleetAccessSnapshotIdentity `json:"whitelist"`
	Ops       []fleetAccessSnapshotOperator `json:"ops"`
	Bans      []fleetAccessSnapshotBan      `json:"bans"`
}

type fleetAccessSnapshotObservation struct {
	AccessSnapshot fleetAccessSnapshotState `json:"access_snapshot"`
}

type fleetArtifactState struct {
	Datapacks []string `json:"datapacks"`
	Mods      []string `json:"mods"`
}

type fleetArtifactsObservation struct {
	Artifacts fleetArtifactState `json:"artifacts"`
}

func registerFleetObservationRoutes(group *pulpgin.RouterGroup) {
	group.GET("/servers/:id/status", fleetStatusObservationHandler)
	group.GET("/servers/:id/settings", fleetSettingsObservationHandler)
	group.GET("/servers/:id/gamerules", fleetGameRulesObservationHandler)
	group.GET("/servers/:id/players", fleetPlayersObservationHandler)
	group.GET("/servers/:id/player-history", fleetPlayerHistoryObservationHandler)
	group.GET("/servers/:id/access", fleetAccessObservationHandler)
	group.GET("/servers/:id/access-snapshot", fleetAccessSnapshotObservationHandler)
	group.GET("/servers/:id/artifacts", fleetArtifactsObservationHandler)
}

func fleetStatusObservationHandler(c *pulpgin.Context) {
	server, ok := fleetObservationServer(c)
	if !ok {
		return
	}
	c.JSON(200, fleetStatusObservation{Status: string(server.Status)})
}

func fleetSettingsObservationHandler(c *pulpgin.Context) {
	id, ok := fleetObservationContainer(c)
	if !ok {
		return
	}
	output, ok := fleetObservationExec(c, id, []string{"sh", "-c", "cat /data/server.properties 2>/dev/null || echo ''"})
	if !ok {
		return
	}
	settings, err := parseFleetSettings(output)
	if err != nil {
		fleetObservationBadGateway(c)
		return
	}
	c.JSON(200, fleetSettingsObservation{Settings: settings})
}

func fleetGameRulesObservationHandler(c *pulpgin.Context) {
	id, ok := fleetObservationContainer(c)
	if !ok {
		return
	}
	output, ok := fleetObservationExec(c, id, []string{"sh", "-c", fleetGameRuleObservationCommand()})
	if !ok {
		return
	}
	rules, err := parseFleetGameRules(output)
	if err != nil {
		fleetObservationBadGateway(c)
		return
	}
	c.JSON(200, fleetGameRulesObservation{GameRules: rules})
}

func fleetPlayersObservationHandler(c *pulpgin.Context) {
	id, ok := fleetObservationContainer(c)
	if !ok {
		return
	}
	output, ok := fleetObservationExec(c, id, []string{"rcon", "list"})
	if !ok {
		return
	}
	players, err := parseFleetPlayers(output)
	if err != nil {
		fleetObservationBadGateway(c)
		return
	}
	c.JSON(200, fleetPlayersObservation{Players: players})
}

func fleetPlayerHistoryObservationHandler(c *pulpgin.Context) {
	id, ok := fleetObservationContainer(c)
	if !ok {
		return
	}
	output, ok := fleetObservationExec(c, id, []string{"sh", "-c", "(test -f /data/usercache.json && cat /data/usercache.json) || echo '[]'"})
	if !ok {
		return
	}
	history, err := parseFleetPlayerHistory(output)
	if err != nil {
		fleetObservationBadGateway(c)
		return
	}
	c.JSON(200, fleetPlayerHistoryObservation{PlayerHistory: history})
}

func fleetAccessObservationHandler(c *pulpgin.Context) {
	id, ok := fleetObservationContainer(c)
	if !ok {
		return
	}
	properties, ok := fleetObservationExec(c, id, []string{"sh", "-c", "cat /data/server.properties 2>/dev/null || echo ''"})
	if !ok {
		return
	}
	settings, err := parseFleetSettings(properties)
	if err != nil {
		fleetObservationBadGateway(c)
		return
	}
	state := fleetAccessState{
		WhitelistEnabled: settings["white_list"] == "true",
		Whitelist:        []fleetAccessIdentity{},
		Ops:              []fleetAccessIdentity{},
		Bans:             []fleetAccessIdentity{},
	}
	files := []struct {
		command string
		target  *[]fleetAccessIdentity
	}{
		{"(test -f /data/whitelist.json && cat /data/whitelist.json) || echo '[]'", &state.Whitelist},
		{"(test -f /data/ops.json && cat /data/ops.json) || echo '[]'", &state.Ops},
		{"(test -f /data/banned-players.json && cat /data/banned-players.json) || echo '[]'", &state.Bans},
	}
	for _, file := range files {
		output, ok := fleetObservationExec(c, id, []string{"sh", "-c", file.command})
		if !ok {
			return
		}
		identities, err := parseFleetAccessIdentities(output)
		if err != nil {
			fleetObservationBadGateway(c)
			return
		}
		*file.target = identities
	}
	c.JSON(200, fleetAccessObservation{Access: state})
}

func fleetAccessSnapshotObservationHandler(c *pulpgin.Context) {
	id, ok := fleetObservationContainer(c)
	if !ok {
		return
	}
	files := []string{
		"(test -f /data/whitelist.json && cat /data/whitelist.json) || echo '[]'",
		"(test -f /data/ops.json && cat /data/ops.json) || echo '[]'",
		"(test -f /data/banned-players.json && cat /data/banned-players.json) || echo '[]'",
	}
	outputs := make([]string, len(files))
	for index, command := range files {
		var ok bool
		outputs[index], ok = fleetObservationExec(c, id, []string{"sh", "-c", command})
		if !ok {
			return
		}
	}
	whitelist, err := parseFleetAccessSnapshotWhitelist(outputs[0])
	if err != nil {
		fleetObservationBadGateway(c)
		return
	}
	ops, err := parseFleetAccessSnapshotOps(outputs[1])
	if err != nil {
		fleetObservationBadGateway(c)
		return
	}
	bans, err := parseFleetAccessSnapshotBans(outputs[2])
	if err != nil {
		fleetObservationBadGateway(c)
		return
	}
	c.JSON(200, fleetAccessSnapshotObservation{AccessSnapshot: fleetAccessSnapshotState{
		Whitelist: whitelist,
		Ops:       ops,
		Bans:      bans,
	}})
}

func fleetArtifactsObservationHandler(c *pulpgin.Context) {
	id, ok := fleetObservationContainer(c)
	if !ok {
		return
	}
	datapackOutput, ok := fleetObservationExec(c, id, []string{"sh", "-c", "ls -1p /data/world/datapacks/ 2>/dev/null || echo ''"})
	if !ok {
		return
	}
	modOutput, ok := fleetObservationExec(c, id, []string{"sh", "-c", "ls -1p /data/mods/ 2>/dev/null || echo ''"})
	if !ok {
		return
	}
	datapacks, err := parseFleetArtifactFilenames(datapackOutput, ".zip")
	if err != nil {
		fleetObservationBadGateway(c)
		return
	}
	mods, err := parseFleetArtifactFilenames(modOutput, ".jar")
	if err != nil {
		fleetObservationBadGateway(c)
		return
	}
	if !fleetArtifactInventoryWithinBound(datapacks, mods) {
		fleetObservationBadGateway(c)
		return
	}
	c.JSON(200, fleetArtifactsObservation{Artifacts: fleetArtifactState{Datapacks: datapacks, Mods: mods}})
}

func fleetObservationContainer(c *pulpgin.Context) (string, bool) {
	server, ok := fleetObservationServer(c)
	if !ok {
		return "", false
	}
	return server.ID, true
}

func fleetObservationServer(c *pulpgin.Context) (*docker.Server, bool) {
	id := strings.TrimSpace(c.Param("id"))
	if !validFleetIdentity(id) {
		c.JSON(400, pulpgin.H{"error": "invalid container identity"})
		return nil, false
	}
	server, err := docker.Get(id)
	if err != nil {
		c.JSON(404, pulpgin.H{"error": "server not found"})
		return nil, false
	}
	if server == nil || server.ID != id {
		c.JSON(404, pulpgin.H{"error": "server not found"})
		return nil, false
	}
	return server, true
}

func fleetObservationExec(c *pulpgin.Context, id string, command []string) (string, bool) {
	output, err := docker.Exec(id, command)
	if err != nil {
		fleetObservationBadGateway(c)
		return "", false
	}
	if len(output) > fleetObservationMaxBytes {
		fleetObservationBadGateway(c)
		return "", false
	}
	return output, true
}

func fleetObservationBadGateway(c *pulpgin.Context) {
	c.JSON(502, pulpgin.H{"error": "runtime observation unavailable"})
}

func parseFleetSettings(output string) (map[string]string, error) {
	if len(output) > fleetObservationMaxBytes {
		return nil, errors.New("settings observation exceeds limit")
	}
	settings := make(map[string]string)
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(strings.TrimSuffix(line, "\r"))
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, found := strings.Cut(line, "=")
		if !found {
			continue
		}
		responseKey, observed := fleetObservedSettings[strings.TrimSpace(key)]
		if !observed {
			if strings.TrimSpace(key) == "white-list" {
				responseKey = "white_list"
			} else {
				continue
			}
		}
		value = strings.TrimSpace(value)
		if len(value) > 256 || strings.IndexFunc(value, unicode.IsControl) >= 0 {
			return nil, errors.New("invalid observed setting")
		}
		settings[responseKey] = value
	}
	return settings, nil
}

func fleetGameRuleObservationCommand() string {
	var command strings.Builder
	for _, rule := range fleetObservedGameRules {
		fmt.Fprintf(&command, "echo \"RULE:%s:$(rcon 'gamerule %s' 2>/dev/null)\"\n", rule, rule)
	}
	return command.String()
}

func parseFleetGameRules(output string) (map[string]string, error) {
	if len(output) > fleetObservationMaxBytes {
		return nil, errors.New("gamerule observation exceeds limit")
	}
	allowed := make(map[string]struct{}, len(fleetObservedGameRules))
	for _, rule := range fleetObservedGameRules {
		allowed[rule] = struct{}{}
	}
	result := make(map[string]string)
	for _, line := range strings.Split(output, "\n") {
		if !strings.HasPrefix(line, "RULE:") {
			continue
		}
		parts := strings.SplitN(line, ":", 3)
		if len(parts) != 3 {
			continue
		}
		rule := parts[1]
		if _, ok := allowed[rule]; !ok {
			return nil, errors.New("unexpected gamerule")
		}
		fields := strings.Fields(parts[2])
		if len(fields) == 0 {
			continue
		}
		value := strings.Trim(fields[len(fields)-1], "[](){}.,:;\"'")
		if !validFleetRuleValue(value) {
			return nil, errors.New("invalid gamerule value")
		}
		result[rule] = value
	}
	return result, nil
}

func parseFleetPlayers(output string) ([]string, error) {
	if len(output) > fleetObservationMaxBytes {
		return nil, errors.New("player observation exceeds limit")
	}
	index := strings.LastIndex(output, ":")
	if index < 0 || index == len(output)-1 {
		return []string{}, nil
	}
	raw := strings.TrimSpace(output[index+1:])
	if raw == "" {
		return []string{}, nil
	}
	parts := strings.Split(raw, ",")
	if len(parts) > fleetObservationMaxPlayers {
		return nil, errors.New("too many players")
	}
	players := make([]string, 0, len(parts))
	for _, part := range parts {
		name := strings.TrimSpace(part)
		if !validFleetPlayerName(name) {
			return nil, errors.New("invalid player name")
		}
		players = append(players, name)
	}
	sort.Strings(players)
	return players, nil
}

func parseFleetPlayerHistory(output string) ([]fleetPlayerHistoryEntry, error) {
	var source []struct {
		UUID      string `json:"uuid"`
		Name      string `json:"name"`
		ExpiresOn string `json:"expiresOn"`
	}
	if err := decodeFleetObservationJSON(output, &source); err != nil {
		return nil, errors.New("invalid player history observation")
	}
	if len(source) > fleetObservationMaxIdentities {
		return nil, errors.New("too many player history identities")
	}
	seen := make(map[string]struct{}, len(source))
	result := make([]fleetPlayerHistoryEntry, 0, len(source))
	for _, entry := range source {
		entry.UUID = strings.TrimSpace(entry.UUID)
		entry.Name = strings.TrimSpace(entry.Name)
		entry.ExpiresOn = strings.TrimSpace(entry.ExpiresOn)
		if !validFleetObservationUUID(entry.UUID) || !validFleetObservedIdentityName(entry.Name) || !validFleetObservedText(entry.ExpiresOn, 64) {
			return nil, errors.New("invalid player history identity")
		}
		if _, exists := seen[entry.UUID]; exists {
			return nil, errors.New("duplicate player history identity")
		}
		seen[entry.UUID] = struct{}{}
		result = append(result, fleetPlayerHistoryEntry{UUID: entry.UUID, Name: entry.Name, ExpiresOn: entry.ExpiresOn})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].UUID < result[j].UUID })
	return result, nil
}

func validFleetPlayerName(value string) bool {
	if len(value) == 0 || len(value) > 64 {
		return false
	}
	for _, r := range value {
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_') {
			return false
		}
	}
	return true
}

func parseFleetAccessIdentities(output string) ([]fleetAccessIdentity, error) {
	if len(output) > fleetObservationMaxBytes {
		return nil, errors.New("access observation exceeds limit")
	}
	var source []struct {
		UUID string `json:"uuid"`
		Name string `json:"name"`
	}
	if err := json.Unmarshal([]byte(output), &source); err != nil {
		return nil, errors.New("invalid access observation")
	}
	if len(source) > fleetObservationMaxIdentities {
		return nil, errors.New("too many access identities")
	}
	result := make([]fleetAccessIdentity, 0, len(source))
	for _, identity := range source {
		identity.Name = strings.TrimSpace(identity.Name)
		identity.UUID = strings.TrimSpace(identity.UUID)
		if !validFleetPlayerName(identity.Name) || !validFleetAccessUUID(identity.UUID) {
			return nil, errors.New("invalid access identity")
		}
		result = append(result, fleetAccessIdentity{UUID: identity.UUID, Name: identity.Name})
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Name == result[j].Name {
			return result[i].UUID < result[j].UUID
		}
		return result[i].Name < result[j].Name
	})
	return result, nil
}

func validFleetAccessUUID(value string) bool {
	if value == "" {
		return true
	}
	if len(value) > 64 {
		return false
	}
	for _, r := range value {
		if !((r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F') || (r >= '0' && r <= '9') || r == '-') {
			return false
		}
	}
	return true
}

func parseFleetAccessSnapshotWhitelist(output string) ([]fleetAccessSnapshotIdentity, error) {
	var source []struct {
		UUID string `json:"uuid"`
		Name string `json:"name"`
	}
	if err := decodeFleetObservationJSON(output, &source); err != nil {
		return nil, errors.New("invalid access snapshot whitelist")
	}
	if len(source) > fleetObservationMaxIdentities {
		return nil, errors.New("too many access snapshot whitelist identities")
	}
	seen := make(map[string]struct{}, len(source))
	result := make([]fleetAccessSnapshotIdentity, 0, len(source))
	for _, entry := range source {
		entry.UUID, entry.Name = strings.TrimSpace(entry.UUID), strings.TrimSpace(entry.Name)
		if !validFleetObservationUUID(entry.UUID) || !validFleetObservedIdentityName(entry.Name) {
			return nil, errors.New("invalid access snapshot whitelist identity")
		}
		if _, exists := seen[entry.UUID]; exists {
			return nil, errors.New("duplicate access snapshot whitelist identity")
		}
		seen[entry.UUID] = struct{}{}
		result = append(result, fleetAccessSnapshotIdentity{UUID: entry.UUID, Name: entry.Name})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].UUID < result[j].UUID })
	return result, nil
}

func parseFleetAccessSnapshotOps(output string) ([]fleetAccessSnapshotOperator, error) {
	var source []struct {
		UUID                string `json:"uuid"`
		Name                string `json:"name"`
		Level               int    `json:"level"`
		BypassesPlayerLimit *bool  `json:"bypassesPlayerLimit"`
	}
	if err := decodeFleetObservationJSON(output, &source); err != nil {
		return nil, errors.New("invalid access snapshot ops")
	}
	if len(source) > fleetObservationMaxIdentities {
		return nil, errors.New("too many access snapshot ops")
	}
	seen := make(map[string]struct{}, len(source))
	result := make([]fleetAccessSnapshotOperator, 0, len(source))
	for _, entry := range source {
		entry.UUID, entry.Name = strings.TrimSpace(entry.UUID), strings.TrimSpace(entry.Name)
		if !validFleetObservationUUID(entry.UUID) || !validFleetObservedIdentityName(entry.Name) || entry.Level < 1 || entry.Level > 4 || entry.BypassesPlayerLimit == nil {
			return nil, errors.New("invalid access snapshot operator")
		}
		if _, exists := seen[entry.UUID]; exists {
			return nil, errors.New("duplicate access snapshot operator")
		}
		seen[entry.UUID] = struct{}{}
		result = append(result, fleetAccessSnapshotOperator{UUID: entry.UUID, Name: entry.Name, Level: entry.Level, BypassesPlayerLimit: *entry.BypassesPlayerLimit})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].UUID < result[j].UUID })
	return result, nil
}

func parseFleetAccessSnapshotBans(output string) ([]fleetAccessSnapshotBan, error) {
	var source []struct {
		UUID    string `json:"uuid"`
		Name    string `json:"name"`
		Created string `json:"created"`
		Source  string `json:"source"`
		Expires string `json:"expires"`
		Reason  string `json:"reason"`
	}
	if err := decodeFleetObservationJSON(output, &source); err != nil {
		return nil, errors.New("invalid access snapshot bans")
	}
	if len(source) > fleetObservationMaxIdentities {
		return nil, errors.New("too many access snapshot bans")
	}
	seen := make(map[string]struct{}, len(source))
	result := make([]fleetAccessSnapshotBan, 0, len(source))
	for _, entry := range source {
		entry.UUID, entry.Name = strings.TrimSpace(entry.UUID), strings.TrimSpace(entry.Name)
		entry.Created, entry.Source = strings.TrimSpace(entry.Created), strings.TrimSpace(entry.Source)
		entry.Expires, entry.Reason = strings.TrimSpace(entry.Expires), strings.TrimSpace(entry.Reason)
		if !validFleetObservationUUID(entry.UUID) || !validFleetObservedIdentityName(entry.Name) ||
			!validFleetObservedText(entry.Created, 64) || !validFleetObservedText(entry.Source, 64) ||
			!validFleetObservedText(entry.Expires, 64) || !validFleetObservedText(entry.Reason, 512) {
			return nil, errors.New("invalid access snapshot ban")
		}
		if _, exists := seen[entry.UUID]; exists {
			return nil, errors.New("duplicate access snapshot ban")
		}
		seen[entry.UUID] = struct{}{}
		result = append(result, fleetAccessSnapshotBan{UUID: entry.UUID, Name: entry.Name, Created: entry.Created, Source: entry.Source, Expires: entry.Expires, Reason: entry.Reason})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].UUID < result[j].UUID })
	return result, nil
}

func decodeFleetObservationJSON(output string, target any) error {
	if len(output) > fleetObservationMaxBytes {
		return errors.New("runtime observation exceeds limit")
	}
	decoder := json.NewDecoder(strings.NewReader(output))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New("runtime observation must contain one JSON value")
	}
	return nil
}

func validFleetObservationUUID(value string) bool {
	if len(value) != 36 {
		return false
	}
	for index, char := range value {
		if index == 8 || index == 13 || index == 18 || index == 23 {
			if char != '-' {
				return false
			}
			continue
		}
		if !((char >= '0' && char <= '9') || (char >= 'a' && char <= 'f')) {
			return false
		}
	}
	return true
}

func validFleetObservedIdentityName(value string) bool {
	if len(value) == 0 || len(value) > 32 {
		return false
	}
	for _, char := range value {
		if !((char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') || (char >= '0' && char <= '9') || char == '_' || char == '.' || char == '-' || char == ' ') {
			return false
		}
	}
	return true
}

func validFleetObservedText(value string, limit int) bool {
	return value != "" && len(value) <= limit && strings.TrimSpace(value) == value && strings.IndexFunc(value, unicode.IsControl) < 0
}

func parseFleetArtifactFilenames(output, extension string) ([]string, error) {
	if len(output) > fleetObservationMaxBytes {
		return nil, errors.New("artifact observation exceeds limit")
	}
	seen := make(map[string]struct{})
	for _, line := range strings.Split(output, "\n") {
		name := strings.TrimSpace(strings.TrimSuffix(line, "\r"))
		if name == "" {
			continue
		}
		if !strings.HasSuffix(strings.ToLower(name), strings.ToLower(extension)) || !validFleetArtifactFilename(name) {
			continue
		}
		seen[name] = struct{}{}
		if len(seen) > fleetObservationMaxArtifacts {
			return nil, errors.New("too many artifacts")
		}
	}
	result := make([]string, 0, len(seen))
	for name := range seen {
		result = append(result, name)
	}
	sort.Strings(result)
	return result, nil
}

func fleetArtifactInventoryWithinBound(datapacks, mods []string) bool {
	return len(datapacks) <= fleetObservationMaxArtifacts &&
		len(mods) <= fleetObservationMaxArtifacts-len(datapacks)
}

func validFleetArtifactFilename(value string) bool {
	if value == "" || len(value) > 255 || value == "." || value == ".." || strings.ContainsAny(value, "/\\") {
		return false
	}
	return strings.IndexFunc(value, unicode.IsControl) < 0
}
