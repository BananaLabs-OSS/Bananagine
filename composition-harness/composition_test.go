package compositionharness

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func TestProductionApplicationBundleFacadeLifecycle(t *testing.T) {
	repoRoot, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	devRoot := filepath.Dir(repoRoot)
	bundleRoot, storageRoot, hostExe := stageProductionApplicationBundle(t, repoRoot, devRoot)
	runtime := newFakeDockerRuntime(t)

	var output lockedBuffer
	firstPort := freePort(t)
	stop := startPulpProcess(t, hostExe, bundleRoot, &output, productionBundleEnvironment(firstPort, storageRoot, runtime.URL),
		"-app", filepath.Join(bundleRoot, "Bananagine", "composition", "pulp.app.toml"),
		"-storage-root", storageRoot)
	firstURL := fmt.Sprintf("http://127.0.0.1:%d", firstPort)
	waitForHTTP(t, firstURL+"/health", stop, &output)

	status, body := requestJSON(t, http.MethodGet, firstURL+"/templates", nil)
	if status != http.StatusOK || !bytes.Contains(body, []byte(`"name":"example-minecraft"`)) {
		t.Fatalf("production template facade = %d %s\nhost:\n%s", status, body, output.String())
	}

	create := map[string]any{"template": "example-minecraft", "server_id": "production-e2e-server"}
	status, body = requestAuthorizedJSON(t, http.MethodPost, firstURL+"/orchestration/servers", create)
	if status != http.StatusCreated {
		t.Fatalf("production create = %d %s\nhost:\n%s", status, body, output.String())
	}
	if !bytes.Contains(body, []byte(`"name":"production-e2e-server"`)) {
		t.Fatalf("production create response = %s", body)
	}
	var created struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(body, &created); err != nil || created.ID == "" {
		t.Fatalf("decode canonical created server: %v body=%s", err, body)
	}

	// The public retry keeps the exact old response shape but must not create a
	// second privileged runtime object.
	status, body = requestAuthorizedJSON(t, http.MethodPost, firstURL+"/orchestration/servers", create)
	if status != http.StatusCreated {
		t.Fatalf("production create replay = %d %s\nfake docker:\n%s\nhost:\n%s", status, body, runtime.Requests(), output.String())
	}
	if got := runtime.CreateCount(); got != 1 {
		t.Fatalf("privileged create count = %d, want 1 after replay", got)
	}

	status, body = requestAuthorizedJSON(t, http.MethodGet, firstURL+"/orchestration/servers", nil)
	if status != http.StatusOK || !bytes.Contains(body, []byte(`"id":"`+created.ID+`"`)) {
		t.Fatalf("production list = %d %s\nhost:\n%s", status, body, output.String())
	}
	restartBody := map[string]any{"server_id": "production-e2e-server", "node_id": "game-node-1"}
	status, body = requestFleetJSON(t, http.MethodPost, firstURL+"/orchestration/servers/"+created.ID+"/restart", restartBody, "restart-stable", "restart-effect")
	if status != http.StatusOK || strings.TrimSpace(string(body)) != `{"container_id":"`+created.ID+`","id":"`+created.ID+`","node_id":"game-node-1","server_id":"production-e2e-server","status":"restarted"}` {
		t.Fatalf("production restart = %d %s\nhost:\n%s", status, body, output.String())
	}
	if got := runtime.RestartCount(); got != 1 {
		t.Fatalf("privileged restart count = %d, want 1", got)
	}
	status, body = requestAuthorizedJSON(t, http.MethodPost, firstURL+"/orchestration/servers/"+created.ID+"/restart", restartBody)
	if status != http.StatusBadRequest || runtime.RestartCount() != 1 {
		t.Fatalf("headerless restart = %d %s restarts=%d, want 400 without work", status, body, runtime.RestartCount())
	}
	status, body = requestFleetJSON(t, http.MethodPost, firstURL+"/orchestration/servers/"+created.ID+"/restart", map[string]any{"server_id": "production-e2e-server", "node_id": "game-node-1", "injected": true}, "restart-invalid", "restart-invalid-effect")
	if status != http.StatusBadRequest || runtime.RestartCount() != 1 {
		t.Fatalf("restart unknown field = %d %s restarts=%d, want 400 without work", status, body, runtime.RestartCount())
	}
	status, body = requestFleetJSON(t, http.MethodPost, firstURL+"/orchestration/servers/"+created.ID+"/restart", map[string]any{"server_id": "foreign-server", "node_id": "game-node-1"}, "restart-cross-scope", "restart-cross-scope-effect")
	if status != http.StatusNotFound || runtime.RestartCount() != 1 {
		t.Fatalf("restart cross-scope identity = %d %s restarts=%d, want 404 without work", status, body, runtime.RestartCount())
	}

	reconfigureBody := map[string]any{
		"server_id": "production-e2e-server", "node_id": "game-node-1",
		"resources": map[string]any{"cpu_limit": 0, "memory_limit": 0},
		"env": map[string]string{
			"FLEET_SETTING_difficulty":     "hard",
			"FLEET_GAMERULE_keepInventory": "true",
		},
	}
	status, body = requestFleetJSON(t, http.MethodPost, firstURL+"/orchestration/servers/"+created.ID+"/reconfigure", reconfigureBody, "reconfigure-stable", "reconfigure-effect")
	if status != http.StatusOK || !bytes.Contains(body, []byte(`"status":"reconfigured"`)) {
		t.Fatalf("production reconfigure = %d %s\nfake docker:\n%s\nhost:\n%s", status, body, runtime.Requests(), output.String())
	}
	if got := runtime.ExecCount(); got != 2 {
		t.Fatalf("privileged reconfigure exec count = %d, want 2", got)
	}
	execV2Body := map[string]any{"server_id": "production-e2e-server", "node_id": "game-node-1", "cmd": []string{"rcon", "save-all flush"}}
	status, body = requestAuthorizedJSON(t, http.MethodPost, firstURL+"/orchestration/servers/"+created.ID+"/exec", execV2Body)
	if status != http.StatusBadRequest || runtime.ExecCount() != 2 {
		t.Fatalf("exec v1 accepted v2 body = %d %s execs=%d", status, body, runtime.ExecCount())
	}
	status, body = requestFleetJSON(t, http.MethodPost, firstURL+"/orchestration/servers/"+created.ID+"/exec", map[string]any{"cmd": []string{"rcon", "save-all flush"}}, "exec-v2-stable", "exec-v2-effect")
	if status != http.StatusBadRequest || runtime.ExecCount() != 2 {
		t.Fatalf("exec v1 accepted v2 headers = %d %s execs=%d", status, body, runtime.ExecCount())
	}
	status, body = requestFleetJSON(t, http.MethodPost, firstURL+"/orchestration/servers/"+created.ID+"/exec-v2", execV2Body, "exec-v2-stable", "exec-v2-effect")
	if status != http.StatusOK || strings.TrimSpace(string(body)) != `{"output":"\n"}` || runtime.ExecCount() != 3 {
		t.Fatalf("exec v2 = %d %s execs=%d", status, body, runtime.ExecCount())
	}
	status, body = requestFleetJSON(t, http.MethodPost, firstURL+"/orchestration/servers/"+created.ID+"/exec-v2", map[string]any{"server_id": "production-e2e-server", "node_id": "game-node-1", "cmd": []string{"rcon", "stop"}}, "exec-v2-stable", "exec-v2-effect")
	if status != http.StatusConflict || runtime.ExecCount() != 3 {
		t.Fatalf("exec v2 changed replay = %d %s execs=%d", status, body, runtime.ExecCount())
	}

	// Restart the actual Pulp app with the same staged manifest, WASM bytes,
	// scoped storage, and fake privileged host. The facade must rehydrate its
	// template/owner composition and discover the already-created runtime.
	stop()
	secondPort := freePort(t)
	restartStop := startPulpProcess(t, hostExe, bundleRoot, &output, productionBundleEnvironment(secondPort, storageRoot, runtime.URL),
		"-app", filepath.Join(bundleRoot, "Bananagine", "composition", "pulp.app.toml"),
		"-storage-root", storageRoot)
	secondURL := fmt.Sprintf("http://127.0.0.1:%d", secondPort)
	waitForHTTP(t, secondURL+"/health", restartStop, &output)
	status, body = requestAuthorizedJSON(t, http.MethodGet, secondURL+"/orchestration/servers", nil)
	if status != http.StatusOK || !bytes.Contains(body, []byte(`"id":"`+created.ID+`"`)) {
		t.Fatalf("production list after Pulp restart = %d %s\nhost:\n%s", status, body, output.String())
	}
	status, body = requestFleetJSON(t, http.MethodPost, secondURL+"/orchestration/servers/"+created.ID+"/restart", restartBody, "restart-stable", "restart-effect")
	if status != http.StatusOK || runtime.RestartCount() != 1 || strings.TrimSpace(string(body)) != `{"container_id":"`+created.ID+`","id":"`+created.ID+`","node_id":"game-node-1","server_id":"production-e2e-server","status":"restarted"}` {
		t.Fatalf("restart replay after Pulp restart = %d %s restarts=%d\nhost:\n%s", status, body, runtime.RestartCount(), output.String())
	}
	status, body = requestFleetJSON(t, http.MethodPost, secondURL+"/orchestration/servers/"+created.ID+"/reconfigure", reconfigureBody, "reconfigure-stable", "reconfigure-effect")
	if status != http.StatusOK || !bytes.Contains(body, []byte(`"status":"reconfigured"`)) || runtime.ExecCount() != 3 {
		t.Fatalf("reconfigure replay after Pulp restart = %d %s execs=%d\nhost:\n%s", status, body, runtime.ExecCount(), output.String())
	}
	status, body = requestFleetJSON(t, http.MethodPost, secondURL+"/orchestration/servers/"+created.ID+"/exec-v2", execV2Body, "exec-v2-stable", "exec-v2-effect")
	if status != http.StatusOK || strings.TrimSpace(string(body)) != `{"output":"\n"}` || runtime.ExecCount() != 3 {
		t.Fatalf("exec v2 replay after Pulp restart = %d %s execs=%d", status, body, runtime.ExecCount())
	}

	lifecycleBody := map[string]any{
		"server_id": "production-e2e-server", "node_id": "game-node-1",
		"resources": map[string]any{"cpu_limit": 0, "memory_limit": 0},
		"env":       map[string]string{},
	}
	status, body = requestFleetJSON(t, http.MethodPost, secondURL+"/orchestration/servers/"+created.ID+"/suspend", lifecycleBody, "suspend-stable", "suspend-effect")
	if status != http.StatusOK || !bytes.Contains(body, []byte(`"status":"suspended"`)) {
		t.Fatalf("production suspend = %d %s\nhost:\n%s", status, body, output.String())
	}
	suspendedExecs := runtime.ExecCount()
	status, _ = requestFleetJSON(t, http.MethodPost, secondURL+"/orchestration/servers/"+created.ID+"/suspend", lifecycleBody, "suspend-stable", "suspend-effect")
	if status != http.StatusOK || runtime.ExecCount() != suspendedExecs {
		t.Fatalf("suspend replay repeated privileged work: status=%d execs=%d", status, runtime.ExecCount())
	}

	status, body = requestFleetJSON(t, http.MethodPost, secondURL+"/orchestration/servers/"+created.ID+"/resume", lifecycleBody, "resume-stable", "resume-effect")
	if status != http.StatusOK || !bytes.Contains(body, []byte(`"status":"resumed"`)) {
		t.Fatalf("production resume = %d %s\nhost:\n%s", status, body, output.String())
	}
	resumeRestarts := runtime.RestartCount()
	status, _ = requestFleetJSON(t, http.MethodPost, secondURL+"/orchestration/servers/"+created.ID+"/resume", lifecycleBody, "resume-stable", "resume-effect")
	if status != http.StatusOK || runtime.RestartCount() != resumeRestarts {
		t.Fatalf("resume replay repeated privileged work: status=%d restarts=%d", status, runtime.RestartCount())
	}

	status, body = requestFleetJSON(t, http.MethodPost, secondURL+"/orchestration/servers/"+created.ID+"/regenerate", restartBody, "regenerate-stable", "regenerate-effect")
	if status != http.StatusOK || !bytes.Contains(body, []byte(`"status":"regenerated"`)) {
		t.Fatalf("production regenerate = %d %s\nhost:\n%s", status, body, output.String())
	}
	regenerateExecs, regenerateRestarts := runtime.ExecCount(), runtime.RestartCount()
	status, _ = requestFleetJSON(t, http.MethodPost, secondURL+"/orchestration/servers/"+created.ID+"/regenerate", restartBody, "regenerate-stable", "regenerate-effect")
	if status != http.StatusOK || runtime.ExecCount() != regenerateExecs || runtime.RestartCount() != regenerateRestarts {
		t.Fatalf("regenerate replay repeated privileged work: status=%d execs=%d restarts=%d", status, runtime.ExecCount(), runtime.RestartCount())
	}

	status, body = requestJSON(t, http.MethodGet, secondURL+"/orchestration/servers/"+created.ID+"/status", nil)
	if status != http.StatusUnauthorized {
		t.Fatalf("unauthenticated runtime status = %d %s, want 401", status, body)
	}
	status, body = requestJSON(t, http.MethodGet, secondURL+"/orchestration/servers/"+created.ID+"/player-history", nil)
	if status != http.StatusUnauthorized {
		t.Fatalf("unauthenticated runtime player history = %d %s, want 401", status, body)
	}
	status, body = requestAuthorizedJSON(t, http.MethodGet, secondURL+"/orchestration/servers/"+created.ID+"/status", nil)
	if status != http.StatusOK || strings.TrimSpace(string(body)) != `{"status":"running"}` {
		t.Fatalf("runtime status = %d %s", status, body)
	}
	status, body = requestAuthorizedJSON(t, http.MethodGet, secondURL+"/orchestration/servers/"+created.ID+"/settings", nil)
	if status != http.StatusOK || strings.TrimSpace(string(body)) != `{"settings":{"difficulty":"hard","gamemode":"survival","motd":"Production Test","white_list":"true"}}` {
		t.Fatalf("runtime settings = %d %s", status, body)
	}
	status, body = requestAuthorizedJSON(t, http.MethodGet, secondURL+"/orchestration/servers/"+created.ID+"/gamerules", nil)
	if status != http.StatusOK || strings.TrimSpace(string(body)) != `{"gamerules":{"doInsomnia":"false","keepInventory":"true","spawnRadius":"10"}}` {
		t.Fatalf("runtime gamerules = %d %s", status, body)
	}
	status, body = requestAuthorizedJSON(t, http.MethodGet, secondURL+"/orchestration/servers/"+created.ID+"/players", nil)
	if status != http.StatusOK || strings.TrimSpace(string(body)) != `{"players":["Alex_2","Steve"]}` {
		t.Fatalf("runtime players = %d %s", status, body)
	}
	status, body = requestAuthorizedJSON(t, http.MethodGet, secondURL+"/orchestration/servers/"+created.ID+"/player-history", nil)
	if status != http.StatusOK || strings.TrimSpace(string(body)) != `{"player_history":[{"uuid":"123e4567-e89b-12d3-a456-426614174000","name":"Steve","expires_on":"2026-08-01 00:00:00 +0000"}]}` {
		t.Fatalf("runtime player history = %d %s", status, body)
	}
	status, body = requestAuthorizedJSON(t, http.MethodGet, secondURL+"/orchestration/servers/"+created.ID+"/access", nil)
	if status != http.StatusOK || strings.TrimSpace(string(body)) != `{"access":{"whitelist_enabled":true,"whitelist":[{"uuid":"123e4567-e89b-12d3-a456-426614174000","name":"Steve"}],"ops":[{"uuid":"223e4567-e89b-12d3-a456-426614174000","name":"Alex_2"}],"bans":[{"uuid":"323e4567-e89b-12d3-a456-426614174000","name":"Griefer"}]}}` {
		t.Fatalf("runtime access = %d %s", status, body)
	}
	status, body = requestAuthorizedJSON(t, http.MethodGet, secondURL+"/orchestration/servers/"+created.ID+"/access-snapshot", nil)
	if status != http.StatusOK || strings.TrimSpace(string(body)) != `{"access_snapshot":{"whitelist":[{"uuid":"123e4567-e89b-12d3-a456-426614174000","name":"Steve"}],"ops":[{"uuid":"223e4567-e89b-12d3-a456-426614174000","name":"Alex_2","level":4,"bypasses_player_limit":true}],"bans":[{"uuid":"323e4567-e89b-12d3-a456-426614174000","name":"Griefer","created":"2026-07-26 00:00:00 +0000","source":"Console","expires":"forever","reason":"x-ray"}]}}` {
		t.Fatalf("runtime access snapshot = %d %s", status, body)
	}
	status, body = requestAuthorizedJSON(t, http.MethodGet, secondURL+"/orchestration/servers/"+created.ID+"/artifacts", nil)
	if status != http.StatusOK || strings.TrimSpace(string(body)) != `{"artifacts":{"datapacks":["adventure.zip","quality.ZIP"],"mods":["fabric-api.jar","voicechat.jar"]}}` {
		t.Fatalf("runtime artifacts = %d %s", status, body)
	}

	observationExecs := runtime.ExecCount()
	runtime.AddForeignContainer("foreign-container", "pulp-foreign-app-default-foreign-cell-primary-server")
	status, body = requestAuthorizedJSON(t, http.MethodGet, secondURL+"/orchestration/servers/foreign-container/status", nil)
	if status != http.StatusNotFound || runtime.ExecCount() != observationExecs {
		t.Fatalf("cross-scope observation = %d %s execs=%d, want 404 without exec", status, body, runtime.ExecCount())
	}
	status, body = requestAuthorizedJSON(t, http.MethodGet, secondURL+"/orchestration/servers/"+created.ID+"../status", nil)
	if status != http.StatusBadRequest || runtime.ExecCount() != observationExecs {
		t.Fatalf("injected observation identity = %d %s execs=%d, want 400 without exec", status, body, runtime.ExecCount())
	}
	runtime.SetOversizeExecContains("server.properties")
	status, body = requestAuthorizedJSON(t, http.MethodGet, secondURL+"/orchestration/servers/"+created.ID+"/settings", nil)
	runtime.SetOversizeExecContains("")
	if status != http.StatusBadGateway {
		t.Fatalf("oversized observation = %d %s, want 502", status, body)
	}

	status, body = requestAuthorizedJSON(t, http.MethodDelete, secondURL+"/orchestration/servers/"+created.ID, nil)
	if status != http.StatusNoContent || len(body) != 0 {
		t.Fatalf("production destroy = %d %s\nfake docker:\n%s\nhost:\n%s", status, body, runtime.Requests(), output.String())
	}
	status, body = requestAuthorizedJSON(t, http.MethodDelete, secondURL+"/orchestration/servers/"+created.ID, nil)
	if status != http.StatusNoContent || len(body) != 0 {
		t.Fatalf("production destroy replay = %d %s\nhost:\n%s", status, body, output.String())
	}
	if got := runtime.DestroyCount(); got != 1 {
		t.Fatalf("privileged destroy count = %d, want 1 after replay", got)
	}
}

