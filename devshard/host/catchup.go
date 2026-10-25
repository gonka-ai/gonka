package host

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"google.golang.org/protobuf/proto"

	devshard "devshard"
	"devshard/heightsync"
	"devshard/logging"
	"devshard/signing"
	"devshard/storage"
	"devshard/types"
)

// SnapshotCatchUpMinGap is the minimum nonce gap before CatchUpToNonce
// prefers a peer state snapshot over paged diff replay. At or below one
// storage diff page (64 nonces), a single GetDiffPages round-trip already
// covers the gap without snapshot marshaling or root-audit overhead.
const SnapshotCatchUpMinGap = uint64(storage.DiffPageMaxNonces)

// ErrCatchUpIncomplete reports that peer catch-up could not reach the
// minimum nonce required to bridge an incoming request.
var ErrCatchUpIncomplete = errors.New("peer catch-up could not reach required nonce")

// PeerSnapshot bundles an encoded EscrowState snapshot with the creator-signed
// diffs and host state signatures needed for zero-trust verification.
type PeerSnapshot struct {
	Nonce        uint64
	Snapshot     []byte
	AnchorDiff   *types.Diff
	FloorRefDiff *types.Diff
	Signatures   map[uint32][]byte
}

// PeerCatchUpClient is the read-only peer client surface used by a lagging
// host to self-heal missing session state from group peers.
type PeerCatchUpClient interface {
	GetSnapshot(ctx context.Context, maxNonce uint64) (*PeerSnapshot, error)
	GetDiffPages(ctx context.Context, fromNonce, toNonce uint64, fn func([]types.Diff) error) error
	GetSignatures(ctx context.Context, nonce uint64) (map[uint32][]byte, error)
}

// PeerCatchUpSource resolves the outbound catch-up client for a peer slot.
type PeerCatchUpSource func(ctx context.Context, slot int) (PeerCatchUpClient, error)

// WithCatchUpSource configures peer catch-up on a Host at construction time.
func WithCatchUpSource(src PeerCatchUpSource) HostOption {
	return func(h *Host) { h.catchUpSource = src }
}

// SetCatchUpSource injects or updates the peer catch-up client resolver.
func (h *Host) SetCatchUpSource(src PeerCatchUpSource) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.catchUpSource = src
}

// ServeGetSnapshot returns a snapshot at or below maxNonce (0 = any tip)
// together with the creator-signed anchor diff, optional height-sync floor
// reference diff, and accumulated slot signatures at the snapshot nonce.
func (h *Host) ServeGetSnapshot(maxNonce uint64) (*PeerSnapshot, error) {
	if h.store == nil {
		return nil, storage.ErrSnapshotNotFound
	}

	// Prefer the live in-memory state when its tip is within maxNonce and its
	// anchor diff is already durable: this serves fresh tips even before the
	// periodic SnapshotInterval disk write fires.
	h.mu.Lock()
	liveNonce := h.sm.LatestNonce()
	var liveSnap []byte
	var liveFloor *types.FloorIndexProto
	if liveNonce > 0 && (maxNonce == 0 || liveNonce <= maxNonce) {
		st := h.sm.ExportState()
		committed := h.sm.ExportCommittedEntries()
		sealed := h.sm.ExportSealedNonces()
		liveFloor = h.sm.ExportHeightSyncFloor()
		data, err := MarshalStateSnapshotWithCommitted(st, committed, sealed, liveFloor)
		if err == nil {
			liveSnap = data
		}
	}
	h.mu.Unlock()

	if len(liveSnap) > 0 {
		if snap, err := h.assemblePeerSnapshot(liveNonce, liveSnap, liveFloor); err == nil {
			return snap, nil
		}
	}

	snapNonce, snapBytes, err := h.store.LoadSnapshot(h.escrowID)
	if err != nil {
		return nil, err
	}
	if snapNonce == 0 || (maxNonce > 0 && snapNonce > maxNonce) {
		return nil, storage.ErrSnapshotNotFound
	}
	_, _, _, floorProto, err := UnmarshalStateSnapshotWithCommitted(snapBytes)
	if err != nil {
		return nil, err
	}
	return h.assemblePeerSnapshot(snapNonce, snapBytes, floorProto)
}

