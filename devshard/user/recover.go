package user

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"

	"devshard/signing"
	"devshard/state"
	"devshard/storage"
	"devshard/types"
)

// snapshotInterval controls how often a state snapshot is saved -- both
// during diff replay at recovery time AND during runtime via
// Session.maybeSaveSnapshotLocked. After replay finishes, a final
// snapshot is saved at the latest nonce so the next restart is fast.
// During runtime, every snapshotInterval committed diffs trigger an
// asynchronous snapshot so the persisted per-host catch-up cursor
// (HostSyncNonce) stays fresh.
const snapshotInterval = 500

// sessionSnapshot is the on-disk wrapper for a session snapshot. It bundles
// the state-machine state with the session-level per-host sync cursor so
// that after a restart we can both:
//   - restore the state machine without replaying every diff, AND
//   - restore the per-host catch-up cursor so we know where each host left
//     off applying diffs.
//
// Without (2), every host appears to be at nonce 0 after a restart and the
// proxy sends only the newest diff in each request. Hosts that were behind
// the snapshot at restart time then reject with "invalid nonce: must be
// sequential: expected M, got N" because we never re-send the gap diffs.
//
// Backward compatibility: older snapshots stored a bare types.EscrowState
// JSON. On load, if the wrapper unmarshal yields State == nil, we retry
// as a bare EscrowState and treat HostSyncNonce as unknown. The recovery
// path then loads the entire diff history into sess.diffs so any host
// that was stranded behind the prior snapshot self-heals via host-side
// silent-skip (host.applyAndPersist drops diffs whose Nonce <= currentNonce).
type sessionSnapshot struct {
	State         *types.EscrowState `json:"state"`
	HostSyncNonce map[int]uint64     `json:"host_sync_nonce,omitempty"`
}

// decodeSnapshot decodes the on-disk snapshot blob. Returns the state and
// the per-host sync cursor (nil for legacy bare-EscrowState snapshots).
func decodeSnapshot(data []byte) (*types.EscrowState, map[int]uint64, error) {
	var blob sessionSnapshot
	if err := json.Unmarshal(data, &blob); err == nil && blob.State != nil {
		return blob.State, blob.HostSyncNonce, nil
	}
	// Legacy format: top-level EscrowState fields. Re-unmarshal as bare.
	var bare types.EscrowState
	if err := json.Unmarshal(data, &bare); err != nil {
		return nil, nil, err
	}
	return &bare, nil, nil
}