func stageProductionApplicationBundle(t *testing.T, repoRoot, devRoot string) (bundleRoot, storageRoot, hostExe string) {
	t.Helper()
	temp := t.TempDir()
	bundleRoot = filepath.Join(temp, "application")
	storageRoot = filepath.Join(temp, "storage")
	goCache := filepath.Join(temp, "gocache")
	for _, dir := range []string{
		filepath.Join(bundleRoot, "Bananagine", "composition"),
		filepath.Join(bundleRoot, "Bananagine", "registry-cell"),
		filepath.Join(bundleRoot, "Bananagine", "template-catalog-cell"),
		filepath.Join(bundleRoot, "Bananagine", "state-cell"),
		filepath.Join(bundleRoot, "Bananagine", "worker-cell"),
		filepath.Join(bundleRoot, "Bananagine", "pulp-cell"),
		filepath.Join(bundleRoot, "Pulp-Lua", "pulp-cell"),
		filepath.Join(bundleRoot, "pulp-engines", "workload-inventory-sqlite-cell"),
		filepath.Join(bundleRoot, "pulp-engines", "capacity-scheduler-sqlite-cell"),
		filepath.Join(bundleRoot, "pulp-engines", "workload-provisioning-sqlite-cell"),
		filepath.Join(bundleRoot, "pulp-engines", "runtime-control-sqlite-cell"),
		filepath.Join(storageRoot, "apps", "bananagine", "default", "cells", "bananagine", "primary", "templates"),
	} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	copyBundleFile(t, filepath.Join(repoRoot, "composition", "pulp.app.toml"), filepath.Join(bundleRoot, "Bananagine", "composition", "pulp.app.toml"))
	copyBundleFile(t, filepath.Join(repoRoot, "composition", "bananagine.lua"), filepath.Join(bundleRoot, "Bananagine", "composition", "bananagine.lua"))
	copyBundleFile(t, filepath.Join(repoRoot, "composition", "lua-orchestrator.pulp.cell.toml"), filepath.Join(bundleRoot, "Bananagine", "composition", "lua-orchestrator.pulp.cell.toml"))
	copyBundleFile(t, filepath.Join(repoRoot, "registry-cell", "pulp.cell.toml"), filepath.Join(bundleRoot, "Bananagine", "registry-cell", "pulp.cell.toml"))
	copyBundleFile(t, filepath.Join(repoRoot, "template-catalog-cell", "pulp.cell.toml"), filepath.Join(bundleRoot, "Bananagine", "template-catalog-cell", "pulp.cell.toml"))
	copyBundleFile(t, filepath.Join(repoRoot, "state-cell", "pulp.cell.toml"), filepath.Join(bundleRoot, "Bananagine", "state-cell", "pulp.cell.toml"))
	copyBundleFile(t, filepath.Join(repoRoot, "worker-cell", "pulp.cell.toml"), filepath.Join(bundleRoot, "Bananagine", "worker-cell", "pulp.cell.toml"))
	facadeManifest := filepath.Join(bundleRoot, "Bananagine", "pulp-cell", "pulp.cell.toml")
	copyBundleFile(t, filepath.Join(repoRoot, "pulp-cell", "pulp.cell.toml"), facadeManifest)
	expandProductionFacadeManifest(t, facadeManifest)
	copyBundleFile(t, filepath.Join(repoRoot, "templates", "example-minecraft.yaml"), filepath.Join(storageRoot, "apps", "bananagine", "default", "cells", "bananagine", "primary", "templates", "example-minecraft.yaml"))

	build(t, filepath.Join(repoRoot, "state-cell"), filepath.Join(bundleRoot, "Bananagine", "state-cell", "runtime-catalog-state.wasm"), goCache, true)
	build(t, filepath.Join(repoRoot, "worker-cell"), filepath.Join(bundleRoot, "Bananagine", "worker-cell", "async-http-job.wasm"), goCache, true)
	build(t, filepath.Join(repoRoot, "pulp-cell"), filepath.Join(bundleRoot, "Bananagine", "pulp-cell", "bananagine.wasm"), goCache, true)
	build(t, filepath.Join(devRoot, "Pulp-Lua", "pulp-cell"), filepath.Join(bundleRoot, "Pulp-Lua", "pulp-cell", "lua-orchestrator.wasm"), goCache, true)
	for _, engine := range []string{"workload-inventory", "capacity-scheduler", "workload-provisioning", "runtime-control"} {
		source := filepath.Join(devRoot, "pulp-engines", engine+"-sqlite-cell")
		destination := filepath.Join(bundleRoot, "pulp-engines", engine+"-sqlite-cell")
		copyBundleFile(t, filepath.Join(source, "pulp.cell.toml"), filepath.Join(destination, "pulp.cell.toml"))
		build(t, filepath.Join(source, "cmd", engine), filepath.Join(destination, engine+".wasm"), goCache, true)
	}
	hostExe = build(t, filepath.Join(repoRoot, "pulp-deployment"), filepath.Join(temp, "pulp-host.exe"), goCache, false)
	return bundleRoot, storageRoot, hostExe
}