func (h *Host) assemblePeerSnapshot(snapNonce uint64, snapBytes []byte, floorProto *types.FloorIndexProto) (*PeerSnapshot, error) {
	anchorRecs, err := h.store.GetDiffs(h.escrowID, snapNonce, snapNonce)
	if err != nil {
		return nil, err
	}
	if len(anchorRecs) == 0 {
		return nil, storage.ErrSnapshotNotFound
	}
	anchor := anchorRecs[0].Diff

	var floorRef *types.Diff
	if floorProto != nil && len(floorProto.Entries) > 0 {
		lastNonce := floorProto.Entries[len(floorProto.Entries)-1].GetNonce()
		if lastNonce == snapNonce {
			cp := anchor
			floorRef = &cp
		} else if lastNonce > 0 && lastNonce < snapNonce {
			refRecs, refErr := h.store.GetDiffs(h.escrowID, lastNonce, lastNonce)
			if refErr != nil || len(refRecs) == 0 {
				return nil, storage.ErrSnapshotNotFound
			}
			cp := refRecs[0].Diff
			floorRef = &cp
		}
	}

	sigs, _ := h.store.GetSignatures(h.escrowID, snapNonce)
	return &PeerSnapshot{
		Nonce:        snapNonce,
		Snapshot:     snapBytes,
		AnchorDiff:   &anchor,
		FloorRefDiff: floorRef,
		Signatures:   sigs,
	}, nil
}

// requestCatchUpTargets returns (minRequiredNonce, targetNonce, needed) for
// an incoming HostRequest given localNonce.
//   - When req.Diffs is empty and req.Nonce > localNonce, the host must reach
//     req.Nonce (e.g. Finalize resume or tip probe).
//   - When req.Diffs starts above localNonce+1, the host must reach at least
//     minDiffNonce-1 so req.Diffs can apply contiguously, and may opportunistically
//     adopt a peer snapshot up to max(minDiffNonce-1, req.Nonce).
func requestCatchUpTargets(localNonce uint64, req HostRequest) (minRequired, target uint64, needed bool) {
	if len(req.Diffs) == 0 {
		if req.Nonce > localNonce {
			return req.Nonce, req.Nonce, true
		}
		return 0, 0, false
	}
	minDiff := req.Diffs[0].Nonce
	maxDiff := req.Diffs[0].Nonce
	for _, d := range req.Diffs[1:] {
		if d.Nonce < minDiff {
			minDiff = d.Nonce
		}
		if d.Nonce > maxDiff {
			maxDiff = d.Nonce
		}
	}
	if minDiff <= localNonce+1 {
		return 0, 0, false
	}
	minRequired = minDiff - 1
	target = max(minRequired, max(maxDiff, req.Nonce))
	return minRequired, target, true
}

// PreCatchUpForRequest runs CatchUpForRequest ahead of HandleRequest (for
// example in ServeInference before inbound height-sync classification) and
// retains any newly applied diffs so HandleRequest still answers heartbeats,
// close-ready stamps, and tip txs from the caught-up range.
func (h *Host) PreCatchUpForRequest(ctx context.Context, req HostRequest) error {
	h.mu.Lock()
	if requestBlockedWhenUnavailable(req) && !h.completionRequestsEnabled() {
		h.mu.Unlock()
		return nil
	}
	hasCatchUp := h.catchUpSource != nil || (len(req.Diffs) == 0 && h.store != nil)
	_, _, need := requestCatchUpTargets(h.sm.LatestNonce(), req)
	h.mu.Unlock()
	if !hasCatchUp || !need {
		return nil
	}
	diffs, err := h.CatchUpForRequest(ctx, req)
	if len(diffs) > 0 {
		h.mu.Lock()
		h.pendingCatchUpDiffs = append(h.pendingCatchUpDiffs, diffs...)
		h.mu.Unlock()
	}
	return err
}

