package user

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log"

	"devshard/heightsync"
	"devshard/signing"
	"devshard/state"
	"devshard/storage"
	"devshard/types"
)

// ErrLocalStateUnrecoverable marks a proven integrity failure in persisted
// diff history: a nonce gap, a missing tail, or a state-root mismatch during
// replay. Gateway startup deactivates and skips such escrows (like
// bridge.ErrEscrowNotFound). Transient failures (I/O errors, full disk, chain
// timeouts) are never classified and stay fatal, so escrow rotation cannot
// mint replacements into a broken environment.
var ErrLocalStateUnrecoverable = errors.New("local session state unrecoverable")

// validateDiffRange verifies the stored diffs cover [from, to] contiguously.
// A missing tail (diffs end before latest_nonce) would otherwise recover
// silently behind the hosts.
func validateDiffRange(records []types.DiffRecord, from, to uint64) error {
	expected := from
	for _, rec := range records {
		if rec.Nonce != expected {
			return fmt.Errorf("%w: missing nonce %d, next stored nonce is %d",
				ErrLocalStateUnrecoverable, expected, rec.Nonce)
		}
		expected++
	}
	if expected <= to {
		return fmt.Errorf("%w: missing trailing nonces %d..%d",
			ErrLocalStateUnrecoverable, expected, to)
	}
	return nil
}

// unrecoverableRange turns a paged-read hole into the deactivate-and-skip
// sentinel. Other errors (store failures, apply failures) pass through.
func unrecoverableRange(scope string, from, to uint64, err error) error {
	var gap *storage.DiffGapError
	if errors.As(err, &gap) {
		return fmt.Errorf("%s %d..%d: %w: %s", scope, from, to, ErrLocalStateUnrecoverable, gap.Error())
	}
	return err
}

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
// A blob with no state, or a cursor that does not name every host, is not
// restored. Recovery replays from nonce 1. A host recorded at cursor 0 is a
// real cursor: that host's prefix is backfilled.
type sessionSnapshot struct {
	State            *types.EscrowState     `json:"state"`
	HostSyncNonce    map[int]uint64         `json:"host_sync_nonce,omitempty"`
	CommittedEntries map[uint64][]byte      `json:"committed_entries,omitempty"`
	SealedNonces     map[uint64]uint64      `json:"sealed_nonces,omitempty"`
	HeightSyncFloor  *types.FloorIndexProto `json:"height_sync_floor,omitempty"`
}

// decodeSnapshot decodes a wrapped session snapshot. A bare EscrowState, or a
// wrapper whose state is missing, is rejected.
func decodeSnapshot(data []byte) (*types.EscrowState, map[int]uint64, map[uint64][]byte, map[uint64]uint64, *types.FloorIndexProto, error) {
	var blob sessionSnapshot
	if err := json.Unmarshal(data, &blob); err != nil {
		return nil, nil, nil, nil, nil, err
	}
	if blob.State == nil {
		return nil, nil, nil, nil, nil, errors.New("snapshot state missing")
	}
	return blob.State, blob.HostSyncNonce, blob.CommittedEntries, blob.SealedNonces, blob.HeightSyncFloor, nil
}

// hostCursorComplete reports whether cursor names every host in the group.
// A missing map, or a map that skips a host, is not a cursor: recovery must
// not treat the gap as "host is at 0" and backfill 1..snapshot under a
// restored state. A present 0 is a real cursor.
func hostCursorComplete(cursor map[int]uint64, groupSize int) bool {
	if cursor == nil || groupSize <= 0 {
		return false
	}
	for h := 0; h < groupSize; h++ {
		if _, ok := cursor[h]; !ok {
			return false
		}
	}
	return true
}

