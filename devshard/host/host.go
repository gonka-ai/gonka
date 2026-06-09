package host

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"google.golang.org/protobuf/proto"

	"devshard"
	"devshard/gossip"
	"devshard/logging"
	"devshard/observability"
	"devshard/signing"
	"devshard/state"
	"devshard/storage"
	"devshard/types"
)

// finishGossipGraceRotations is the number of full slot rotations to wait
// before re-broadcasting a locally-proposed MsgFinishInference that the user
// sequencer has not yet included in a diff. With round-robin host selection
// (nonce % len(group)), one rotation = len(group) nonces, so the effective
// grace is finishGossipGraceRotations * len(group) nonces.
//
// Two rotations gives the user two natural chances to pick up the Finish
// from the executor host's devshard_meta tail (once per rotation) before we
// fall back to peer-to-peer recovery gossip. Increase if direct-contact
// recovery should be preferred more strongly; decrease for snappier gossip.
const finishGossipGraceRotations uint64 = 2

// InferencePayload carries the actual request data for the current inference.
// The host verifies these against the signed MsgStartInference in the diff.
type InferencePayload struct {
	Prompt      []byte
	Model       string
	InputLength uint64
	MaxTokens   uint64
	StartedAt   int64
}

// HostRequest carries diffs from the user to a host.
type HostRequest struct {
	Diffs   []types.Diff
	Nonce   uint64            // nonce of the current request
	Payload *InferencePayload // nil if no new inference (e.g., Finalize, empty diffs)
}

// HostResponse carries the host's reply back to the user.
type HostResponse struct {
	StateSig           []byte // nil = withheld
	StateHash          []byte // always set after applying diffs
	Nonce              uint64 // current nonce after applying diffs
	Receipt            []byte // executor receipt sig, nil if not executor
	ConfirmedAt        int64  // executor wall-clock timestamp, 0 if not executor
	Mempool            []*types.DevshardTx
	ExecutionJob       *devshard.ExecuteRequest // non-nil if this host is the executor and execution is deferred
	CachedResponseBody []byte // non-nil when reconnecting to a completed inference
	StreamBytesRead    int64  // total bytes read from the host HTTP response body (SSE streams only)
	InferenceID        uint64
	ReceiptExpected    bool
	ReceiptReason      observability.Reason
	ExecutionExpected  bool
}

type receiptOutcome struct {
	inferenceID       uint64
	receiptExpected   bool
	reason            observability.Reason
	executionExpected bool
}

// AcceptanceChecker is an optional hook that lets the host withhold its
// signature when a diff contains content the host considers unacceptable
// (e.g. suspicious timestamps, insufficient max_cost). Return a non-nil
// error to withhold; nil to allow signing.
type AcceptanceChecker interface {
	Check(st types.EscrowState, applied []*types.DevshardTx) error
}

const (
	defaultValidationWorkers   = 20
	defaultValidationQueueSize = 20_000
)

// Host processes user requests: applies diffs, executes inference, signs state.
type Host struct {
	mu           sync.Mutex
	sm           *state.StateMachine
	signer       signing.Signer
	verifier     signing.Verifier
	engine       devshard.InferenceEngine
	validator    devshard.ValidationEngine // optional, nil = no validation
	escrowID     string
	epochID      uint64
	slotIDs      map[uint32]bool
	group        []types.SlotAssignment
	mempool      *Mempool
	checker      AcceptanceChecker
	store        storage.Storage // optional, nil = no persistence
	gsp          *gossip.Gossip  // optional, nil = no gossip pruning
	availability devshard.AvailabilityProvider

	snapshotInFlight      atomic.Bool  // prevents overlapping async snapshot writes
	validationObsInFlight atomic.Int32 // caps concurrent async validation-obs writes

	// Lookup maps built from group at construction time.
	slotToAddr  map[uint32]string   // slotID -> validator address
	addrToSlots map[string][]uint32 // address -> all slotIDs owned

	sortedSlots        []uint32            // deterministic slot order for this host
	executing          map[uint64]struct{} // inference IDs with in-flight execution
	validating         map[uint64]struct{} // inference IDs with queued or in-flight validation
	validationQueue    chan validateJob
	completedResponses map[uint64][]byte // inference ID -> cached ML response body
	ownSeed            int64             // deterministic seed derived from signer + escrowID

	// Payload prune tracking. These fields are host-local off-state and must
	// NOT participate in the state root or snapshot. The deterministic seal now
	// lives in the state machine (autoSealLocked); the host only emits a
	// payload-prune event for each inference that the applied diff sealed.
	pruneSink   PruneEventSink
	prunedFired map[uint64]struct{}       // inference IDs we've already emitted a prune for
	maxNonce    devshard.MaxNonceProvider // nil = do not enforce
}

// SnapshotInterval controls how often hosts persist full state snapshots.
const SnapshotInterval = 500

func NewHost(
	sm *state.StateMachine,
	signer signing.Signer,
	engine devshard.InferenceEngine,
	escrowID string,
	group []types.SlotAssignment,
	checker AcceptanceChecker,
	opts ...HostOption,
) (*Host, error) {
	if err := types.ValidateGroup(group); err != nil {
		return nil, err
	}
	addr := signer.Address()
	slotIDs := make(map[uint32]bool)
	slotToAddr := make(map[uint32]string, len(group))
	addrToSlots := make(map[string][]uint32, len(group))
	for _, s := range group {
		slotToAddr[s.SlotID] = s.ValidatorAddress
		addrToSlots[s.ValidatorAddress] = append(addrToSlots[s.ValidatorAddress], s.SlotID)
		if s.ValidatorAddress == addr {
			slotIDs[s.SlotID] = true
		}
	}

	// Check state's WarmKeys for existing bindings, then try the warm key
	// resolver directly (without caching in SM state, which would change
	// the state root before any diffs are applied).
	if len(slotIDs) == 0 {
		warmKeys := sm.WarmKeys()
		for slotID, warmAddr := range warmKeys {
			if warmAddr == addr {
				slotIDs[slotID] = true
			}
		}
	}
	if len(slotIDs) == 0 {
		for _, s := range group {
			if sm.CheckWarmKey(addr, s.ValidatorAddress) {
				slotIDs[s.SlotID] = true
			}
		}
	}

	if len(slotIDs) == 0 {
		return nil, fmt.Errorf("%w: %s", types.ErrHostNotInGroup, addr)
	}

	sortedSlots := slices.Sorted(maps.Keys(slotIDs))

	// Derive deterministic seed from signer + escrowID.
	seedSig, err := signer.Sign([]byte(escrowID))
	if err != nil {
		return nil, fmt.Errorf("derive seed: %w", err)
	}
	ownSeed, err := state.DeriveSeed(seedSig)
	if err != nil {
		return nil, fmt.Errorf("derive seed: %w", err)
	}

	h := &Host{
		sm:                    sm,
		signer:                signer,
		engine:                engine,
		escrowID:              escrowID,
		slotIDs:               slotIDs,
		group:                 group,
		mempool:               NewMempool(),
		checker:               checker,
		slotToAddr:            slotToAddr,
		addrToSlots:           addrToSlots,
		sortedSlots:           sortedSlots,
		executing:             make(map[uint64]struct{}),
		validating:            make(map[uint64]struct{}),
		completedResponses:    make(map[uint64][]byte),
		ownSeed:               ownSeed,
		prunedFired:           make(map[uint64]struct{}),
	}
	for _, opt := range opts {
		opt(h)
	}
	if h.validator != nil {
		h.validationQueue = make(chan validateJob, defaultValidationQueueSize)
		h.startValidationWorkers(defaultValidationWorkers)
	}
	return h, nil
}