func copyBundleFile(t *testing.T, source, destination string) {
	t.Helper()
	contents, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(destination, contents, 0o600); err != nil {
		t.Fatal(err)
	}
}

// The image startup command expands the facade's operator-owned configuration
// before it starts the otherwise byte-identical application bundle. Keep that
// one production transform in the E2E so TOML sees concrete scalar values.
func expandProductionFacadeManifest(t *testing.T, path string) {
	t.Helper()
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	expanded := strings.NewReplacer(
		"${EXTERNAL_HOST}", "127.0.0.1",
		"${SERVICE_TOKEN}", "production-test-token",
		"${CPU_BUDGET}", "0",
		"${MEMORY_BUDGET}", "0",
	).Replace(string(contents))
	if strings.Contains(expanded, "${") {
		t.Fatalf("production facade expansion left an unresolved placeholder in %s", path)
	}
	if err := os.WriteFile(path, []byte(expanded), 0o600); err != nil {
		t.Fatal(err)
	}
}

func productionBundleEnvironment(port int, storageRoot, dockerURL string) []string {
	return []string{
		"HTTP_PORT=" + strconv.Itoa(port),
		"HTTP_FETCH_ALLOW=127.0.0.0/8,::1/128",
		"PULP_WAZERO_CACHE=" + filepath.Join(storageRoot, "wazero"),
		"SERVICE_TOKEN=production-test-token",
		"EXTERNAL_HOST=127.0.0.1",
		"CPU_BUDGET=0",
		"MEMORY_BUDGET=0",
		"DOCKER_HOST=tcp://" + strings.TrimPrefix(dockerURL, "http://"),
	}
}

