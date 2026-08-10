package host

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"google.golang.org/protobuf/proto"

	"common/completionapi"

	"common/storage/payloads"

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

	// Resume cursor for same-nonce reconnect (transport only; not a chain message).
	// See gateway-attempt-reconnect-plan.md R2/R6.
	DeliveredEvents  int64
	DeliveredPartial int64
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
	LiveAttach         bool   // true when reconnecting to an in-flight live stream
	StreamBytesRead    int64  // total bytes read from the host HTTP response body (SSE streams only)
	InferenceID        uint64
	ReceiptExpected    bool
	ReceiptReason      observability.Reason
	ExecutionExpected  bool

	// Resume cursor echoed from the request for live/cached attach.
	DeliveredEvents  int64
	DeliveredPartial int64
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
	validator          devshard.ValidationEngine // optional, nil = no validation
	validationRecorder devshard.ValidationCompletionRecorder
	escrowID           string
	epochID            uint64
	slotIDs            map[uint32]bool
	group              []types.SlotAssignment
	mempool      *Mempool
	checker      AcceptanceChecker
	store        storage.Storage // optional, nil = no persistence
	payloads     PayloadRetriever // optional, nil = no disk resume tier (5e)
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
	liveStreams        map[uint64]*LiveStream // in-flight SSE fan-out hubs (R6)
	ownSeed            int64                  // deterministic seed derived from signer + escrowID

	validationLifecycleMu sync.RWMutex
	validationStartOnce   sync.Once
	validationCloseOnce   sync.Once
	validationClosed      bool

	maxNonce devshard.MaxNonceProvider // nil = do not enforce
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
		liveStreams:           make(map[uint64]*LiveStream),
		ownSeed:               ownSeed,
	}
	for _, opt := range opts {
		opt(h)
	}
	return h, nil
}

// Start launches background workers owned by this host. Callers should invoke
// Start only after the host is registered somewhere that will also Close it.
func (h *Host) Start() {
	if h.validator == nil {
		return
	}
	h.validationStartOnce.Do(func() {
		h.validationLifecycleMu.Lock()
		defer h.validationLifecycleMu.Unlock()
		if h.validationClosed {
			return
		}
		q := make(chan validateJob, defaultValidationQueueSize)
		h.validationQueue = q
		h.startValidationWorkers(q, defaultValidationWorkers)
	})
}

