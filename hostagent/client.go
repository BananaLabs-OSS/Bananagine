// Package hostagent implements Bananagine's private Evolution assignment
// client. Host identity is operator-owned local configuration, never input
// from an assignment payload or HTTP caller.
package hostagent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const ContractVersion = "assignment-reconcile.v1"

type PrincipalCredential struct {
	Token     string
	ExpiresAt time.Time
}
type Config struct {
	BaseURL, HostID, AgentID string
	Principals               []PrincipalCredential
	Lease                    time.Duration
	MaxActions               int
	RetryDelay               time.Duration
}
type Invocation struct {
	HostID, AgentID, WorkloadID string
	Generation                  uint64
	Payload                     []byte
	IdempotencyKey              string
}
type Outcome struct{ State string }
type Engine interface {
	Create(context.Context, Invocation) (Outcome, error)
	Start(context.Context, Invocation) (Outcome, error)
	Update(context.Context, Invocation) (Outcome, error)
	Stop(context.Context, Invocation) (Outcome, error)
	Delete(context.Context, Invocation) (Outcome, error)
}
type Retryable interface{ Retryable() bool }

type Client struct {
	cfg       Config
	transport Transport
	engine    Engine
	now       func() time.Time
	mu        sync.Mutex
	sequence  uint64
}

type Transport interface {
	Post(context.Context, string, map[string]string, []byte) (int, []byte, error)
}
type nativeTransport struct{ client *http.Client }

func (t nativeTransport) Post(ctx context.Context, endpoint string, headers map[string]string, body []byte) (int, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return 0, nil, err
	}
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	response, err := t.client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	return response.StatusCode, raw, err
}

func New(cfg Config, httpClient *http.Client, engine Engine) (*Client, error) {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 10 * time.Second}
	}
	return NewWithTransport(cfg, nativeTransport{httpClient}, engine)
}

func NewWithTransport(cfg Config, transport Transport, engine Engine) (*Client, error) {
	if cfg.BaseURL == "" || cfg.HostID == "" || cfg.AgentID == "" || len(cfg.Principals) == 0 || engine == nil {
		return nil, errors.New("host agent requires local endpoint, identity, principal, and engine")
	}
	endpoint, parseErr := url.Parse(cfg.BaseURL)
	localHTTP := endpoint.Scheme == "http" && (endpoint.Hostname() == "127.0.0.1" || endpoint.Hostname() == "::1" || endpoint.Hostname() == "localhost")
	if parseErr != nil || endpoint.Host == "" || (endpoint.Scheme != "https" && !localHTTP) {
		return nil, errors.New("host agent endpoint must use TLS")
	}
	if cfg.Lease == 0 {
		cfg.Lease = time.Minute
	}
	if cfg.Lease < time.Second || cfg.Lease > 5*time.Minute {
		return nil, errors.New("invalid assignment lease")
	}
	if cfg.MaxActions == 0 {
		cfg.MaxActions = 8
	}
	if cfg.MaxActions < 1 || cfg.MaxActions > 64 {
		return nil, errors.New("invalid action bound")
	}
	if cfg.RetryDelay == 0 {
		cfg.RetryDelay = 5 * time.Second
	}
	if cfg.RetryDelay < time.Second || cfg.RetryDelay > time.Hour {
		return nil, errors.New("invalid retry delay")
	}
	if transport == nil {
		return nil, errors.New("host agent requires transport")
	}
	return &Client{cfg: cfg, transport: transport, engine: engine, now: time.Now}, nil
}

type HostObservation struct {
	ContractVersion     string `json:"contract_version"`
	CommandID           string `json:"command_id"`
	Observed            string `json:"observed"`
	HeartbeatGeneration uint64 `json:"heartbeat_generation"`
	AtUnixMilli         int64  `json:"at_unix_milli"`
	CPUMillicores       int64  `json:"cpu_millicores"`
	MemoryBytes         int64  `json:"memory_bytes"`
	StorageBytes        int64  `json:"storage_bytes"`
}

