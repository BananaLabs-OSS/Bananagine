package gameworker

import (
	"crypto/sha256"
	"encoding/json"
	"sort"
	"strings"
)

type Submitter interface {
	Submit(SubmitRequest) (uint32, error)
}

type PollResult struct {
	Done    bool
	Status  int
	Headers map[string]string
	Body    []byte
	Error   string
}

type Poller interface {
	Result(uint32) (PollResult, error)
	Cancel(uint32) error
}

type State struct {
	jobs map[string]*ownedJob
}

type ownedJob struct {
	request     SubmitRequest
	fingerprint [sha256.Size]byte
	hostTaskID  uint32
	job         Job
}

func NewState() *State {
	return &State{jobs: make(map[string]*ownedJob)}
}

func (s *State) Submit(request SubmitRequest, submitter Submitter) (Job, error) {
	if s == nil || submitter == nil {
		return Job{}, internal("worker owner is unavailable")
	}
	request.IdempotencyKey = strings.TrimSpace(request.IdempotencyKey)
	request.Method = strings.ToUpper(strings.TrimSpace(request.Method))
	request.URL = strings.TrimSpace(request.URL)
	if request.IdempotencyKey == "" || request.Method == "" || request.URL == "" {
		return Job{}, invalid("idempotency_key, method, and url are required")
	}
	fingerprint, err := requestFingerprint(request)
	if err != nil {
		return Job{}, internal("encode worker request")
	}
	if prior, exists := s.jobs[request.IdempotencyKey]; exists {
		if prior.fingerprint != fingerprint {
			return Job{}, conflict("idempotency_key was already used for different work")
		}
		return cloneJob(prior.job), nil
	}
	taskID, err := submitter.Submit(request)
	if err != nil {
		return Job{}, internal(err.Error())
	}
	job := Job{IdempotencyKey: request.IdempotencyKey, State: StatePending}
	s.jobs[request.IdempotencyKey] = &ownedJob{
		request: request, fingerprint: fingerprint, hostTaskID: taskID, job: job,
	}
	return cloneJob(job), nil
}

func (s *State) Status(key string, poller Poller) (Job, error) {
	owned, err := s.lookup(key)
	if err != nil {
		return Job{}, err
	}
	if owned.job.State != StatePending {
		return cloneJob(owned.job), nil
	}
	if poller == nil {
		return Job{}, internal("worker poller is unavailable")
	}
	result, err := poller.Result(owned.hostTaskID)
	if err != nil {
		return Job{}, internal(err.Error())
	}
	if !result.Done {
		return cloneJob(owned.job), nil
	}
	owned.job.Status = result.Status
	owned.job.Headers = cloneStrings(result.Headers)
	owned.job.Body = append([]byte(nil), result.Body...)
	owned.job.Error = result.Error
	if result.Error != "" {
		owned.job.State = StateFailed
	} else {
		owned.job.State = StateCompleted
	}
	return cloneJob(owned.job), nil
}

func (s *State) Cancel(key string, poller Poller) (Job, error) {
	owned, err := s.lookup(key)
	if err != nil {
		return Job{}, err
	}
	if owned.job.State != StatePending {
		return cloneJob(owned.job), nil
	}
	if poller == nil {
		return Job{}, internal("worker poller is unavailable")
	}
	if err := poller.Cancel(owned.hostTaskID); err != nil {
		return Job{}, internal(err.Error())
	}
	owned.job.State = StateCancelled
	return cloneJob(owned.job), nil
}

func (s *State) Export() Snapshot {
	keys := make([]string, 0, len(s.jobs))
	for key, owned := range s.jobs {
		if owned.job.State != StatePending {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	jobs := make([]Job, 0, len(keys))
	for _, key := range keys {
		jobs = append(jobs, cloneJob(s.jobs[key].job))
	}
	return Snapshot{Version: SnapshotVersion, Jobs: jobs}
}

func (s *State) Import(request ImportRequest) error {
	if request.Snapshot.Version != SnapshotVersion {
		return invalid("unsupported worker snapshot version")
	}
	for _, job := range request.Snapshot.Jobs {
		job.IdempotencyKey = strings.TrimSpace(job.IdempotencyKey)
		if job.IdempotencyKey == "" || job.State == StatePending {
			return invalid("worker snapshot contains an invalid job")
		}
		if _, duplicate := s.jobs[job.IdempotencyKey]; duplicate {
			return conflict("worker snapshot contains a duplicate job")
		}
		s.jobs[job.IdempotencyKey] = &ownedJob{job: cloneJob(job)}
	}
	return nil
}

func (s *State) lookup(key string) (*ownedJob, error) {
	if s == nil {
		return nil, internal("worker owner is unavailable")
	}
	key = strings.TrimSpace(key)
	if key == "" {
		return nil, invalid("idempotency_key is required")
	}
	owned, exists := s.jobs[key]
	if !exists {
		return nil, notFound("worker job not found")
	}
	return owned, nil
}

func requestFingerprint(request SubmitRequest) ([sha256.Size]byte, error) {
	wire, err := json.Marshal(request)
	if err != nil {
		return [sha256.Size]byte{}, err
	}
	return sha256.Sum256(wire), nil
}

func cloneJob(job Job) Job {
	job.Headers = cloneStrings(job.Headers)
	job.Body = append([]byte(nil), job.Body...)
	return job
}

func cloneStrings(source map[string]string) map[string]string {
	if source == nil {
		return nil
	}
	cloned := make(map[string]string, len(source))
	for key, value := range source {
		cloned[key] = value
	}
	return cloned
}

func invalid(message string) error {
	return &ServiceError{Code: CodeInvalidArgument, Message: message}
}

func conflict(message string) error {
	return &ServiceError{Code: CodeConflict, Message: message}
}

func notFound(message string) error {
	return &ServiceError{Code: CodeNotFound, Message: message}
}

func internal(message string) error {
	return &ServiceError{Code: CodeInternal, Message: message, Retryable: true}
}