// CatchUpForRequest ensures the host has caught up far enough to serve req,
// returning any diffs applied during catch-up in nonce order.
func (h *Host) CatchUpForRequest(ctx context.Context, req HostRequest) ([]types.Diff, error) {
	minReq, target, needed := requestCatchUpTargets(h.LatestNonce(), req)
	if !needed {
		return nil, nil
	}
	return h.catchUpRange(ctx, minReq, target)
}

// CatchUpToNonce catches the host up to targetNonce from local durable storage
// or group peers. Returns any diffs applied during catch-up.
func (h *Host) CatchUpToNonce(ctx context.Context, targetNonce uint64) ([]types.Diff, error) {
	if targetNonce <= h.LatestNonce() {
		return nil, nil
	}
	return h.catchUpRange(ctx, targetNonce, targetNonce)
}

func (h *Host) catchUpRange(ctx context.Context, minRequired, targetNonce uint64) ([]types.Diff, error) {
	if maxChain := h.chainMaxNonce(); maxChain > 0 && minRequired > uint64(maxChain) {
		return nil, fmt.Errorf("%w: nonce %d exceeds chain maximum %d", types.ErrNonceLimitExceeded, minRequired, maxChain)
	}
	if maxChain := h.chainMaxNonce(); maxChain > 0 && targetNonce > uint64(maxChain) {
		targetNonce = uint64(maxChain)
	}

	h.catchUpMu.Lock()
	defer h.catchUpMu.Unlock()

	// Step 1: fast-forward from shared/durable local storage if another replica
	// or earlier write already persisted ahead of in-memory state.
	if h.store != nil && h.LatestNonce() < minRequired {
		h.mu.Lock()
		cur := h.sm.LatestNonce()
		if cur < minRequired {
			_ = h.applyDurablePagesLocked(ctx, cur+1, minRequired, "catchup", "durable_fast_forward", minRequired)
		}
		h.mu.Unlock()
	}

	localNonce := h.LatestNonce()
	if localNonce >= minRequired {
		return nil, nil
	}

	h.mu.Lock()
	src := h.catchUpSource
	h.mu.Unlock()
	if src == nil {
		return nil, nil
	}

	peerSlots := h.peerCatchUpSlots()
	if len(peerSlots) == 0 {
		return nil, nil
	}

	var applied []types.Diff

	// Step 2: when the gap exceeds one diff page (64 nonces), try zero-trust
	// snapshot catch-up first to avoid replaying hundreds or thousands of pages.
	if targetNonce-localNonce > SnapshotCatchUpMinGap {
		if anchorDiff, ok := h.tryAdoptPeerSnapshot(ctx, src, peerSlots, localNonce, targetNonce); ok {
			applied = append(applied, anchorDiff)
			localNonce = h.LatestNonce()
		}
	}

	// Step 3: bridge any remaining tail (or full range when below threshold /
	// no peer snapshot was available) via paged creator-signed diffs. When
	// concurrent fan-out (e.g. Finalize) is still persisting minRequired on a
	// peer, retry briefly before giving up.
	if localNonce < targetNonce {
		tail, _ := h.catchUpViaDiffPages(ctx, src, peerSlots, targetNonce)
		applied = append(applied, tail...)
	}
	for _, wait := range []time.Duration{20 * time.Millisecond, 60 * time.Millisecond, 150 * time.Millisecond} {
		if h.LatestNonce() >= minRequired || ctx.Err() != nil {
			break
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
		case <-timer.C:
		}
		if ctx.Err() != nil {
			break
		}
		tail, _ := h.catchUpViaDiffPages(ctx, src, peerSlots, targetNonce)
		applied = append(applied, tail...)
	}

	if h.LatestNonce() < minRequired {
		return applied, fmt.Errorf("%w: local=%d required=%d target=%d",
			ErrCatchUpIncomplete, h.LatestNonce(), minRequired, targetNonce)
	}
	return applied, nil
}