// minHostSyncNonce returns the smallest cursor value across all hosts
// in the group. Any host not present in the cursor map is treated as
// "unknown" -> 0, which forces full diff backfill on recovery.
func minHostSyncNonce(cursor map[int]uint64, groupSize int) uint64 {
	if len(cursor) == 0 || groupSize == 0 {
		return 0
	}
	var minNonce uint64
	sawAny := false
	for h := 0; h < groupSize; h++ {
		v, ok := cursor[h]
		if !ok {
			return 0
		}
		if !sawAny || v < minNonce {
			minNonce = v
			sawAny = true
		}
	}
	return minNonce
}

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
	boundVersion string,
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
	if meta.Version != "" && boundVersion != "" && meta.Version != boundVersion {
		return nil, nil, fmt.Errorf("session version mismatch: stored %s, requested %s", meta.Version, boundVersion)
	}
	recoveredVersion := meta.Version
	if recoveredVersion == "" {
		recoveredVersion = boundVersion
	}
	if recoveredVersion == "" {
		return nil, nil, fmt.Errorf("session version required for escrow %s", escrowID)
	}

	stateOpts := append(smOpts, state.WithVersion(types.EffectiveStateRootAndProtocolVersion))
	if pv, ok := recoveredProtocolVersion(boundVersion); ok {
		stateOpts = append(stateOpts, state.WithProtocolVersion(pv))
	}

	sm, err := state.NewStateMachine(
		escrowID, meta.Config, meta.Group, meta.InitialBalance,
		meta.CreatorAddr, verifier, store,
		stateOpts...,
	)
	if err != nil {
		return nil, nil, fmt.Errorf("create state machine: %w", err)
	}

	sess, err := NewSession(sm, signer, escrowID, meta.Group, clients, verifier, WithStorage(store))
	if err != nil {
		return nil, nil, fmt.Errorf("create session: %w", err)
	}

	if meta.LatestNonce == 0 {
		return finishRecover(sess, sm)
	}

	// Try to restore from a snapshot to skip replaying old diffs.
	// snapshotCursor==nil indicates either no snapshot or a legacy
	// bare-EscrowState snapshot; either case forces full diff backfill
	// below so any stranded host can self-heal.
	var snapshotCursor map[int]uint64
	legacySnapshot := false
	replayFrom := uint64(1)
	snapNonce, snapData, snapErr := store.LoadSnapshot(escrowID)
	if snapErr == nil && snapNonce > 0 && snapNonce <= meta.LatestNonce {
		snapState, cursor, decodeErr := decodeSnapshot(snapData)
		if decodeErr != nil {
			log.Printf("recover_session escrow=%s snapshot_nonce=%d unmarshal_failed=%v (replaying from 1)", escrowID, snapNonce, decodeErr)
		} else {
			sm.RestoreState(snapState)
			replayFrom = snapNonce + 1
			sess.nonce = snapNonce
			snapshotCursor = cursor
			legacySnapshot = cursor == nil
			log.Printf("recover_session escrow=%s snapshot_restored nonce=%d replay_from=%d total=%d skipped=%d host_cursors=%d legacy=%t",
				escrowID, snapNonce, replayFrom, meta.LatestNonce, snapNonce, len(cursor), legacySnapshot)
		}
	} else if snapErr != nil && !errors.Is(snapErr, storage.ErrSnapshotNotFound) {
		log.Printf("recover_session escrow=%s snapshot_load_error=%v (replaying from 1)", escrowID, snapErr)
	}

	// Restore the per-host catch-up cursor.
	for h, n := range snapshotCursor {
		sess.hostSyncNonce[h] = n
	}

	// Backfill sess.diffs with pre-snapshot diffs that some host may still
	// need. sess.diffs must contain a contiguous range covering every
	// host's expected next-nonce, otherwise diffsForHost produces a
	// non-contiguous slice and the host rejects (it requires sequential
	// nonces, only silent-skipping diffs <= its currentNonce).
	//
	// For a fresh new-format snapshot all hosts are typically at or near
	// snapNonce, so backfillFrom is close to snapNonce and the load is
	// small. For a legacy snapshot (cursor unknown) min returns 0 and we
	// load the entire pre-snapshot history once -- a one-time slow
	// recovery that self-heals stranded hosts.
	if snapNonce > 0 {
		backfillFrom := minHostSyncNonce(sess.hostSyncNonce, len(group)) + 1
		if backfillFrom <= snapNonce {
			backfillRecords, berr := store.GetDiffs(escrowID, backfillFrom, snapNonce)
			if berr != nil {
				return nil, nil, fmt.Errorf("get backfill diffs %d..%d: %w", backfillFrom, snapNonce, berr)
			}
			log.Printf("recover_session escrow=%s diff_backfill from=%d to=%d count=%d",
				escrowID, backfillFrom, snapNonce, len(backfillRecords))
			for _, rec := range backfillRecords {
				sess.diffs = append(sess.diffs, rec.Diff)
			}
		}
	}

	if replayFrom > meta.LatestNonce {
		// No post-snapshot diffs to replay. Save a fresh snapshot if the
		// existing one is legacy (or missing) so it gets upgraded to the
		// new format with the (possibly-empty) cursor for next time.
		if legacySnapshot || snapshotCursor == nil {
			saveSnapshot(store, sm, escrowID, meta.LatestNonce, sess.hostSyncNonce)
		}
		return finishRecover(sess, sm)
	}

	records, err := store.GetDiffs(escrowID, replayFrom, meta.LatestNonce)
	if err != nil {
		return nil, nil, fmt.Errorf("get diffs: %w", err)
	}

	log.Printf("recover_session escrow=%s replaying diffs %d..%d (%d records)", escrowID, replayFrom, meta.LatestNonce, len(records))

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

		for slotID, sig := range rec.Signatures {
			if _, ok := sess.signatures[rec.Nonce]; !ok {
				sess.signatures[rec.Nonce] = make(map[uint32][]byte)
			}
			sess.signatures[rec.Nonce][slotID] = sig
		}
	}

	// Save a snapshot at the latest nonce so subsequent restarts are fast.
	// We always save when we encountered a legacy snapshot so it gets
	// upgraded to the new wrapper format on the very first restart with
	// this code, removing the "bare EscrowState" footgun without manual
	// snapshot deletion on the server.
	if replayFrom == 1 || uint64(len(records)) >= snapshotInterval || legacySnapshot {
		saveSnapshot(store, sm, escrowID, meta.LatestNonce, sess.hostSyncNonce)
	}

	return finishRecover(sess, sm)
}

