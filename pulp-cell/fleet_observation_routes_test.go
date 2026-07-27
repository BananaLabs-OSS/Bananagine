package main

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func TestParseFleetSettingsReturnsOnlyTypedAllowlist(t *testing.T) {
	got, err := parseFleetSettings(strings.Join([]string{
		"# generated",
		"difficulty=hard",
		"gamemode=survival",
		"allow-nether=true",
		"white-list=true",
		"rcon.password=do-not-expose",
	}, "\n"))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"difficulty": "hard", "gamemode": "survival",
		"allow_nether": "true", "white_list": "true",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("settings = %#v, want %#v", got, want)
	}
}

func TestFleetGameRuleObservationIsFixedAndStrict(t *testing.T) {
	command := fleetGameRuleObservationCommand()
	for _, rule := range fleetObservedGameRules {
		want := fmt.Sprintf(`echo "RULE:%s:$(rcon 'gamerule %s' 2>/dev/null)"`, rule, rule)
		if !strings.Contains(command, want) {
			t.Fatalf("command does not contain fixed probe %q", want)
		}
	}
	got, err := parseFleetGameRules("RULE:keepInventory:Gamerule keepInventory is currently set to: true\nRULE:spawnRadius:10\n")
	if err != nil {
		t.Fatal(err)
	}
	if got["keepInventory"] != "true" || got["spawnRadius"] != "10" {
		t.Fatalf("gamerules = %#v", got)
	}
	got, err = parseFleetGameRules("RULE:doInsomnia:Gamerule doInsomnia is currently set to: false\n")
	if err != nil || got["doInsomnia"] != "false" {
		t.Fatalf("doInsomnia gamerule = %#v, %v", got, err)
	}
	if _, err := parseFleetGameRules("RULE:injectedRule:true\n"); err == nil {
		t.Fatal("unexpected gamerule was accepted")
	}
	if _, err := parseFleetGameRules("RULE:keepInventory:true;stop\n"); err == nil {
		t.Fatal("injected gamerule value was accepted")
	}
}

func TestParseFleetPlayerHistoryProjectsOnlyFixedIdentityFields(t *testing.T) {
	got, err := parseFleetPlayerHistory(`[{"uuid":"123e4567-e89b-12d3-a456-426614174000","name":"Steve","expiresOn":"2026-08-01 00:00:00 +0000"}]`)
	if err != nil {
		t.Fatal(err)
	}
	want := []fleetPlayerHistoryEntry{{UUID: "123e4567-e89b-12d3-a456-426614174000", Name: "Steve", ExpiresOn: "2026-08-01 00:00:00 +0000"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("player history = %#v, want %#v", got, want)
	}
	if _, err := parseFleetPlayerHistory(`[{"uuid":"123e4567-e89b-12d3-a456-426614174000","name":"Steve","expiresOn":"later","extra":"no"}]`); err == nil {
		t.Fatal("player history accepted an unknown source field")
	}
	if _, err := parseFleetPlayerHistory(`[{"uuid":"123e4567-e89b-12d3-a456-426614174000","name":"Steve","expiresOn":"later"},{"uuid":"123e4567-e89b-12d3-a456-426614174000","name":"Alex","expiresOn":"later"}]`); err == nil {
		t.Fatal("player history accepted a duplicate UUID")
	}
}

func TestParseFleetAccessSnapshotProjectsExactTypedFields(t *testing.T) {
	whitelist, err := parseFleetAccessSnapshotWhitelist(`[{"uuid":"123e4567-e89b-12d3-a456-426614174000","name":"Steve"}]`)
	if err != nil {
		t.Fatal(err)
	}
	ops, err := parseFleetAccessSnapshotOps(`[{"uuid":"223e4567-e89b-12d3-a456-426614174000","name":"Alex_2","level":4,"bypassesPlayerLimit":true}]`)
	if err != nil {
		t.Fatal(err)
	}
	bans, err := parseFleetAccessSnapshotBans(`[{"uuid":"323e4567-e89b-12d3-a456-426614174000","name":"Griefer","created":"2026-07-26 00:00:00 +0000","source":"Console","expires":"forever","reason":"x-ray"}]`)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(whitelist, []fleetAccessSnapshotIdentity{{UUID: "123e4567-e89b-12d3-a456-426614174000", Name: "Steve"}}) {
		t.Fatalf("whitelist = %#v", whitelist)
	}
	if !reflect.DeepEqual(ops, []fleetAccessSnapshotOperator{{UUID: "223e4567-e89b-12d3-a456-426614174000", Name: "Alex_2", Level: 4, BypassesPlayerLimit: true}}) {
		t.Fatalf("ops = %#v", ops)
	}
	if !reflect.DeepEqual(bans, []fleetAccessSnapshotBan{{UUID: "323e4567-e89b-12d3-a456-426614174000", Name: "Griefer", Created: "2026-07-26 00:00:00 +0000", Source: "Console", Expires: "forever", Reason: "x-ray"}}) {
		t.Fatalf("bans = %#v", bans)
	}
	if _, err := parseFleetAccessSnapshotOps(`[{"uuid":"223e4567-e89b-12d3-a456-426614174000","name":"Alex","level":5,"bypassesPlayerLimit":false}]`); err == nil {
		t.Fatal("access snapshot accepted an invalid operator level")
	}
	if _, err := parseFleetAccessSnapshotOps(`[{"uuid":"223e4567-e89b-12d3-a456-426614174000","name":"Alex","level":4}]`); err == nil {
		t.Fatal("access snapshot accepted a missing operator bypass flag")
	}
	if _, err := parseFleetAccessSnapshotBans(`[{"uuid":"323e4567-e89b-12d3-a456-426614174000","name":"Griefer","created":"now","source":"Console","expires":"forever","reason":"x","unknown":true}]`); err == nil {
		t.Fatal("access snapshot accepted an unknown source field")
	}
}

func TestParseFleetPlayersReturnsBoundedNamedList(t *testing.T) {
	got, err := parseFleetPlayers("There are 2 of a max of 20 players online: Steve, Alex_2")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, []string{"Alex_2", "Steve"}) {
		t.Fatalf("players = %#v", got)
	}
	if _, err := parseFleetPlayers("There are 1 of a max of 20 players online: Steve;stop"); err == nil {
		t.Fatal("invalid player name was accepted")
	}
	tooMany := "players: " + strings.Repeat("A,", fleetObservationMaxPlayers) + "A"
	if _, err := parseFleetPlayers(tooMany); err == nil {
		t.Fatal("oversized player list was accepted")
	}
}

