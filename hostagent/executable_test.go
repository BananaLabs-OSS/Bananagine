package hostagent

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"
)

func executablePayload(t *testing.T) []byte {
	t.Helper()
	spec := ExecutableSpec{Version: ExecutableSpecVersion, Generation: 4, Template: "java-vanilla", Environment: map[string]string{"VERSION": "1.21.8"}, Resources: ExecutableResources{CPUMillicores: 2000, MemoryBytes: 2 << 30, StorageBytes: 10 << 30}}
	wire, _ := json.Marshal(spec)
	sum := sha256.Sum256(wire)
	spec.SHA256 = hex.EncodeToString(sum[:])
	payload, _ := json.Marshal(executableAssignment{WorkloadID: "work", HostID: "host", Generation: 7, Executable: &spec})
	return payload
}

func TestExecutableAssignmentDigestGenerationAndHostFences(t *testing.T) {
	inv := Invocation{HostID: "host", AgentID: "agent", WorkloadID: "work", Generation: 7, Payload: executablePayload(t)}
	spec, err := DecodeExecutable(inv)
	if err != nil || spec.Template != "java-vanilla" || spec.Environment["VERSION"] != "1.21.8" {
		t.Fatalf("spec=%#v err=%v", spec, err)
	}
	for _, mutate := range []func(*Invocation){func(i *Invocation) { i.HostID = "other" }, func(i *Invocation) { i.Generation++ }, func(i *Invocation) { i.Payload[len(i.Payload)-10] ^= 1 }} {
		bad := inv
		bad.Payload = append([]byte(nil), inv.Payload...)
		mutate(&bad)
		if _, err := DecodeExecutable(bad); err == nil {
			t.Fatal("forged executable assignment accepted")
		}
	}
}
