package compositionharness

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestProductionShapeSharedEngineCompositionPreservesCreateParity keeps the
// real Bananagine product cells and Lua manifest intact, then adds the four
// portable workload operators in a separately staged application. The create
// route remains owned by Bananagine while the shared cells prove they can be
// placed beside that real product path without changing its public result.
func TestProductionShapeSharedEngineCompositionPreservesCreateParity(t *testing.T) {
	repoRoot, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	devRoot := filepath.Dir(repoRoot)

	baselineBundle, baselineStorage, baselineHost := stageProductionApplicationBundle(t, repoRoot, devRoot)
	baselineRuntime := newFakeDockerRuntime(t)
	baseline := runProductionShapeCreate(t, baselineHost, baselineBundle, baselineStorage, baselineRuntime)

	sharedBundle, sharedStorage, sharedHost := stageProductionShapeSharedEngineBundle(t, repoRoot, devRoot)
	sharedRuntime := newFakeDockerRuntime(t)
	shared := runProductionShapeCreate(t, sharedHost, sharedBundle, sharedStorage, sharedRuntime)

	if !bytes.Equal(baseline, shared) {
		t.Fatalf("shared-engine create response = %s, want current composition response %s", shared, baseline)
	}
}

func stageProductionShapeSharedEngineBundle(t *testing.T, repoRoot, devRoot string) (bundleRoot, storageRoot, hostExe string) {
	t.Helper()
	bundleRoot, storageRoot, _ = stageProductionApplicationBundle(t, repoRoot, devRoot)
	goCache := filepath.Join(t.TempDir(), "gocache")
	for _, cell := range []string{
		"workload-inventory-sqlite-cell",
		"capacity-scheduler-sqlite-cell",
		"workload-provisioning-sqlite-cell",
		"runtime-control-sqlite-cell",
	} {
		source := filepath.Join(devRoot, "pulp-engines", cell)
		destination := filepath.Join(bundleRoot, "pulp-engines", cell)
		if err := os.MkdirAll(destination, 0o755); err != nil {
			t.Fatal(err)
		}
		copyBundleFile(t, filepath.Join(source, "pulp.cell.toml"), filepath.Join(destination, "pulp.cell.toml"))
		engine := strings.TrimSuffix(cell, "-sqlite-cell")
		build(t, filepath.Join(source, "cmd", engine), filepath.Join(destination, engine+".wasm"), goCache, true)
	}

	appPath := filepath.Join(bundleRoot, "Bananagine", "composition", "pulp.app.toml")
	app, err := os.ReadFile(appPath)
	if err != nil {
		t.Fatal(err)
	}
	const currentTail = "  \"../pulp-cell/pulp.cell.toml\",\n]"
	const sharedTail = `  "../pulp-cell/pulp.cell.toml",
  "../../pulp-engines/workload-inventory-sqlite-cell/pulp.cell.toml",
  "../../pulp-engines/capacity-scheduler-sqlite-cell/pulp.cell.toml",
  "../../pulp-engines/workload-provisioning-sqlite-cell/pulp.cell.toml",
  "../../pulp-engines/runtime-control-sqlite-cell/pulp.cell.toml",
]`
	expanded := strings.Replace(string(app), currentTail, sharedTail, 1)
	if expanded == string(app) {
		t.Fatalf("production composition shape changed; missing expected cell-list tail")
	}
	if err := os.WriteFile(appPath, []byte(expanded), 0o600); err != nil {
		t.Fatal(err)
	}
	hostExe = build(t,
		filepath.Join(repoRoot, "composition-harness", "shared-engine-proof", "host"),
		filepath.Join(t.TempDir(), "production-shape-proof-host.exe"),
		goCache,
		false,
	)
	return bundleRoot, storageRoot, hostExe
}

func runProductionShapeCreate(t *testing.T, hostExe, bundleRoot, storageRoot string, runtime *fakeDockerRuntime) []byte {
	t.Helper()
	var output lockedBuffer
	port := freePort(t)
	stop := startPulpProcess(t, hostExe, bundleRoot, &output,
		productionBundleEnvironment(port, storageRoot, runtime.URL),
		"-app", filepath.Join(bundleRoot, "Bananagine", "composition", "pulp.app.toml"),
		"-storage-root", storageRoot,
	)
	baseURL := fmt.Sprintf("http://127.0.0.1:%d", port)
	waitForHTTP(t, baseURL+"/health", stop, &output)
	status, body := requestAuthorizedJSON(t, "POST", baseURL+"/orchestration/servers", map[string]any{
		"template":  "example-minecraft",
		"server_id": "production-shape-parity-server",
	})
	if status != 201 {
		stop()
		t.Fatalf("production-shape create = %d %s\nhost:\n%s", status, body, output.String())
	}
	if runtime.CreateCount() != 1 {
		stop()
		t.Fatalf("production-shape privileged create count = %d, want 1", runtime.CreateCount())
	}
	stop()
	return body
}
