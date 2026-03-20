package user

import (
	"bytes"
	"fmt"

	"subnet/signing"
	"subnet/state"
	"subnet/storage"
	"subnet/types"
)

// RecoverSession rebuilds a user Session from persisted storage.
// It loads session metadata and diffs, replays them through a fresh
// StateMachine, and restores nonce, signatures, and diff history.
// The group parameter must match the stored group; a mismatch returns an error.
// Optional SMOptions (e.g. WithWarmKeyResolver) are forwarded to NewStateMachine.
func RecoverSession(
	store storage.Storage,
	signer signing.Signer,
	verifier signing.Verifier,
	escrowID string,
	group []types.SlotAssignment,
	clients []HostClient,
	smOpts ...state.SMOption,
) (*Session, *state.StateMachine, error) {
	meta, err := store.GetSessionMeta(escrowID)
	if err != nil {
		return nil, nil, fmt.Errorf("get session meta: %w", err)
	}

	if len(group) != len(meta.Group) {
		return nil, nil, fmt.Errorf("group size mismatch: caller %d, stored %d", len(group), len(meta.Group))
	}
	for i := range group {
		if group[i].SlotID != meta.Group[i].SlotID || group[i].ValidatorAddress != meta.Group[i].ValidatorAddress {
			return nil, nil, fmt.Errorf("group mismatch at slot %d", i)
		}
	}

	sm := state.NewStateMachine(
		escrowID, meta.Config, meta.Group, meta.InitialBalance,
		meta.CreatorAddr, verifier,
		smOpts...,
	)

	sess, err := NewSession(sm, signer, escrowID, meta.Group, clients, verifier, WithStorage(store))
	if err != nil {
		return nil, nil, fmt.Errorf("create session: %w", err)
	}

	if meta.LatestNonce == 0 {
		return sess, sm, nil
	}

	records, err := store.GetDiffs(escrowID, 1, meta.LatestNonce)
	if err != nil {
		return nil, nil, fmt.Errorf("get diffs: %w", err)
	}

	for _, rec := range records {
		sm.InjectWarmKeys(rec.WarmKeyDelta)
		root, applyErr := sm.ApplyLocal(rec.Nonce, rec.Txs)
		if applyErr != nil {
			return nil, nil, fmt.Errorf("replay nonce %d: %w", rec.Nonce, applyErr)
		}
		if len(rec.StateHash) > 0 && len(root) > 0 {
			if !bytes.Equal(root, rec.StateHash) {
				return nil, nil, fmt.Errorf("state root mismatch at nonce %d", rec.Nonce)
			}
		}

		sess.diffs = append(sess.diffs, rec.Diff)
		sess.nonce = rec.Nonce

		// Restore signatures for this nonce.
		for slotID, sig := range rec.Signatures {
			if _, ok := sess.signatures[rec.Nonce]; !ok {
				sess.signatures[rec.Nonce] = make(map[uint32][]byte)
			}
			sess.signatures[rec.Nonce][slotID] = sig
		}
	}

	return sess, sm, nil
}
