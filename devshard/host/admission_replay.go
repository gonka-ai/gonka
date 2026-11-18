package host

import (
	"bytes"
	"context"
	"fmt"

	"devshard/state"
	"devshard/storage"
	"devshard/types"
)

// ReplayStoredDiff trusts only the local prefix marked by live host admission.
// Legacy v7 rows (including a crash before marking) are checked as new work,
// then atomically marked before committing the trial state. Neither peer
// signatures nor a snapshot can advance this private watermark.
func ReplayStoredDiff(ctx context.Context, sm *state.StateMachine, store storage.Storage, escrowID string, rec types.DiffRecord) ([]byte, error) {
	if !types.RequiresMainnetHeightProof(sm.ProtocolVersion()) {
		return sm.ApplyLocalPersisted(rec.Nonce, rec.Txs)
	}
	meta, err := store.GetSessionMeta(escrowID)
	if err != nil {
		return nil, err
	}
	if rec.Nonce <= meta.HostAdmissionNonce {
		if err := sm.VerifyDiffUserSig(rec.Diff); err != nil {
			return nil, err
		}
		return sm.ApplyLocalPersisted(rec.Nonce, rec.Txs)
	}
	validated, err := sm.ValidateDiffsForAdmission(ctx, []types.Diff{rec.Diff})
	if err != nil {
		return nil, err
	}
	vd := validated[0]
	if len(rec.StateHash) > 0 && !bytes.Equal(vd.Root, rec.StateHash) {
		return nil, fmt.Errorf("host admission replay state hash mismatch at nonce %d", rec.Nonce)
	}
	rec.HostAdmitted = true
	if err := store.AppendDiff(escrowID, rec); err != nil {
		return nil, err
	}
	if !sm.CommitValidated(vd) {
		return nil, fmt.Errorf("host admission replay nonce changed: %d", rec.Nonce)
	}
	return vd.Root, nil
}
