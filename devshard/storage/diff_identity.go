package storage

import (
	"bytes"
	"errors"
	"fmt"
	"log/slog"

	"devshard/observability"
	"devshard/types"
)

// ErrDiffFork is returned when AppendDiff finds an existing row at the same
// (escrow, nonce) whose payload differs from the candidate. Identical replay
// is success (HA at-least-once); a payload conflict is a real state fork.
var ErrDiffFork = errors.New("diff fork: conflicting payload at same nonce")

// HA assumptions for AppendDiff / host catch-up (proposal §7.5):
//
//  1. The gateway (devshardctl) is the single-instance sequencer per escrow, so
//     composeDiffLocked never races another writer on the same nonce. Persist-
//     first compose makes persist failure "store unchanged, memory unchanged".
//  2. Host duplicates under multi-instance HA are byte-identical because the
//     gateway sequenced them — which is why ON CONFLICT DO NOTHING + identity
//     check is correct rather than lossy.
//
// See docs/proposals/ha-diff-persist-consistency.md.

// checkDiffReplayIdentity returns nil when want and have are the same durable
// diff payload (idempotent replay). On mismatch it increments
// diff_fork_detected and returns ErrDiffFork.
//
// Compared: UserSig, PostStateRoot, StateHash, Txs, WarmKeyDelta.
// Not compared: CreatedAt (may differ across writers), Signatures (upserted
// separately with ON CONFLICT DO UPDATE).
func checkDiffReplayIdentity(escrowID string, want, have types.DiffRecord) error {
	if want.Nonce != have.Nonce ||
		!bytes.Equal(want.UserSig, have.UserSig) ||
		!bytes.Equal(want.PostStateRoot, have.PostStateRoot) ||
		!bytes.Equal(want.StateHash, have.StateHash) ||
		!equalWarmKeyDelta(want.WarmKeyDelta, have.WarmKeyDelta) ||
		!equalTxs(want.Txs, have.Txs) {
		observability.IncDiffForkDetected(escrowID)
		slog.Error("diff_fork_detected",
			"escrow_id", escrowID,
			"nonce", want.Nonce,
			"error", ErrDiffFork,
		)
		return fmt.Errorf("%w: escrow %s nonce %d", ErrDiffFork, escrowID, want.Nonce)
	}
	return nil
}

// checkDiffReplayIdentityRaw compares a candidate against durable columns
// without unmarshaling txs (Postgres/SQLite conflict path).
func checkDiffReplayIdentityRaw(
	escrowID string,
	nonce uint64,
	wantTxsProto, wantUserSig, wantPost, wantHash []byte,
	wantWarmJSON *string,
	haveTxsProto, haveUserSig, havePost, haveHash []byte,
	haveWarmJSON *string,
) error {
	warmEqual := (wantWarmJSON == nil && haveWarmJSON == nil) ||
		(wantWarmJSON != nil && haveWarmJSON != nil && *wantWarmJSON == *haveWarmJSON)
	if !bytes.Equal(wantTxsProto, haveTxsProto) ||
		!bytes.Equal(wantUserSig, haveUserSig) ||
		!bytes.Equal(wantPost, havePost) ||
		!bytes.Equal(wantHash, haveHash) ||
		!warmEqual {
		observability.IncDiffForkDetected(escrowID)
		slog.Error("diff_fork_detected",
			"escrow_id", escrowID,
			"nonce", nonce,
			"error", ErrDiffFork,
		)
		return fmt.Errorf("%w: escrow %s nonce %d", ErrDiffFork, escrowID, nonce)
	}
	return nil
}
