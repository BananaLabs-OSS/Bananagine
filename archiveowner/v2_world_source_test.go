package archiveowner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type sourceTestScope struct {
	id string
}

func (s sourceTestScope) Validate() error {
	if s.id == "" {
		return errors.New("empty test scope")
	}
	return nil
}

func (s sourceTestScope) RoutingID() string {
	return "pulp-scope/v1/" + s.id
}

type recordedAccessResolver struct {
	mu       sync.Mutex
	access   map[string]ScopedAccess
	resolved []string
}

func (r *recordedAccessResolver) ResolveBananagine(_ context.Context, scope Scope, nodeID string) (ScopedAccess, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	key := scope.RoutingID() + "\x00" + nodeID
	r.resolved = append(r.resolved, key)
	access, ok := r.access[key]
	if !ok {
		return ScopedAccess{}, errors.New("unknown scoped node")
	}
	return access, nil
}

func TestV2WorldSourceStreamsStagesAndCleansWorld(t *testing.T) {
	t.Parallel()

	const token = "scoped-token"
	var gotDelete atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get(serviceTokenHeader) != token {
			t.Errorf("service token = %q", request.Header.Get(serviceTokenHeader))
			http.Error(writer, "unauthorized", http.StatusUnauthorized)
			return
		}
		switch {
		case request.Method == http.MethodGet && request.URL.Path == "/orchestration/worlds/server-1":
			writer.Header().Set("Content-Type", "application/zip")
			writer.WriteHeader(http.StatusOK)
			if flusher, ok := writer.(http.Flusher); ok {
				flusher.Flush()
			}
			_, _ = writer.Write([]byte("world-"))
			_, _ = writer.Write([]byte("archive"))
		case request.Method == http.MethodDelete && request.URL.Path == "/orchestration/worlds/world-1":
			gotDelete.Store(true)
			writer.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()

	scope := sourceTestScope{id: "evolution|primary|host|one"}
	resolver := &recordedAccessResolver{access: map[string]ScopedAccess{
		scope.RoutingID() + "\x00node-1": {BaseURL: server.URL, ServiceToken: token},
	}}
	stagingDir := t.TempDir()
	source := newSourceForTest(t, resolver, stagingDir)

	snapshot, err := source.SnapshotWorld(context.Background(), scope, V2WorldSnapshotRequest{
		Kind: worldKind, ServerID: "server-1", OrderID: "order-1", NodeID: "node-1",
		Timeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Size != int64(len("world-archive")) {
		t.Fatalf("size = %d", snapshot.Size)
	}
	body, err := io.ReadAll(snapshot.Body)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "world-archive" {
		t.Fatalf("body = %q", body)
	}
	if err := snapshot.Body.Close(); err != nil {
		t.Fatal(err)
	}
	assertEmptyDirectory(t, stagingDir)

	if err := source.CleanupWorld(context.Background(), scope, V2WorldCleanupRequest{
		ServerID: "server-1", OrderID: "order-1", NodeID: "node-1",
		ContainerID: "container-1", CleanupWorld: "world-1", Timeout: time.Second,
	}); err != nil {
		t.Fatal(err)
	}
	if !gotDelete.Load() {
		t.Fatal("cleanup request was not observed")
	}
}

func TestV2WorldSourceBackupFlushesAndAlwaysRestoresSaves(t *testing.T) {
	t.Parallel()

	const token = "backup-token"
	var mu sync.Mutex
	var operations []string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get(serviceTokenHeader) != token {
			http.Error(writer, "unauthorized", http.StatusUnauthorized)
			return
		}
		switch {
		case request.Method == http.MethodPost && request.URL.Path == "/orchestration/servers/container-1/exec":
			var body struct {
				Cmd []string `json:"cmd"`
			}
			if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
				t.Errorf("decode exec request: %v", err)
				http.Error(writer, "bad request", http.StatusBadRequest)
				return
			}
			if len(body.Cmd) != 2 || body.Cmd[0] != "rcon" {
				t.Errorf("exec command = %#v", body.Cmd)
				http.Error(writer, "bad command", http.StatusBadRequest)
				return
			}
			mu.Lock()
			operations = append(operations, body.Cmd[1])
			mu.Unlock()
			writer.Header().Set("Content-Type", "application/json")
			_, _ = writer.Write([]byte(`{"output":"ok"}`))
		case request.Method == http.MethodGet && request.URL.Path == "/orchestration/worlds/server-1":
			mu.Lock()
			operations = append(operations, "fetch")
			mu.Unlock()
			writer.Header().Set("Content-Type", "application/zip")
			_, _ = writer.Write([]byte("backup"))
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()

	scope := sourceTestScope{id: "backup-scope"}
	resolver := &recordedAccessResolver{access: map[string]ScopedAccess{
		scope.RoutingID() + "\x00node-1": {BaseURL: server.URL, ServiceToken: token},
	}}
	source := newSourceForTest(t, resolver, t.TempDir())
	snapshot, err := source.SnapshotWorld(context.Background(), scope, V2WorldSnapshotRequest{
		Kind: worldKind, ServerID: "server-1", NodeID: "node-1", ContainerID: "container-1",
		Timeout: time.Second, FlushSaves: true, FlushDelay: time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer snapshot.Body.Close()

	mu.Lock()
	got := append([]string(nil), operations...)
	mu.Unlock()
	want := []string{"save-off", "save-all flush", "fetch", "save-on"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("operations = %v, want %v", got, want)
	}
}

func TestV2WorldSourceCancellationStillRestoresSaves(t *testing.T) {
	t.Parallel()

	const token = "recovery-token"
	var saveOn atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			http.NotFound(writer, request)
			return
		}
		var body struct {
			Cmd []string `json:"cmd"`
		}
		_ = json.NewDecoder(request.Body).Decode(&body)
		if len(body.Cmd) == 2 && body.Cmd[1] == "save-on" {
			saveOn.Add(1)
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"output":"ok"}`))
	}))
	defer server.Close()

	scope := sourceTestScope{id: "cancel-scope"}
	resolver := &recordedAccessResolver{access: map[string]ScopedAccess{
		scope.RoutingID() + "\x00": {BaseURL: server.URL, ServiceToken: token},
	}}
	source := newSourceForTest(t, resolver, t.TempDir())
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err := source.SnapshotWorld(ctx, scope, V2WorldSnapshotRequest{
		Kind: worldKind, ServerID: "server-1", ContainerID: "container-1",
		Timeout: time.Second, FlushSaves: true, FlushDelay: 200 * time.Millisecond,
	})
	if err == nil || !strings.Contains(err.Error(), "flush delay") {
		t.Fatalf("error = %v", err)
	}
	if saveOn.Load() != 1 {
		t.Fatalf("save-on attempts = %d", saveOn.Load())
	}
}

func TestV2WorldSourceSaveOnFailureDiscardsSnapshot(t *testing.T) {
	t.Parallel()

	const token = "save-on-failure-token"
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch {
		case request.Method == http.MethodPost:
			var body struct {
				Cmd []string `json:"cmd"`
			}
			_ = json.NewDecoder(request.Body).Decode(&body)
			if len(body.Cmd) == 2 && body.Cmd[1] == "save-on" {
				http.Error(writer, "restore failed", http.StatusInternalServerError)
				return
			}
			_, _ = writer.Write([]byte(`{"output":"ok"}`))
		case request.Method == http.MethodGet:
			writer.Header().Set("Content-Type", "application/zip")
			_, _ = writer.Write([]byte("backup"))
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()

	scope := sourceTestScope{id: "save-on-failure-scope"}
	stagingDir := t.TempDir()
	resolver := &recordedAccessResolver{access: map[string]ScopedAccess{
		scope.RoutingID() + "\x00": {BaseURL: server.URL, ServiceToken: token},
	}}
	source := newSourceForTest(t, resolver, stagingDir)
	snapshot, err := source.SnapshotWorld(context.Background(), scope, V2WorldSnapshotRequest{
		Kind: worldKind, ServerID: "server-1", ContainerID: "container-1",
		Timeout: time.Second, FlushSaves: true,
	})
	if err == nil || !strings.Contains(err.Error(), "restore saves") {
		t.Fatalf("error = %v", err)
	}
	if snapshot.Body != nil || snapshot.Size != 0 {
		t.Fatalf("snapshot returned after save-on failure: %#v", snapshot)
	}
	assertEmptyDirectory(t, stagingDir)
}

func TestV2WorldSourceKeepsConcurrentScopesAndNodesIsolated(t *testing.T) {
	t.Parallel()

	type target struct {
		scope sourceTestScope
		node  string
		token string
		body  string
	}
	targets := []target{
		{scope: sourceTestScope{id: "scope-a"}, node: "node-a", token: "token-a", body: "archive-a"},
		{scope: sourceTestScope{id: "scope-b"}, node: "node-b", token: "token-b", body: "archive-b"},
		{scope: sourceTestScope{id: "scope-c"}, node: "node-c", token: "token-c", body: "archive-c"},
	}
	resolver := &recordedAccessResolver{access: make(map[string]ScopedAccess)}
	servers := make([]*httptest.Server, 0, len(targets))
	for _, target := range targets {
		target := target
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			if request.Header.Get(serviceTokenHeader) != target.token {
				http.Error(writer, "cross-scope credential", http.StatusUnauthorized)
				return
			}
			writer.Header().Set("Content-Type", "application/zip")
			_, _ = writer.Write([]byte(target.body))
		}))
		servers = append(servers, server)
		resolver.access[target.scope.RoutingID()+"\x00"+target.node] = ScopedAccess{
			BaseURL: server.URL, ServiceToken: target.token,
		}
	}
	defer func() {
		for _, server := range servers {
			server.Close()
		}
	}()

	source := newSourceForTest(t, resolver, t.TempDir())
	var wait sync.WaitGroup
	errs := make(chan error, len(targets)*8)
	for iteration := 0; iteration < 8; iteration++ {
		for _, target := range targets {
			target := target
			wait.Add(1)
			go func() {
				defer wait.Done()
				snapshot, err := source.SnapshotWorld(context.Background(), target.scope, V2WorldSnapshotRequest{
					Kind: worldKind, ServerID: "server-1", NodeID: target.node, Timeout: time.Second,
				})
				if err != nil {
					errs <- err
					return
				}
				body, readErr := io.ReadAll(snapshot.Body)
				closeErr := snapshot.Body.Close()
				if readErr != nil || closeErr != nil || string(body) != target.body {
					errs <- fmt.Errorf("body=%q read=%v close=%v", body, readErr, closeErr)
				}
			}()
		}
	}
	wait.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}

func TestV2WorldSourceRejectsUnsafeAndUnscopedRequests(t *testing.T) {
	t.Parallel()

	var resolves atomic.Int32
	source := newSourceForTest(t, ScopedAccessResolverFunc(func(context.Context, Scope, string) (ScopedAccess, error) {
		resolves.Add(1)
		return ScopedAccess{}, errors.New("must not resolve")
	}), t.TempDir())

	tests := []V2WorldSnapshotRequest{
		{Kind: "other", ServerID: "server-1", Timeout: time.Second},
		{Kind: worldKind, ServerID: "../server", Timeout: time.Second},
		{Kind: worldKind, ServerID: "server-1", FlushSaves: true, Timeout: time.Second},
		{Kind: worldKind, ServerID: "server-1", FlushDelay: time.Millisecond, Timeout: time.Second},
	}
	for _, request := range tests {
		if _, err := source.SnapshotWorld(context.Background(), sourceTestScope{id: "scope"}, request); err == nil {
			t.Errorf("request unexpectedly accepted: %#v", request)
		}
	}
	if resolves.Load() != 0 {
		t.Fatalf("resolver called %d times for invalid requests", resolves.Load())
	}
}

func newSourceForTest(t *testing.T, resolver ScopedAccessResolver, stagingDir string) *V2WorldSource {
	t.Helper()
	source, err := NewV2WorldSource(V2WorldSourceConfig{
		Access: resolver, Client: &http.Client{}, StagingDir: stagingDir,
		RestoreTimeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	return source
}

func assertEmptyDirectory(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("staging directory contains %d entries", len(entries))
	}
}