func finishRecover(sess *Session, sm *state.StateMachine) (*Session, *state.StateMachine, error) {
	if err := sm.RebuildSealedInferenceIndex(); err != nil {
		return nil, nil, fmt.Errorf("rebuild sealed inference index: %w", err)
	}
	return sess, sm, nil
}

// recoveredProtocolVersion derives protocol compatibility only from explicit
// protocol-version tokens. Route versions like "v1" stay on the normal path
// unless the caller provided WithProtocolVersion in smOpts.
func recoveredProtocolVersion(boundVersion string) (types.ProtocolVersion, bool) {
	raw := strings.TrimSpace(boundVersion)
	if raw == "" {
		return "", false
	}
	pv, err := types.ParseProtocolVersion(raw)
	if err != nil {
		return "", false
	}
	return pv, true
}

// saveSnapshot is the synchronous snapshot writer used during recovery.
// It deep-copies state via sm.ExportState (under the SM RLock) and the
// caller-provided cursor before marshaling, so the caller is free to
// mutate hostSyncNonce after this returns.
func saveSnapshot(store storage.Storage, sm *state.StateMachine, escrowID string, nonce uint64, hostSyncNonce map[int]uint64) {
	cursor := make(map[int]uint64, len(hostSyncNonce))
	for k, v := range hostSyncNonce {
		cursor[k] = v
	}
	writeSnapshot(store, escrowID, nonce, sm.ExportState(), cursor)
}

// writeSnapshot persists a pre-prepared snapshot blob. The caller must
// have already deep-copied state and cursor (so this function performs
// only the JSON marshal + storage write and can run without any session
// or state-machine locks held -- this is what enables async background
// snapshots from the runtime hot path).
func writeSnapshot(store storage.Storage, escrowID string, nonce uint64, state *types.EscrowState, cursor map[int]uint64) {
	_ = writeSnapshotErr(store, escrowID, nonce, state, cursor)
}

// writeSnapshotErr is writeSnapshot with an error return, for synchronous
// callers (e.g. Session.FlushSnapshot on retire) that want to know whether the
// snapshot landed. It logs on failure exactly like writeSnapshot.
func writeSnapshotErr(store storage.Storage, escrowID string, nonce uint64, state *types.EscrowState, cursor map[int]uint64) error {
	blob := sessionSnapshot{State: state, HostSyncNonce: cursor}
	data, err := json.Marshal(blob)
	if err != nil {
		log.Printf("recover_session escrow=%s snapshot_marshal_failed=%v", escrowID, err)
		return err
	}
	if err := store.SaveSnapshot(escrowID, nonce, data); err != nil {
		log.Printf("recover_session escrow=%s snapshot_save_failed=%v", escrowID, err)
		return err
	}
	log.Printf("recover_session escrow=%s snapshot_saved nonce=%d size_bytes=%d host_cursors=%d",
		escrowID, nonce, len(data), len(cursor))
	return nil
}
