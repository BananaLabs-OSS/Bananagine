// Package archiveowner provides the privileged Bananagine source boundary used
// by the fleet archive owner. It deliberately owns only world fetch, world
// cleanup, and the save-flush dance needed before a backup snapshot.
package archiveowner

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
	"unicode"
)

const (
	worldKind             = "bananagine_world"
	serviceTokenHeader    = "X-Service-Token"
	defaultRestoreTimeout = 15 * time.Second
	maxErrorBodyBytes     = 8 * 1024
)

// Scope is the subset of ext.Scope required by the adapter. ext.Scope
// satisfies this interface directly; keeping the boundary structural avoids
// coupling Bananagine's public client package to a particular Pulp host build.
type Scope interface {
	Validate() error
	RoutingID() string
}

// ScopedAccess is one node's privileged Bananagine HTTP boundary. Credentials
// are supplied out of band by the host and never accepted from effect payloads.
type ScopedAccess struct {
	BaseURL      string
	ServiceToken string
}

// ScopedAccessResolver resolves Bananagine access for exactly the caller scope
// and selected node. Implementations should fail closed for an unknown node.
type ScopedAccessResolver interface {
	ResolveBananagine(context.Context, Scope, string) (ScopedAccess, error)
}

// ScopedAccessResolverFunc adapts a function to ScopedAccessResolver.
type ScopedAccessResolverFunc func(context.Context, Scope, string) (ScopedAccess, error)

func (f ScopedAccessResolverFunc) ResolveBananagine(ctx context.Context, scope Scope, nodeID string) (ScopedAccess, error) {
	return f(ctx, scope, nodeID)
}

// V2WorldSourceConfig supplies host-owned dependencies. StagingDir is optional;
// the operating-system temporary directory is used when it is empty.
type V2WorldSourceConfig struct {
	Access         ScopedAccessResolver
	Client         *http.Client
	StagingDir     string
	RestoreTimeout time.Duration
}

// V2WorldSource is safe for concurrent use. It holds no endpoint, credential,
// or operation state between calls.
type V2WorldSource struct {
	access         ScopedAccessResolver
	client         *http.Client
	stagingDir     string
	restoreTimeout time.Duration
}

// V2WorldSnapshotRequest describes a Bananagine world fetch. ContainerID is
// required only when FlushSaves is true.
type V2WorldSnapshotRequest struct {
	Kind        string
	ServerID    string
	OrderID     string
	NodeID      string
	ContainerID string
	Timeout     time.Duration
	FlushSaves  bool
	FlushDelay  time.Duration
}

// V2WorldCleanupRequest describes an idempotent Bananagine world cleanup.
type V2WorldCleanupRequest struct {
	ServerID     string
	OrderID      string
	NodeID       string
	ContainerID  string
	CleanupWorld string
	Timeout      time.Duration
}

// V2WorldSnapshot is a staged, streaming snapshot with a positive known size.
// Closing Body also removes the private staging file.
type V2WorldSnapshot struct {
	Body io.ReadCloser
	Size int64
}

// HTTPStatusError reports a non-success response without exposing its endpoint
// or credential.
type HTTPStatusError struct {
	Operation  string
	StatusCode int
	Body       string
}

func (e *HTTPStatusError) Error() string {
	if e.Body == "" {
		return fmt.Sprintf("bananagine %s: unexpected HTTP status %d", e.Operation, e.StatusCode)
	}
	return fmt.Sprintf("bananagine %s: unexpected HTTP status %d: %s", e.Operation, e.StatusCode, e.Body)
}

// NewV2WorldSource constructs a fail-closed source adapter.
func NewV2WorldSource(config V2WorldSourceConfig) (*V2WorldSource, error) {
	if config.Access == nil {
		return nil, errors.New("bananagine archive source: scoped access resolver is required")
	}
	if config.Client == nil {
		return nil, errors.New("bananagine archive source: HTTP client is required")
	}
	if config.RestoreTimeout < 0 {
		return nil, errors.New("bananagine archive source: restore timeout cannot be negative")
	}
	restoreTimeout := config.RestoreTimeout
	if restoreTimeout == 0 {
		restoreTimeout = defaultRestoreTimeout
	}
	return &V2WorldSource{
		access:         config.Access,
		client:         config.Client,
		stagingDir:     config.StagingDir,
		restoreTimeout: restoreTimeout,
	}, nil
}

