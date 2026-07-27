package main

import (
	"reflect"
	"strings"
	"testing"
)

func TestFleetReconfigureCommandsAreTypedAllowlistedAndDeterministic(t *testing.T) {
	commands, err := fleetReconfigureCommands(map[string]string{
		"FLEET_GAMERULE_keepInventory": "true",
		"FLEET_SETTING_difficulty":     "hard",
		"FLEET_SETTING_gamemode":       "survival",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := [][]string{
		{"rcon", "gamerule keepInventory true"},
		{"rcon", "difficulty hard"},
		{"rcon", "defaultgamemode survival"},
	}
	if !reflect.DeepEqual(commands, want) {
		t.Fatalf("commands = %#v, want %#v", commands, want)
	}
}

func TestFleetReconfigureCommandsRejectUnconstrainedInput(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
	}{
		{name: "unknown setting", env: map[string]string{"JAVA_TOOL_OPTIONS": "-agentlib:jdwp"}},
		{name: "difficulty injection", env: map[string]string{"FLEET_SETTING_difficulty": "hard; stop"}},
		{name: "gamerule name injection", env: map[string]string{"FLEET_GAMERULE_keepInventory;stop": "true"}},
		{name: "gamerule value injection", env: map[string]string{"FLEET_GAMERULE_keepInventory": "true; stop"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := fleetReconfigureCommands(test.env); err == nil {
				t.Fatalf("fleetReconfigureCommands(%#v) accepted unconstrained input", test.env)
			}
		})
	}
}

func TestFleetLifecycleIdentityRejectsTraversalAndControlCharacters(t *testing.T) {
	for _, value := range []string{"", " leading", "trailing ", "../server", "server/child", "server\nchild"} {
		if validFleetIdentity(value) {
			t.Errorf("validFleetIdentity(%q) = true", value)
		}
	}
	for _, value := range []string{"server-42", "node_1", "effect:01", "container.example"} {
		if !validFleetIdentity(value) {
			t.Errorf("validFleetIdentity(%q) = false", value)
		}
	}
}

func TestFleetEffectIdentityMatchesCanonicalEffectEnvelope(t *testing.T) {
	for _, value := range []string{"checkout/order:1", "effect with internal spaces", "fleet:resume:42"} {
		if !validFleetEffectIdentity(value) {
			t.Errorf("validFleetEffectIdentity(%q) = false", value)
		}
	}
	for _, value := range []string{"", " leading", "trailing ", "effect\nother"} {
		if validFleetEffectIdentity(value) {
			t.Errorf("validFleetEffectIdentity(%q) = true", value)
		}
	}
}

func TestFleetLifecycleStatusesMatchCanonicalExecutor(t *testing.T) {
	want := map[string]string{
		"reconfigure": "reconfigured",
		"suspend":     "suspended",
		"resume":      "resumed",
		"restart":     "restarted",
		"regenerate":  "regenerated",
	}
	for action, status := range want {
		if got := fleetLifecycleStatus(action); got != status {
			t.Errorf("fleetLifecycleStatus(%q) = %q, want %q", action, got, status)
		}
	}
	if got := fleetLifecycleStatus("exec"); got != "" {
		t.Fatalf("unsupported status = %q, want empty", got)
	}
}

func TestFleetLifecycleResourceChangesFailClosed(t *testing.T) {
	err := executeFleetLifecycle("reconfigure", "container-1", fleetLifecycleRequest{
		ServerID: "server-1",
		NodeID:   "node-1",
		Resources: fleetResources{
			CPU: 2,
		},
	})
	if err == nil || !strings.Contains(err.Error(), "not supported") {
		t.Fatalf("resource reconfigure error = %v, want unsupported failure", err)
	}
}
