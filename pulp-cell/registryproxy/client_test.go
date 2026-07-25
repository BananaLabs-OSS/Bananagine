package registryproxy

import (
	"errors"
	"testing"

	"github.com/BananaLabs-OSS/Fiber/pulp/workflow"
	"github.com/bananalabs-oss/bananagine/registry"
	"github.com/vmihailenco/msgpack/v5"
)

type fakeCaller struct {
	target   string
	function string
	request  workflow.DispatchRequest
	response []byte
	err      error
}

func (f *fakeCaller) Call(target, function string, payload []byte) ([]byte, error) {
	f.target = target
	f.function = function
	if err := msgpack.Unmarshal(payload, &f.request); err != nil {
		return nil, err
	}
	return f.response, f.err
}

func encodeDispatch(t *testing.T, value any) []byte {
	t.Helper()
	data, err := msgpack.Marshal(workflow.DispatchResult{Value: value})
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestDispatchPreservesTypedRegistryResult(t *testing.T) {
	caller := &fakeCaller{
		response: encodeDispatch(t, registry.Success(registry.Server{ID: "game-1"})),
	}
	client := Client{Caller: caller, Target: "bananagine-lua"}
	result, err := Dispatch[registry.Server](client, registry.FnGet, registry.GetRequest{ID: "game-1"})
	if err != nil {
		t.Fatal(err)
	}
	if !result.OK || result.Value.ID != "game-1" {
		t.Fatalf("result = %#v", result)
	}
	if caller.target != "bananagine-lua" || caller.function != workflow.FnDispatch {
		t.Fatalf("call = target %q function %q", caller.target, caller.function)
	}
	if caller.request.Event != registry.FnGet {
		t.Fatalf("event = %q", caller.request.Event)
	}
}

func TestDispatchKeepsDomainFailureInBand(t *testing.T) {
	caller := &fakeCaller{
		response: encodeDispatch(t, registry.Failure[registry.Server](&registry.ServiceError{
			Code:    registry.CodeNotFound,
			Message: "Server not found",
		})),
	}
	result, err := Dispatch[registry.Server](
		Client{Caller: caller, Target: "bananagine-lua"},
		registry.FnGet,
		registry.GetRequest{ID: "missing"},
	)
	if err != nil {
		t.Fatalf("domain failure escaped as transport error: %v", err)
	}
	if result.OK || result.Error == nil || result.Error.Code != registry.CodeNotFound {
		t.Fatalf("result = %#v", result)
	}
}

func TestDispatchReportsDependencyFailure(t *testing.T) {
	want := errors.New("lua unavailable")
	caller := &fakeCaller{err: want}
	_, err := Dispatch[registry.Server](
		Client{Caller: caller, Target: "bananagine-lua"},
		registry.FnGet,
		registry.GetRequest{ID: "game-1"},
	)
	if err == nil || !errors.Is(err, want) {
		t.Fatalf("error = %v", err)
	}
}

func TestLegacyHTTPFailureParity(t *testing.T) {
	tests := []struct {
		name      string
		operation string
		service   *registry.ServiceError
		status    int
		message   string
	}{
		{
			name:      "register validation",
			operation: registry.FnRegister,
			service:   &registry.ServiceError{Code: registry.CodeInvalidArgument, Message: "Server ID required"},
			status:    400,
			message:   "Server ID required",
		},
		{
			name:      "get lower-case legacy body",
			operation: registry.FnGet,
			service:   &registry.ServiceError{Code: registry.CodeNotFound, Message: "Server not found"},
			status:    404,
			message:   "server not found",
		},
		{
			name:      "update upper-case legacy body",
			operation: registry.FnUpdate,
			service:   &registry.ServiceError{Code: registry.CodeNotFound, Message: "Server not found"},
			status:    404,
			message:   "Server not found",
		},
		{
			name:      "remove-match lower-case legacy body",
			operation: registry.FnRemoveMatch,
			service:   &registry.ServiceError{Code: registry.CodeNotFound, Message: "server not found"},
			status:    404,
			message:   "server not found",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			status, message := LegacyHTTPFailure(test.operation, test.service)
			if status != test.status || message != test.message {
				t.Fatalf("got (%d, %q), want (%d, %q)", status, message, test.status, test.message)
			}
		})
	}
}
