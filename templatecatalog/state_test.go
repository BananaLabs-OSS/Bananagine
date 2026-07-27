package templatecatalog

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestStateReplaceIsIdempotentAndSorted(t *testing.T) {
	state := NewState()
	request := ReplaceRequest{
		RequestID: "templates-42",
		Entries: []Entry{
			{Name: "zeta", Game: "generic", ConfigJSON: json.RawMessage(`{"mode":"z"}`)},
			{Name: "alpha", Game: "generic", ConfigJSON: json.RawMessage(`{"mode":"a"}`)},
		},
	}
	first, err := state.Replace(request)
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := state.Replace(request)
	if err != nil {
		t.Fatal(err)
	}
	if first.Revision != 1 || !reflect.DeepEqual(first, replayed) {
		t.Fatalf("replace/replay = %#v / %#v", first, replayed)
	}
	if got := []string{first.Entries[0].Name, first.Entries[1].Name}; !reflect.DeepEqual(got, []string{"alpha", "zeta"}) {
		t.Fatalf("catalog order = %v", got)
	}
	conflicting := request
	conflicting.Entries[0].Label = "different"
	if _, err := state.Replace(conflicting); err == nil {
		t.Fatal("conflicting idempotency replay succeeded")
	}
}

func TestStateSnapshotRestoresRestartedOwnerAndRemainsIsolated(t *testing.T) {
	original := NewState()
	_, err := original.Replace(ReplaceRequest{
		RequestID: "replace",
		Entries:   []Entry{{Name: "minecraft", Game: "block", ConfigJSON: json.RawMessage(`{"difficulty":"hard"}`)}},
	})
	if err != nil {
		t.Fatal(err)
	}
	restarted := NewState()
	if got := restarted.List(); got.Revision != 0 || len(got.Entries) != 0 {
		t.Fatalf("fresh owner leaked state: %#v", got)
	}
	restored, err := restarted.Import(ImportRequest{RequestID: "restore", Snapshot: original.Export()})
	if err != nil {
		t.Fatal(err)
	}
	if restored.Revision != 1 || len(restored.Entries) != 1 || restored.Entries[0].Name != "minecraft" {
		t.Fatalf("restored catalog = %#v", restored)
	}
	other := NewState()
	if len(other.List().Entries) != 0 {
		t.Fatal("independent owner shared restored state")
	}
}
