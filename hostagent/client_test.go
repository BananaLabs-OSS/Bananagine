package hostagent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeEngine struct {
	mu    sync.Mutex
	calls []string
	in    Invocation
	err   error
}

func (e *fakeEngine) run(op string, in Invocation) (Outcome, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.calls = append(e.calls, op)
	e.in = in
	return Outcome{State: "running"}, e.err
}
func (e *fakeEngine) Create(_ context.Context, i Invocation) (Outcome, error) {
	return e.run("create", i)
}
func (e *fakeEngine) Start(_ context.Context, i Invocation) (Outcome, error) {
	return e.run("start", i)
}
func (e *fakeEngine) Update(_ context.Context, i Invocation) (Outcome, error) {
	return e.run("update", i)
}
func (e *fakeEngine) Stop(_ context.Context, i Invocation) (Outcome, error) { return e.run("stop", i) }
func (e *fakeEngine) Delete(_ context.Context, i Invocation) (Outcome, error) {
	return e.run("delete", i)
}

type classifiedError struct{ retry bool }

type recordingTransport struct {
	path    string
	headers map[string]string
	body    []byte
}

func (t *recordingTransport) Post(_ context.Context, endpoint string, headers map[string]string, body []byte) (int, []byte, error) {
	t.path, t.headers, t.body = endpoint, headers, append([]byte(nil), body...)
	return http.StatusOK, []byte(`{}`), nil
}

func TestCapabilityTransportCarriesAuthenticatedHeartbeatWithoutHostSelector(t *testing.T) {
	transport := &recordingTransport{}
	client, err := NewWithTransport(Config{BaseURL: "https://evolution.internal", HostID: "host-local", AgentID: "agent-local", Principals: []PrincipalCredential{{"signed", time.Now().Add(time.Hour)}}}, transport, &fakeEngine{})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	client.now = func() time.Time { return now }
	err = client.Observe(context.Background(), HostObservation{Observed: "ready", HeartbeatGeneration: 9, AtUnixMilli: now.UnixMilli(), CPUMillicores: 4000, MemoryBytes: 8 << 30})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(transport.path, "/internal/host-agent/observe") || transport.headers["Authorization"] != "Bearer signed" {
		t.Fatalf("transport=%#v", transport)
	}
	var wire map[string]any
	if json.Unmarshal(transport.body, &wire) != nil || wire["host_id"] != nil || wire["agent_id"] != nil {
		t.Fatalf("caller-selected identity field leaked into observation: %s", transport.body)
	}
}

func (e classifiedError) Error() string   { return "engine failed" }
func (e classifiedError) Retryable() bool { return e.retry }
func action(op, host string) Action {
	payload := []byte(`{"template":"vanilla"}`)
	sum := sha256.Sum256(payload)
	digest := hex.EncodeToString(sum[:])
	return Action{Desired: Desired{"work-1", host, 7, 3, op, payload, digest, "pending"}, Fence: Fence{"lease-1", 2, 7, digest, time.Unix(2_000_000_000, 0).Add(time.Minute).UnixMilli()}}
}

func TestMapsEveryAssignmentOperationWithoutNodeOverride(t *testing.T) {
	for _, op := range []string{"create", "start", "update", "stop", "delete"} {
		t.Run(op, func(t *testing.T) {
			var mu sync.Mutex
			claims := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer newest" {
					t.Errorf("credential=%q", r.Header.Get("Authorization"))
				}
				switch r.URL.Path {
				case "/internal/host-agent/claim":
					mu.Lock()
					claims++
					n := claims
					mu.Unlock()
					if n == 1 {
						json.NewEncoder(w).Encode(Result{Action: ptr(action(op, "node-local"))})
					} else {
						json.NewEncoder(w).Encode(Result{})
					}
				case "/internal/host-agent/receipt":
					json.NewEncoder(w).Encode(Result{Settled: true})
				default:
					http.Error(w, "unexpected", 404)
				}
			}))
			defer server.Close()
			engine := &fakeEngine{}
			client, err := New(Config{BaseURL: server.URL, HostID: "node-local", AgentID: "agent-local", Principals: []PrincipalCredential{{"old", time.Unix(2_000_000_000, 0).Add(time.Second)}, {"newest", time.Unix(2_000_000_000, 0).Add(time.Hour)}}, MaxActions: 2}, server.Client(), engine)
			if err != nil {
				t.Fatal(err)
			}
			client.now = func() time.Time { return time.Unix(2_000_000_000, 0) }
			processed, err := client.Step(context.Background())
			if err != nil || processed != 1 {
				t.Fatalf("step=%d,%v", processed, err)
			}
			if len(engine.calls) != 1 || engine.calls[0] != op {
				t.Fatalf("calls=%v", engine.calls)
			}
			if engine.in.HostID != "node-local" || engine.in.AgentID != "agent-local" || engine.in.WorkloadID != "work-1" || strings.Contains(string(engine.in.Payload), "node-local") {
				t.Fatalf("invocation=%#v", engine.in)
			}
		})
	}
}