// SnapshotWorld fetches one world through Bananagine's authenticated world
// endpoint. The response is copied incrementally to private host staging so the
// archive owner receives a positive known size without buffering a multi-GB
// world in memory.
//
// For backups, save-on is attempted with an independent bounded context after
// every save-off attempt, including when the caller is cancelled or the fetch
// fails. A snapshot is never returned when save-on fails.
func (s *V2WorldSource) SnapshotWorld(
	ctx context.Context,
	scope Scope,
	request V2WorldSnapshotRequest,
) (snapshot V2WorldSnapshot, returnErr error) {
	if err := validateScope(scope); err != nil {
		return V2WorldSnapshot{}, err
	}
	if err := validateSnapshotRequest(request); err != nil {
		return V2WorldSnapshot{}, err
	}
	operationCtx, cancel := context.WithTimeout(ctx, request.Timeout)
	defer cancel()

	access, baseURL, err := s.resolveAccess(operationCtx, scope, request.NodeID)
	if err != nil {
		return V2WorldSnapshot{}, err
	}

	restoreSaves := false
	defer func() {
		if !restoreSaves {
			return
		}
		restoreCtx, restoreCancel := context.WithTimeout(context.Background(), s.restoreTimeout)
		defer restoreCancel()
		if err := s.execRCON(restoreCtx, baseURL, access.ServiceToken, request.ContainerID, "save-on"); err != nil {
			if snapshot.Body != nil {
				returnErr = errors.Join(returnErr, snapshot.Body.Close())
				snapshot = V2WorldSnapshot{}
			}
			returnErr = errors.Join(returnErr, fmt.Errorf("bananagine backup restore saves: %w", err))
		}
	}()

	if request.FlushSaves {
		// The command may have reached the container even if the response is
		// lost, so recovery becomes mandatory before the first attempt.
		restoreSaves = true
		if err := s.execRCON(operationCtx, baseURL, access.ServiceToken, request.ContainerID, "save-off"); err != nil {
			return V2WorldSnapshot{}, fmt.Errorf("bananagine backup disable saves: %w", err)
		}
		if err := s.execRCON(operationCtx, baseURL, access.ServiceToken, request.ContainerID, "save-all flush"); err != nil {
			return V2WorldSnapshot{}, fmt.Errorf("bananagine backup flush saves: %w", err)
		}
		if err := waitContext(operationCtx, request.FlushDelay); err != nil {
			return V2WorldSnapshot{}, fmt.Errorf("bananagine backup flush delay: %w", err)
		}
	}

	snapshot, err = s.fetchWorld(operationCtx, scope, baseURL, access.ServiceToken, request.ServerID)
	if err != nil {
		return V2WorldSnapshot{}, err
	}
	return snapshot, nil
}

// CleanupWorld calls Bananagine's idempotent scoped world deletion endpoint.
func (s *V2WorldSource) CleanupWorld(ctx context.Context, scope Scope, request V2WorldCleanupRequest) error {
	if err := validateScope(scope); err != nil {
		return err
	}
	if err := validateCleanupRequest(request); err != nil {
		return err
	}
	operationCtx, cancel := context.WithTimeout(ctx, request.Timeout)
	defer cancel()

	access, baseURL, err := s.resolveAccess(operationCtx, scope, request.NodeID)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(
		operationCtx,
		http.MethodDelete,
		joinEndpointPath(baseURL, "/orchestration/worlds/", request.CleanupWorld),
		nil,
	)
	if err != nil {
		return fmt.Errorf("bananagine cleanup world: %w", err)
	}
	setPrivilegedHeaders(req, access.ServiceToken, "")
	response, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("bananagine cleanup world: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		return readStatusError("cleanup world", response)
	}
	_, _ = io.Copy(io.Discard, response.Body)
	return nil
}