// HostMempool returns the host's mempool. Use this to construct a
// StalenessChecker after host creation, then set it via WithChecker option
// or pass it during construction.
func (h *Host) HostMempool() *Mempool { return h.mempool }

// HostOption configures optional Host behavior.
type HostOption func(*Host)

// WithStorage sets the storage backend for diff persistence.
func WithStorage(s storage.Storage) HostOption {
	return func(h *Host) { h.store = s }
}

// WithEpochID pins the host to the mainnet epoch stored on its DevshardEscrow.
// Payload storage and validation use this epoch to route across epoch changes.
func WithEpochID(epochID uint64) HostOption {
	return func(h *Host) { h.epochID = epochID }
}

// WithVerifier sets the signature verifier for gossip sig accumulation.
func WithVerifier(v signing.Verifier) HostOption {
	return func(h *Host) { h.verifier = v }
}

// WithGossip sets the gossip instance for pruning on finalization.
func WithGossip(g *gossip.Gossip) HostOption {
	return func(h *Host) { h.gsp = g }
}

// WithValidator sets the validation engine for validating other hosts' inferences.
func WithValidator(v devshard.ValidationEngine) HostOption {
	return func(h *Host) { h.validator = v }
}

func WithAvailabilityProvider(p devshard.AvailabilityProvider) HostOption {
	return func(h *Host) { h.availability = p }
}

// WithMaxNonceProvider enforces chain max_nonce on the host, reserving
// FinalizeNonceReserve(groupSize) nonces so settlement can succeed on-chain.
func WithMaxNonceProvider(p devshard.MaxNonceProvider) HostOption {
	return func(h *Host) { h.maxNonce = p }
}

// WithGrace adds a StalenessChecker to the host's acceptance chain.
// If a checker was already set via the constructor, both are composed
// via CompositeChecker.
func WithGrace(grace uint64) HostOption {
	return func(h *Host) {
		sc := NewStalenessChecker(h.mempool, grace)
		if h.checker != nil {
			h.checker = NewCompositeChecker(sc, h.checker)
		} else {
			h.checker = sc
		}
	}
}

// WithPruneSink installs a sink that receives InferencePruneEvent emissions
// after each applied diff. Tier A (terminal-status) and Tier C (stale Finished)
// events both flow through this hook. Default is nil, in which case the host
// emits nothing and behaves exactly as before.
func WithPruneSink(s PruneEventSink) HostOption {
	return func(h *Host) { h.pruneSink = s }
}

func (h *Host) StateRoot() ([]byte, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.sm.ComputeStateRoot()
}

func (h *Host) MempoolTxs() []*types.DevshardTx {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.mempool.Txs()
}

func (h *Host) EscrowID() string              { return h.escrowID }
func (h *Host) Group() []types.SlotAssignment { return h.group }
func (h *Host) SlotIDs() map[uint32]bool      { return h.slotIDs }

// PrimarySlot returns the lowest slot ID owned by this host.
// Deterministic: derived from sortedSlots which is sorted at construction time.
func (h *Host) PrimarySlot() uint32 { return h.sortedSlots[0] }

// IsGroupMemberAddr returns true if addr is a group member (owns at least one slot).
// Safe to call without locking -- addrToSlots is immutable after construction.
func (h *Host) IsGroupMemberAddr(addr string) bool {
	_, ok := h.addrToSlots[addr]
	return ok
}

// SnapshotState returns a deep copy of the current state.
func (h *Host) SnapshotState() types.EscrowState {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.sm.SnapshotState()
}

// IsWarmKeyAddress returns true if addr is a known warm key in the current state.
func (h *Host) IsWarmKeyAddress(addr string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.sm.IsWarmKeyAddress(addr)
}

// IsWarmKeyForSlot returns true if addr is an authorized warm key for the
// given slot, either via existing state bindings or via the bridge resolver.
func (h *Host) IsWarmKeyForSlot(addr string, slotID uint32) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	warmKeys := h.sm.WarmKeys()
	if warmKeys[slotID] == addr {
		return true
	}
	expected, ok := h.slotToAddr[slotID]
	return ok && h.sm.CheckWarmKey(addr, expected)
}

func (h *Host) Signer() signing.Signer { return h.signer }

func (h *Host) HandleRequest(ctx context.Context, req HostRequest) (*HostResponse, error) {
	h.mu.Lock()

	if requestBlockedWhenUnavailable(req) && !h.completionRequestsEnabled() {
		avail := h.currentAvailability()
		logging.Debug("completion rejected: devshard_requests_enabled=false",
			"subsystem", "host",
			"enabled", avail.Enabled,
			"epochID", avail.EpochID,
			"availabilityTime", avail.Time,
		)
		h.mu.Unlock()
		return nil, devshard.ErrRequestsDisabled
	}

	// (a) Apply all new diffs.
	var lastAppliedTxs []*types.DevshardTx
	diffsApplied := false
	for _, diff := range req.Diffs {
		if err := h.checkDiffNonceLimitLocked(diff); err != nil {
			h.mu.Unlock()
			return nil, err
		}
		if err := h.applyAndPersist(diff); err != nil {
			h.mu.Unlock()
			return nil, observability.Classify(observability.ReasonApplyErr, observability.WhereHostApplyDiff, err)
		}
		lastAppliedTxs = diff.Txs
		diffsApplied = true
	}

	// (b) Sign executor receipt (sync, under mutex).
	receipt, confirmedAt, job, cachedBody, receiptOutcome, err := h.signReceipt(req)
	if err != nil {
		h.mu.Unlock()
		return nil, err
	}

	// (c) Sign state (with acceptance check + mempool staleness).
	stateSig, root, nonce, err := h.signIfAccepted(lastAppliedTxs)
	if err != nil {
		h.mu.Unlock()
		return nil, observability.Classify(observability.ReasonStateSignErr, observability.WhereHostSignState, err)
	}
	if stateSig == nil {
		observability.Log(ctx, observability.LevelInfo, "state signature withheld", observability.StageReceipt, observability.WhereHostSignState, h.escrowID, observability.ReasonStateSignatureWithheld, nil,
			"inference_id", receiptOutcome.inferenceID,
			"nonce", nonce)
	}

	// (d) Collect validation candidates under mutex.
	validationJobs := h.collectValidationJobs()

	// (e) Collect locally-proposed Finish txs that the user has not yet
	// absorbed into a diff. Computed under mutex; broadcast outside it.
	var staleFinishes []*types.DevshardTx
	if diffsApplied {
		staleFinishes = h.collectStaleFinishesLocked()
	}

	h.mu.Unlock()

	// (f) Execution job for caller to run via RunExecution.
	// Execution is always deferred so the caller can send the receipt
	// before inference starts (SSE flow).

	// (g) Validate other hosts' inferences outside mutex.
	for _, vj := range validationJobs {
		h.enqueueValidation(vj)
	}

	// (h) Recovery gossip: re-broadcast locally produced Finish that the
	// user sequencer skipped. gossip.BroadcastTxs dedups by tx hash so
	// repeated triggers across diffs are harmless.
	if len(staleFinishes) > 0 && h.gsp != nil {
		go h.broadcastTxsBestEffort(staleFinishes)
	}

	return &HostResponse{
		StateSig:           stateSig,
		StateHash:          root,
		Nonce:              nonce,
		Receipt:            receipt,
		ConfirmedAt:        confirmedAt,
		Mempool:            h.mempool.Txs(),
		ExecutionJob:       job,
		CachedResponseBody: cachedBody,
		InferenceID:        receiptOutcome.inferenceID,
		ReceiptExpected:    receiptOutcome.receiptExpected,
		ReceiptReason:      receiptOutcome.reason,
		ExecutionExpected:  receiptOutcome.executionExpected,
	}, nil
}

