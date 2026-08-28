package compositionharness

import (
	"bytes"
	"fmt"
	"path/filepath"
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
	return stageProductionApplicationBundle(t, repoRoot, devRoot)
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