func (h *Host) peerCatchUpSlots() []int {
	seen := make(map[string]bool, len(h.group))
	var slots []int
	for _, s := range h.group {
		if h.slotIDs[s.SlotID] {
			continue
		}
		if seen[s.ValidatorAddress] {
			continue
		}
		seen[s.ValidatorAddress] = true
		slots = append(slots, int(s.SlotID))
	}
	slices.Sort(slots)
	return slots
}

func (h *Host) tryAdoptPeerSnapshot(
	ctx context.Context,
	src PeerCatchUpSource,
	peerSlots []int,
	localNonce, maxNonce uint64,
) (types.Diff, bool) {
	for _, slot := range peerSlots {
		if ctx.Err() != nil {
			return types.Diff{}, false
		}
		client, err := src(ctx, slot)
		if err != nil || client == nil {
			continue
		}
		snap, err := client.GetSnapshot(ctx, maxNonce)
		if err != nil || snap == nil {
			continue
		}
		if snap.Nonce <= localNonce || snap.Nonce > maxNonce {
			continue
		}
		if snap.Nonce-localNonce <= SnapshotCatchUpMinGap {
			continue
		}
		anchor, adoptErr := h.verifyAndInstallSnapshot(ctx, src, peerSlots, snap)
		if adoptErr != nil {
			logging.Debug("peer snapshot rejected during catch-up",
				"subsystem", "host",
				"escrow_id", h.escrowID,
				"peer_slot", slot,
				"snapshot_nonce", snap.Nonce,
				"error", adoptErr)
			continue
		}
		logging.Info("adopted verified peer snapshot",
			"subsystem", "host",
			"escrow_id", h.escrowID,
			"peer_slot", slot,
			"snapshot_nonce", snap.Nonce)
		return anchor, true
	}
	return types.Diff{}, false
}