func requestBlockedWhenUnavailable(req HostRequest) bool {
	if req.Payload != nil {
		return true
	}
	for _, diff := range req.Diffs {
		for _, tx := range diff.Txs {
			if tx.GetStartInference() != nil ||
				tx.GetTimeoutInference() != nil ||
				tx.GetValidation() != nil ||
				tx.GetValidationVote() != nil {
				return true
			}
		}
	}
	return false
}

func (h *Host) CompletionRequestsEnabled() bool {
	return h.completionRequestsEnabled()
}

func (h *Host) completionRequestsEnabled() bool {
	return h.currentAvailability().Enabled
}

func (h *Host) currentAvailability() devshard.AvailabilityStatus {
	if h.availability == nil {
		return devshard.AvailabilityStatus{Enabled: true}
	}
	return h.availability.CurrentAvailability()
}

// checkDiffNonceLimitLocked enforces chain max_nonce before applying a new diff.
// Caller must hold h.mu.
func (h *Host) checkDiffNonceLimitLocked(diff types.Diff) error {
	currentNonce := h.sm.LatestNonce()
	if diff.Nonce <= currentNonce {
		return nil
	}
	maxNonce := h.chainMaxNonce()
	if maxNonce == 0 {
		return nil
	}
	max := uint64(maxNonce)
	if diff.Nonce > max {
		return fmt.Errorf("%w: nonce %d exceeds chain maximum %d", types.ErrNonceLimitExceeded, diff.Nonce, maxNonce)
	}
	if h.sm.Phase() != types.PhaseActive {
		return nil
	}
	if !types.DiffHasActiveCompletionWork(diff) {
		return nil
	}
	activeCap := types.MaxActiveNonce(maxNonce, len(h.group))
	if diff.Nonce > activeCap {
		reserve := types.FinalizeNonceReserve(len(h.group))
		return fmt.Errorf("%w: nonce %d exceeds active cap %d (reserved %d for finalization/settlement)",
			types.ErrNonceLimitExceeded, diff.Nonce, activeCap, reserve)
	}
	return nil
}

func (h *Host) chainMaxNonce() uint32 {
	if h.maxNonce == nil {
		return 0
	}
	return h.maxNonce.MaxNonce()
}

// applyAndPersist applies a diff, removes included txs from mempool, and persists.
// Captures WarmKeyDelta (new warm key bindings introduced by this diff) for replay.
// Caller must hold h.mu.
func (h *Host) applyAndPersist(diff types.Diff) error {
	currentNonce := h.sm.LatestNonce()
	if diff.Nonce <= currentNonce {
		return nil
	}
	if err := h.checkDiffNonceLimitLocked(diff); err != nil {
		return err
	}
	phaseBefore := h.sm.Phase()
	var warmBefore map[uint32]string
	if h.store != nil {
		warmBefore = h.sm.WarmKeys()
	}
	// Capture the live inference ids before applying so we can detect which
	// ones the deterministic seal (state machine autoSeal) folds out of live
	// state during this diff. Only needed when a prune sink is wired.
	var liveBefore map[uint64]struct{}
	if h.pruneSink != nil {
		liveBefore = h.sm.LiveInferenceIDs()
	}
	root, err := h.sm.ApplyDiff(diff)
	if err != nil {
		return fmt.Errorf("apply diff nonce %d: %w", diff.Nonce, err)
	}
	h.mempool.RemoveIncluded(diff.Txs)

	// Evict cached responses for finalized or timed-out inferences.
	for _, tx := range diff.Txs {
		if fi := tx.GetFinishInference(); fi != nil {
			delete(h.completedResponses, fi.InferenceId)
		}
		if ti := tx.GetTimeoutInference(); ti != nil {
			delete(h.completedResponses, ti.InferenceId)
		}
	}

	// Emit one payload-prune event per inference this diff sealed. The seal is
	// the deterministic state-machine fold; here we only react to it. Pruning
	// is host-local off-state, carries no clock, and never mutates the root.
	// Restricted to seals that happened in the Active phase (autoSeal); the
	// settlement drain tears the whole session down and is handled elsewhere.
	if h.pruneSink != nil && phaseBefore == types.PhaseActive {
		h.emitSealPrunesLocked(liveBefore)
	}

	if h.store != nil {
		warmAfter := h.sm.WarmKeys()
		delta := types.ComputeWarmKeyDelta(warmBefore, warmAfter)
		rec := types.DiffRecord{Diff: diff, StateHash: root, WarmKeyDelta: delta}
		if err := h.store.AppendDiff(h.escrowID, rec); err != nil {
			return observability.Classify(observability.ReasonPersistDiffErr, observability.WhereHostApplyDiff, fmt.Errorf("persist diff nonce %d: %w", diff.Nonce, err))
		}
		// Validation obs recording runs only after successful ApplyDiff. Correctness
		// depends on ApplyDiff rejecting late/sealed validations before this runs;
		// do not move recording before ApplyDiff.
		h.recordValidationObsFromAppliedDiff(diff.Txs)
		phaseAfter := h.sm.Phase()
		settledNow := phaseBefore != types.PhaseSettlement && phaseAfter == types.PhaseSettlement
		shouldSnapshot := settledNow || diff.Nonce%SnapshotInterval == 0
		h.maybeSaveSnapshotLocked(diff.Nonce, shouldSnapshot, settledNow)
	}
	return nil
}