func (c *Client) Observe(ctx context.Context, q HostObservation) error {
	if q.ContractVersion == "" {
		q.ContractVersion = "infrastructure-host.v1"
	}
	if q.CommandID == "" {
		q.CommandID = c.command("observe")
	}
	return c.post(ctx, "/internal/host-agent/observe", q, nil)
}

func (c *Client) Step(ctx context.Context) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	processed := 0
	for processed < c.cfg.MaxActions {
		now := c.now().UTC()
		var result Result
		if err := c.post(ctx, "/internal/host-agent/claim", ClaimRequest{ContractVersion, c.command("claim"), 0, now.UnixMilli(), c.cfg.Lease.Milliseconds()}, &result); err != nil {
			return processed, err
		}
		if result.Action == nil {
			return processed, nil
		}
		if err := c.validateAction(*result.Action, now); err != nil {
			return processed, err
		}
		processed++
		if err := c.executeAndSettle(ctx, *result.Action, now); err != nil {
			return processed, err
		}
	}
	return processed, nil
}

// Run is a bounded-rate pull loop. Each tick performs at most MaxActions and
// stops immediately on authentication, transport, or reconciliation failure
// so a supervisor can apply deployment policy instead of spinning.
func (c *Client) Run(ctx context.Context, interval time.Duration) error {
	if interval < 100*time.Millisecond || interval > time.Minute {
		return errors.New("invalid host agent poll interval")
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if _, err := c.Step(ctx); err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (c *Client) executeAndSettle(ctx context.Context, a Action, now time.Time) error {
	inv := Invocation{c.cfg.HostID, c.cfg.AgentID, a.Desired.WorkloadID, a.Desired.Generation, append([]byte(nil), a.Desired.Payload...), fmt.Sprintf("assignment/%s/%d/%d/%s", a.Desired.WorkloadID, a.Fence.Generation, a.Fence.Attempt, a.Fence.PayloadSHA256)}
	var out Outcome
	var err error
	switch a.Desired.Operation {
	case "create":
		out, err = c.engine.Create(ctx, inv)
	case "start":
		out, err = c.engine.Start(ctx, inv)
	case "update":
		out, err = c.engine.Update(ctx, inv)
	case "stop":
		out, err = c.engine.Stop(ctx, inv)
	case "delete":
		out, err = c.engine.Delete(ctx, inv)
	default:
		return errors.New("unsupported assignment operation")
	}
	success := err == nil
	state := out.State
	detail := ""
	if state == "" {
		if success {
			state = "applied"
		} else {
			state = "failed"
		}
	}
	if err != nil {
		detail = bounded(err.Error(), 1024)
	}
	receipt := ReceiptRequest{ContractVersion, fmt.Sprintf("receipt/%s/%d/%d", a.Desired.WorkloadID, a.Fence.Generation, a.Fence.Attempt), a.Desired.WorkloadID, ExactReceipt{a.Fence.LeaseID, a.Fence.Attempt, a.Fence.Generation, a.Fence.PayloadSHA256, success, state, detail, now.UnixMilli()}}
	var settled Result
	if postErr := c.post(ctx, "/internal/host-agent/receipt", receipt, &settled); postErr != nil {
		return postErr
	}
	if err == nil {
		return nil
	}
	delay := c.cfg.RetryDelay
	if classified, ok := err.(Retryable); ok && !classified.Retryable() {
		delay = time.Hour
	}
	retry := RetryRequest{ContractVersion, fmt.Sprintf("retry/%s/%d/%d", a.Desired.WorkloadID, a.Fence.Generation, a.Fence.Attempt), a.Desired.WorkloadID, a.Fence.Attempt, now.Add(delay).UnixMilli()}
	if postErr := c.post(ctx, "/internal/host-agent/retry", retry, &settled); postErr != nil {
		return postErr
	}
	return nil
}

func (c *Client) IssueCallback(ctx context.Context, workload, operation string, expiry time.Time) (string, error) {
	var result struct {
		Token string `json:"token"`
	}
	err := c.post(ctx, "/internal/host-agent/capability", map[string]any{"workload_id": workload, "operation": operation, "expires_at_unix": expiry.Unix()}, &result)
	return result.Token, err
}
func (c *Client) validateAction(a Action, now time.Time) error {
	if a.Desired.HostID != c.cfg.HostID || a.Desired.WorkloadID == "" || a.Desired.Generation == 0 || a.Fence.Generation != a.Desired.Generation || a.Fence.Attempt == 0 || a.Fence.LeaseID == "" || a.Fence.LeaseExpiresUnixMilli <= now.UnixMilli() {
		return errors.New("invalid or cross-host assignment fence")
	}
	sum := sha256.Sum256(a.Desired.Payload)
	if hex.EncodeToString(sum[:]) != a.Fence.PayloadSHA256 || a.Desired.PayloadSHA256 != a.Fence.PayloadSHA256 {
		return errors.New("assignment payload digest mismatch")
	}
	return nil
}
func (c *Client) command(kind string) string {
	c.sequence++
	return fmt.Sprintf("agent/%s/%s/%d/%d", c.cfg.AgentID, kind, c.now().UnixMilli(), c.sequence)
}
func (c *Client) credential(now time.Time) (string, error) {
	var selected PrincipalCredential
	for _, candidate := range c.cfg.Principals {
		if candidate.Token != "" && candidate.ExpiresAt.After(now) && candidate.ExpiresAt.After(selected.ExpiresAt) {
			selected = candidate
		}
	}
	if selected.Token == "" {
		return "", errors.New("no unexpired host principal")
	}
	return selected.Token, nil
}
func (c *Client) post(ctx context.Context, path string, input, output any) error {
	token, err := c.credential(c.now())
	if err != nil {
		return err
	}
	raw, err := json.Marshal(input)
	if err != nil {
		return err
	}
	status, body, err := c.transport.Post(ctx, strings.TrimRight(c.cfg.BaseURL, "/")+path, map[string]string{"Authorization": "Bearer " + token, "Content-Type": "application/json"}, raw)
	if err != nil {
		return err
	}
	if status < 200 || status >= 300 {
		return fmt.Errorf("evolution host agent %s rejected: status %d", path, status)
	}
	if output != nil && json.Unmarshal(body, output) != nil {
		return errors.New("invalid Evolution host-agent response")
	}
	return nil
}
func bounded(value string, limit int) string {
	if len(value) > limit {
		return value[:limit]
	}
	return value
}

type ClaimRequest struct {
	Version, CommandID        string
	ExpectedAttempt           uint64
	NowUnixMilli, LeaseMillis int64
}
type RetryRequest struct {
	Version, CommandID, WorkloadID string
	ExpectedAttempt                uint64
	RetryAtUnixMilli               int64
}
type ReceiptRequest struct {
	Version, CommandID, WorkloadID string
	Receipt                        ExactReceipt
}
type ExactReceipt struct {
	LeaseID               string
	Attempt, Generation   uint64
	PayloadSHA256         string
	Success               bool
	ObservedState, Detail string
	RecordedAtUnixMilli   int64
}
type Desired struct {
	WorkloadID, HostID    string
	Generation, Revision  uint64
	Operation             string
	Payload               []byte
	PayloadSHA256, Status string
}
type Fence struct {
	LeaseID               string
	Attempt, Generation   uint64
	PayloadSHA256         string
	LeaseExpiresUnixMilli int64
}
type Action struct {
	Desired Desired
	Fence   Fence
}
type Result struct {
	Desired *Desired `json:"desired,omitempty"`
	Action  *Action  `json:"action,omitempty"`
	Settled bool     `json:"settled"`
}
