package hostagent

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
)

const ExecutableSpecVersion = "sessions.workload-exec.v1"

type ExecutableResources struct {
	CPUMillicores int64 `json:"cpu_millicores"`
	MemoryBytes   int64 `json:"memory_bytes"`
	StorageBytes  int64 `json:"storage_bytes"`
}
type ExecutableSpec struct {
	Version     string              `json:"version"`
	Generation  uint64              `json:"generation"`
	Template    string              `json:"template"`
	Environment map[string]string   `json:"environment,omitempty"`
	Resources   ExecutableResources `json:"resources"`
	SHA256      string              `json:"sha256"`
}
type executableAssignment struct {
	WorkloadID string          `json:"workload_id"`
	HostID     string          `json:"host_id"`
	Generation uint64          `json:"generation"`
	Executable *ExecutableSpec `json:"executable,omitempty"`
}

func DecodeExecutable(inv Invocation) (ExecutableSpec, error) {
	var payload executableAssignment
	if json.Unmarshal(inv.Payload, &payload) != nil {
		return ExecutableSpec{}, errors.New("invalid executable assignment payload")
	}
	if payload.WorkloadID != inv.WorkloadID || payload.HostID != inv.HostID || payload.Generation != inv.Generation {
		return ExecutableSpec{}, errors.New("assignment identity or generation mismatch")
	}
	if payload.Executable == nil || payload.Executable.Version != ExecutableSpecVersion || payload.Executable.Generation == 0 || payload.Executable.Template == "" {
		return ExecutableSpec{}, errors.New("assignment has no executable specification")
	}
	spec := *payload.Executable
	digest := spec.SHA256
	spec.SHA256 = ""
	wire, _ := json.Marshal(spec)
	sum := sha256.Sum256(wire)
	if digest == "" || hex.EncodeToString(sum[:]) != digest {
		return ExecutableSpec{}, errors.New("executable specification digest mismatch")
	}
	spec.SHA256 = digest
	return spec, nil
}