func (h *Host) verifyAndInstallSnapshot(
	ctx context.Context,
	src PeerCatchUpSource,
	peerSlots []int,
	snap *PeerSnapshot,
) (types.Diff, error) {
	if snap.AnchorDiff == nil {
		return types.Diff{}, fmt.Errorf("missing anchor diff")
	}
	if snap.AnchorDiff.Nonce != snap.Nonce {
		return types.Diff{}, fmt.Errorf("anchor diff nonce %d != snapshot nonce %d", snap.AnchorDiff.Nonce, snap.Nonce)
	}
	if len(snap.AnchorDiff.PostStateRoot) == 0 {
		return types.Diff{}, fmt.Errorf("empty anchor post_state_root")
	}
	if err := h.sm.VerifyDiffUserSig(*snap.AnchorDiff); err != nil {
		return types.Diff{}, fmt.Errorf("anchor diff user sig: %w", err)
	}

	snapState, committedEntries, sealedNonces, floorProto, err := UnmarshalStateSnapshotWithCommitted(snap.Snapshot)
	if err != nil {
		return types.Diff{}, fmt.Errorf("unmarshal snapshot: %w", err)
	}
	if snapState == nil || snapState.LatestNonce != snap.Nonce {
		gotNonce := uint64(0)
		if snapState != nil {
			gotNonce = snapState.LatestNonce
		}
		return types.Diff{}, fmt.Errorf("snapshot state latest_nonce %d != %d", gotNonce, snap.Nonce)
	}
	if err := h.sm.ValidateSnapshotImmutable(snapState); err != nil {
		return types.Diff{}, err
	}

	verifiedSigs, err := h.verifySnapshotPeerSignatures(ctx, src, peerSlots, snap, snapState)
	if err != nil {
		return types.Diff{}, err
	}

	floor, floorRefToPersist, err := h.verifySnapshotFloor(snap, floorProto)
	if err != nil {
		return types.Diff{}, err
	}

	scratch := h.sm.NewScratch()
	if err := scratch.RestoreStateWithFloor(snapState, floor); err != nil {
		return types.Diff{}, fmt.Errorf("restore scratch state: %w", err)
	}
	if err := scratch.RestoreCommittedEntries(committedEntries); err != nil {
		return types.Diff{}, fmt.Errorf("audit committed entries: %w", err)
	}
	scratch.RestoreSealedNonces(sealedNonces)

	gotRoot, err := scratch.ComputeStateRoot()
	if err != nil {
		return types.Diff{}, fmt.Errorf("compute scratch root: %w", err)
	}
	if !bytes.Equal(gotRoot, snap.AnchorDiff.PostStateRoot) {
		return types.Diff{}, fmt.Errorf("snapshot root %x != signed anchor root %x at nonce %d",
			gotRoot, snap.AnchorDiff.PostStateRoot, snap.Nonce)
	}

	if h.store != nil {
		var batch []types.DiffRecord
		if floorRefToPersist != nil && floorRefToPersist.Nonce != snap.Nonce {
			batch = append(batch, types.DiffRecord{
				Diff:      *floorRefToPersist,
				StateHash: floorRefToPersist.PostStateRoot,
			})
		}
		batch = append(batch, types.DiffRecord{
			Diff:       *snap.AnchorDiff,
			StateHash:  snap.AnchorDiff.PostStateRoot,
			Signatures: verifiedSigs,
		})
		if err := storage.PersistDiffBatch(ctx, h.store, h.escrowID, batch); err != nil {
			return types.Diff{}, fmt.Errorf("persist snapshot anchor diff: %w", err)
		}
		if err := h.store.SaveSnapshot(h.escrowID, snap.Nonce, snap.Snapshot); err != nil {
			return types.Diff{}, fmt.Errorf("save adopted snapshot: %w", err)
		}
	}

	h.mu.Lock()
	adopted := h.sm.AdoptVerifiedScratch(scratch)
	if adopted {
		h.refreshWarmSlotsLocked(snapState.WarmKeys)
		h.mempool.RemoveIncluded(snap.AnchorDiff.Txs)
		h.mempool.PruneAdoptedSnapshot(snapState)
	}
	h.mu.Unlock()
	if !adopted {
		return types.Diff{}, fmt.Errorf("local state already reached nonce %d", snap.Nonce)
	}
	return *snap.AnchorDiff, nil
}

// verifySnapshotPeerSignatures ensures at least f+1 = (totalSlots-1)/3 + 1
// peer slots (capped at total non-local slots) signed StateSignatureContent
// over (escrowID, snap.Nonce, anchorDiff.PostStateRoot). Combined with the
// creator's signature on anchorDiff and matching ComputeStateRoot(), this
// prevents even a colluding creator + f Byzantine peers from forging state.
func (h *Host) verifySnapshotPeerSignatures(
	ctx context.Context,
	src PeerCatchUpSource,
	peerSlots []int,
	snap *PeerSnapshot,
	snapState *types.EscrowState,
) (map[uint32][]byte, error) {
	nonLocalSlots := 0
	for _, s := range h.group {
		if !h.slotIDs[s.SlotID] {
			nonLocalSlots++
		}
	}
	if nonLocalSlots == 0 {
		return nil, nil
	}
	needed := (len(h.group)-1)/3 + 1
	if needed > nonLocalSlots {
		needed = nonLocalSlots
	}

	sigContent := &types.StateSignatureContent{
		StateRoot: snap.AnchorDiff.PostStateRoot,
		EscrowId:  h.escrowID,
		Nonce:     snap.Nonce,
	}
	sigData, err := proto.Marshal(sigContent)
	if err != nil {
		return nil, fmt.Errorf("marshal state sig content: %w", err)
	}
	verifier := h.effectiveVerifier()

	verified := make(map[uint32][]byte)
	tryAbsorb := func(candidates map[uint32][]byte) {
		for slotID, sig := range candidates {
			if h.slotIDs[slotID] || len(sig) == 0 {
				continue
			}
			if _, already := verified[slotID]; already {
				continue
			}
			coldAddr, ok := h.slotToAddr[slotID]
			if !ok {
				continue
			}
			recovered, recErr := verifier.RecoverAddress(sigData, sig)
			if recErr != nil {
				continue
			}
			warmAddr := snapState.WarmKeys[slotID]
			if recovered == coldAddr || (warmAddr != "" && recovered == warmAddr) || h.sm.CheckWarmKey(recovered, coldAddr) {
				verified[slotID] = append([]byte(nil), sig...)
			}
		}
	}

	tryAbsorb(snap.Signatures)
	for _, slot := range peerSlots {
		if len(verified) >= needed {
			break
		}
		if ctx.Err() != nil {
			break
		}
		client, cErr := src(ctx, slot)
		if cErr != nil || client == nil {
			continue
		}
		more, sErr := client.GetSignatures(ctx, snap.Nonce)
		if sErr != nil {
			continue
		}
		tryAbsorb(more)
	}

	if len(verified) < needed {
		return nil, fmt.Errorf("insufficient peer state signatures at nonce %d: got %d, need %d",
			snap.Nonce, len(verified), needed)
	}
	return verified, nil
}