func (s *V2WorldSource) fetchWorld(
	ctx context.Context,
	scope Scope,
	baseURL *url.URL,
	serviceToken, serverID string,
) (V2WorldSnapshot, error) {
	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodGet,
		joinEndpointPath(baseURL, "/orchestration/worlds/", serverID),
		nil,
	)
	if err != nil {
		return V2WorldSnapshot{}, fmt.Errorf("bananagine fetch world: %w", err)
	}
	setPrivilegedHeaders(req, serviceToken, "")
	response, err := s.client.Do(req)
	if err != nil {
		return V2WorldSnapshot{}, fmt.Errorf("bananagine fetch world: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return V2WorldSnapshot{}, readStatusError("fetch world", response)
	}
	contentType, _, parseErr := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if parseErr != nil || contentType != "application/zip" {
		return V2WorldSnapshot{}, errors.New("bananagine fetch world: response is not application/zip")
	}

	prefix := stagingPrefix(scope.RoutingID())
	file, err := os.CreateTemp(s.stagingDir, prefix)
	if err != nil {
		return V2WorldSnapshot{}, fmt.Errorf("bananagine fetch world: create staging file: %w", err)
	}
	path := file.Name()
	removeStaging := true
	defer func() {
		if removeStaging {
			_ = file.Close()
			_ = os.Remove(path)
		}
	}()

	size, err := io.Copy(file, response.Body)
	if err != nil {
		return V2WorldSnapshot{}, fmt.Errorf("bananagine fetch world: stream response: %w", err)
	}
	if response.ContentLength >= 0 && size != response.ContentLength {
		return V2WorldSnapshot{}, fmt.Errorf(
			"bananagine fetch world: content length mismatch: received %d, expected %d",
			size,
			response.ContentLength,
		)
	}
	if size <= 0 {
		return V2WorldSnapshot{}, errors.New("bananagine fetch world: archive is empty")
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return V2WorldSnapshot{}, fmt.Errorf("bananagine fetch world: rewind staging file: %w", err)
	}
	removeStaging = false
	return V2WorldSnapshot{
		Body: &removeOnCloseFile{File: file, path: path},
		Size: size,
	}, nil
}

func (s *V2WorldSource) execRCON(
	ctx context.Context,
	baseURL *url.URL,
	serviceToken, containerID, command string,
) error {
	body, err := json.Marshal(struct {
		Cmd []string `json:"cmd"`
	}{Cmd: []string{"rcon", command}})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		joinEndpointPath(baseURL, "/orchestration/servers/", containerID, "/exec"),
		bytes.NewReader(body),
	)
	if err != nil {
		return err
	}
	setPrivilegedHeaders(req, serviceToken, "application/json")
	response, err := s.client.Do(req)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return readStatusError("execute save command", response)
	}
	_, _ = io.Copy(io.Discard, response.Body)
	return nil
}

func (s *V2WorldSource) resolveAccess(
	ctx context.Context,
	scope Scope,
	nodeID string,
) (ScopedAccess, *url.URL, error) {
	access, err := s.access.ResolveBananagine(ctx, scope, nodeID)
	if err != nil {
		return ScopedAccess{}, nil, fmt.Errorf("bananagine archive source: resolve scoped access: %w", err)
	}
	if strings.TrimSpace(access.ServiceToken) == "" {
		return ScopedAccess{}, nil, errors.New("bananagine archive source: scoped service credential is required")
	}
	baseURL, err := url.Parse(strings.TrimSpace(access.BaseURL))
	if err != nil {
		return ScopedAccess{}, nil, errors.New("bananagine archive source: scoped endpoint is invalid")
	}
	if (baseURL.Scheme != "http" && baseURL.Scheme != "https") ||
		baseURL.Host == "" ||
		baseURL.User != nil ||
		baseURL.RawQuery != "" ||
		baseURL.Fragment != "" {
		return ScopedAccess{}, nil, errors.New("bananagine archive source: scoped endpoint must be an HTTP(S) origin")
	}
	return access, baseURL, nil
}

