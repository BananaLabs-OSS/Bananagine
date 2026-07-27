package gameworker

import (
	"errors"
	"reflect"
	"testing"
)

type fakeWorker struct {
	nextID uint32
	result map[uint32]PollResult
}

func (w *fakeWorker) Submit(SubmitRequest) (uint32, error) {
	w.nextID++
	return w.nextID, nil
}

func (w *fakeWorker) Result(id uint32) (PollResult, error) {
	return w.result[id], nil
}

func (w *fakeWorker) Cancel(uint32) error { return nil }

func TestStateIdempotencyAndInstanceIsolation(t *testing.T) {
	worker := &fakeWorker{result: map[uint32]PollResult{}}
	request := SubmitRequest{IdempotencyKey: "probe-1", Method: "GET", URL: "https://example.test/health"}
	first := NewState()
	job, err := first.Submit(request, worker)
	if err != nil || job.State != StatePending {
		t.Fatalf("first submit = (%#v, %v)", job, err)
	}
	replayed, err := first.Submit(request, worker)
	if err != nil || !reflect.DeepEqual(replayed, job) || worker.nextID != 1 {
		t.Fatalf("idempotent replay = (%#v, %v), submits=%d", replayed, err, worker.nextID)
	}
	conflict := request
	conflict.URL += "/different"
	if _, err := first.Submit(conflict, worker); err == nil {
		t.Fatal("conflicting idempotency key succeeded")
	}
	second := NewState()
	if _, err := second.Submit(request, worker); err != nil || worker.nextID != 2 {
		t.Fatalf("isolated owner submit = %v, submits=%d", err, worker.nextID)
	}
}

func TestStateTerminalReceiptSurvivesSnapshotRestart(t *testing.T) {
	worker := &fakeWorker{result: map[uint32]PollResult{
		1: {Done: true, Status: 204, Headers: map[string]string{"X-Node": "a"}, Body: []byte("ok")},
	}}
	state := NewState()
	request := SubmitRequest{IdempotencyKey: "probe-1", Method: "GET", URL: "https://example.test/health"}
	if _, err := state.Submit(request, worker); err != nil {
		t.Fatal(err)
	}
	completed, err := state.Status("probe-1", worker)
	if err != nil || completed.State != StateCompleted || completed.Status != 204 {
		t.Fatalf("completed = (%#v, %v)", completed, err)
	}
	restarted := NewState()
	if err := restarted.Import(ImportRequest{Snapshot: state.Export()}); err != nil {
		t.Fatal(err)
	}
	replayed, err := restarted.Status("probe-1", nil)
	if err != nil || replayed.State != StateCompleted || string(replayed.Body) != "ok" {
		t.Fatalf("restored receipt = (%#v, %v)", replayed, err)
	}
}

func TestStateWorkerErrorsFailClosed(t *testing.T) {
	state := NewState()
	_, err := state.Submit(
		SubmitRequest{IdempotencyKey: "x", Method: "GET", URL: "https://example.test"},
		submitterFunc(func(SubmitRequest) (uint32, error) { return 0, errors.New("queue down") }),
	)
	if err == nil {
		t.Fatal("submit error was ignored")
	}
}

type submitterFunc func(SubmitRequest) (uint32, error)

func (fn submitterFunc) Submit(request SubmitRequest) (uint32, error) {
	return fn(request)
}