// emitSealPrunesLocked dispatches one payload-prune event per inference that
// the just-applied diff sealed: an id that was live before the apply and is no
// longer live afterwards (the state machine's deterministic autoSeal folded it
// into SealedAcc). Pruning is host-local off-state -- it carries no clock and
// never mutates the root, so it cannot diverge state. The PayloadEpoch carries
// h.epochID (the only epoch the executor stored under for this session, set via
// WithEpochID). Dedupe via prunedFired tolerates the same id appearing twice.
// Caller must hold h.mu and must have verified h.pruneSink is non-nil.
func (h *Host) emitSealPrunesLocked(liveBefore map[uint64]struct{}) {
	if len(liveBefore) == 0 {
		return
	}
	for id := range liveBefore {
		if _, stillLive := h.sm.GetInference(id); stillLive {
			continue
		}
		if _, fired := h.prunedFired[id]; fired {
			continue
		}
		// Label terminal vs stale-finished from the sealed snapshot (metrics
		// only). Fall back to terminal if the obs lookup is unavailable.
		reason := PruneReasonTerminal
		if rec, ok := h.sm.LookupSealedInference(id); ok && !isTerminalStatus(rec.Status) {
			reason = PruneReasonStaleFinished
		}
		h.prunedFired[id] = struct{}{}
		h.pruneSink.OnInferencePrunable(InferencePruneEvent{
			EscrowID:          h.escrowID,
			InferenceID:       id,
			Reason:            reason,
			PayloadEpoch:      h.epochID,
			PayloadEpochKnown: h.epochID != 0,
		})
	}
}

// maybeSaveSnapshotLocked copies the current state when shouldSnapshot is true.
// JSON marshaling and storage I/O happen asynchronously outside h.mu.
// Caller must hold h.mu.
func (h *Host) maybeSaveSnapshotLocked(nonce uint64, shouldSnapshot, settledNow bool) {
	if h.store == nil || nonce == 0 || !shouldSnapshot {
		return
	}
	if !settledNow && !h.snapshotInFlight.CompareAndSwap(false, true) {
		return
	}

	store := h.store
	escrowID := h.escrowID
	state := h.sm.ExportState()
	committedEntries := h.sm.ExportCommittedEntries()
	sealedNonces := h.sm.ExportSealedNonces()

	go func() {
		if !settledNow {
			defer h.snapshotInFlight.Store(false)
		}
		writeSnapshot(store, escrowID, nonce, state, committedEntries, sealedNonces)
	}()
}

func writeSnapshot(store storage.Storage, escrowID string, nonce uint64, state *types.EscrowState, committedEntries map[uint64][]byte, sealedNonces map[uint64]uint64) {
	data, err := MarshalStateSnapshotWithCommitted(state, committedEntries, sealedNonces)
	if err != nil {
		logging.Warn("failed to marshal host snapshot", "escrow_id", escrowID, "nonce", nonce, "error", err)
		return
	}
	if err := store.SaveSnapshot(escrowID, nonce, data); err != nil {
		logging.Warn("failed to persist host snapshot", "escrow_id", escrowID, "nonce", nonce, "error", err)
	}
}

// ApplyCatchUpDiffs applies diffs the host hasn't seen yet.
// Already-applied diffs (nonce <= current) are silently skipped.
func (h *Host) ApplyCatchUpDiffs(diffs []types.Diff) {
	h.mu.Lock()
	for _, diff := range diffs {
		_ = h.applyAndPersist(diff)
	}
	staleFinishes := h.collectStaleFinishesLocked()
	h.mu.Unlock()

	if len(staleFinishes) > 0 && h.gsp != nil {
		go h.broadcastTxsBestEffort(staleFinishes)
	}
}

// broadcastTxsBestEffort keeps gossip asynchronous/non-blocking for the host
// hot path. BroadcastTxs is intentionally fire-and-forget.
func (h *Host) broadcastTxsBestEffort(txs []*types.DevshardTx) {
	h.gsp.BroadcastTxs(context.Background(), txs)
}

// collectStaleFinishesLocked returns locally proposed MsgFinishInference txs
// that the user sequencer has not yet included in a diff after the grace
// period. Caller must hold h.mu. See Mempool.StaleFinishes for the criterion.
func (h *Host) collectStaleFinishesLocked() []*types.DevshardTx {
	if h.gsp == nil {
		return nil
	}
	grace := finishGossipGraceRotations * uint64(len(h.group))
	return h.mempool.StaleFinishes(h.sm.LatestNonce(), grace)
}

// signIfAccepted computes state root, checks acceptance, signs if allowed,
// stores sig and checks finalization. Caller must hold h.mu.
func (h *Host) signIfAccepted(applied []*types.DevshardTx) (stateSig, root []byte, nonce uint64, err error) {
	nonce = h.sm.LatestNonce()
	root, err = h.sm.ComputeStateRoot()
	if err != nil {
		return nil, nil, 0, fmt.Errorf("compute state root: %w", err)
	}

	if h.checker != nil {
		if err := h.checker.Check(h.sm.SnapshotState(), applied); err != nil {
			return nil, root, nonce, nil // withhold
		}
	}

	sig, err := h.signState(nonce, root)
	if err != nil {
		return nil, nil, 0, fmt.Errorf("sign state root: %w", err)
	}
	stateSig = sig

	if h.store != nil {
		for slotID := range h.slotIDs {
			if err := h.store.AddSignature(h.escrowID, nonce, slotID, sig); err != nil {
				logging.Debug("store own sig failed", "subsystem", "host", "nonce", nonce, "error", err)
			}
		}
		h.checkFinalization(nonce)
	}

	return stateSig, root, nonce, nil
}

func (h *Host) findDiff(diffs []types.Diff, nonce uint64) *types.Diff {
	for i := range diffs {
		if diffs[i].Nonce == nonce {
			return &diffs[i]
		}
	}
	return nil
}