func TestRejectsCrossHostAndDigestForgeryBeforeEngine(t *testing.T) {
	for _, mutate := range []func(*Action){func(a *Action) { a.Desired.HostID = "node-other" }, func(a *Action) { a.Desired.Payload = []byte("changed") }} {
		engine := &fakeEngine{}
		a := action("create", "node-local")
		mutate(&a)
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { json.NewEncoder(w).Encode(Result{Action: &a}) }))
		client, _ := New(Config{BaseURL: server.URL, HostID: "node-local", AgentID: "agent-local", Principals: []PrincipalCredential{{"token", time.Unix(2_000_000_000, 0).Add(time.Hour)}}}, server.Client(), engine)
		client.now = func() time.Time { return time.Unix(2_000_000_000, 0) }
		if _, err := client.Step(context.Background()); err == nil {
			t.Fatal("forged assignment accepted")
		}
		if len(engine.calls) != 0 {
			t.Fatal("engine invoked for forged assignment")
		}
		server.Close()
	}
}

func TestRetryClassificationAndExactFences(t *testing.T) {
	for _, tc := range []struct {
		name  string
		retry bool
		delay time.Duration
	}{{"transient", true, 5 * time.Second}, {"permanent", false, time.Hour}} {
		t.Run(tc.name, func(t *testing.T) {
			var paths []string
			var retry RetryRequest
			claims := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				paths = append(paths, r.URL.Path)
				switch r.URL.Path {
				case "/internal/host-agent/claim":
					claims++
					if claims == 1 {
						json.NewEncoder(w).Encode(Result{Action: ptr(action("start", "node-local"))})
					} else {
						json.NewEncoder(w).Encode(Result{})
					}
				case "/internal/host-agent/receipt":
					var receipt ReceiptRequest
					json.NewDecoder(r.Body).Decode(&receipt)
					if receipt.Receipt.LeaseID != "lease-1" || receipt.Receipt.Attempt != 2 || receipt.Receipt.Generation != 7 || receipt.Receipt.Success {
						t.Errorf("receipt=%#v", receipt)
					}
					json.NewEncoder(w).Encode(Result{Settled: false})
				case "/internal/host-agent/retry":
					json.NewDecoder(r.Body).Decode(&retry)
					json.NewEncoder(w).Encode(Result{Settled: true})
				}
			}))
			defer server.Close()
			engine := &fakeEngine{err: classifiedError{tc.retry}}
			client, _ := New(Config{BaseURL: server.URL, HostID: "node-local", AgentID: "agent-local", Principals: []PrincipalCredential{{"token", time.Unix(2_000_000_000, 0).Add(time.Hour)}}, RetryDelay: 5 * time.Second, MaxActions: 2}, server.Client(), engine)
			now := time.Unix(2_000_000_000, 0)
			client.now = func() time.Time { return now }
			if _, err := client.Step(context.Background()); err != nil {
				t.Fatal(err)
			}
			if retry.ExpectedAttempt != 2 || retry.WorkloadID != "work-1" || retry.RetryAtUnixMilli != now.Add(tc.delay).UnixMilli() {
				t.Fatalf("retry=%#v paths=%v", retry, paths)
			}
		})
	}
}

func TestBoundedPullAndCallbackCapability(t *testing.T) {
	claims := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/internal/host-agent/capability":
			json.NewEncoder(w).Encode(map[string]string{"token": "callback-token"})
		case "/internal/host-agent/claim":
			claims++
			json.NewEncoder(w).Encode(Result{Action: ptr(action("stop", "node-local"))})
		case "/internal/host-agent/receipt":
			json.NewEncoder(w).Encode(Result{Settled: true})
		}
	}))
	defer server.Close()
	engine := &fakeEngine{}
	client, _ := New(Config{BaseURL: server.URL, HostID: "node-local", AgentID: "agent-local", Principals: []PrincipalCredential{{"token", time.Now().Add(time.Hour)}}, MaxActions: 3}, server.Client(), engine)
	client.now = time.Now
	if count, err := client.Step(context.Background()); err != nil || count != 3 || claims != 3 {
		t.Fatalf("bounded step=%d claims=%d err=%v", count, claims, err)
	}
	token, err := client.IssueCallback(context.Background(), "work-1", "connection.connect", time.Now().Add(time.Minute))
	if err != nil || token != "callback-token" {
		t.Fatalf("callback=%q,%v", token, err)
	}
}

func TestFailsClosedWithoutUnexpiredProtectedPrincipal(t *testing.T) {
	client, _ := New(Config{BaseURL: "https://private.invalid", HostID: "node", AgentID: "agent", Principals: []PrincipalCredential{{"expired", time.Unix(1, 0)}}}, nil, &fakeEngine{})
	client.now = func() time.Time { return time.Unix(2, 0) }
	if _, err := client.Step(context.Background()); err == nil {
		t.Fatal("expired principal used")
	}
}
func ptr[T any](v T) *T { return &v }
