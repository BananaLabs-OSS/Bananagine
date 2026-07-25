// Package registryproxy is the compatibility boundary between Bananagine's
// HTTP/API cell and the Lua-composed registry application.
package registryproxy

import (
	"fmt"

	"github.com/BananaLabs-OSS/Fiber/pulp/workflow"
	"github.com/bananalabs-oss/bananagine/registry"
	"github.com/vmihailenco/msgpack/v5"
)

type Caller interface {
	Call(target, function string, payload []byte) ([]byte, error)
}

type Client struct {
	Caller Caller
	Target string
}

// Dispatch invokes one Bananagine Lua workflow and recovers the registry's
// typed in-band result envelope from the orchestrator's generic value.
func Dispatch[T any](client Client, event string, payload any) (registry.Result[T], error) {
	var zero registry.Result[T]
	if client.Caller == nil || client.Target == "" {
		return zero, fmt.Errorf("registry composition client is not configured")
	}
	request := workflow.DispatchRequest{Event: event, Payload: payload}
	if err := request.Validate(); err != nil {
		return zero, fmt.Errorf("validate registry dispatch: %w", err)
	}
	encoded, err := msgpack.Marshal(request)
	if err != nil {
		return zero, fmt.Errorf("encode registry dispatch: %w", err)
	}
	response, err := client.Caller.Call(client.Target, workflow.FnDispatch, encoded)
	if err != nil {
		return zero, fmt.Errorf("call registry workflow %s: %w", event, err)
	}
	var dispatch workflow.DispatchResult
	if err := msgpack.Unmarshal(response, &dispatch); err != nil {
		return zero, fmt.Errorf("decode registry dispatch: %w", err)
	}
	if err := dispatch.Validate(); err != nil {
		return zero, fmt.Errorf("validate registry result: %w", err)
	}
	result, err := workflow.DecodeValue[registry.Result[T]](dispatch)
	if err != nil {
		return zero, fmt.Errorf("decode typed registry result: %w", err)
	}
	return result, nil
}