// minHostSyncNonce returns the smallest cursor value across a complete host
// map. Callers must pass a cursor hostCursorComplete accepts. A host at 0
// pulls the minimum to 0, and recovery backfills that host from nonce 1.
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

	stateOpts := append(smOpts, state.WithVersion(recoveredVersion))

	sm, err := state.NewStateMachine(
		escrowID, meta.Config, meta.Group, meta.InitialBalance,
		meta.CreatorAddr, verifier, store,
		stateOpts...,
	)
	if err != nil {
		return nil, nil, fmt.Errorf("create state machine: %w", err)
	}

	sess, err := NewSession(sm, signer, escrowID, meta.Group, clients, verifier,
		WithStorage(store), WithHeartbeatConfig(sm.HeartbeatConfig()))
	if err != nil {
		return nil, nil, fmt.Errorf("create session: %w", err)
	}

	if meta.LatestNonce == 0 {
		return finishRecover(sess, sm, 1)
	}

	// Try to restore from a snapshot to skip replaying old diffs. A blob
	// that does not carry a cursor for every host is ignored: replaying
	// from nonce 1 is the recovery, not a backfill under restored state.
	var snapshotCursor map[int]uint64
	snapshotRestored := false
	replayFrom := uint64(1)
	snapNonce, snapData, snapErr := store.LoadSnapshot(escrowID)
	if snapErr == nil && snapNonce > 0 && snapNonce <= meta.LatestNonce {
		snapState, cursor, committedEntries, sealedNonces, floorProto, decodeErr := decodeSnapshot(snapData)
		if decodeErr != nil {
			log.Printf("recover_session escrow=%s snapshot_nonce=%d unmarshal_failed=%v (replaying from 1)", escrowID, snapNonce, decodeErr)
		} else if !hostCursorComplete(cursor, len(group)) {
			log.Printf("recover_session escrow=%s snapshot_nonce=%d incomplete_host_cursor=%d (replaying from 1)",
				escrowID, snapNonce, len(cursor))
		} else {
			// A rejected blob degrades to a journal replay; if that cannot run
			// either, RestoreStateWithFloor fails closed rather than serving
			// L0 from a floor we could not verify.
			floor, floorErr := heightsync.FloorIndexFromProto(heightsync.FloorConfig{}, floorProto)
			if floorErr != nil {
				log.Printf("recover_session escrow=%s snapshot_nonce=%d floor_blob_rejected=%v (rebuilding from diffs)",
					escrowID, snapNonce, floorErr)
				floor = nil
			}
			if restErr := sm.RestoreStateWithFloor(snapState, floor); restErr != nil {
				return nil, nil, fmt.Errorf("restore snapshot nonce %d: %w", snapNonce, restErr)
			}
			sm.RestoreCommittedEntries(committedEntries)
			sm.RestoreSealedNonces(sealedNonces)
			replayFrom = snapNonce + 1
			sess.nonce = snapNonce
			snapshotCursor = cursor
			snapshotRestored = true
			log.Printf("recover_session escrow=%s snapshot_restored nonce=%d replay_from=%d total=%d skipped=%d host_cursors=%d",
				escrowID, snapNonce, replayFrom, meta.LatestNonce, snapNonce, len(cursor))
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
	// A complete cursor whose slowest host is behind snapNonce backfills
	// that suffix into sess.diffs. A host at 0 is included: that is a real
	// cursor, and the read is paged. An ignored snapshot (future nonce,
	// decode failure, incomplete cursor) replays from 1, which already
	// covers the range, so this block does not run.
	if snapshotRestored {
		backfillFrom := minHostSyncNonce(sess.hostSyncNonce, len(group)) + 1
		if backfillFrom <= snapNonce {
			var backfilled int
			berr := storage.ReadDiffPages(store, escrowID, backfillFrom, snapNonce, func(page []types.DiffRecord) error {
				for _, rec := range page {
					sess.diffs = append(sess.diffs, rec.Diff)
					for slotID, sig := range rec.Signatures {
						if _, ok := sess.signatures[rec.Nonce]; !ok {
							sess.signatures[rec.Nonce] = make(map[uint32][]byte)
						}
						sess.signatures[rec.Nonce][slotID] = sig
					}
				}
				sess.dropDiffPrefixLocked()
				backfilled += len(page)
				return nil
			})
			if berr != nil {
				if uerr := unrecoverableRange("backfill diffs", backfillFrom, snapNonce, berr); uerr != berr {
					return nil, nil, uerr
				}
				return nil, nil, fmt.Errorf("get backfill diffs %d..%d: %w", backfillFrom, snapNonce, berr)
			}
			log.Printf("recover_session escrow=%s diff_backfill from=%d to=%d count=%d",
				escrowID, backfillFrom, snapNonce, backfilled)
		}
	}

	if replayFrom > meta.LatestNonce {
		// Snapshot-only path never runs the replay loop that restores
		// signatures from DiffRecords. Reload the final-nonce signatures
		// from the store so settlement can proceed without host round-trips
		// when they were persisted before the restart.
		if err := restoreSignaturesFromStore(sess, store, escrowID, meta.LatestNonce); err != nil {
			return nil, nil, err
		}
		return finishRecover(sess, sm, replayFrom)
	}

	log.Printf("recover_session escrow=%s replaying diffs %d..%d", escrowID, replayFrom, meta.LatestNonce)

	var replayed int
	err = storage.ReadDiffPages(store, escrowID, replayFrom, meta.LatestNonce, func(page []types.DiffRecord) error {
		for _, rec := range page {
			sm.InjectWarmKeys(rec.WarmKeyDelta)
			root, applyErr := sm.ApplyLocalPersisted(rec.Nonce, rec.Txs)
			if applyErr != nil {
				if errors.Is(applyErr, types.ErrInvalidNonce) {
					return fmt.Errorf("%w: replay nonce %d: %w",
						ErrLocalStateUnrecoverable, rec.Nonce, applyErr)
				}
				return fmt.Errorf("replay nonce %d: %w", rec.Nonce, applyErr)
			}
			if len(rec.StateHash) > 0 && len(root) > 0 {
				if !bytes.Equal(root, rec.StateHash) {
					return fmt.Errorf("%w: state root mismatch at nonce %d",
						ErrLocalStateUnrecoverable, rec.Nonce)
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
		sess.dropDiffPrefixLocked()
		replayed += len(page)
		return nil
	})
	if err != nil {
		if uerr := unrecoverableRange("replay diffs", replayFrom, meta.LatestNonce, err); uerr != err {
			return nil, nil, uerr
		}
		return nil, nil, err
	}

	// Save a snapshot at the latest nonce so subsequent restarts are fast.
	if replayFrom == 1 || uint64(replayed) >= snapshotInterval {
		saveSnapshot(store, sm, escrowID, meta.LatestNonce, sess.hostSyncNonce)
	}

	return finishRecover(sess, sm, replayFrom)
}

// restoreSignaturesFromStore loads persisted signatures for nonce into the
// session. Used on the snapshot-only recovery path where the replay loop
// (the usual signature restorer) is skipped. Missing/empty is not an error —
// CollectSignatures can still refill from hosts after ComputeStateRoot
// fallback in fetchSignature.
func restoreSignaturesFromStore(sess *Session, store storage.Storage, escrowID string, nonce uint64) error {
	if store == nil || nonce == 0 {
		return nil
	}
	sigs, err := store.GetSignatures(escrowID, nonce)
	if err != nil {
		return fmt.Errorf("get signatures at nonce %d: %w", nonce, err)
	}
	if len(sigs) == 0 {
		return nil
	}
	if _, ok := sess.signatures[nonce]; !ok {
		sess.signatures[nonce] = make(map[uint32][]byte)
	}
	for slotID, sig := range sigs {
		sess.signatures[nonce][slotID] = sig
	}
	log.Printf("recover_session escrow=%s signatures_restored nonce=%d slots=%d", escrowID, nonce, len(sigs))
	return nil
}

func finishRecover(sess *Session, sm *state.StateMachine, replayFrom uint64) (*Session, *state.StateMachine, error) {
	if err := sm.RebuildSealedInferenceIndex(); err != nil {
		return nil, nil, fmt.Errorf("rebuild sealed inference index: %w", err)
	}
	restoreHeartbeatProducer(sess, sm)
	// Signatures are loaded only from the slowest cursor on; the trim marks
	// the nonces below it as read-from-store.
	sess.dropDiffPrefixLocked()
	if sess.store == nil {
		return sess, sm, nil
	}
	meta, err := sess.store.GetSessionMeta(sess.escrowID)
	if err != nil {
		return nil, nil, fmt.Errorf("get session meta for validation obs rebuild: %w", err)
	}
	// A tail replay or a snapshot restore already has the obs rows the live
	// path wrote. Rebuilding them would clear that durable state, so only the
	// applied-key set is read back from the journal.
	if replayFrom != 1 {
		if err := restoreAppliedTxKeys(sess, meta.LatestNonce); err != nil {
			return nil, nil, err
		}
		return sess, sm, nil
	}
	if err := storage.RebuildValidationObsFromJournal(
		sess.store,
		sess.escrowID,
		1,
		meta.LatestNonce,
		storage.SealedInferenceIDsSorted(sm.ExportSealedNonces()),
		func(page []types.DiffRecord) error {
			noteAppliedTxKeys(sess, page)
			return nil
		},
	); err != nil {
		return nil, nil, fmt.Errorf("rebuild validation obs: %w", err)
	}
	return sess, sm, nil
}

// restoreAppliedTxKeys re-seeds the applied-key set from the journal so a host
// mempool copy of an already-included tx is not re-queued. A hole leaves the
// keys read so far: dedup is best effort, the state is already recovered.
func restoreAppliedTxKeys(sess *Session, latest uint64) error {
	err := storage.ReadDiffPages(sess.store, sess.escrowID, 1, latest, func(page []types.DiffRecord) error {
		noteAppliedTxKeys(sess, page)
		return nil
	})
	var gap *storage.DiffGapError
	if errors.As(err, &gap) {
		log.Printf("recover_session escrow=%s applied_tx_keys_partial: %v", sess.escrowID, gap)
		return nil
	}
	if err != nil {
		return fmt.Errorf("restore applied tx keys 1..%d: %w", latest, err)
	}
	return nil
}

// noteAppliedTxKeys records keys from one journal page. The page is not retained.
func noteAppliedTxKeys(sess *Session, page []types.DiffRecord) {
	if sess == nil {
		return
	}
	sess.mu.Lock()
	defer sess.mu.Unlock()
	for _, rec := range page {
		noteAppliedTxsLocked(sess, rec.Txs)
	}
}

func noteAppliedTxsLocked(sess *Session, txs []*types.DevshardTx) {
	for _, tx := range txs {
		if key := devshardTxKey(tx); key != "" {
			sess.appliedTxKeys[key] = struct{}{}
		}
	}
	if len(sess.appliedTxKeys) > maxAppliedTxKeys {
		clear(sess.appliedTxKeys)
	}
}

// restoreHeartbeatProducer restores the producer from the reconstructed log
// (spec §10.4). There is no counter to carry over: a turn is named by the nonce
// its span opens at, so the next span's identity follows from the restored
// nonce. The session tracker is a clone of the SM's so compose can
// report turn N's sync_vector without sharing the SM mutex. Wall-clock
// lastTurnover is not persisted: a recovered quiet session is due immediately
// rather than waiting out Interval from a lost t_last. An in-flight turn
// still suppresses the next span until TurnTimeout, measured from recovery.
func restoreHeartbeatProducer(sess *Session, sm *state.StateMachine) {
	if sess == nil || sm == nil {
		return
	}
	sess.mu.Lock()
	defer sess.mu.Unlock()
	if clone := sm.HeightSyncCloneTurnTracker(); clone != nil {
		sess.turnTracker = clone
	}
	if sess.heartbeat == nil {
		return
	}
	if rec := sess.turnTracker.Latest(); rec != nil && rec.State == heightsync.TurnOpen {
		sess.heartbeat.OpenTurn(sess.nowLocked())
		return
	}
	sess.heartbeat.SettleTurn()
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
	writeSnapshot(store, escrowID, nonce, sm.ExportState(), cursor, sm.ExportCommittedEntries(), sm.ExportSealedNonces(), sm.ExportHeightSyncFloor())
}

// writeSnapshot persists a pre-prepared snapshot blob. The caller must
// have already deep-copied state and cursor (so this function performs
// only the JSON marshal + storage write and can run without any session
// or state-machine locks held -- this is what enables async background
// snapshots from the runtime hot path).
func writeSnapshot(store storage.Storage, escrowID string, nonce uint64, state *types.EscrowState, cursor map[int]uint64, committedEntries map[uint64][]byte, sealedNonces map[uint64]uint64, heightSyncFloor *types.FloorIndexProto) {
	_ = writeSnapshotErr(store, escrowID, nonce, state, cursor, committedEntries, sealedNonces, heightSyncFloor)
}

// writeSnapshotErr is writeSnapshot with an error return, for synchronous
// callers (e.g. Session.FlushSnapshot on retire) that want to know whether the
// snapshot landed. It logs on failure exactly like writeSnapshot.
func writeSnapshotErr(store storage.Storage, escrowID string, nonce uint64, state *types.EscrowState, cursor map[int]uint64, committedEntries map[uint64][]byte, sealedNonces map[uint64]uint64, heightSyncFloor *types.FloorIndexProto) error {
	blob := sessionSnapshot{State: state, HostSyncNonce: cursor, CommittedEntries: committedEntries, SealedNonces: sealedNonces, HeightSyncFloor: heightSyncFloor}
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