// signReceipt verifies the payload and signs the executor receipt (sync, under mutex).
// Returns the receipt sig, confirmed_at timestamp, an ExecuteRequest if this host is the executor,
// and cached response body if the inference already completed (reconnect case).
// Caller must hold h.mu.
func (h *Host) signReceipt(req HostRequest) ([]byte, int64, *devshard.ExecuteRequest, []byte, receiptOutcome, error) {
	outcome := receiptOutcome{reason: observability.ReasonNotExecutor}
	if req.Payload == nil {
		outcome.reason = observability.ReasonPayloadAbsent
		return nil, 0, nil, nil, outcome, nil
	}
	targetDiff := h.findDiff(req.Diffs, req.Nonce)
	if targetDiff == nil {
		outcome.reason = observability.ReasonTargetDiffAbsent
		return nil, 0, nil, nil, outcome, nil
	}

	for _, tx := range targetDiff.Txs {
		start := tx.GetStartInference()
		if start == nil {
			continue
		}
		outcome.inferenceID = start.InferenceId
		executorSlot := h.group[start.InferenceId%uint64(len(h.group))].SlotID
		if !h.slotIDs[executorSlot] {
			continue
		}
		outcome.receiptExpected = true

		// Verify payload matches signed diff.
		if err := VerifyPayload(req.Payload, start.PromptHash, start.Model, start.InputLength, start.MaxTokens, start.StartedAt); err != nil {
			return nil, 0, nil, nil, outcome, observability.Classify(observability.ReasonPayloadVerifyErr, observability.WhereHostSignReceipt, err)
		}

		// Sign executor receipt with wall-clock confirmed_at.
		confirmedAt := time.Now().Unix()
		receiptContent := &types.ExecutorReceiptContent{
			InferenceId: start.InferenceId,
			PromptHash:  start.PromptHash,
			Model:       start.Model,
			InputLength: start.InputLength,
			MaxTokens:   start.MaxTokens,
			StartedAt:   start.StartedAt,
			EscrowId:    h.escrowID,
			ConfirmedAt: confirmedAt,
		}
		receiptData, err := proto.Marshal(receiptContent)
		if err != nil {
			return nil, 0, nil, nil, outcome, observability.Classify(observability.ReasonReceiptMarshalErr, observability.WhereHostSignReceipt, fmt.Errorf("marshal executor receipt: %w", err))
		}
		sig, err := h.signer.Sign(receiptData)
		if err != nil {
			return nil, 0, nil, nil, outcome, observability.Classify(observability.ReasonReceiptSignErr, observability.WhereHostSignReceipt, fmt.Errorf("sign executor receipt: %w", err))
		}

		// Add MsgConfirmStart to mempool so it survives HTTP failures.
		// If the response is lost (e.g. 503), the next request delivers it via mempool.
		h.mempool.Add(MempoolEntry{
			Tx: &types.DevshardTx{Tx: &types.DevshardTx_ConfirmStart{ConfirmStart: &types.MsgConfirmStart{
				InferenceId: start.InferenceId,
				ExecutorSig: sig,
				ConfirmedAt: confirmedAt,
			}}},
			ProposedAt: h.sm.LatestNonce(),
		})

		// Dedup: return receipt (proves executor alive) but skip execution.
		if _, dup := h.executing[start.InferenceId]; dup {
			outcome.reason = observability.ReasonAlreadyExecuting
			return sig, confirmedAt, nil, nil, outcome, nil
		}

		// Already completed: execution finished, response cached.
		if cached, ok := h.completedResponses[start.InferenceId]; ok {
			outcome.reason = observability.ReasonCachedResponse
			return sig, confirmedAt, nil, cached, outcome, nil
		}

		h.executing[start.InferenceId] = struct{}{}
		outcome.executionExpected = true
		outcome.reason = observability.ReasonOK

		job := &devshard.ExecuteRequest{
			InferenceID: start.InferenceId,
			Model:       start.Model,
			Prompt:      req.Payload.Prompt,
			PromptHash:  start.PromptHash,
			InputLength: start.InputLength,
			MaxTokens:   start.MaxTokens,
			EscrowID:    h.escrowID,
			EpochID:     h.epochID,
		}
		return sig, confirmedAt, job, nil, outcome, nil
	}
	return nil, 0, nil, nil, outcome, nil
}

// executeAsync runs inference and adds MsgFinishInference to the mempool.
// Delegates to RunExecution which also caches the response body for reconnection.
func (h *Host) executeAsync(ctx context.Context, job *devshard.ExecuteRequest) {
	_, _ = h.RunExecution(ctx, job)
}

func (h *Host) ReleaseExecution(inferenceID uint64) {
	h.mu.Lock()
	delete(h.executing, inferenceID)
	h.mu.Unlock()
}

// RunExecution executes an inference job and adds MsgFinishInference to the mempool.
// This is the deferred execution path -- used when DeferExecution=true in HandleRequest.
// The caller typically streams results to the client before calling this.
func (h *Host) RunExecution(ctx context.Context, job *devshard.ExecuteRequest) (*devshard.ExecuteResult, error) {
	// Find the internal job metadata for cleanup/mempool.
	inferenceID := job.InferenceID
	executorSlot := h.group[inferenceID%uint64(len(h.group))].SlotID
	diffNonce := h.LatestNonce()

	defer h.ReleaseExecution(inferenceID)

	result, err := h.engine.Execute(ctx, *job)
	if err != nil {
		reason, where := observability.ErrorReason(err, observability.ReasonExecuteErr, observability.WhereHostExecute)
		return nil, observability.FailReceiptOrphan(ctx, h.escrowID, reason, where,
			observability.StageFinished, "execute failed", err, "inference_id", inferenceID)
	}

	finishMsg := &types.MsgFinishInference{
		InferenceId:  inferenceID,
		ResponseHash: result.ResponseHash,
		InputTokens:  result.InputTokens,
		OutputTokens: result.OutputTokens,
		ExecutorSlot: executorSlot,
		EscrowId:     h.escrowID,
	}
	proposerSig, err := h.signProposer(finishMsg)
	if err != nil {
		return result, observability.FailReceiptOrphan(ctx, h.escrowID,
			observability.ReasonSignFinishErr, observability.WhereHostPublishFinish,
			observability.StageFinished, "sign finish msg failed", err, "inference_id", inferenceID)
	}
	finishMsg.ProposerSig = proposerSig

	h.mempool.Add(MempoolEntry{
		Tx: &types.DevshardTx{Tx: &types.DevshardTx_FinishInference{
			FinishInference: finishMsg,
		}},
		ProposedAt: diffNonce,
	})
	if result.PartialResponse {
		reason := observability.Reason(result.PartialResponseReason)
		if reason == "" {
			reason = observability.ReasonPartialResponseInterrupted
		}
		partialWhere := result.PartialResponseWhere
		observability.Log(ctx, observability.LevelWarn, "finish published from partial response", observability.StageFinished, observability.WhereHostPublishFinish, h.escrowID, reason, nil,
			"inference_id", inferenceID,
			"partial_where", partialWhere)
	}
	if len(result.ResponseBody) > 0 {
		h.mu.Lock()
		h.completedResponses[inferenceID] = result.ResponseBody
		h.mu.Unlock()
	}
	observability.SetMempoolSize(h.escrowID, h.mempool.Len())

	return result, nil
}

// validateJob captures data needed to run validateAsync outside the mutex.
type validateJob struct {
	inferenceID     uint64
	validatorSlot   uint32
	flow            validationFlow
	model           string
	promptHash      []byte
	responseHash    []byte
	inputTokens     uint64
	outputTokens    uint64
	escrowID        string
	executorAddress string
	epochID         uint64
}

type validationFlow string

const (
	validationFlowShouldValidate validationFlow = "should_validate"
	validationFlowChallenged     validationFlow = "challenged"
)