// Close releases host-owned background workers. It is safe to call multiple
// times and safe to call on hosts that were never started.
func (h *Host) Close() {
	h.validationCloseOnce.Do(func() {
		h.validationLifecycleMu.Lock()
		defer h.validationLifecycleMu.Unlock()
		h.validationClosed = true
		if h.validationQueue != nil {
			close(h.validationQueue)
			h.validationQueue = nil
		}
	})
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

// PayloadRetriever is the optional disk resume tier consulted after
// completedResponses is evicted (Step 5e). Matches session.PayloadStore /
// common/storage/payloads.Storage.Retrieve.
type PayloadRetriever interface {
	Retrieve(ctx context.Context, escrowID string, inferenceID, epochID uint64) (prompt, response []byte, err error)
}

// WithPayloadRetriever enables same-nonce reconnect from the durable payload
// store after the in-RAM completedResponses cache is gone.
func WithPayloadRetriever(r PayloadRetriever) HostOption {
	return func(h *Host) { h.payloads = r }
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

// WithValidationCompletionRecorder sets the recorder called after MsgValidation
// is successfully queued by the async validation path.
func WithValidationCompletionRecorder(r devshard.ValidationCompletionRecorder) HostOption {
	return func(h *Host) { h.validationRecorder = r }
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
//
// Production HostManager intentionally does not use this option: settlement
// protection comes from deterministic drain accounting (settleLiveRecordLocked),
// not signature withholding. WithGrace must not be paired with finish-gossip
// recovery — gossip is a best-effort convenience for the user to sequence a
// real Finish at actual cost; withholding would freeze settlement instead.
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
func (h *Host) EpochID() uint64               { return h.epochID }
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
		if err := h.applyAndPersistReconciling(ctx, diff); err != nil {
			h.mu.Unlock()
			return nil, observability.Classify(observability.ReasonApplyErr, observability.WhereHostApplyDiff, err)
		}
		lastAppliedTxs = diff.Txs
		diffsApplied = true
	}

	// (b) Sign executor receipt (sync, under mutex; may briefly unlock for
	// payload-store I/O on the post-eviction resume path).
	receipt, confirmedAt, job, cachedBody, liveAttach, receiptOutcome, err := h.signReceipt(ctx, req)
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
		LiveAttach:         liveAttach,
		InferenceID:        receiptOutcome.inferenceID,
		ReceiptExpected:    receiptOutcome.receiptExpected,
		ReceiptReason:      receiptOutcome.reason,
		ExecutionExpected:  receiptOutcome.executionExpected,
		DeliveredEvents:    req.DeliveredEvents,
		DeliveredPartial:   req.DeliveredPartial,
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

// applyAndPersist persists then commits a contiguous next-nonce diff
// (persist-first). Captures WarmKeyDelta from ValidateDiff for replay.
//
// Callers that may see HA catch-up gaps must use applyAndPersistReconciling.
// AppendDiff is idempotent for identical already-durable rows (Phase 1), so a
// stale standby that fast-forwarded then re-persists the tip does not fail.
// Persist uses bounded retry; on exhaustion memory is unchanged (no eviction).
//
// Caller must hold h.mu. May unlock briefly during persist backoff.
func (h *Host) applyAndPersist(ctx context.Context, diff types.Diff) error {
	currentNonce := h.sm.LatestNonce()
	if diff.Nonce <= currentNonce {
		return nil
	}
	if err := h.checkDiffNonceLimitLocked(diff); err != nil {
		return err
	}

	phaseBefore := h.sm.Phase()
	if h.store != nil {
		warmBefore := h.sm.WarmKeys()
		vd, err := h.sm.ValidateDiff(diff)
		if err != nil {
			return fmt.Errorf("validate diff nonce %d: %w", diff.Nonce, err)
		}
		delta := types.ComputeWarmKeyDelta(warmBefore, vd.WarmAfter)
		rec := types.DiffRecord{Diff: diff, StateHash: vd.Root, WarmKeyDelta: delta}
		if err := h.persistDiffRetryLocked(ctx, rec); err != nil {
			return observability.Classify(observability.ReasonPersistDiffErr, observability.WhereHostApplyDiff, fmt.Errorf("persist diff nonce %d: %w", diff.Nonce, err))
		}
		// CommitValidated installs the precomputed post-state (no second
		// applyCore) and flushes the buffered obs writes. It returns false when
		// another request committed this nonce while we unlocked for persist
		// backoff, in which case the diff is already applied.
		if !h.sm.CommitValidated(vd) {
			return nil
		}
	} else if _, err := h.sm.ApplyDiff(diff); err != nil {
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

	if h.store != nil {
		// Validation obs recording runs only after the diff is committed.
		// Correctness depends on the trial apply rejecting late/sealed
		// validations before this runs; do not move recording before commit.
		h.recordValidationObsFromAppliedDiff(diff.Txs)
		phaseAfter := h.sm.Phase()
		settledNow := phaseBefore != types.PhaseSettlement && phaseAfter == types.PhaseSettlement
		shouldSnapshot := settledNow || diff.Nonce%SnapshotInterval == 0
		h.maybeSaveSnapshotLocked(diff.Nonce, shouldSnapshot, settledNow)
	}
	return nil
}

// persistDiffRetryLocked retries AppendDiff with backoff, unlocking h.mu during
// waits so other requests can proceed. On exhaustion returns ErrPersistExhausted
// without mutating host memory (persist-first: ValidateDiff left the SM
// unchanged, and the commit happens only after this returns nil). Caller must
// hold h.mu; it is held again on return. See storage.AppendDiffWithRetry.
func (h *Host) persistDiffRetryLocked(ctx context.Context, rec types.DiffRecord) error {
	return storage.AppendDiffWithRetryUnlocked(ctx, h.store, h.escrowID, rec, h.mu.Lock, h.mu.Unlock)
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
		_ = h.applyAndPersistReconciling(context.Background(), diff)
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
//
// Recovery gossip (broadcastTxsBestEffort) is independent of WithGrace: it
// gives the payer another path to pick up a Finish before settlement drain
// auto-credits at reserved cost. It does not gate signing.
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
// cached response body if the inference already completed, and liveAttach when a reconnect
// should join an in-flight LiveStream (R6).
//
// Authorization comes from applied escrow state for req.Nonce, not from MsgStartInference
// bytes in the request. applyAndPersist may skip stale diffs without verifying them; those
// skipped bytes must never authorize execution.
// Caller must hold h.mu. May briefly unlock for payload-store I/O.
func (h *Host) signReceipt(ctx context.Context, req HostRequest) ([]byte, int64, *devshard.ExecuteRequest, []byte, bool, receiptOutcome, error) {
	outcome := receiptOutcome{reason: observability.ReasonNotExecutor}
	if req.Payload == nil {
		outcome.reason = observability.ReasonPayloadAbsent
		return nil, 0, nil, nil, false, outcome, nil
	}
	if h.findDiff(req.Diffs, req.Nonce) == nil {
		outcome.reason = observability.ReasonTargetDiffAbsent
		return nil, 0, nil, nil, false, outcome, nil
	}

	// Protocol: inference_id == nonce. Authorize only from applied state.
	inferenceID := req.Nonce
	outcome.inferenceID = inferenceID
	executorSlot := h.group[inferenceID%uint64(len(h.group))].SlotID
	if !h.slotIDs[executorSlot] {
		// Here reason is default observability.ReasonNotExecutor
		return nil, 0, nil, nil, false, outcome, nil
	}
	outcome.receiptExpected = true

	// HandleRequest applies diffs before signReceipt. A newly applied
	// MsgStartInference creates Inferences[id] in memory (and reserves cost).
	// If the target diff was stale/skipped, ApplyDiff never ran and ok is false.
	rec, ok := h.sm.GetInference(inferenceID)
	if !ok {
		outcome.reason = observability.ReasonInferenceDisappeared
		return nil, 0, nil, nil, false, outcome, nil
	}

	// Verify payload against the applied record (not unverified request-diff fields).
	if err := VerifyPayload(req.Payload, rec.PromptHash, rec.Model, rec.InputLength, rec.MaxTokens, rec.StartedAt); err != nil {
		return nil, 0, nil, nil, false, outcome, observability.Classify(observability.ReasonPayloadVerifyErr, observability.WhereHostSignReceipt, err)
	}

	h.pruneLiveStreamLocked(inferenceID)
	_, alreadyExecuting := h.executing[inferenceID]
	live := h.liveStreams[inferenceID]
	cached, hasCached := h.completedResponses[inferenceID]

	// Prefer durable RAM cache over a live buffer (drain→persist→forget).
	if hasCached {
		sig, confirmedAt, err := h.ensureConfirmStartLocked(inferenceID, rec)
		if err != nil {
			return nil, 0, nil, nil, false, outcome, err
		}
		outcome.reason = observability.ReasonCachedResponse
		return sig, confirmedAt, nil, cached, false, outcome, nil
	}

	// Mid-generation reconnect: join the in-flight LiveStream from the resume cursor.
	if alreadyExecuting || live != nil {
		sig, confirmedAt, err := h.ensureConfirmStartLocked(inferenceID, rec)
		if err != nil {
			return nil, 0, nil, nil, false, outcome, err
		}
		outcome.reason = observability.ReasonAlreadyExecuting
		if live != nil {
			outcome.executionExpected = true
			return sig, confirmedAt, nil, nil, true, outcome, nil
		}
		// Marked executing but no live buffer (race / pruned): receipt-only resume failure.
		return sig, confirmedAt, nil, nil, false, outcome, nil
	}

	// First-path execution for a still-pending inference.
	if rec.Status == types.StatusPending {
		sig, confirmedAt, err := h.ensureConfirmStartLocked(inferenceID, rec)
		if err != nil {
			return nil, 0, nil, nil, false, outcome, err
		}

		h.executing[inferenceID] = struct{}{}
		outcome.executionExpected = true
		outcome.reason = observability.ReasonOK

		job := &devshard.ExecuteRequest{
			InferenceID: inferenceID,
			Model:       rec.Model,
			Prompt:      req.Payload.Prompt,
			PromptHash:  rec.PromptHash,
			InputLength: rec.InputLength,
			MaxTokens:   rec.MaxTokens,
			EscrowID:    h.escrowID,
			EpochID:     h.epochID,
		}
		return sig, confirmedAt, job, nil, false, outcome, nil
	}

	// Terminal inference with no RAM cache: last resume tier is the payload store (5e).
	if body, ok := h.lookupPayloadResponseLocked(ctx, inferenceID); ok {
		rec, ok = h.sm.GetInference(inferenceID)
		if !ok {
			outcome.reason = observability.ReasonInferenceDisappeared
			return nil, 0, nil, nil, false, outcome, nil
		}
		sig, confirmedAt, err := h.ensureConfirmStartLocked(inferenceID, rec)
		if err != nil {
			return nil, 0, nil, nil, false, outcome, err
		}
		outcome.reason = observability.ReasonCachedResponse
		return sig, confirmedAt, nil, body, false, outcome, nil
	}

	outcome.reason = observability.ReasonInferenceDisappeared
	return nil, 0, nil, nil, false, outcome, nil
}

// lookupPayloadResponseLocked loads a completed response body from the durable
// payload store. Caller must hold h.mu; the method unlocks for I/O and re-locks.
func (h *Host) lookupPayloadResponseLocked(ctx context.Context, inferenceID uint64) ([]byte, bool) {
	if h.payloads == nil {
		return nil, false
	}
	escrowID := h.escrowID
	epochID := h.epochID
	retriever := h.payloads
	h.mu.Unlock()
	_, body, err := retrievePayloadResponse(ctx, retriever, escrowID, inferenceID, epochID)
	h.mu.Lock()
	if err != nil || len(body) == 0 {
		return nil, false
	}
	return body, true
}

// retrievePayloadResponse tries the primary epoch then adjacent epochs (epoch
// boundary race), mirroring HostManager.retrievePayloadsWithAdjacentEpochs.
func retrievePayloadResponse(ctx context.Context, r PayloadRetriever, escrowID string, inferenceID, epochID uint64) ([]byte, []byte, error) {
	prompt, response, err := r.Retrieve(ctx, escrowID, inferenceID, epochID)
	if err == nil {
		return prompt, response, nil
	}
	if !errors.Is(err, payloads.ErrNotFound) {
		return nil, nil, err
	}
	adjacent := make([]uint64, 0, 2)
	if epochID > 0 {
		adjacent = append(adjacent, epochID-1)
	}
	adjacent = append(adjacent, epochID+1)
	for _, adj := range adjacent {
		prompt, response, err = r.Retrieve(ctx, escrowID, inferenceID, adj)
		if err == nil {
			return prompt, response, nil
		}
		if !errors.Is(err, payloads.ErrNotFound) {
			return nil, nil, err
		}
	}
	return nil, nil, payloads.ErrNotFound
}

// ensureConfirmStartLocked returns an executor receipt, reusing an existing
// mempool MsgConfirmStart for this inference when present (R1 no-op on reconnect).
// Caller must hold h.mu.
func (h *Host) ensureConfirmStartLocked(inferenceID uint64, rec types.InferenceRecord) ([]byte, int64, error) {
	if sig, confirmedAt, ok := h.findConfirmStartLocked(inferenceID); ok {
		return sig, confirmedAt, nil
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
		return nil, 0, observability.Classify(observability.ReasonReceiptMarshalErr, observability.WhereHostSignReceipt, fmt.Errorf("marshal executor receipt: %w", err))
	}
	sig, err := h.signer.Sign(receiptData)
	if err != nil {
		return nil, 0, observability.Classify(observability.ReasonReceiptSignErr, observability.WhereHostSignReceipt, fmt.Errorf("sign executor receipt: %w", err))
	}
	h.mempool.Add(MempoolEntry{
		Tx: &types.DevshardTx{Tx: &types.DevshardTx_ConfirmStart{ConfirmStart: &types.MsgConfirmStart{
			InferenceId: inferenceID,
			ExecutorSig: sig,
			ConfirmedAt: confirmedAt,
		}}},
		ProposedAt: h.sm.LatestNonce(),
	})
	return sig, confirmedAt, nil
}

func (h *Host) findConfirmStartLocked(inferenceID uint64) (sig []byte, confirmedAt int64, ok bool) {
	for _, tx := range h.mempool.Txs() {
		if cs := tx.GetConfirmStart(); cs != nil && cs.InferenceId == inferenceID {
			return cs.ExecutorSig, cs.ConfirmedAt, true
		}
	}
	return nil, 0, false
}

func (h *Host) pruneLiveStreamLocked(inferenceID uint64) {
	stream := h.liveStreams[inferenceID]
	if stream == nil {
		return
	}
	if !stream.Expired(time.Now()) {
		return
	}
	// Detach from the attach map so reconnects fail cleanly, but do not Close:
	// Close would tear down the primary reader and discard in-flight ML output
	// for a still-producing generation.
	delete(h.liveStreams, inferenceID)
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
// Execution lifetime is detached from the gateway request context inside the
// inference engine: a client disconnect stops proxying but does not abort ML
// drain, payload store, or finish publication (bounded by the engine drain timeout).
//
// While executing, a LiveStream hub is registered so same-nonce reconnects can
// attach from a resume cursor (R6). On success the body is persisted to
// completedResponses and the live buffer is forgotten.
func (h *Host) RunExecution(ctx context.Context, job *devshard.ExecuteRequest) (*devshard.ExecuteResult, error) {
	// Find the internal job metadata for cleanup/mempool.
	inferenceID := job.InferenceID
	executorSlot := h.group[inferenceID%uint64(len(h.group))].SlotID
	diffNonce := h.LatestNonce()

	stream := newLiveStream()
	if job.ResponseWriter != nil {
		stream.SetPrimary(job.ResponseWriter)
		job.ResponseWriter = stream
	}
	h.mu.Lock()
	h.liveStreams[inferenceID] = stream
	h.mu.Unlock()

	defer func() {
		h.ReleaseExecution(inferenceID)
		h.mu.Lock()
		delete(h.liveStreams, inferenceID)
		h.mu.Unlock()
		stream.Close(nil)
		// The caller writes devshard_meta to the same ResponseWriter, so the
		// primary reader must be finished with it before we return.
		stream.WaitPrimary()
	}()

	result, err := h.engine.Execute(ctx, *job)
	if err != nil {
		stream.Close(err)
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
		stream.Close(err)
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
		// Drain complete → persist → forget live RAM buffer.
		delete(h.liveStreams, inferenceID)
		h.mu.Unlock()
	}
	stream.Close(nil)
	observability.SetMempoolSize(h.escrowID, h.mempool.Len())

	return result, nil
}

// AttachLiveStream joins an in-flight inference from the resume cursor and
// blocks until the stream completes or the writer fails.
func (h *Host) AttachLiveStream(inferenceID uint64, w http.ResponseWriter, deliveredEvents, deliveredPartial int64) error {
	if err := validateResumeCursor(deliveredEvents, deliveredPartial); err != nil {
		return err
	}
	h.mu.Lock()
	h.pruneLiveStreamLocked(inferenceID)
	stream := h.liveStreams[inferenceID]
	h.mu.Unlock()
	if stream == nil {
		// Covers never-registered, drain→persist→forget, and TTL detach alike:
		// prune removes the map entry without Close, so it surfaces here rather
		// than as ErrLiveStreamPruned.
		return ErrLiveStreamGone
	}
	return stream.Subscribe(w, deliveredEvents, deliveredPartial)
}

// LiveStreamForTest exposes the live hub for unit tests.
func (h *Host) LiveStreamForTest(inferenceID uint64) *LiveStream {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.liveStreams[inferenceID]
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
	h.validationLifecycleMu.RLock()
	q := h.validationQueue
	closed := h.validationClosed
	h.validationLifecycleMu.RUnlock()
	if h.validator == nil || q == nil || closed {
		return nil
	}
	if !h.completionRequestsEnabled() {
		return nil
	}

	st := h.sm.SnapshotState()
	available := cap(q) - len(q)
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

func (h *Host) startValidationWorkers(q <-chan validateJob, count int) {
	for i := 0; i < count; i++ {
		go func() {
			for job := range q {
				h.validateAsync(context.Background(), job)
			}
		}()
	}
}

func (h *Host) enqueueValidation(job validateJob) {
	h.validationLifecycleMu.RLock()
	q := h.validationQueue
	closed := h.validationClosed
	if q == nil || closed {
		h.validationLifecycleMu.RUnlock()
		h.mu.Lock()
		delete(h.validating, job.inferenceID)
		h.mu.Unlock()
		return
	}

	select {
	case q <- job:
		h.validationLifecycleMu.RUnlock()
		observability.IncValidation(observability.StageValidationPicked, observability.MetricStatusQueued)
		observability.SetValidationQueueDepth(h.escrowID, len(h.validationQueue))
	default:
		h.validationLifecycleMu.RUnlock()
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

	if h.validationRecorder != nil {
		if err := h.validationRecorder.AllowValidationSubmit(ctx, h.escrowID, job.inferenceID); err != nil {
			logging.Info("validation submit abandoned after lease check",
				"subsystem", "host",
				"inference_id", job.inferenceID,
				"error", err,
			)
			return
		}
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

	if h.validationRecorder != nil {
		if err := h.validationRecorder.MarkValidationSubmitted(ctx, h.escrowID, job.inferenceID); err != nil {
			if errors.Is(err, devshard.ErrValidationLeaseAbandoned) {
				logging.Info("mark validation submitted skipped: lease abandoned", "subsystem", "host", "inference_id", job.inferenceID, "error", err)
			} else {
				logging.Error("mark validation submitted failed", "subsystem", "host", "inference_id", job.inferenceID, "error", err)
			}
		}
	}
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
		if err := h.applyAndPersistReconciling(ctx, diff); err != nil {
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
		if err := h.applyAndPersistReconciling(context.Background(), diff); err != nil {
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

// VerifyPayload checks that an InferencePayload matches the expected on-chain fields
// and that those accounting fields cover the actual prompt workload.
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
	if err := verifyPayloadWorkload(p); err != nil {
		return err
	}
	return nil
}

// verifyPayloadWorkload binds signed accounting fields to the prompt body the
// executor will run: input_length must equal prompt byte length (gateway
// convention), and declared max_tokens must cover the effective body limit.
func verifyPayloadWorkload(p *InferencePayload) error {
	promptLen := uint64(len(p.Prompt))
	if p.InputLength != promptLen {
		return fmt.Errorf("%w: input_length %d != prompt bytes %d", types.ErrPayloadMismatch, p.InputLength, promptLen)
	}
	bodyMaxTokens, err := completionapi.EffectiveMaxTokens(p.Prompt)
	if err != nil {
		return fmt.Errorf("%w: prompt max_tokens: %v", types.ErrPayloadMismatch, err)
	}
	if bodyMaxTokens > p.MaxTokens {
		return fmt.Errorf("%w: prompt max_tokens %d exceeds declared %d", types.ErrPayloadMismatch, bodyMaxTokens, p.MaxTokens)
	}
	return nil
}
