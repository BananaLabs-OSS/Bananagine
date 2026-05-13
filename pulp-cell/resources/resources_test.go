// Tests for the per-server Resources override + JVM heap formula
// shipped in 0fcb57b. Mirrors the inline block in pulp-cell/main.go's
// POST /orchestration/servers handler — if either changes, both must.
package resources

import (
	"testing"
)

func TestApply_EmptyOverride_LeavesYamlDefaults(t *testing.T) {
	// No fields set → container keeps whatever the YAML default was.
	// MEMORY should not be injected since there's no heap source.
	c := &Container{
		MemoryLimit: 2 * 1024 * 1024 * 1024, // 2 GiB YAML default
		CPULimit:    1.5,
		Environment: map[string]string{"MOTD": "default-motd"},
	}
	Apply(c, Override{}, nil)
	if c.MemoryLimit != 2*1024*1024*1024 {
		t.Errorf("MemoryLimit changed unexpectedly: %d", c.MemoryLimit)
	}
	if c.CPULimit != 1.5 {
		t.Errorf("CPULimit changed unexpectedly: %f", c.CPULimit)
	}
	if _, ok := c.Environment["MEMORY"]; ok {
		t.Errorf("MEMORY env injected without a heap source: %q", c.Environment["MEMORY"])
	}
	if c.Environment["MOTD"] != "default-motd" {
		t.Errorf("YAML env stripped: %q", c.Environment["MOTD"])
	}
}

func TestApply_LegacyFields_OverrideYamlOnly(t *testing.T) {
	c := &Container{
		MemoryLimit: 1 * 1024 * 1024 * 1024,
		CPULimit:    1.0,
		Environment: map[string]string{},
	}
	Apply(c, Override{
		MemoryLimit: 4 * 1024 * 1024 * 1024,
		CPULimit:    2.0,
	}, nil)
	if c.MemoryLimit != 4*1024*1024*1024 {
		t.Errorf("MemoryLimit not overridden: got %d", c.MemoryLimit)
	}
	if c.CPULimit != 2.0 {
		t.Errorf("CPULimit not overridden: got %f", c.CPULimit)
	}
	// Legacy path doesn't touch MemorySwap or MEMORY env.
	if c.MemorySwap != 0 {
		t.Errorf("legacy override should not pin MemorySwap: got %d", c.MemorySwap)
	}
	if _, ok := c.Environment["MEMORY"]; ok {
		t.Error("legacy override should not derive a JVM heap")
	}
}

func TestApply_NewTierFields_OverrideLegacyAndYaml(t *testing.T) {
	// MaxRamMb / MaxCpuCores must win when supplied alongside legacy.
	// Also pins MemorySwap to MemoryLimit (no-burst-into-swap rule)
	// and derives MEMORY from MaxRamMb-1536.
	c := &Container{
		MemoryLimit: 1 * 1024 * 1024 * 1024,
		CPULimit:    1.0,
		Environment: map[string]string{},
	}
	Apply(c, Override{
		MemoryLimit: 2 * 1024 * 1024 * 1024, // legacy: should be overridden
		CPULimit:    1.5,                    // legacy: should be overridden
		MaxRamMb:    6144,                   // 6 GiB → wins
		MaxCpuCores: 3.0,                    // wins
	}, nil)
	wantMem := int64(6144) * 1024 * 1024
	if c.MemoryLimit != wantMem {
		t.Errorf("MemoryLimit = %d, want %d (MaxRamMb wins)", c.MemoryLimit, wantMem)
	}
	if c.CPULimit != 3.0 {
		t.Errorf("CPULimit = %f, want 3.0 (MaxCpuCores wins)", c.CPULimit)
	}
	if c.MemorySwap != wantMem {
		t.Errorf("MemorySwap = %d, want %d (pinned to MemoryLimit)", c.MemorySwap, wantMem)
	}
	// Default heap derivation: 6144 - 1536 = 4608 MB → "4608M"
	if c.Environment["MEMORY"] != "4608M" {
		t.Errorf("MEMORY env = %q, want %q", c.Environment["MEMORY"], "4608M")
	}
}

func TestApply_ExplicitJvmHeap_OverridesFormula(t *testing.T) {
	// Operator can pin the heap directly when the default formula is
	// wrong (e.g. modpacks that need more headroom than 1.5 GiB).
	c := &Container{Environment: map[string]string{}}
	Apply(c, Override{
		MaxRamMb:  4096,
		JvmHeapMb: 3000, // explicit; should win over 4096-1536=2560
	}, nil)
	if c.Environment["MEMORY"] != "3000M" {
		t.Errorf("explicit JvmHeapMb ignored: MEMORY=%q want 3000M", c.Environment["MEMORY"])
	}
}

func TestApply_CallerEnv_WinsOverDerivedHeap(t *testing.T) {
	// Caller-supplied MEMORY (Evolution passing through a per-order
	// override) must win over the tier-derived heap. Last-write-wins
	// semantic is the contract pulp-cell relies on.
	c := &Container{Environment: map[string]string{}}
	Apply(c, Override{
		MaxRamMb: 6144, // would derive 4608M
	}, map[string]string{
		"MEMORY": "5000M",
		"MOTD":   "from-caller",
	})
	if c.Environment["MEMORY"] != "5000M" {
		t.Errorf("caller MEMORY override lost: got %q want 5000M", c.Environment["MEMORY"])
	}
	if c.Environment["MOTD"] != "from-caller" {
		t.Errorf("caller env stripped: MOTD=%q", c.Environment["MOTD"])
	}
}

func TestApply_NilEnvironment_Initialized(t *testing.T) {
	// Container.Environment is map type — nil is a valid input shape
	// from a freshly-decoded request. Apply must initialize it lazily
	// so the heap derivation doesn't panic on nil-map write.
	c := &Container{Environment: nil}
	Apply(c, Override{MaxRamMb: 4608}, nil)
	if c.Environment == nil {
		t.Fatal("Environment not initialized")
	}
	if c.Environment["MEMORY"] != "3072M" {
		t.Errorf("MEMORY = %q, want 3072M (4608-1536)", c.Environment["MEMORY"])
	}
}

func TestApply_MaxRamMbAlone_DerivesHeapNoCpuChange(t *testing.T) {
	// Memory override without CPU override — CPU should keep YAML
	// default. Customers on RAM-only tiers still get correct CPU.
	c := &Container{
		CPULimit:    2.0, // YAML
		Environment: map[string]string{},
	}
	Apply(c, Override{MaxRamMb: 4096}, nil)
	if c.CPULimit != 2.0 {
		t.Errorf("CPULimit changed: got %f want 2.0", c.CPULimit)
	}
	if c.Environment["MEMORY"] != "2560M" {
		t.Errorf("MEMORY = %q, want 2560M (4096-1536)", c.Environment["MEMORY"])
	}
}

func TestApply_MaxCpuCoresAlone_NoMemoryChange(t *testing.T) {
	// Symmetric: CPU override without RAM override leaves memory alone
	// and does NOT derive MEMORY env (no heap source).
	c := &Container{
		MemoryLimit: 4 * 1024 * 1024 * 1024,
		Environment: map[string]string{},
	}
	Apply(c, Override{MaxCpuCores: 2.5}, nil)
	if c.MemoryLimit != 4*1024*1024*1024 {
		t.Errorf("MemoryLimit changed: got %d", c.MemoryLimit)
	}
	if c.CPULimit != 2.5 {
		t.Errorf("CPULimit = %f, want 2.5", c.CPULimit)
	}
	if _, ok := c.Environment["MEMORY"]; ok {
		t.Error("MEMORY env derived without MaxRamMb")
	}
}