// collectValidationJobs finds finished inferences that this host should validate.
// Caller must hold h.mu.
func (h *Host) collectValidationJobs() []validateJob {
	if h.validator == nil || h.validationQueue == nil {
		return nil
	}
	if !h.completionRequestsEnabled() {
		return nil
	}

	st := h.sm.SnapshotState()
	available := cap(h.validationQueue) - len(h.validationQueue)
	if available <= 0 {
		return nil
	}
	var jobs []validateJob

	for infID, rec := range st.Inferences {
		if rec.Status != types.StatusFinished && rec.Status != types.StatusChallenged {
			continue
		}
		if h.slotIDs[rec.ExecutorSlot] {
			continue
		}

		alreadyValidated := false
		for slot := range h.slotIDs {
			if rec.ValidatedBy.IsSet(slot) {
				alreadyValidated = true
				break
			}
		}
		if alreadyValidated {
			continue
		}
		if _, ok := h.validating[infID]; ok {
			continue
		}
		if h.hasMempoolValidationOrVote(infID) {
			continue
		}

		executorAddr := h.slotToAddr[rec.ExecutorSlot]

		// Phase 1 samples by ValidationRate; Phase 2 is mandatory so VoteThreshold is reachable.
		flow := validationFlowChallenged
		if rec.Status == types.StatusFinished {
			mySlotCount := uint32(len(h.slotIDs))
			executorSlotCount := h.sm.AddressSlotCount(executorAddr)
			totalSlots := h.sm.TotalSlots()
			if !state.ShouldValidate(h.ownSeed, infID, mySlotCount, executorSlotCount, totalSlots, st.Config.ValidationRate) {
				continue
			}
			flow = validationFlowShouldValidate
		}

		validatorSlot := h.sortedSlots[0]

		h.validating[infID] = struct{}{}
		jobs = append(jobs, validateJob{
			inferenceID:     infID,
			validatorSlot:   validatorSlot,
			flow:            flow,
			model:           rec.Model,
			promptHash:      rec.PromptHash,
			responseHash:    rec.ResponseHash,
			inputTokens:     rec.InputTokens,
			outputTokens:    rec.OutputTokens,
			escrowID:        h.escrowID,
			executorAddress: executorAddr,
			epochID:         h.epochID,
		})
		available--
		if available == 0 {
			break
		}
	}

	return jobs
}

func (h *Host) startValidationWorkers(count int) {
	for i := 0; i < count; i++ {
		go func() {
			for job := range h.validationQueue {
				h.validateAsync(context.Background(), job)
			}
		}()
	}
}

func (h *Host) enqueueValidation(job validateJob) {
	if h.validationQueue == nil {
		h.mu.Lock()
		delete(h.validating, job.inferenceID)
		h.mu.Unlock()
		return
	}

	select {
	case h.validationQueue <- job:
		observability.IncValidation(observability.StageValidationPicked, observability.MetricStatusQueued)
		observability.SetValidationQueueDepth(h.escrowID, len(h.validationQueue))
	default:
		h.mu.Lock()
		delete(h.validating, job.inferenceID)
		h.mu.Unlock()
		observability.IncValidation(observability.StageValidationPicked, observability.MetricStatusError)
		observability.IncValidationQueueDrop()
		observability.Log(context.Background(), observability.LevelWarn, "validation queue full; retry later", observability.StageValidationPicked, observability.WhereHostValidationQueue, h.escrowID, observability.ReasonQueueFull, nil, "inference_id", job.inferenceID)
	}
}

// hasMempoolValidationOrVote returns true if a MsgValidation or
// MsgValidationVote for infID from this host is already in the mempool.
// Caller must hold h.mu.
func (h *Host) hasMempoolValidationOrVote(infID uint64) bool {
	for _, tx := range h.mempool.Txs() {
		if v := tx.GetValidation(); v != nil && v.InferenceId == infID {
			if h.slotIDs[v.ValidatorSlot] {
				return true
			}
		}
		if v := tx.GetValidationVote(); v != nil && v.InferenceId == infID {
			if h.slotIDs[v.VoterSlot] {
				return true
			}
		}
	}
	return false
}

// validateAsync emits MsgValidation when status is Finished, MsgValidationVote
// when Challenged. Re-reads status after Validate returns to catch races where
// another host challenged the inference while this validator was running.
// Called outside the mutex.
func (h *Host) validateAsync(ctx context.Context, job validateJob) {
	ctx, _ = logging.WithRequestID(ctx, fmt.Sprintf("validate-%d", job.inferenceID))
	observability.IncValidation(observability.StageValidationStarted, observability.MetricStatusOK)
	observability.Log(ctx, observability.LevelInfo, "validation started", observability.StageValidationStarted, observability.WhereHostValidate, h.escrowID, "", nil,
		"inference_id", job.inferenceID,
		"executor_address", job.executorAddress,
		"validator_slot", job.validatorSlot,
		"validation_flow", string(job.flow))
	defer func() {
		h.mu.Lock()
		delete(h.validating, job.inferenceID)
		h.mu.Unlock()
		if h.validationQueue != nil {
			observability.SetValidationQueueDepth(h.escrowID, len(h.validationQueue))
		}
	}()

	result, err := h.validator.Validate(ctx, devshard.ValidateRequest{
		InferenceID:     job.inferenceID,
		Model:           job.model,
		PromptHash:      job.promptHash,
		ResponseHash:    job.responseHash,
		InputTokens:     job.inputTokens,
		OutputTokens:    job.outputTokens,
		EscrowID:        job.escrowID,
		ExecutorAddress: job.executorAddress,
		EpochID:         job.epochID,
	})
	if err != nil {
		// Payload already pruned on the executor: the validation window is
		// effectively over for us. Drop silently -- no MsgValidation, no
		// challenge, no error in the executor receipt path.
		if errors.Is(err, devshard.ErrValidationSkipped) {
			logging.Info("validation skipped: payload pruned",
				"subsystem", "host",
				"inference_id", job.inferenceID,
				"executor_address", job.executorAddress,
				"epoch_id", job.epochID,
			)
			return
		}
		reason, where := observability.ErrorReason(err, observability.ReasonValidateErr, observability.WhereHostValidate)
		observability.FailValidationFinished(ctx, h.escrowID, reason, where, "validate failed", err,
			"inference_id", job.inferenceID,
			"executor_address", job.executorAddress,
			"validator_slot", job.validatorSlot,
			"validation_flow", string(job.flow))
		return
	}

	rec, ok := h.sm.GetInference(job.inferenceID)
	if !ok {
		observability.FailValidationFinished(ctx, h.escrowID,
			observability.ReasonInferenceDisappeared, observability.WhereHostValidate,
			"validate: inference disappeared", nil,
			"inference_id", job.inferenceID,
			"executor_address", job.executorAddress,
			"validator_slot", job.validatorSlot,
			"validation_flow", string(job.flow))
		return
	}
	observability.IncValidation(observability.StageValidationFinished, observability.MetricStatusOK)

	var tx *types.DevshardTx
	var validationTx string
	switch rec.Status {
	case types.StatusFinished:
		// TODO: if this MsgValidation lands after another host has already
		// challenged the inference, the state machine records participation
		// without vote weight. Counting that requires a coordinated upgrade.
		msg := &types.MsgValidation{
			InferenceId:   job.inferenceID,
			ValidatorSlot: job.validatorSlot,
			Valid:         result.Valid,
			EscrowId:      h.escrowID,
		}
		proposerSig, err := h.signProposer(msg)
		if err != nil {
			observability.LogValidationOrphan(ctx, h.escrowID,
				observability.ReasonSignValidationErr, observability.WhereHostPublishValidation,
				observability.StageVotePublished, "sign validation msg failed", err,
				"inference_id", job.inferenceID,
				"executor_address", job.executorAddress,
				"validator_slot", job.validatorSlot,
				"validation_flow", string(job.flow),
				"validation_result", validationResultLabel(result.Valid),
				"validation_reason", result.Reason,
				"result_valid", result.Valid)
			return
		}
		msg.ProposerSig = proposerSig
		tx = &types.DevshardTx{Tx: &types.DevshardTx_Validation{Validation: msg}}
		validationTx = "validation"
	case types.StatusChallenged:
		msg := &types.MsgValidationVote{
			InferenceId: job.inferenceID,
			VoterSlot:   job.validatorSlot,
			VoteValid:   result.Valid,
			EscrowId:    h.escrowID,
		}
		proposerSig, err := h.signProposer(msg)
		if err != nil {
			observability.LogValidationOrphan(ctx, h.escrowID,
				observability.ReasonSignVoteErr, observability.WhereHostPublishValidation,
				observability.StageVotePublished, "sign validation vote failed", err,
				"inference_id", job.inferenceID,
				"executor_address", job.executorAddress,
				"validator_slot", job.validatorSlot,
				"validation_flow", string(job.flow),
				"validation_result", validationResultLabel(result.Valid),
				"validation_reason", result.Reason,
				"vote_valid", result.Valid)
			return
		}
		msg.ProposerSig = proposerSig
		tx = &types.DevshardTx{Tx: &types.DevshardTx_ValidationVote{ValidationVote: msg}}
		validationTx = "validation_vote"
	default:
		observability.IncValidation(observability.StageVotePublished, observability.MetricStatusError)
		observability.Log(ctx, observability.LevelInfo, "validation skipped after status changed", observability.StageVotePublished, observability.WhereHostPublishValidation, h.escrowID, observability.ReasonValidationStatusChanged, nil,
			"inference_id", job.inferenceID,
			"executor_address", job.executorAddress,
			"validator_slot", job.validatorSlot,
			"validation_flow", string(job.flow),
			"validation_result", validationResultLabel(result.Valid),
			"validation_reason", result.Reason,
			"result_valid", result.Valid)
		return
	}

	h.mu.Lock()
	h.mempool.Add(MempoolEntry{
		Tx:         tx,
		ProposedAt: h.sm.LatestNonce(),
	})
	observability.SetMempoolSize(h.escrowID, h.mempool.Len())
	h.mu.Unlock()
	observability.IncValidation(observability.StageVotePublished, observability.MetricStatusOK)
	fields := []any{
		"inference_id", job.inferenceID,
		"executor_address", job.executorAddress,
		"validator_slot", job.validatorSlot,
		"validation_flow", string(job.flow),
		"validation_tx", validationTx,
		"validation_result", validationResultLabel(result.Valid),
		"validation_reason", result.Reason,
		"result_valid", result.Valid,
	}
	fields = append(fields, result.Details...)
	observability.Log(ctx, observability.LevelInfo, "validation tx published", observability.StageVotePublished, observability.WhereHostPublishValidation, h.escrowID, observability.ReasonOK, nil, fields...)
}

