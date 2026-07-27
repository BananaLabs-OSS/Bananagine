// Package compositionclient is the typed facade-to-Lua adapter shared by
// Bananagine's reusable owner boundaries.
package compositionclient

import (
	"fmt"

	"github.com/BananaLabs-OSS/Fiber/pulp/workflow"
	"github.com/vmihailenco/msgpack/v5"
)

type Caller interface {
	Call(target, function string, payload []byte) ([]byte, error)
}

func Dispatch[T any](caller Caller, target, event string, payload any) (T, error) {
	var zero T
	if caller == nil {
		return zero, fmt.Errorf("composition caller is required")
	}
	request := workflow.DispatchRequest{Event: event, Payload: payload}
	if err := request.Validate(); err != nil {
		return zero, fmt.Errorf("validate dispatch: %w", err)
	}
	encoded, err := msgpack.Marshal(request)
	if err != nil {
		return zero, fmt.Errorf("encode dispatch: %w", err)
	}
	response, err := caller.Call(target, workflow.FnDispatch, encoded)
	if err != nil {
		return zero, fmt.Errorf("dispatch %s: %w", event, err)
	}
	var result workflow.DispatchResult
	if err := msgpack.Unmarshal(response, &result); err != nil {
		return zero, fmt.Errorf("decode dispatch: %w", err)
	}
	if err := result.Validate(); err != nil {
		return zero, fmt.Errorf("validate dispatch result: %w", err)
	}
	value, err := workflow.DecodeValue[T](result)
	if err != nil {
		return zero, fmt.Errorf("decode typed value: %w", err)
	}
	return value, nil
}