func requestAuthorizedJSON(t *testing.T, method, url string, payload any) (int, []byte) {
	t.Helper()
	var body io.Reader
	if payload != nil {
		data, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		body = bytes.NewReader(data)
	}
	request, err := http.NewRequest(method, url, body)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("X-Service-Token", "production-test-token")
	if payload != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := (&http.Client{Timeout: 5 * time.Second}).Do(request)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	return response.StatusCode, data
}

func requestFleetJSON(t *testing.T, method, url string, payload any, idempotencyKey, effectID string) (int, []byte) {
	t.Helper()
	var body io.Reader
	if payload != nil {
		data, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		body = bytes.NewReader(data)
	}
	request, err := http.NewRequest(method, url, body)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("X-Service-Token", "production-test-token")
	request.Header.Set("Idempotency-Key", idempotencyKey)
	request.Header.Set("X-Pulp-Effect-ID", effectID)
	if payload != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := (&http.Client{Timeout: 5 * time.Second}).Do(request)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	return response.StatusCode, data
}

type fakeDockerRuntime struct {
	mu           sync.Mutex
	containers   map[string]fakeDockerContainer
	creates      int
	restarts     int
	destroys     int
	execs        int
	URL          string
	requests     []string
	execCommands map[string][]string
	oversizeExec string
}

type fakeDockerContainer struct {
	id   string
	name string
}

func newFakeDockerRuntime(t *testing.T) *fakeDockerRuntime {
	t.Helper()
	runtime := &fakeDockerRuntime{containers: make(map[string]fakeDockerContainer), execCommands: make(map[string][]string)}
	server := httptest.NewServer(http.HandlerFunc(runtime.serveHTTP))
	runtime.URL = server.URL
	t.Cleanup(server.Close)
	return runtime
}

func (r *fakeDockerRuntime) CreateCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.creates
}

func (r *fakeDockerRuntime) RestartCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.restarts
}

func (r *fakeDockerRuntime) DestroyCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.destroys
}

func (r *fakeDockerRuntime) ExecCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.execs
}

func (r *fakeDockerRuntime) SetOversizeExecContains(value string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.oversizeExec = value
}

func (r *fakeDockerRuntime) AddForeignContainer(id, name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.containers[id] = fakeDockerContainer{id: id, name: name}
}

func (r *fakeDockerRuntime) Requests() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return strings.Join(r.requests, "\n")
}

func (r *fakeDockerRuntime) serveHTTP(w http.ResponseWriter, request *http.Request) {
	path := dockerAPIPath(request.URL.Path)
	if path == "/_ping" {
		w.Header().Set("Api-Version", "1.41")
		w.WriteHeader(http.StatusOK)
		return
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	r.requests = append(r.requests, request.Method+" "+path+"?"+request.URL.RawQuery)
	switch {
	case request.Method == http.MethodGet && path == "/containers/json":
		items := make([]map[string]any, 0, len(r.containers))
		for _, container := range r.containers {
			items = append(items, r.containerList(container))
		}
		writeFakeDockerJSON(w, http.StatusOK, items)
		return
	case request.Method == http.MethodPost && path == "/containers/create":
		name := request.URL.Query().Get("name")
		if name == "" {
			writeFakeDockerJSON(w, http.StatusBadRequest, map[string]string{"message": "name is required"})
			return
		}
		if _, found := r.find(name); found {
			writeFakeDockerJSON(w, http.StatusConflict, map[string]string{"message": "Conflict. The container name is already in use."})
			return
		}
		container := fakeDockerContainer{id: "fake-" + name, name: name}
		r.containers[container.id] = container
		r.creates++
		writeFakeDockerJSON(w, http.StatusCreated, map[string]any{"Id": container.id, "Warnings": nil})
		return
	case request.Method == http.MethodPost && strings.HasPrefix(path, "/containers/") && strings.HasSuffix(path, "/exec"):
		parts := strings.Split(strings.Trim(path, "/"), "/")
		if len(parts) != 3 {
			break
		}
		if _, found := r.find(parts[1]); !found {
			writeFakeDockerJSON(w, http.StatusNotFound, map[string]string{"message": "No such container: " + parts[1]})
			return
		}
		var payload struct {
			Cmd []string `json:"Cmd"`
		}
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil || len(payload.Cmd) == 0 {
			writeFakeDockerJSON(w, http.StatusBadRequest, map[string]string{"message": "exec command required"})
			return
		}
		r.execs++
		execID := fmt.Sprintf("fake-exec-%d", r.execs)
		r.execCommands[execID] = append([]string(nil), payload.Cmd...)
		writeFakeDockerJSON(w, http.StatusCreated, map[string]string{"Id": execID})
		return
	case request.Method == http.MethodPost && strings.HasPrefix(path, "/exec/") && strings.HasSuffix(path, "/start"):
		parts := strings.Split(strings.Trim(path, "/"), "/")
		if len(parts) != 3 || r.execCommands[parts[1]] == nil {
			writeFakeDockerJSON(w, http.StatusNotFound, map[string]string{"message": "No such exec"})
			return
		}
		// Drain the attach options before hijacking. On Windows, closing a TCP
		// connection with unread request bytes resets the connection instead of
		// delivering a clean EOF to Docker's multiplexed-stream reader.
		_, _ = io.Copy(io.Discard, request.Body)
		_ = request.Body.Close()
		r.writeExecAttach(w, r.execCommands[parts[1]])
		return
	case request.Method == http.MethodGet && strings.HasPrefix(path, "/exec/") && strings.HasSuffix(path, "/json"):
		parts := strings.Split(strings.Trim(path, "/"), "/")
		if len(parts) != 3 || r.execCommands[parts[1]] == nil {
			writeFakeDockerJSON(w, http.StatusNotFound, map[string]string{"message": "No such exec"})
			return
		}
		writeFakeDockerJSON(w, http.StatusOK, map[string]any{"ID": parts[1], "Running": false, "ExitCode": 0})
		return
	}

	key, action, recognized := fakeDockerContainerRoute(path)
	if !recognized {
		writeFakeDockerJSON(w, http.StatusNotFound, map[string]string{"message": "unsupported fake Docker endpoint"})
		return
	}
	container, found := r.find(key)
	if !found {
		writeFakeDockerJSON(w, http.StatusNotFound, map[string]string{"message": "No such container: " + key})
		return
	}
	switch {
	case request.Method == http.MethodGet && action == "inspect":
		writeFakeDockerJSON(w, http.StatusOK, r.containerInspect(container))
	case request.Method == http.MethodPost && action == "start":
		w.WriteHeader(http.StatusNoContent)
	case request.Method == http.MethodPost && action == "restart":
		r.restarts++
		w.WriteHeader(http.StatusNoContent)
	case request.Method == http.MethodPost && action == "stop":
		w.WriteHeader(http.StatusNoContent)
	case request.Method == http.MethodDelete && action == "destroy":
		delete(r.containers, container.id)
		r.destroys++
		w.WriteHeader(http.StatusNoContent)
	default:
		writeFakeDockerJSON(w, http.StatusNotFound, map[string]string{"message": "unsupported fake Docker operation"})
	}
}

func (r *fakeDockerRuntime) writeExecAttach(w http.ResponseWriter, command []string) {
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "hijacking unsupported", http.StatusInternalServerError)
		return
	}
	connection, buffer, err := hijacker.Hijack()
	if err != nil {
		return
	}
	_, _ = buffer.WriteString("HTTP/1.1 101 UPGRADED\r\nContent-Type: application/vnd.docker.raw-stream\r\nConnection: Upgrade\r\nUpgrade: tcp\r\n\r\n")
	output := r.execOutput(command)
	size := len(output)
	_, _ = buffer.Write([]byte{1, 0, 0, 0, byte(size >> 24), byte(size >> 16), byte(size >> 8), byte(size)})
	_, _ = buffer.WriteString(output)
	_ = buffer.Flush()
	_ = connection.Close()
}

