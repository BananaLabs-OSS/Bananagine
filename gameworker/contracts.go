// Package gameworker defines Bananagine's scoped asynchronous host-effect
// boundary. The WASM owner keeps idempotency and receipts; Pulp-ext-workers
// owns goroutines, network access, quotas, cancellation, and instance scope.
package gameworker

import "fmt"

const (
	Capability = "bananagine.worker.v1"

	FnSubmit         = Capability + ".http.submit"
	FnStatus         = Capability + ".status"
	FnCancel         = Capability + ".cancel"
	FnSnapshotExport = Capability + ".snapshot.export"
	FnSnapshotImport = Capability + ".snapshot.import"

	SnapshotVersion = 1
)

type SubmitRequest struct {
	IdempotencyKey string            `json:"idempotency_key" msgpack:"idempotency_key"`
	Method         string            `json:"method" msgpack:"method"`
	URL            string            `json:"url" msgpack:"url"`
	Headers        map[string]string `json:"headers,omitempty" msgpack:"headers,omitempty"`
	Body           []byte            `json:"body,omitempty" msgpack:"body,omitempty"`
	TimeoutMs      uint32            `json:"timeout_ms,omitempty" msgpack:"timeout_ms,omitempty"`
}

type StatusRequest struct {
	IdempotencyKey string `json:"idempotency_key" msgpack:"idempotency_key"`
}

type Job struct {
	IdempotencyKey string            `json:"idempotency_key" msgpack:"idempotency_key"`
	State          string            `json:"state" msgpack:"state"`
	Status         int               `json:"status,omitempty" msgpack:"status,omitempty"`
	Headers        map[string]string `json:"headers,omitempty" msgpack:"headers,omitempty"`
	Body           []byte            `json:"body,omitempty" msgpack:"body,omitempty"`
	Error          string            `json:"error,omitempty" msgpack:"error,omitempty"`
}

type Snapshot struct {
	Version int   `json:"version" msgpack:"version"`
	Jobs    []Job `json:"jobs" msgpack:"jobs"`
}

type ImportRequest struct {
	Snapshot Snapshot `json:"snapshot" msgpack:"snapshot"`
}

type Ack struct {
	Status string `json:"status" msgpack:"status"`
}

const (
	StatePending   = "pending"
	StateCompleted = "completed"
	StateFailed    = "failed"
	StateCancelled = "cancelled"

	CodeInvalidArgument = "invalid_argument"
	CodeConflict        = "conflict"
	CodeNotFound        = "not_found"
	CodeInternal        = "internal"
)

type ServiceError struct {
	Code      string `json:"code" msgpack:"code"`
	Message   string `json:"message" msgpack:"message"`
	Retryable bool   `json:"retryable" msgpack:"retryable"`
}

func (e *ServiceError) Error() string {
	if e == nil {
		return ""
	}
	return e.Message
}

type Result[T any] struct {
	OK    bool          `json:"ok" msgpack:"ok"`
	Value T             `json:"value,omitempty" msgpack:"value,omitempty"`
	Error *ServiceError `json:"error,omitempty" msgpack:"error,omitempty"`
}

func Success[T any](value T) Result[T] {
	return Result[T]{OK: true, Value: value}
}

func Failure[T any](err error) Result[T] {
	var zero T
	serviceErr, ok := err.(*ServiceError)
	if !ok {
		serviceErr = &ServiceError{Code: CodeInternal, Message: fmt.Sprint(err), Retryable: true}
	}
	return Result[T]{Value: zero, Error: serviceErr}
}