func validateScope(scope Scope) error {
	if scope == nil {
		return errors.New("bananagine archive source: scope is required")
	}
	if err := scope.Validate(); err != nil {
		return fmt.Errorf("bananagine archive source: scope: %w", err)
	}
	if strings.TrimSpace(scope.RoutingID()) == "" {
		return errors.New("bananagine archive source: scope routing ID is required")
	}
	return nil
}

func validateSnapshotRequest(request V2WorldSnapshotRequest) error {
	if request.Kind != worldKind {
		return errors.New("bananagine archive source: invalid world source kind")
	}
	if err := validateSegment("server ID", request.ServerID); err != nil {
		return err
	}
	if request.NodeID != "" {
		if err := validateSegment("node ID", request.NodeID); err != nil {
			return err
		}
	}
	if request.Timeout <= 0 {
		return errors.New("bananagine archive source: snapshot timeout must be positive")
	}
	if request.FlushDelay < 0 || request.FlushDelay >= request.Timeout {
		return errors.New("bananagine archive source: invalid flush delay")
	}
	if request.FlushSaves {
		if err := validateSegment("container ID", request.ContainerID); err != nil {
			return err
		}
	} else if request.FlushDelay != 0 {
		return errors.New("bananagine archive source: flush delay requires save flushing")
	}
	return nil
}

func validateCleanupRequest(request V2WorldCleanupRequest) error {
	if err := validateSegment("cleanup world", request.CleanupWorld); err != nil {
		return err
	}
	if request.NodeID != "" {
		if err := validateSegment("node ID", request.NodeID); err != nil {
			return err
		}
	}
	if request.Timeout <= 0 {
		return errors.New("bananagine archive source: cleanup timeout must be positive")
	}
	return nil
}

func validateSegment(name, value string) error {
	if value == "" ||
		len(value) > 256 ||
		strings.TrimSpace(value) != value ||
		strings.Contains(value, "..") ||
		strings.ContainsAny(value, `/\`) {
		return fmt.Errorf("bananagine archive source: invalid %s", name)
	}
	for _, char := range value {
		if unicode.IsControl(char) {
			return fmt.Errorf("bananagine archive source: invalid %s", name)
		}
	}
	return nil
}

func setPrivilegedHeaders(request *http.Request, serviceToken, contentType string) {
	request.Header.Set(serviceTokenHeader, serviceToken)
	if contentType != "" {
		request.Header.Set("Content-Type", contentType)
	}
}

func joinEndpointPath(baseURL *url.URL, parts ...string) string {
	joined := strings.TrimRight(baseURL.Path, "/")
	for _, part := range parts {
		if strings.HasPrefix(part, "/") {
			joined += part
		} else {
			joined += url.PathEscape(part)
		}
	}
	clone := *baseURL
	clone.Path = joined
	clone.RawPath = ""
	return clone.String()
}

func readStatusError(operation string, response *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(response.Body, maxErrorBodyBytes+1))
	if len(body) > maxErrorBodyBytes {
		body = append(body[:maxErrorBodyBytes], []byte("...")...)
	}
	return &HTTPStatusError{
		Operation:  operation,
		StatusCode: response.StatusCode,
		Body:       strings.TrimSpace(string(body)),
	}
}

func waitContext(ctx context.Context, delay time.Duration) error {
	if delay == 0 {
		return nil
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func stagingPrefix(routingID string) string {
	sum := sha256.Sum256([]byte(routingID))
	return "bananagine-world-" + hex.EncodeToString(sum[:6]) + "-*.zip"
}

type removeOnCloseFile struct {
	*os.File
	path string
	once sync.Once
	err  error
}

func (f *removeOnCloseFile) Close() error {
	f.once.Do(func() {
		f.err = errors.Join(f.File.Close(), os.Remove(f.path))
	})
	return f.err
}