func (r *fakeDockerRuntime) execOutput(command []string) string {
	joined := strings.Join(command, "\x00")
	if r.oversizeExec != "" && strings.Contains(joined, r.oversizeExec) {
		return strings.Repeat("x", (64<<10)+1)
	}
	if len(command) == 2 && command[0] == "rcon" && command[1] == "list" {
		return "There are 2 of a max of 20 players online: Steve, Alex_2\n"
	}
	if len(command) != 3 || command[0] != "sh" || command[1] != "-c" {
		return "\n"
	}
	switch command[2] {
	case "cat /data/server.properties 2>/dev/null || echo ''":
		return "difficulty=hard\ngamemode=survival\nmotd=Production Test\nwhite-list=true\nrcon.password=not-observable\n"
	case "(test -f /data/whitelist.json && cat /data/whitelist.json) || echo '[]'":
		return `[{"uuid":"123e4567-e89b-12d3-a456-426614174000","name":"Steve"}]`
	case "(test -f /data/ops.json && cat /data/ops.json) || echo '[]'":
		return `[{"uuid":"223e4567-e89b-12d3-a456-426614174000","name":"Alex_2","level":4,"bypassesPlayerLimit":true}]`
	case "(test -f /data/banned-players.json && cat /data/banned-players.json) || echo '[]'":
		return `[{"uuid":"323e4567-e89b-12d3-a456-426614174000","name":"Griefer","created":"2026-07-26 00:00:00 +0000","source":"Console","expires":"forever","reason":"x-ray"}]`
	case "(test -f /data/usercache.json && cat /data/usercache.json) || echo '[]'":
		return `[{"uuid":"123e4567-e89b-12d3-a456-426614174000","name":"Steve","expiresOn":"2026-08-01 00:00:00 +0000"}]`
	case "ls -1p /data/world/datapacks/ 2>/dev/null || echo ''":
		return "quality.ZIP\nadventure.zip\nREADME.txt\n"
	case "ls -1p /data/mods/ 2>/dev/null || echo ''":
		return "voicechat.jar\nfabric-api.jar\nconfig\n"
	}
	if strings.Contains(command[2], `echo "RULE:`) {
		return "RULE:keepInventory:Gamerule keepInventory is currently set to: true\nRULE:spawnRadius:10\nRULE:doInsomnia:Gamerule doInsomnia is currently set to: false\n"
	}
	return "\n"
}

func (r *fakeDockerRuntime) find(key string) (fakeDockerContainer, bool) {
	if container, found := r.containers[key]; found {
		return container, true
	}
	for _, container := range r.containers {
		if container.name == key {
			return container, true
		}
	}
	return fakeDockerContainer{}, false
}

func (r *fakeDockerRuntime) containerList(container fakeDockerContainer) map[string]any {
	return map[string]any{
		"Id":              container.id,
		"Names":           []string{"/" + container.name},
		"State":           "running",
		"Ports":           []map[string]any{{"PrivatePort": 25565, "PublicPort": 5521, "Type": "tcp"}},
		"NetworkSettings": map[string]any{"Networks": map[string]any{}},
	}
}

func (r *fakeDockerRuntime) containerInspect(container fakeDockerContainer) map[string]any {
	return map[string]any{
		"Id":    container.id,
		"Name":  "/" + container.name,
		"State": map[string]any{"Status": "running"},
		"NetworkSettings": map[string]any{
			"Networks": map[string]any{},
			"Ports":    map[string]any{"25565/tcp": []map[string]string{{"HostIp": "0.0.0.0", "HostPort": "5521"}}},
		},
		"HostConfig": map[string]any{"Memory": 0, "NanoCpus": 0},
	}
}

func dockerAPIPath(path string) string {
	trimmed := strings.TrimPrefix(path, "/")
	parts := strings.SplitN(trimmed, "/", 2)
	if len(parts) == 2 && strings.HasPrefix(parts[0], "v") {
		return "/" + parts[1]
	}
	return path
}

func fakeDockerContainerRoute(path string) (key, action string, recognized bool) {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) < 2 || parts[0] != "containers" {
		return "", "", false
	}
	key = parts[1]
	switch {
	case len(parts) == 3 && parts[2] == "json":
		return key, "inspect", true
	case len(parts) == 3 && (parts[2] == "start" || parts[2] == "restart" || parts[2] == "stop"):
		return key, parts[2], true
	case len(parts) == 2:
		return key, "destroy", true
	default:
		return "", "", false
	}
}

func writeFakeDockerJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func (b *lockedBuffer) Write(data []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.Write(data)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.String()
}