// verifySnapshotFloor validates the un-hashed height-sync floor index carried
// alongside the snapshot. For any future producing nonce m > snap.Nonce,
// FloorIndex.AsOf(m) consults only the final entry in floorProto.Entries, so
// verifying that entry against its creator-signed diff prevents a peer from
// injecting an unbacked floor height/hash.
func (h *Host) verifySnapshotFloor(
	snap *PeerSnapshot,
	floorProto *types.FloorIndexProto,
) (*heightsync.FloorIndex, *types.Diff, error) {
	if floorProto == nil || len(floorProto.Entries) == 0 {
		return heightsync.NewFloorIndex(), nil, nil
	}
	floor, err := heightsync.FloorIndexFromProto(heightsync.FloorConfig{}, floorProto)
	if err != nil {
		return nil, nil, fmt.Errorf("invalid snapshot floor proto: %w", err)
	}
	last := floorProto.Entries[len(floorProto.Entries)-1]
	if last.Nonce == 0 || last.Nonce > snap.Nonce {
		return nil, nil, fmt.Errorf("snapshot floor tip nonce %d out of range 1..%d", last.Nonce, snap.Nonce)
	}
	if last.Author != heightsync.SequencerSigner && int(last.Author) >= len(h.group) {
		return nil, nil, fmt.Errorf("snapshot floor tip author %d out of group", last.Author)
	}

	var refDiff *types.Diff
	if last.Nonce == snap.Nonce {
		refDiff = snap.AnchorDiff
	} else {
		if snap.FloorRefDiff == nil || snap.FloorRefDiff.Nonce != last.Nonce {
			return nil, nil, fmt.Errorf("missing floor reference diff at nonce %d", last.Nonce)
		}
		if err := h.sm.VerifyDiffUserSig(*snap.FloorRefDiff); err != nil {
			return nil, nil, fmt.Errorf("floor reference diff sig at nonce %d: %w", last.Nonce, err)
		}
		refDiff = snap.FloorRefDiff
	}
	if !diffCarriesHeightStamp(refDiff.Txs, last.Height, last.Hash) {
		return nil, nil, fmt.Errorf("floor tip (%d, %x) not present in signed diff %d",
			last.Height, last.Hash, last.Nonce)
	}
	return floor, refDiff, nil
}

func diffCarriesHeightStamp(txs []*types.DevshardTx, wantHeight uint64, wantHash []byte) bool {
	for _, tx := range txs {
		h, hash, ok := heightsync.RefStamp(tx)
		if ok && h == wantHeight && bytes.Equal(hash, wantHash) {
			return true
		}
	}
	return false
}