func validationResultLabel(valid bool) string {
	if valid {
		return "valid"
	}
	return "invalid"
}

// AccumulateGossipSig verifies and stores a signature received via gossip.
// The sig must recover to group[senderSlot] and the stateHash must match the
// stored DiffRecord for that nonce.
func (h *Host) AccumulateGossipSig(nonce uint64, stateHash, sig []byte, senderSlot uint32) error {
	h.mu.Lock()
	defer h.mu.Unlock()

	if h.verifier == nil || h.store == nil {
		return fmt.Errorf("host not configured for sig accumulation (verifier=%v, store=%v)", h.verifier != nil, h.store != nil)
	}

	expected, ok := h.slotToAddr[senderSlot]
	if !ok {
		return fmt.Errorf("sender slot %d not in group", senderSlot)
	}

	// Verify sig recovers to the expected address.
	sigContent := &types.StateSignatureContent{
		StateRoot: stateHash,
		EscrowId:  h.escrowID,
		Nonce:     nonce,
	}
	sigData, mErr := proto.Marshal(sigContent)
	if mErr != nil {
		return fmt.Errorf("marshal sig content: %w", mErr)
	}
	addr, err := h.verifier.RecoverAddress(sigData, sig)
	if err != nil {
		return fmt.Errorf("recover address: %w", err)
	}
	if addr != expected {
		warmKeys := h.sm.WarmKeys()
		if warmKeys[senderSlot] != addr && !h.sm.CheckWarmKey(addr, expected) {
			return fmt.Errorf("sig from slot %d: expected %s, got %s", senderSlot, expected, addr)
		}
	}

	// Verify stateHash matches stored record.
	records, err := h.store.GetDiffs(h.escrowID, nonce, nonce)
	if err != nil || len(records) == 0 {
		return fmt.Errorf("no stored diff at nonce %d", nonce)
	}
	if !bytes.Equal(records[0].StateHash, stateHash) {
		return fmt.Errorf("state hash mismatch at nonce %d: stored %x, gossip %x", nonce, records[0].StateHash, stateHash)
	}

	// Store sig for all slots owned by this validator address (use cold address for lookup).
	storeAddr := addr
	if addr != expected {
		storeAddr = expected
	}
	for _, slot := range h.addrToSlots[storeAddr] {
		if err := h.store.AddSignature(h.escrowID, nonce, slot, sig); err != nil {
			return err
		}
	}
	h.checkFinalization(nonce)
	return nil
}

// ApplyRecoveredDiffs applies diffs fetched during gossip recovery.
// Returns GossipSig for each successfully applied nonce.
func (h *Host) ApplyRecoveredDiffs(ctx context.Context, diffs []types.Diff) ([]gossip.GossipSig, error) {
	h.mu.Lock()
	defer h.mu.Unlock()

	var sigs []gossip.GossipSig

	for _, diff := range diffs {
		if err := h.applyAndPersist(diff); err != nil {
			return sigs, fmt.Errorf("apply recovered diff nonce %d: %w", diff.Nonce, err)
		}

		// Sign state with acceptance check (same path as HandleRequest).
		stateSig, root, nonce, err := h.signIfAccepted(nil)
		if err != nil {
			return sigs, fmt.Errorf("sign recovered state: %w", err)
		}

		if stateSig != nil && h.store != nil {
			for slotID := range h.slotIDs {
				sigs = append(sigs, gossip.GossipSig{
					Nonce:     nonce,
					StateHash: root,
					Sig:       stateSig,
					SlotID:    slotID,
				})
			}
		}
	}

	return sigs, nil
}