func TestPulpLuaComposesGamePlatformOwners(t *testing.T) {
	repoRoot, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	devRoot := filepath.Dir(repoRoot)
	temp := t.TempDir()
	goCache := filepath.Join(temp, "gocache")
	if err := os.MkdirAll(goCache, 0o755); err != nil {
		t.Fatal(err)
	}

	registryWASM := build(t, filepath.Join(repoRoot, "registry-cell"), filepath.Join(temp, "runtime-directory.wasm"), goCache, true)
	templateCatalogWASM := build(t, filepath.Join(repoRoot, "template-catalog-cell"), filepath.Join(temp, "template-catalog.wasm"), goCache, true)
	workerWASM := build(t, filepath.Join(repoRoot, "worker-cell"), filepath.Join(temp, "async-http-job.wasm"), goCache, true)
	luaWASM := build(t, filepath.Join(devRoot, "Pulp-Lua", "pulp-cell"), filepath.Join(temp, "bananagine-lua.wasm"), goCache, true)
	probeWASM := build(t, filepath.Join(repoRoot, "composition", "probe-cell"), filepath.Join(temp, "composition-probe.wasm"), goCache, true)
	hostExe := build(t, filepath.Join(repoRoot, "pulp-deployment"), filepath.Join(temp, "pulp-host.exe"), goCache, false)

	registryManifest := materializeManifest(
		t,
		filepath.Join(repoRoot, "registry-cell", "pulp.cell.toml"),
		filepath.Join(temp, "registry.toml"),
		registryWASM,
	)
	templateCatalogManifest := materializeManifest(
		t,
		filepath.Join(repoRoot, "template-catalog-cell", "pulp.cell.toml"),
		filepath.Join(temp, "template-catalog.toml"),
		templateCatalogWASM,
	)
	workerManifest := materializeManifest(
		t,
		filepath.Join(repoRoot, "worker-cell", "pulp.cell.toml"),
		filepath.Join(temp, "worker.toml"),
		workerWASM,
	)
	luaManifest := materializeManifest(
		t,
		filepath.Join(repoRoot, "composition", "lua-orchestrator.pulp.cell.toml"),
		filepath.Join(temp, "lua.toml"),
		luaWASM,
	)
	probeManifest := materializeManifest(
		t,
		filepath.Join(repoRoot, "composition", "probe-cell", "pulp.cell.toml"),
		filepath.Join(temp, "probe.toml"),
		probeWASM,
	)
	scriptSource := filepath.Join(repoRoot, "composition", "bananagine.lua")
	script, err := os.ReadFile(scriptSource)
	if err != nil {
		t.Fatal(err)
	}
	scriptPath := filepath.Join(temp, "bananagine.lua")
	if err := os.WriteFile(scriptPath, script, 0o600); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(script)
	appManifest := filepath.Join(temp, "pulp.app.toml")
	app := fmt.Sprintf(`schema_version = 1
name = "bananagine-composition-test"
version = "0.1.0"
cells = [
  %q,
  %q,
  %q,
  %q,
  %q,
]

[orchestrator]
manifest = %q
script = %q
sha256 = "%x"
`, filepath.Base(registryManifest), filepath.Base(templateCatalogManifest), filepath.Base(workerManifest), filepath.Base(luaManifest), filepath.Base(probeManifest),
		filepath.Base(luaManifest), filepath.Base(scriptPath), digest)
	if err := os.WriteFile(appManifest, []byte(app), 0o600); err != nil {
		t.Fatal(err)
	}

	var processOutput lockedBuffer
	port := freePort(t)
	stop := startPulpProcess(t, hostExe, temp, &processOutput, []string{
		"HTTP_PORT=" + strconv.Itoa(port),
		"HTTP_FETCH_ALLOW=127.0.0.0/8,::1/128",
		"PULP_WAZERO_CACHE=" + filepath.Join(temp, "wazero"),
	}, "-app", appManifest)

	baseURL := fmt.Sprintf("http://127.0.0.1:%d", port)
	waitForHTTP(t, baseURL+"/composition/health", stop, &processOutput)

	server := map[string]any{
		"id":         "game-1",
		"type":       "game",
		"mode":       "survival",
		"host":       "127.0.0.1",
		"port":       25565,
		"players":    1,
		"maxPlayers": 4,
		"matches": map[string]any{
			"match-1": map[string]any{
				"status":  "ready",
				"need":    2,
				"players": []string{"alice"},
			},
		},
		"Metadata": map[string]string{"region": "central"},
	}
	status, body := requestJSON(t, http.MethodPost, baseURL+"/composition/registry/servers", server)
	if status != http.StatusCreated {
		t.Fatalf("register status = %d body=%s\nhost:\n%s", status, body, processOutput.String())
	}
	var registered map[string]any
	if err := json.Unmarshal(body, &registered); err != nil {
		t.Fatalf("decode register response: %v body=%s", err, body)
	}
	if registered["id"] != "game-1" {
		t.Fatalf("registered = %#v", registered)
	}

	status, body = requestJSON(t, http.MethodGet, baseURL+"/composition/registry/servers?hasCapacity=true&hasReadyMatch=true", nil)
	if status != http.StatusOK {
		t.Fatalf("list status = %d body=%s\nhost:\n%s", status, body, processOutput.String())
	}
	var listed []map[string]any
	if err := json.Unmarshal(body, &listed); err != nil {
		t.Fatalf("decode list response: %v body=%s", err, body)
	}
	if len(listed) != 1 || listed[0]["id"] != "game-1" {
		t.Fatalf("listed = %#v", listed)
	}

	status, body = requestJSON(t, http.MethodGet, baseURL+"/composition/registry/servers/game-1", nil)
	if status != http.StatusOK {
		t.Fatalf("get status = %d body=%s", status, body)
	}
	var fetched map[string]any
	if err := json.Unmarshal(body, &fetched); err != nil {
		t.Fatalf("decode get response: %v body=%s", err, body)
	}
	if fetched["id"] != "game-1" {
		t.Fatalf("fetched = %#v", fetched)
	}

	status, body = requestJSON(t, http.MethodGet, baseURL+"/composition/registry/servers/missing", nil)
	if status != http.StatusNotFound || strings.TrimSpace(string(body)) != `{"error":"server not found"}` {
		t.Fatalf("missing get = status %d body=%s", status, body)
	}

	status, body = requestJSON(t, http.MethodPost, baseURL+"/composition/registry/servers", map[string]any{
		"type": "game",
	})
	if status != http.StatusBadRequest || strings.TrimSpace(string(body)) != `{"error":"Server ID required"}` {
		t.Fatalf("invalid register = status %d body=%s", status, body)
	}

	status, body = requestJSON(t, http.MethodPost, baseURL+"/composition/templates/replace", map[string]any{
		"request_id": "catalog-1",
		"entries": []map[string]any{{
			"name": "generic-game", "game": "generic", "label": "Generic Game",
			"cpu_limit": 1.5, "memory_limit": 2147483648,
			"config_json": map[string]any{"difficulty": "normal"},
		}},
	})
	if status != http.StatusOK {
		t.Fatalf("replace templates = %d %s\nhost:\n%s", status, body, processOutput.String())
	}
	status, body = requestJSON(t, http.MethodGet, baseURL+"/composition/templates", nil)
	if status != http.StatusOK || !bytes.Contains(body, []byte(`"name":"generic-game"`)) {
		t.Fatalf("list templates = %d %s", status, body)
	}
	status, body = requestJSON(t, http.MethodGet, baseURL+"/composition/templates/generic-game/config", nil)
	if status != http.StatusOK || strings.TrimSpace(string(body)) != `{"difficulty":"normal"}` {
		t.Fatalf("get template config = %d %s", status, body)
	}

	status, body = requestJSON(t, http.MethodPost, baseURL+"/composition/worker/submit", map[string]any{
		"idempotency_key": "health-probe-1",
		"method":          "GET",
		"url":             baseURL + "/composition/health",
	})
	if status != http.StatusAccepted {
		t.Fatalf("worker submit = %d %s\nhost:\n%s", status, body, processOutput.String())
	}
	waitForWorkerCompletion(t, baseURL+"/composition/worker/health-probe-1", &processOutput)

	// The Pulp host may instantiate this package more than once. Each instance
	// must own independent registry, catalog, and worker state while reusing
	// the same immutable WASM artifacts.
	stop()
	hostManifest := filepath.Join(temp, "pulp.host.toml")
	host := fmt.Sprintf(`schema_version = 1
name = "bananagine-composition-host-test"

[[applications]]
id = "bananagine-composition-test"
manifest = %q
instances = 2
aliases = ["node-a", "node-b"]
storage_namespace = "bananagine-composition-test"
event_namespace = "bananagine-composition-test"

[[routes]]
path = "/node-a"
application = "bananagine-composition-test"
instance = "node-a"

[[routes]]
path = "/node-b"
application = "bananagine-composition-test"
instance = "node-b"
`, filepath.Base(appManifest))
	if err := os.WriteFile(hostManifest, []byte(host), 0o600); err != nil {
		t.Fatal(err)
	}

	gatewayPort := freePort(t)
	hostStop := startPulpProcess(t, hostExe, temp, &processOutput, []string{
		"HTTP_FETCH_ALLOW=127.0.0.0/8,::1/128",
		"PULP_WAZERO_CACHE=" + filepath.Join(temp, "wazero"),
	}, "-host", hostManifest, "-http-port", strconv.Itoa(gatewayPort))
	gatewayURL := fmt.Sprintf("http://127.0.0.1:%d", gatewayPort)
	waitForHTTP(t, gatewayURL+"/node-a/composition/health", hostStop, &processOutput)
	waitForHTTP(t, gatewayURL+"/node-b/composition/health", hostStop, &processOutput)

	status, body = requestJSON(t, http.MethodPost, gatewayURL+"/node-a/composition/templates/replace", map[string]any{
		"request_id": "node-a-catalog",
		"entries": []map[string]any{{
			"name": "node-a-only", "game": "generic", "label": "Node A",
		}},
	})
	if status != http.StatusOK {
		t.Fatalf("node-a replace templates = %d %s\nhost:\n%s", status, body, processOutput.String())
	}
	assertCatalogNames(t, gatewayURL+"/node-a/composition/templates", []string{"node-a-only"})
	assertCatalogNames(t, gatewayURL+"/node-b/composition/templates", nil)

	status, body = requestJSON(t, http.MethodPost, gatewayURL+"/node-a/composition/registry/servers", map[string]any{
		"id": "node-a-game", "type": "game",
	})
	if status != http.StatusCreated {
		t.Fatalf("node-a register = %d %s\nhost:\n%s", status, body, processOutput.String())
	}
	assertRegistryIDs(t, gatewayURL+"/node-a/composition/registry/servers", []string{"node-a-game"})
	assertRegistryIDs(t, gatewayURL+"/node-b/composition/registry/servers", nil)

	// The same idempotency key with different request URLs would conflict if
	// worker ownership leaked across application instances.
	for _, node := range []string{"node-a", "node-b"} {
		status, body = requestJSON(t, http.MethodPost, gatewayURL+"/"+node+"/composition/worker/submit", map[string]any{
			"idempotency_key": "shared-key",
			"method":          "GET",
			"url":             gatewayURL + "/" + node + "/composition/health",
		})
		if status != http.StatusAccepted {
			t.Fatalf("%s worker submit = %d %s\nhost:\n%s", node, status, body, processOutput.String())
		}
		waitForWorkerCompletion(t, gatewayURL+"/"+node+"/composition/worker/shared-key", &processOutput)
	}

	// Restarting reconstructs clean instances. Durable restoration remains an
	// explicit snapshot-import concern rather than ambient process state.
	hostStop()
	restartPort := freePort(t)
	restartStop := startPulpProcess(t, hostExe, temp, &processOutput, []string{
		"HTTP_FETCH_ALLOW=127.0.0.0/8,::1/128",
		"PULP_WAZERO_CACHE=" + filepath.Join(temp, "wazero"),
	}, "-host", hostManifest, "-http-port", strconv.Itoa(restartPort))
	restartURL := fmt.Sprintf("http://127.0.0.1:%d", restartPort)
	waitForHTTP(t, restartURL+"/node-a/composition/health", restartStop, &processOutput)
	assertCatalogNames(t, restartURL+"/node-a/composition/templates", nil)
}