func (h *Host) catchUpViaDiffPages(
	ctx context.Context,
	src PeerCatchUpSource,
	peerSlots []int,
	targetNonce uint64,
) ([]types.Diff, error) {
	var allApplied []types.Diff
	var lastErr error
	for _, slot := range peerSlots {
		fromNonce := h.LatestNonce() + 1
		if fromNonce > targetNonce {
			return allApplied, nil
		}
		if err := ctx.Err(); err != nil {
			return allApplied, err
		}
		client, err := src(ctx, slot)
		if err != nil || client == nil {
			if err != nil {
				lastErr = err
			}
			continue
		}
		err = client.GetDiffPages(ctx, fromNonce, targetNonce, func(page []types.Diff) error {
			if len(page) == 0 {
				return nil
			}
			h.mu.Lock()
			newly, applyErr := h.applyDiffsAwaitingHeadersLocked(ctx, page)
			h.mu.Unlock()
			if len(newly) > 0 {
				allApplied = append(allApplied, newly...)
			}
			return applyErr
		})
		if err != nil {
			lastErr = err
			logging.Debug("peer diff-page catch-up failed",
				"subsystem", "host",
				"escrow_id", h.escrowID,
				"peer_slot", slot,
				"target_nonce", targetNonce,
				"error", err)
		}
		if h.LatestNonce() >= targetNonce {
			return allApplied, nil
		}
	}
	return allApplied, lastErr
}

func (h *Host) effectiveVerifier() signing.Verifier {
	if h.verifier != nil {
		return h.verifier
	}
	return signing.NewSecp256k1Verifier()
}

func (h *Host) refreshWarmSlotsLocked(warmKeys map[uint32]string) {
	if len(warmKeys) == 0 {
		return
	}
	addr := h.signer.Address()
	changed := false
	for slotID, warmAddr := range warmKeys {
		if warmAddr == addr && !h.slotIDs[slotID] {
			h.slotIDs[slotID] = true
			changed = true
		}
	}
	if changed {
		slots := make([]uint32, 0, len(h.slotIDs))
		for s := range h.slotIDs {
			slots = append(slots, s)
		}
		slices.Sort(slots)
		h.sortedSlots = slots
	}
}

// recoverStartedFinishes scans live StatusStarted inferences assigned to this
// host that have no queued finish and attempts synchronous RecoveryStoredOnly
// reconstruction from payload storage. This ensures a restarted/failover host
// queried with Payload == nil (e.g. Finalize resume) returns its owed
// MsgFinishInference in HostResponse.Mempool before settlement forfeits the
// inference at reserved_cost.
func (h *Host) recoverStartedFinishes(ctx context.Context) {
	h.mu.Lock()
	if h.storedResponse == nil || h.engine == nil {
		h.mu.Unlock()
		return
	}
	infs := h.sm.SnapshotInferences()
	var jobs []*devshard.ExecuteRequest
	for id, rec := range infs {
		if rec == nil || rec.Status != types.StatusStarted {
			continue
		}
		executorSlot := h.group[id%uint64(len(h.group))].SlotID
		if !h.slotIDs[executorSlot] {
			continue
		}
		if h.mempool.HasFinish(id) {
			continue
		}
		if _, busy := h.executing[id]; busy {
			continue
		}
		job := h.newExecuteRequest(id, *rec, nil)
		job.Recovery = devshard.RecoveryStoredOnly
		h.executing[id] = struct{}{}
		jobs = append(jobs, job)
	}
	h.mu.Unlock()

	if len(jobs) == 0 {
		return
	}
	slices.SortFunc(jobs, func(a, b *devshard.ExecuteRequest) int {
		if a.InferenceID < b.InferenceID {
			return -1
		}
		if a.InferenceID > b.InferenceID {
			return 1
		}
		return 0
	})
	for _, job := range jobs {
		if ctx.Err() != nil {
			h.ReleaseExecution(job.InferenceID)
			continue
		}
		_, _ = h.RunExecution(ctx, job)
	}
}
