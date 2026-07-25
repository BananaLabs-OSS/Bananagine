package registry

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestStateCharacterizesLegacyRegistry(t *testing.T) {
	state := NewState()

	if _, err := state.Register(Server{}); err == nil || err.Error() != "Server ID required" {
		t.Fatalf("missing ID error = %v, want legacy text", err)
	}

	registered, err := state.Register(Server{
		ID:         "game-1",
		Type:       TypeGame,
		Mode:       "survival",
		Players:    2,
		MaxPlayers: 4,
		Metadata:   map[string]string{"region": "central"},
	})
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	if registered.Matches == nil {
		t.Fatal("game server should initialize an empty match map")
	}

	if _, err := state.PutMatch(PutMatchRequest{
		ServerID: "game-1",
		MatchID:  "match-1",
		Match: Match{
			Status:  StatusReady,
			Need:    2,
			Players: []string{"alice"},
		},
	}); err != nil {
		t.Fatalf("put match: %v", err)
	}

	available := state.List(ListRequest{HasCapacity: true, HasReadyMatch: true})
	if len(available) != 1 || available[0].ID != "game-1" {
		t.Fatalf("filtered list = %#v", available)
	}

	players := 4
	if _, err := state.Update(UpdateRequest{ID: "game-1", Players: &players}); err != nil {
		t.Fatalf("update: %v", err)
	}
	if got := state.List(ListRequest{HasCapacity: true}); got != nil {
		t.Fatalf("full server should not have capacity; got %#v", got)
	}

	state.Unregister("missing") // legacy unregister is idempotent
	state.Unregister("game-1")
	if got := state.List(ListRequest{}); got != nil {
		t.Fatalf("empty list = %#v, want nil legacy shape", got)
	}
}

func TestStateReturnsCopies(t *testing.T) {
	state := NewState()
	original := Server{
		ID:       "game-1",
		Type:     TypeGame,
		Metadata: map[string]string{"region": "central"},
		Matches: map[string]Match{
			"m": {Players: []string{"alice"}},
		},
	}
	if _, err := state.Register(original); err != nil {
		t.Fatal(err)
	}

	original.Metadata["region"] = "mutated"
	original.Matches["m"] = Match{Players: []string{"mallory"}}
	got, err := state.Get("game-1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Metadata["region"] != "central" || got.Matches["m"].Players[0] != "alice" {
		t.Fatalf("stored value was aliased: %#v", got)
	}

	got.Metadata["region"] = "returned-mutation"
	again, _ := state.Get("game-1")
	if again.Metadata["region"] != "central" {
		t.Fatalf("returned value was aliased: %#v", again)
	}
}

func TestServerJSONPreservesLegacyMetadataField(t *testing.T) {
	data, err := json.Marshal(Server{
		ID:       "game-1",
		Metadata: map[string]string{"region": "central"},
	})
	if err != nil {
		t.Fatal(err)
	}
	var wire map[string]any
	if err := json.Unmarshal(data, &wire); err != nil {
		t.Fatal(err)
	}
	if _, ok := wire["Metadata"]; !ok {
		t.Fatalf("legacy Metadata field missing from %s", data)
	}
	if _, ok := wire["metadata"]; ok {
		t.Fatalf("unexpected lowercase metadata field in %s", data)
	}
}

func TestNotFoundIsTyped(t *testing.T) {
	_, err := NewState().Get("missing")
	var serviceErr *ServiceError
	if !errors.As(err, &serviceErr) {
		t.Fatalf("error %T is not ServiceError", err)
	}
	if serviceErr.Code != CodeNotFound || serviceErr.Retryable {
		t.Fatalf("service error = %#v", serviceErr)
	}
}