func TestProductionBundleWiring(t *testing.T) {
	repoRoot, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	devRoot := filepath.Dir(repoRoot)
	mainSource, err := os.ReadFile(filepath.Join(repoRoot, "pulp-cell", "main.go"))
	if err != nil {
		t.Fatal(err)
	}
	lifecycleSource, err := os.ReadFile(filepath.Join(repoRoot, "pulp-cell", "fleet_lifecycle_routes.go"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(mainSource), `orch.POST("/servers/:id/restart"`) != 0 ||
		strings.Count(string(mainSource), "registerFleetLifecycleRoutes(orch)") != 1 ||
		strings.Count(string(lifecycleSource), `group.POST("/servers/:id/restart", fleetLifecycleHandler("restart"))`) != 1 {
		t.Fatal("restart must have exactly one strict lifecycle route registration")
	}

	assertContainsFile(t, filepath.Join(repoRoot, "registry-cell", "pulp.cell.toml"),
		`"runtime-directory.v1.register"`,
		`"runtime-directory.v1.remove_match"`)
	assertContainsFile(t, filepath.Join(repoRoot, "template-catalog-cell", "pulp.cell.toml"),
		`"template-catalog.v1.replace"`,
		`"template-catalog.v1.snapshot.import"`)
	assertContainsFile(t, filepath.Join(repoRoot, "worker-cell", "pulp.cell.toml"),
		`"async-http-job.v1.http.submit"`,
		`capabilities = ["workers"]`)
	assertContainsFile(t, filepath.Join(repoRoot, "composition", "lua-orchestrator.pulp.cell.toml"),
		`"orchestrator.dispatch"`,
		`"runtime-directory.v1.register"`,
		`"runtime-directory.v1.remove_match"`,
		`"template-catalog.v1.replace"`,
		`"async-http-job.v1.http.submit"`,
		`"workload-inventory.v1.workload.create"`,
		`"capacity-scheduler.v1.reserve"`,
		`"workload-provisioning.v1.provision"`,
		`"runtime-control.v1.desired.apply"`,
		`"runtime-directory"`,
		`"template-catalog"`,
		`"async-http-job"`)
	assertContainsFile(t, filepath.Join(repoRoot, "composition", "bananagine.lua"),
		`pulp.on("bananagine.create-plan.v1"`,
		`pulp.call("workload-inventory"`,
		`pulp.call("capacity-scheduler"`,
		`pulp.call("workload-provisioning"`,
		`pulp.call("runtime-control"`)
	assertContainsFile(t, filepath.Join(repoRoot, "pulp-cell", "pulp.cell.toml"),
		`consumes = ["orchestrator.dispatch"]`,
		`depends_on = ["bananagine-lua"]`)

	scriptPath := filepath.Join(repoRoot, "composition", "bananagine.lua")
	script, err := os.ReadFile(scriptPath)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(script)
	appManifestPath := filepath.Join(repoRoot, "composition", "pulp.app.toml")
	assertContainsFile(t, appManifestPath,
		`"../registry-cell/pulp.cell.toml"`,
		`"../template-catalog-cell/pulp.cell.toml"`,
		`artifact = "../state-cell/pulp.cell.toml"`,
		`members = ["runtime-directory", "template-catalog"]`,
		`"../worker-cell/pulp.cell.toml"`,
		`"lua-orchestrator.pulp.cell.toml"`,
		`"../pulp-cell/pulp.cell.toml"`,
		`"../../pulp-engines/workload-inventory-sqlite-cell/pulp.cell.toml"`,
		`"../../pulp-engines/capacity-scheduler-sqlite-cell/pulp.cell.toml"`,
		`"../../pulp-engines/workload-provisioning-sqlite-cell/pulp.cell.toml"`,
		`"../../pulp-engines/runtime-control-sqlite-cell/pulp.cell.toml"`,
		`manifest = "lua-orchestrator.pulp.cell.toml"`,
		`script = "bananagine.lua"`,
		fmt.Sprintf(`sha256 = "%x"`, digest))

	luaManifest, err := os.ReadFile(filepath.Join(repoRoot, "composition", "lua-orchestrator.pulp.cell.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(luaManifest), "\nscript =") {
		t.Fatal("Lua cell manifest must not duplicate the app-owned orchestration script")
	}

	dockerfilePath := filepath.Join(devRoot, "ops", "Pulp.Dockerfile")
	dockerfile, err := os.ReadFile(dockerfilePath)
	if err != nil {
		t.Fatal(err)
	}
	text := string(dockerfile)
	for _, fragment := range []string{
		`/app/application/Bananagine/composition/pulp.app.toml`,
		`-app /tmp/application/Bananagine/composition/pulp.app.toml`,
		`-manifest /tmp/pulp.cell.toml`,
		`/out/application/Bananagine/state-cell/runtime-catalog-state.wasm`,
		`/out/application/Bananagine/worker-cell/async-http-job.wasm`,
	} {
		if !strings.Contains(text, fragment) {
			t.Fatalf("%s does not contain %q", dockerfilePath, fragment)
		}
	}

	// Bananagine also keeps a dedicated image recipe for operators that build
	// the application directly. It must have the same application-mode contract
	// as the shared service recipe: every immutable owner is staged, every WASM
	// manifest receives a build-time digest, and no single-cell launch remains.
	dedicatedDockerfilePath := filepath.Join(repoRoot, "Dockerfile")
	dedicatedDockerfile, err := os.ReadFile(dedicatedDockerfilePath)
	if err != nil {
		t.Fatal(err)
	}
	dedicatedText := string(dedicatedDockerfile)
	for _, fragment := range []string{
		`/out/application/Bananagine/pulp-cell/bananagine.wasm`,
		`/out/application/Bananagine/state-cell/runtime-catalog-state.wasm`,
		`/out/application/Bananagine/worker-cell/async-http-job.wasm`,
		`/out/application/Pulp-Lua/pulp-cell/lua-orchestrator.wasm`,
		`/out/application/Bananagine/composition/pulp.app.toml`,
		`wasm_sha256 =`,
		`require_wasm_sha256 = true`,
		`-app /tmp/application/Bananagine/composition/pulp.app.toml`,
		`/app/data/apps/bananagine/default/cells/bananagine/primary`,
	} {
		if !strings.Contains(dedicatedText, fragment) {
			t.Fatalf("%s does not contain %q", dedicatedDockerfilePath, fragment)
		}
	}
	if strings.Contains(dedicatedText, "-manifest ") {
		t.Fatalf("%s must launch the application manifest, not a legacy single-cell manifest", dedicatedDockerfilePath)
	}

	assertContainsFile(t, filepath.Join(repoRoot, "pulp-deployment", "main.go"),
		`Pulp-ext-docker`,
		`Pulp-ext-fs`,
		`Pulp-ext-http`,
		`Pulp-ext-sqlite`,
		`Pulp-ext-workers`)

	assertContainsFile(t, filepath.Join(devRoot, "ops", "deploy", "gameserver", "deploy.sh"),
		"This consumes an already staged source bundle. It intentionally does not pull",
		"Pulp-Lua deliberately does not appear in REQUIRED_GIT_REPOS.",
		`$SOURCE_ROOT/Bananagine/state-cell/pulp.cell.toml`,
		`$SOURCE_ROOT/Bananagine/worker-cell/pulp.cell.toml`,
		`lua_sha="$(hash_unowned_go_source "$SOURCE_ROOT/Pulp-Lua")"`,
		`printf 'Pulp-Lua tree:%s\n' "$lua_sha" >>"$output"`)
}

func build(t *testing.T, dir, output, goCache string, wasm bool) string {
	t.Helper()
	args := []string{"build", "-buildvcs=false", "-o", output}
	if wasm {
		args = append(args, "-trimpath", "-buildmode=c-shared")
	}
	args = append(args, ".")
	command := exec.Command("go", args...)
	command.Dir = dir
	command.Env = append(os.Environ(), "GOCACHE="+goCache, "GOWORK=off")
	if wasm {
		command.Env = append(command.Env, "GOOS=wasip1", "GOARCH=wasm")
	}
	if combined, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build %s: %v\n%s", dir, err, combined)
	}
	return output
}

func materializeManifest(t *testing.T, source, output, wasm string) string {
	t.Helper()
	data, err := os.ReadFile(source)
	if err != nil {
		t.Fatalf("read manifest %s: %v", source, err)
	}
	lines := strings.Split(string(data), "\n")
	replaced := false
	for index, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "wasm =") {
			lines[index] = "wasm = " + strconv.Quote(wasm)
			replaced = true
			break
		}
	}
	if !replaced {
		t.Fatalf("manifest %s has no wasm field", source)
	}
	if err := os.WriteFile(output, []byte(strings.Join(lines, "\n")), 0o600); err != nil {
		t.Fatal(err)
	}
	return output
}

