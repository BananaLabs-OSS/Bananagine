package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/bananalabs-oss/bananagine/orchestration"
)

const recreateContractV1 = 1

type dockerServerDestroy func(string) error

type recreateReplay struct {
	specDigest string
	receipt    orchestration.RecreateServerReceiptV1
}

// recreateCore keeps idempotency receipts separate from the reusable creation
// operation. The cell is single-threaded, so this map needs no lock.
type recreateCore struct {
	create  creationCore
	destroy dockerServerDestroy
	replays map[string]recreateReplay
}

func newRecreateCore(create creationCore, destroy dockerServerDestroy) *recreateCore {
	return &recreateCore{create: create, destroy: destroy, replays: make(map[string]recreateReplay)}
}

func recreateImmutableDigest(request orchestration.RecreateServerRequestV1) (string, error) {
	immutable := struct {
		Version        int
		IdempotencyKey string
		ReceiptID      string
		Replacement    orchestration.CreateServerRequest
	}{request.Version, request.IdempotencyKey, request.ReceiptID, request.Replacement}
	wire, err := json.Marshal(immutable)
	if err != nil {
		return "", fmt.Errorf("encode immutable recreate specification: %w", err)
	}
	digest := sha256.Sum256(wire)
	return hex.EncodeToString(digest[:]), nil
}

func validRecreateToken(value string) bool {
	if len(value) == 0 || len(value) > 256 {
		return false
	}
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' || r == '.' || r == ':' {
			continue
		}
		return false
	}
	return true
}

func (core *recreateCore) Recreate(oldContainerID string, request orchestration.RecreateServerRequestV1) (orchestration.RecreateServerReceiptV1, error) {
	if !validFleetIdentity(oldContainerID) {
		return orchestration.RecreateServerReceiptV1{}, createFailure(400, "invalid old container identity")
	}
	if request.Version != recreateContractV1 {
		return orchestration.RecreateServerReceiptV1{}, createFailure(400, "unsupported recreate contract version")
	}
	if !validRecreateToken(request.IdempotencyKey) || !validRecreateToken(request.ReceiptID) {
		return orchestration.RecreateServerReceiptV1{}, createFailure(400, "invalid recreate idempotency or receipt identity")
	}
	if !validFleetIdentity(request.Replacement.ServerID) || request.Replacement.ServerID == oldContainerID {
		return orchestration.RecreateServerReceiptV1{}, createFailure(400, "replacement server_id must be a distinct valid logical identity")
	}
	digest, err := recreateImmutableDigest(request)
	if err != nil {
		return orchestration.RecreateServerReceiptV1{}, createFailure(400, "%v", err)
	}
	if len(request.ImmutableSpecSHA256) != 64 || !strings.EqualFold(request.ImmutableSpecSHA256, digest) {
		return orchestration.RecreateServerReceiptV1{}, createFailure(400, "immutable recreate specification digest does not match request")
	}
	if replay, ok := core.replays[request.IdempotencyKey]; ok {
		if replay.specDigest != digest {
			return orchestration.RecreateServerReceiptV1{}, createFailure(409, "idempotency key was already used with a different immutable recreate specification")
		}
		return replay.receipt, nil
	}

	created, err := core.create.Create(request.Replacement)
	if err != nil {
		return orchestration.RecreateServerReceiptV1{}, err
	}
	if err := core.destroy(oldContainerID); err != nil && !isDockerNotFound(err) {
		// Deliberately retain the successful replacement. A retry with the same
		// request will resolve it idempotently and can finish old retirement.
		return orchestration.RecreateServerReceiptV1{}, createFailure(500, "replacement is ready but old runtime was not retired: %v", err)
	}
	// The old runtime's capacity and allocation ownership may be keyed by its
	// concrete ID after normal creation/reconciliation. Release only after its
	// destruction succeeds (or is already absent), never before replacement.
	core.create.capacity.release(oldContainerID)
	core.create.portPools.releaseByServer(oldContainerID)
	core.create.ipp.releaseByServer(oldContainerID)
	receipt := orchestration.RecreateServerReceiptV1{
		Version: recreateContractV1, IdempotencyKey: request.IdempotencyKey, ReceiptID: request.ReceiptID,
		OldContainerID: oldContainerID, Replacement: created.Server, ReplacementReady: true, OldRetired: true,
	}
	core.replays[request.IdempotencyKey] = recreateReplay{specDigest: digest, receipt: receipt}
	return receipt, nil
}
