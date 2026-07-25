package compositionharness

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
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

func TestLuaComposesRegistryCell(t *testing.T) {
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

	registryWASM := build(t, filepath.Join(repoRoot, "registry-cell"), filepath.Join(temp, "bananagine-registry.wasm"), goCache, true)
	luaWASM := build(t, filepath.Join(devRoot, "Pulp-Lua", "pulp-cell"), filepath.Join(temp, "bananagine-lua.wasm"), goCache, true)
	probeWASM := build(t, filepath.Join(repoRoot, "composition", "probe-cell"), filepath.Join(temp, "composition-probe.wasm"), goCache, true)
	hostExe := build(t, filepath.Join(repoRoot, "pulp-deployment"), filepath.Join(temp, "pulp-host.exe"), goCache, false)

	registryManifest := materializeManifest(
		t,
		filepath.Join(repoRoot, "registry-cell", "pulp.cell.toml"),
		filepath.Join(temp, "registry.toml"),
		registryWASM,
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
]

[orchestrator]
manifest = %q
script = %q
sha256 = "%x"
`, filepath.Base(registryManifest), filepath.Base(luaManifest), filepath.Base(probeManifest),
		filepath.Base(luaManifest), filepath.Base(scriptPath), digest)
	if err := os.WriteFile(appManifest, []byte(app), 0o600); err != nil {
		t.Fatal(err)
	}

	port := freePort(t)
	var processOutput lockedBuffer
	command := exec.Command(hostExe, "-app", appManifest)
	command.Dir = temp
	command.Env = append(
		os.Environ(),
		"HTTP_PORT="+strconv.Itoa(port),
		"PULP_WAZERO_CACHE="+filepath.Join(temp, "wazero"),
	)
	command.Stdout = &processOutput
	command.Stderr = &processOutput
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
}

func TestProductionBundleWiring(t *testing.T) {
	repoRoot, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	devRoot := filepath.Dir(repoRoot)

	assertContainsFile(t, filepath.Join(repoRoot, "registry-cell", "pulp.cell.toml"),
		`provides = ["bananagine.registry.v1"]`)
	assertContainsFile(t, filepath.Join(repoRoot, "composition", "lua-orchestrator.pulp.cell.toml"),
		`consumes = ["bananagine.registry.v1"]`,
		`depends_on = ["bananagine-registry"]`)
	assertContainsFile(t, filepath.Join(repoRoot, "pulp-cell", "pulp.cell.toml"),
		`consumes = ["bananagine.app"]`,
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
		`"lua-orchestrator.pulp.cell.toml"`,
		`"../pulp-cell/pulp.cell.toml"`,
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

	dockerfilePath := filepath.Join(devRoot, "Pulp.Dockerfile")
	dockerfile, err := os.ReadFile(dockerfilePath)
	if err != nil {
		t.Fatal(err)
	}
	text := string(dockerfile)
	for _, fragment := range []string{
		`/app/application/Bananagine/composition/pulp.app.toml`,
		`-app /tmp/application/Bananagine/composition/pulp.app.toml`,
		`-manifest /tmp/pulp.cell.toml`,
	} {
		if !strings.Contains(text, fragment) {
			t.Fatalf("%s does not contain %q", dockerfilePath, fragment)
		}
	}

	assertContainsFile(t, filepath.Join(devRoot, "deploy", "gameserver", "deploy.sh"),
		"This consumes an already staged source bundle. It intentionally does not pull",
		"Pulp-Lua deliberately does not appear in REQUIRED_GIT_REPOS.",
		`lua_sha="$(hash_unowned_go_source "$SOURCE_ROOT/Pulp-Lua")"`,
		`printf 'Pulp-Lua tree:%s\n' "$lua_sha" >>"$output"`)
}

func build(t *testing.T, dir, output, goCache string, wasm bool) string {
	t.Helper()
	args := []string{"build", "-buildvcs=false", "-o", output}
	if wasm {
		args = append(args, "-buildmode=c-shared")
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
