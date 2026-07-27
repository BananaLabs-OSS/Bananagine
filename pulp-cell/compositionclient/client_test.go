package compositionclient

import (
	"testing"

	"github.com/BananaLabs-OSS/Fiber/pulp/workflow"
	"github.com/vmihailenco/msgpack/v5"
)

type callerFunc func(target, function string, payload []byte) ([]byte, error)

func (fn callerFunc) Call(target, function string, payload []byte) ([]byte, error) {
	return fn(target, function, payload)
}

func TestDispatchPreservesExactTargetEventAndValue(t *testing.T) {
	value, err := Dispatch[map[string]string](callerFunc(func(target, function string, payload []byte) ([]byte, error) {
		if target != "bananagine-lua" || function != workflow.FnDispatch {
			t.Fatalf("address = %s/%s", target, function)
		}
		var request workflow.DispatchRequest
		if err := msgpack.Unmarshal(payload, &request); err != nil {
			t.Fatal(err)
		}
		if request.Event != "bananagine.template-catalog.v1.list" {
			t.Fatalf("event = %q", request.Event)
		}
		return msgpack.Marshal(workflow.DispatchResult{Value: map[string]string{"status": "ok"}})
	}), "bananagine-lua", "bananagine.template-catalog.v1.list", map[string]any{})
	if err != nil || value["status"] != "ok" {
		t.Fatalf("dispatch = (%v, %v)", value, err)
	}
}
