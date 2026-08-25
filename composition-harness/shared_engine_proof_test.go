package compositionharness

import (
	"bytes"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestSharedEngineProofStagesPortableCellsAndResolvesProviders(t *testing.T) {
	repoRoot, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	devRoot := filepath.Dir(repoRoot)
	temp := t.TempDir()
	bundleRoot := filepath.Join(temp, "application")
	proofRoot := filepath.Join(bundleRoot, "Bananagine", "composition-harness", "shared-engine-proof")
	goCache := filepath.Join(temp, "gocache")
	storageRoot := filepath.Join(temp, "storage")

	for _, relative := range []string{
		filepath.Join("Bananagine", "composition-harness", "shared-engine-proof"),
		filepath.Join("Bananagine", "composition-harness", "shared-engine-proof", "probe"),
		filepath.Join("Bananagine", "template-catalog-cell"),
		filepath.Join("Pulp-Lua", "pulp-cell"),
		filepath.Join("pulp-engines", "workload-inventory-sqlite-cell"),
		filepath.Join("pulp-engines", "capacity-scheduler-sqlite-cell"),
		filepath.Join("pulp-engines", "workload-provisioning-sqlite-cell"),
		filepath.Join("pulp-engines", "runtime-control-sqlite-cell"),
	} {
		mustMkdirAll(t, filepath.Join(bundleRoot, relative))
	}

	for _, name := range []string{"pulp.app.toml", "lua-orchestrator.pulp.cell.toml", "shared-engine-proof.lua"} {
		copyBundleFile(t, filepath.Join(repoRoot, "composition-harness", "shared-engine-proof", name), filepath.Join(proofRoot, name))
	}
	copyBundleFile(t, filepath.Join(repoRoot, "template-catalog-cell", "pulp.cell.toml"), filepath.Join(bundleRoot, "Bananagine", "template-catalog-cell", "pulp.cell.toml"))
	copyBundleFile(t,
		filepath.Join(repoRoot, "composition-harness", "shared-engine-proof", "probe", "pulp.cell.toml"),
		filepath.Join(proofRoot, "probe", "pulp.cell.toml"),
	)

	sharedCells := []string{
		"workload-inventory-sqlite-cell",
		"capacity-scheduler-sqlite-cell",
		"workload-provisioning-sqlite-cell",
		"runtime-control-sqlite-cell",
	}
	for _, cell := range sharedCells {
		source := filepath.Join(devRoot, "pulp-engines", cell)
		destination := filepath.Join(bundleRoot, "pulp-engines", cell)
		copyBundleFile(t, filepath.Join(source, "pulp.cell.toml"), filepath.Join(destination, "pulp.cell.toml"))
		engineName := strings.TrimSuffix(cell, "-sqlite-cell")
		build(t, filepath.Join(source, "cmd", engineName), filepath.Join(destination, engineName+".wasm"), goCache, true)
	}
	build(t, filepath.Join(repoRoot, "template-catalog-cell"), filepath.Join(bundleRoot, "Bananagine", "template-catalog-cell", "bananagine-template-catalog.wasm"), goCache, true)
	build(t,
		filepath.Join(repoRoot, "composition-harness", "shared-engine-proof", "probe"),
		filepath.Join(proofRoot, "probe", "bananagine-shared-engine-proof-probe.wasm"),
		goCache,
		true,
	)
	build(t, filepath.Join(devRoot, "Pulp-Lua", "pulp-cell"), filepath.Join(bundleRoot, "Pulp-Lua", "pulp-cell", "lua-orchestrator.wasm"), goCache, true)
	hostExe := build(t, filepath.Join(repoRoot, "composition-harness", "shared-engine-proof", "host"), filepath.Join(temp, "proof-host.exe"), goCache, false)

	var output lockedBuffer
	port := freePort(t)
	stop := startPulpProcess(t, hostExe, bundleRoot, &output, []string{
		"HTTP_PORT=" + strconv.Itoa(port),
		"PULP_WAZERO_CACHE=" + filepath.Join(temp, "wazero"),
	}, "-app", filepath.Join(proofRoot, "pulp.app.toml"), "-storage-root", storageRoot)
	waitForPulpReady(t, stop, &output)
	baseURL := "http://127.0.0.1:" + strconv.Itoa(port)
	waitForHTTP(t, baseURL+"/proof/health", stop, &output)
	status, body := requestJSON(t, http.MethodPost, baseURL+"/proof/run", map[string]any{})
	if status != http.StatusOK {
		t.Fatalf("proof flow = %d %s\nhost:\n%s", status, body, output.String())
	}
	for _, fragment := range [][]byte{
		[]byte(`"lifecycle":"requested"`),
		[]byte(`"value":"bananagine-proof-reservation"`),
		[]byte(`"status":"materializing"`),
		[]byte(`"desired":"running"`),
	} {
		if !bytes.Contains(body, fragment) {
			t.Fatalf("proof flow response missing %s: %s", fragment, body)
		}
	}

	status, body = requestJSON(t, http.MethodPost, baseURL+"/proof/template", map[string]any{
		"name": "minecraft-java", "game": "minecraft", "label": "Minecraft Java", "engine": "java", "cpu_limit": 1.5, "memory_limit": 1610612736,
	})
	if status != http.StatusOK {
		t.Fatalf("seed template = %d %s\nhost:\n%s", status, body, output.String())
	}
	status, body = requestJSON(t, http.MethodPost, baseURL+"/proof/create-plan", map[string]any{
		"server_id": "lua-owned-server", "template": "minecraft-java", "node_id": "game-node-1", "storage_bytes": 1073741824,
		"node_cpu_millicores": 8000, "node_memory_bytes": 8589934592, "node_storage_bytes": 17179869184, "issued_at": "2026-08-09T12:00:00Z",
	})
	if status != http.StatusOK {
		t.Fatalf("Lua create plan = %d %s\nhost:\n%s", status, body, output.String())
	}
	for _, fragment := range [][]byte{
		[]byte(`"name":"minecraft-java"`), []byte(`"value":"bananagine/server/lua-owned-server"`),
		[]byte(`"cpu_millicores":1500`), []byte(`"memory_bytes":1610612736`), []byte(`"value":"bananagine/template/minecraft-java"`),
	} {
		if !bytes.Contains(body, fragment) {
			t.Fatalf("Lua create plan response missing %s: %s", fragment, body)
		}
	}
}

func mustMkdirAll(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
}

func waitForPulpReady(t *testing.T, stop func(), output *lockedBuffer) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		contents := output.String()
		if strings.Contains(contents, "pulp application ready") {
			return
		}
		if strings.Contains(contents, "application failed to start") || strings.Contains(contents, "manifest load failed") {
			stop()
			t.Fatalf("shared engine proof failed to start\n%s", contents)
		}
		time.Sleep(50 * time.Millisecond)
	}
	stop()
	t.Fatalf("shared engine proof did not become ready\n%s", output.String())
}