func assertContainsFile(t *testing.T, path string, fragments ...string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, fragment := range fragments {
		if !strings.Contains(string(data), fragment) {
			t.Fatalf("%s does not contain %q", path, fragment)
		}
	}
}

func freePort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	return listener.Addr().(*net.TCPAddr).Port
}

func waitForHTTP(t *testing.T, url string, stop func(), output *lockedBuffer) {
	t.Helper()
	client := &http.Client{Timeout: 500 * time.Millisecond}
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		response, err := client.Get(url)
		if err == nil {
			_, _ = io.Copy(io.Discard, response.Body)
			_ = response.Body.Close()
			if response.StatusCode == http.StatusOK {
				return
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	stop()
	t.Fatalf("Pulp composition did not become ready\n%s", output.String())
}

func waitForWorkerCompletion(t *testing.T, url string, output *lockedBuffer) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		status, body := requestJSON(t, http.MethodGet, url, nil)
		if status == http.StatusOK {
			var job struct {
				State  string `json:"state"`
				Status int    `json:"status"`
				Error  string `json:"error"`
			}
			if err := json.Unmarshal(body, &job); err != nil {
				t.Fatalf("decode worker status: %v body=%s", err, body)
			}
			switch job.State {
			case "completed":
				if job.Status != http.StatusOK {
					t.Fatalf("worker HTTP status = %d body=%s", job.Status, body)
				}
				return
			case "failed", "cancelled":
				t.Fatalf("worker terminal state = %s error=%q\nhost:\n%s", job.State, job.Error, output.String())
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("worker did not complete\n%s", output.String())
}

func assertCatalogNames(t *testing.T, url string, want []string) {
	t.Helper()
	status, body := requestJSON(t, http.MethodGet, url, nil)
	if status != http.StatusOK {
		t.Fatalf("list templates = %d %s", status, body)
	}
	var catalog struct {
		Entries []struct {
			Name string `json:"name"`
		} `json:"entries"`
	}
	if err := json.Unmarshal(body, &catalog); err != nil {
		t.Fatalf("decode template catalog: %v body=%s", err, body)
	}
	got := make([]string, 0, len(catalog.Entries))
	for _, entry := range catalog.Entries {
		got = append(got, entry.Name)
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("template catalog names = %v, want %v", got, want)
	}
}

func assertRegistryIDs(t *testing.T, url string, want []string) {
	t.Helper()
	status, body := requestJSON(t, http.MethodGet, url, nil)
	if status != http.StatusOK {
		t.Fatalf("list registry = %d %s", status, body)
	}
	var servers []struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(body, &servers); err != nil {
		t.Fatalf("decode registry list: %v body=%s", err, body)
	}
	got := make([]string, 0, len(servers))
	for _, server := range servers {
		got = append(got, server.ID)
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("registry IDs = %v, want %v", got, want)
	}
}

func startPulpProcess(
	t *testing.T,
	executable string,
	dir string,
	output *lockedBuffer,
	environment []string,
	args ...string,
) func() {
	t.Helper()
	command := exec.Command(executable, args...)
	command.Dir = dir
	command.Env = append(os.Environ(), environment...)
	command.Stdout = output
	command.Stderr = output
	if err := command.Start(); err != nil {
		t.Fatalf("start Pulp host: %v", err)
	}

	var stopOnce sync.Once
	stop := func() {
		stopOnce.Do(func() {
			if command.Process != nil {
				_ = command.Process.Kill()
			}
			_ = command.Wait()
		})
	}
	t.Cleanup(stop)
	return stop
}

func requestJSON(t *testing.T, method, url string, payload any) (int, []byte) {
	t.Helper()
	var body io.Reader
	if payload != nil {
		data, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		body = bytes.NewReader(data)
	}
	request, err := http.NewRequest(method, url, body)
	if err != nil {
		t.Fatal(err)
	}
	if payload != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	client := &http.Client{Timeout: 5 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	return response.StatusCode, data
}
