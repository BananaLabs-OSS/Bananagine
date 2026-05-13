// Package resources is the pure-math kernel of Bananagine's per-server
// resource override logic, lifted out of the inline block in the
// pulp-cell POST /orchestration/servers handler so it can be
// unit-tested. The parent `package main` only builds for
// GOOS=wasip1 GOARCH=wasm because Pulp/Fiber capabilities use
// //go:wasmimport — this subpackage has no Pulp imports and is
// host-buildable.
package resources

import "fmt"

// Container is the subset of docker.CreateRequest fields the resource
// override logic mutates. Mirrors the relevant fields verbatim — see
// Fiber/pulp/docker.CreateRequest. Kept as a local struct so this
// subpackage does not depend on the WASM-only docker capability.
type Container struct {
	MemoryLimit int64
	CPULimit    float64
	MemorySwap  int64
	Environment map[string]string
}

// Override matches the createServerRequest.Resources struct in
// pulp-cell/main.go. Same field names, same types — keep them in sync.
type Override struct {
	// Legacy fields — bytes for memory, fractional cores for CPU.
	MemoryLimit int64
	CPULimit    float64
	// New tier-driven fields.
	MaxCpuCores float64
	MaxRamMb    int64
	JvmHeapMb   int64
}

// Apply mutates `c` to reflect the per-request resource override.
// Precedence (lowest → highest):
//
//  1. YAML default — what the caller passed in `c` already
//  2. Legacy MemoryLimit / CPULimit (bytes / cores)
//  3. New MaxRamMb / MaxCpuCores (megabytes / cores) — these *replace*
//     the legacy values when both are supplied
//  4. Caller-supplied Env["MEMORY"] (string like "4096M") — wins over
//     anything heap-derived
//
// JvmHeapMb derives `MEMORY=<heap>M` automatically: if explicit, use
// that; if zero AND MaxRamMb is set, use MaxRamMb-1536 (reserved for
// the JVM entrypoint + OS overhead).
//
// callerEnv is applied *last* so the caller can always override
// MEMORY explicitly. nil callerEnv is fine — common when the request
// omitted env entirely.
func Apply(c *Container, o Override, callerEnv map[string]string) {
	if c.Environment == nil {
		c.Environment = make(map[string]string)
	}

	// Legacy fields first — they're the pre-tier shape kept for
	// backward compat with any internal caller that hasn't migrated.
	if o.MemoryLimit > 0 {
		c.MemoryLimit = o.MemoryLimit
	}
	if o.CPULimit > 0 {
		c.CPULimit = o.CPULimit
	}

	// New fields override legacy when supplied. MemorySwap is pinned to
	// MemoryLimit so the container can't burst into swap (matches the
	// inline pulp-cell behaviour — keeps the OOM signal honest).
	effectiveMaxRamMb := o.MaxRamMb
	if effectiveMaxRamMb > 0 {
		c.MemoryLimit = effectiveMaxRamMb * 1024 * 1024
		c.MemorySwap = c.MemoryLimit
	}
	if o.MaxCpuCores > 0 {
		c.CPULimit = o.MaxCpuCores
	}

	// JVM heap derivation — explicit JvmHeapMb wins; otherwise fall
	// back to MaxRamMb - 1536 when MaxRamMb is set; otherwise leave
	// MEMORY untouched so any YAML-baked default survives.
	effectiveJvmHeapMb := o.JvmHeapMb
	if effectiveJvmHeapMb == 0 && effectiveMaxRamMb > 0 {
		effectiveJvmHeapMb = effectiveMaxRamMb - 1536
	}
	if effectiveJvmHeapMb > 0 {
		c.Environment["MEMORY"] = fmt.Sprintf("%dM", effectiveJvmHeapMb)
	}

	// Caller env wins — applies last so a deliberate MEMORY override
	// from the orchestrator (Evolution) still takes precedence over
	// the tier-derived heap above.
	for k, v := range callerEnv {
		c.Environment[k] = v
	}
}