// ChallengeReceipt is called by a verifying host to challenge the executor.
// It applies missing diffs, checks if this host is the executor for the given
// inference, verifies the payload fields, signs an executor receipt, and triggers
// async execution. Returns the receipt signature and confirmed_at timestamp,
// or nil if this host cannot produce a receipt (not executor, inference not pending, etc).
//
// On payload validation error, returns (nil, 0, nil) -- not an error, because the
// executor IS reachable. The verifier should already have caught bad payloads
// before forwarding (defense-in-depth).
func (h *Host) ChallengeReceipt(ctx context.Context, inferenceID uint64, payload *InferencePayload, diffs []types.Diff) ([]byte, int64, error) {
	receipt, confirmedAt, job, err := h.challengeReceiptLocked(inferenceID, payload, diffs)
	if err != nil || job == nil {
		return receipt, confirmedAt, err
	}
	h.executeAsync(ctx, job)
	return receipt, confirmedAt, nil
}

// challengeReceiptLocked applies diffs, checks executor eligibility, and signs
// the receipt under the mutex. Returns a non-nil ExecuteRequest when async execution is needed.
func (h *Host) challengeReceiptLocked(inferenceID uint64, payload *InferencePayload, diffs []types.Diff) ([]byte, int64, *devshard.ExecuteRequest, error) {
	h.mu.Lock()
	defer h.mu.Unlock()

	for _, diff := range diffs {
		if err := h.applyAndPersist(diff); err != nil {
			return nil, 0, nil, fmt.Errorf("apply challenge diff nonce %d: %w", diff.Nonce, err)
		}
	}

	rec, ok := h.sm.GetInference(inferenceID)
	if !ok || rec.Status != types.StatusPending {
		return nil, 0, nil, nil
	}
	if !h.slotIDs[rec.ExecutorSlot] {
		return nil, 0, nil, nil
	}
	if payload == nil {
		return nil, 0, nil, nil
	}
	if err := VerifyPayload(payload, rec.PromptHash, rec.Model, rec.InputLength, rec.MaxTokens, rec.StartedAt); err != nil {
		return nil, 0, nil, nil
	}

	confirmedAt := time.Now().Unix()
	receiptContent := &types.ExecutorReceiptContent{
		InferenceId: inferenceID,
		PromptHash:  rec.PromptHash,
		Model:       rec.Model,
		InputLength: rec.InputLength,
		MaxTokens:   rec.MaxTokens,
		StartedAt:   rec.StartedAt,
		EscrowId:    h.escrowID,
		ConfirmedAt: confirmedAt,
	}
	receiptData, err := proto.Marshal(receiptContent)
	if err != nil {
		return nil, 0, nil, fmt.Errorf("marshal executor receipt: %w", err)
	}
	sig, err := h.signer.Sign(receiptData)
	if err != nil {
		return nil, 0, nil, fmt.Errorf("sign executor receipt: %w", err)
	}

	// Dedup: return receipt (proves executor alive) but skip execution
	// if already in-flight or already finished in mempool.
	if _, dup := h.executing[inferenceID]; dup {
		return sig, confirmedAt, nil, nil
	}
	for _, tx := range h.mempool.Txs() {
		if fi := tx.GetFinishInference(); fi != nil && fi.InferenceId == inferenceID {
			return sig, confirmedAt, nil, nil
		}
	}

	h.executing[inferenceID] = struct{}{}

	job := &devshard.ExecuteRequest{
		InferenceID: inferenceID,
		Model:       rec.Model,
		Prompt:      payload.Prompt,
		PromptHash:  rec.PromptHash,
		InputLength: rec.InputLength,
		MaxTokens:   rec.MaxTokens,
		EscrowID:    h.escrowID,
		EpochID:     h.epochID,
	}
	return sig, confirmedAt, job, nil
}

func (h *Host) LatestNonce() uint64 {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.sm.LatestNonce()
}

// LastFinalized returns the highest nonce marked as finalized (>2/3 sigs).
func (h *Host) LastFinalized() (uint64, error) {
	if h.store == nil {
		return 0, fmt.Errorf("no storage configured")
	}
	return h.store.LastFinalized(h.escrowID)
}

// checkFinalization checks if a nonce has enough sigs (>2/3) and marks it finalized.
func (h *Host) checkFinalization(nonce uint64) {
	if h.store == nil {
		return
	}
	sigs, err := h.store.GetSignatures(h.escrowID, nonce)
	if err != nil {
		return
	}
	threshold := 2*len(h.group)/3 + 1
	if len(sigs) >= threshold {
		if err := h.store.MarkFinalized(h.escrowID, nonce); err != nil {
			logging.Debug("mark finalized failed", "subsystem", "host", "nonce", nonce, "error", err)
			return
		}
		if h.gsp != nil {
			h.gsp.PruneBelow(nonce)
		}
	}
}

// GetSignatures returns accumulated signatures for a nonce from storage.
func (h *Host) GetSignatures(nonce uint64) (map[uint32][]byte, error) {
	if h.store == nil {
		return nil, fmt.Errorf("no storage configured")
	}
	return h.store.GetSignatures(h.escrowID, nonce)
}

// signState marshals StateSignatureContent{root, escrowID, nonce} and signs it.
func (h *Host) signState(nonce uint64, root []byte) ([]byte, error) {
	sigContent := &types.StateSignatureContent{
		StateRoot: root,
		EscrowId:  h.escrowID,
		Nonce:     nonce,
	}
	sigData, err := proto.Marshal(sigContent)
	if err != nil {
		return nil, fmt.Errorf("marshal state sig content: %w", err)
	}
	return h.signer.Sign(sigData)
}

// signProposer marshals msg and signs it, returning the proposer signature.
func (h *Host) signProposer(msg proto.Message) ([]byte, error) {
	data, err := proto.Marshal(msg)
	if err != nil {
		return nil, fmt.Errorf("marshal proposer msg: %w", err)
	}
	return h.signer.Sign(data)
}

// VerifyPayload checks that an InferencePayload matches the expected on-chain fields.
// Used by both executor (signReceipt) and verifier (VerifyRefusedTimeout) paths.
func VerifyPayload(p *InferencePayload, promptHash []byte, model string, inputLength, maxTokens uint64, startedAt int64) error {
	hash, err := devshard.CanonicalPromptHash(p.Prompt)
	if err != nil {
		return fmt.Errorf("%w: %v", types.ErrPromptHashMismatch, err)
	}
	if !bytes.Equal(hash, promptHash) {
		return types.ErrPromptHashMismatch
	}
	if p.InputLength != inputLength {
		return fmt.Errorf("%w: input_length %d vs %d", types.ErrPayloadMismatch, p.InputLength, inputLength)
	}
	if p.MaxTokens != maxTokens {
		return fmt.Errorf("%w: max_tokens %d vs %d", types.ErrPayloadMismatch, p.MaxTokens, maxTokens)
	}
	if p.StartedAt != startedAt {
		return fmt.Errorf("%w: started_at %d vs %d", types.ErrPayloadMismatch, p.StartedAt, startedAt)
	}
	if p.Model != model {
		return fmt.Errorf("%w: model %s vs %s", types.ErrPayloadMismatch, p.Model, model)
	}
	return nil
}
