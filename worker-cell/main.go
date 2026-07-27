package main

import (
	"fmt"
	"time"

	"github.com/BananaLabs-OSS/Fiber/pulp"
	"github.com/BananaLabs-OSS/Fiber/pulp/workers"
	"github.com/bananalabs-oss/bananagine/gameworker"
	"github.com/vmihailenco/msgpack/v5"
)

type workerAdapter struct{}

func (workerAdapter) Submit(request gameworker.SubmitRequest) (uint32, error) {
	task := workers.Task{
		Type:      "http.fetch",
		Method:    request.Method,
		URL:       request.URL,
		Headers:   request.Headers,
		Body:      request.Body,
		TimeoutMs: request.TimeoutMs,
	}
	if request.TimeoutMs > 0 {
		return workers.SubmitWithTimeout(task, time.Duration(request.TimeoutMs)*time.Millisecond)
	}
	return workers.Submit(task)
}

func (workerAdapter) Result(taskID uint32) (gameworker.PollResult, error) {
	result, done, err := workers.Result(taskID)
	if err != nil {
		return gameworker.PollResult{}, err
	}
	return gameworker.PollResult{
		Done: done, Status: result.Status, Headers: result.Headers, Body: result.Body, Error: result.Error,
	}, nil
}

func (workerAdapter) Cancel(taskID uint32) error {
	return workers.Cancel(taskID)
}

func init() {
	pulp.OnInit(func([]byte) error {
		state := gameworker.NewState()
		worker := workerAdapter{}
		provide(gameworker.FnSubmit, func(input []byte) (any, error) {
			var request gameworker.SubmitRequest
			if err := decode(input, &request); err != nil {
				return nil, err
			}
			return state.Submit(request, worker)
		})
		provide(gameworker.FnStatus, func(input []byte) (any, error) {
			var request gameworker.StatusRequest
			if err := decode(input, &request); err != nil {
				return nil, err
			}
			return state.Status(request.IdempotencyKey, worker)
		})
		provide(gameworker.FnCancel, func(input []byte) (any, error) {
			var request gameworker.StatusRequest
			if err := decode(input, &request); err != nil {
				return nil, err
			}
			return state.Cancel(request.IdempotencyKey, worker)
		})
		provide(gameworker.FnSnapshotExport, func([]byte) (any, error) {
			return state.Export(), nil
		})
		provide(gameworker.FnSnapshotImport, func(input []byte) (any, error) {
			var request gameworker.ImportRequest
			if err := decode(input, &request); err != nil {
				return nil, err
			}
			if err := state.Import(request); err != nil {
				return nil, err
			}
			return gameworker.Ack{Status: "ok"}, nil
		})
		return nil
	})
}

func main() {}

func provide(name string, implementation func([]byte) (any, error)) {
	pulp.Provide(name, func(input []byte) ([]byte, error) {
		value, err := implementation(input)
		result := gameworker.Success(value)
		if err != nil {
			result = gameworker.Failure[any](err)
		}
		output, marshalErr := msgpack.Marshal(result)
		if marshalErr != nil {
			return nil, fmt.Errorf("encode: %w", marshalErr)
		}
		return output, nil
	})
}

func decode(input []byte, output any) error {
	if len(input) == 0 {
		return fmt.Errorf("decode: empty request")
	}
	if err := msgpack.Unmarshal(input, output); err != nil {
		return fmt.Errorf("decode: %w", err)
	}
	return nil
}