func TestParseFleetAccessIdentitiesProjectsExactIdentityFields(t *testing.T) {
	got, err := parseFleetAccessIdentities(`[{"uuid":"123e4567-e89b-12d3-a456-426614174000","name":"Steve","level":4,"reason":"not exposed"}]`)
	if err != nil {
		t.Fatal(err)
	}
	want := []fleetAccessIdentity{{UUID: "123e4567-e89b-12d3-a456-426614174000", Name: "Steve"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("identities = %#v, want %#v", got, want)
	}
	if _, err := parseFleetAccessIdentities(`[{"uuid":"../../host","name":"Steve"}]`); err == nil {
		t.Fatal("invalid access identity was accepted")
	}
}

func TestParseFleetArtifactFilenamesFiltersPathsAndTypes(t *testing.T) {
	got, err := parseFleetArtifactFilenames("b.zip\na.jar\n../escape.zip\nfolder\nA.ZIP\n", ".zip")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, []string{"A.ZIP", "b.zip"}) {
		t.Fatalf("artifacts = %#v", got)
	}
}

func TestFleetArtifactInventoryCombinedBound(t *testing.T) {
	left := make([]string, fleetObservationMaxArtifacts)
	for index := range left {
		left[index] = fmt.Sprintf("pack-%03d.zip", index)
	}
	if got, err := parseFleetArtifactFilenames(strings.Join(left, "\n"), ".zip"); err != nil || len(got) != fleetObservationMaxArtifacts {
		t.Fatalf("maximum artifact inventory = %d, %v", len(got), err)
	}
	tooMany := append(left, "overflow.zip")
	if _, err := parseFleetArtifactFilenames(strings.Join(tooMany, "\n"), ".zip"); err == nil {
		t.Fatal("artifact inventory over the canonical bound was accepted")
	}
	if fleetArtifactInventoryWithinBound(left[:200], make([]string, 57)) {
		t.Fatal("combined artifact inventory over the canonical bound was accepted")
	}
}

func TestFleetObservationParsersRejectOversizeOutput(t *testing.T) {
	oversize := strings.Repeat("x", fleetObservationMaxBytes+1)
	checks := []struct {
		name string
		run  func() error
	}{
		{name: "settings", run: func() error { _, err := parseFleetSettings(oversize); return err }},
		{name: "gamerules", run: func() error { _, err := parseFleetGameRules(oversize); return err }},
		{name: "players", run: func() error { _, err := parseFleetPlayers(oversize); return err }},
		{name: "player history", run: func() error { _, err := parseFleetPlayerHistory(oversize); return err }},
		{name: "access", run: func() error { _, err := parseFleetAccessIdentities(oversize); return err }},
		{name: "access snapshot whitelist", run: func() error { _, err := parseFleetAccessSnapshotWhitelist(oversize); return err }},
		{name: "artifacts", run: func() error { _, err := parseFleetArtifactFilenames(oversize, ".jar"); return err }},
	}
	for _, check := range checks {
		t.Run(check.name, func(t *testing.T) {
			if err := check.run(); err == nil {
				t.Fatal("oversized runtime output was accepted")
			}
		})
	}
}
