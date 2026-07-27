// Package templatecatalog owns the transport-neutral game-template catalog
// used by Bananagine compositions. The owner stores only public projections;
// filesystem discovery and YAML parsing remain privileged facade work.
package templatecatalog

import (
	"encoding/json"
	"fmt"
)

const (
	Capability = "bananagine.template-catalog.v1"

	FnReplace        = Capability + ".replace"
	FnList           = Capability + ".list"
	FnGet            = Capability + ".get"
	FnSnapshotExport = Capability + ".snapshot.export"
	FnSnapshotImport = Capability + ".snapshot.import"

	SnapshotVersion = 1
)

type Entry struct {
	Name        string          `json:"name" msgpack:"name"`
	Game        string          `json:"game" msgpack:"game"`
	Label       string          `json:"label" msgpack:"label"`
	Engine      string          `json:"engine,omitempty" msgpack:"engine,omitempty"`
	CPULimit    float64         `json:"cpu_limit" msgpack:"cpu_limit"`
	MemoryLimit int64           `json:"memory_limit" msgpack:"memory_limit"`
	ConfigJSON  json.RawMessage `json:"config_json" msgpack:"config_json"`
	RuntimeJSON json.RawMessage `json:"runtime_json,omitempty" msgpack:"runtime_json,omitempty"`
}

type ReplaceRequest struct {
	RequestID string  `json:"request_id" msgpack:"request_id"`
	Entries   []Entry `json:"entries" msgpack:"entries"`
}

type GetRequest struct {
	Name string `json:"name" msgpack:"name"`
}

type Catalog struct {
	Revision uint64  `json:"revision" msgpack:"revision"`
	Entries  []Entry `json:"entries" msgpack:"entries"`
}

type Snapshot struct {
	Version  int     `json:"version" msgpack:"version"`
	Revision uint64  `json:"revision" msgpack:"revision"`
	Entries  []Entry `json:"entries" msgpack:"entries"`
}

type ImportRequest struct {
	RequestID string   `json:"request_id" msgpack:"request_id"`
	Snapshot  Snapshot `json:"snapshot" msgpack:"snapshot"`
}

const (
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
		serviceErr = &ServiceError{Code: CodeInternal, Message: fmt.Sprint(err)}
	}
	return Result[T]{Value: zero, Error: serviceErr}
}
