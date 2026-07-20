package state

import (
	"bytes"
	"fmt"
	"maps"
	"math"
	"slices"
	"sync"

	"google.golang.org/protobuf/proto"

	"devshard/logging"
	"devshard/signing"
	"devshard/storage"
	"devshard/types"
)

func safeMul(a, b uint64) (uint64, bool) {
	if a == 0 || b == 0 {
		return 0, true
	}
	result := a * b
	if result/a != b {
		return 0, false
	}
	return result, true
}

func safeAdd(a, b uint64) (uint64, bool) {
	result := a + b
	if result < a {
		return 0, false
	}
	return result, true
}

// tokenCost computes (a + b) * price with overflow checks.
func tokenCost(a, b, price uint64) (uint64, error) {
	sum, ok := safeAdd(a, b)
	if !ok {
		return 0, types.ErrCostOverflow
	}
	cost, ok := safeMul(sum, price)
	if !ok {
		return 0, types.ErrCostOverflow
	}
	return cost, nil
}

// copyInferences deep-copies an inferences map.
func copyInferences(src map[uint64]*types.InferenceRecord) map[uint64]*types.InferenceRecord {
	dst := make(map[uint64]*types.InferenceRecord, len(src))
	for k, v := range src {
		cp := *v
		if v.PromptHash != nil {
			cp.PromptHash = append([]byte(nil), v.PromptHash...)
		}
		if v.ResponseHash != nil {
			cp.ResponseHash = append([]byte(nil), v.ResponseHash...)
		}
		dst[k] = &cp
	}
	return dst
}

// WarmKeyResolver checks whether warmAddr is authorized to act on behalf of
// coldAddr. Injected callback, wraps a cached bridge query. Called at most
// once per slot. Set to nil when warm keys are not used.
type WarmKeyResolver func(warmAddr, coldAddr string) (bool, error)

// StateMachine applies diffs and tracks session state.
// The embedded RWMutex protects mutable fields in state (Inferences,
// HostStats, WarmKeys, Balance, Phase, nonces).
// Immutable lookup maps (slotToAddress, etc.) are safe to read without locking.
type StateMachine struct {
	mu          sync.RWMutex
	state       *types.EscrowState
	verifier    signing.Verifier
	userAddress string
	// committedEntries keeps the canonical protobuf entry bytes for every
	// inference ID ever created in the session, including records already sealed
	// out of Mutable.Inferences. This preserves byte-identical state roots under
	// Phase 0 without rehydrating the full record set from storage on each diff.
	committedEntries map[uint64][]byte
	// sealedNonces remembers the nonce at which each evicted inference was
	// sealed. It is the only piece of per-id seal metadata that survives in
	// the durable sealed-inference index; everything else needed for cold-path
	// validation lives in committedEntries (and on disk in the snapshot).
	sealedNonces map[uint64]uint64
	inferenceStore    storage.Storage

	// Lookup maps derived from group at construction time.
	slotToAddress      map[uint32]string
	addressToSlotCount map[string]uint32
	addressToSlots     map[string][]uint32 // address -> sorted slot IDs
	totalSlots         uint32

	warmResolver    WarmKeyResolver       // optional, nil = no warm key support
}

// SMOption configures optional StateMachine behavior.
type SMOption func(*StateMachine)

// WithWarmKeyResolver sets a callback for warm key verification.
func WithWarmKeyResolver(r WarmKeyResolver) SMOption {
	return func(sm *StateMachine) { sm.warmResolver = r }
}

// WithStateRootAndProtocolVersion binds the state-root and settlement protocol
// tag (not the versiond runtime name). Callers must pass a non-empty value.
func WithStateRootAndProtocolVersion(version string) SMOption {
	return func(sm *StateMachine) {
		sm.state.StateRootAndProtocolVersion = version
	}
}

// WithVersion is an alias for WithStateRootAndProtocolVersion.
func WithVersion(version string) SMOption {
	return WithStateRootAndProtocolVersion(version)
}

// EffectiveV2Composition reports whether this session uses Phase 1 v2
// state-root composition. This binary always returns true (sealed accumulator).
func (sm *StateMachine) EffectiveV2Composition() bool {
	return true
}

func NewStateMachine(
	escrowID string,
	config types.SessionConfig,
	group []types.SlotAssignment,
	balance uint64,
	userAddress string,
	verifier signing.Verifier,
	store storage.Storage,
	opts ...SMOption,
) (*StateMachine, error) {
	if store == nil {
		return nil, fmt.Errorf("inference store is required")
	}
	slotToAddr := make(map[uint32]string, len(group))
	addrToSlotCount := make(map[string]uint32, len(group))
	for _, s := range group {
		slotToAddr[s.SlotID] = s.ValidatorAddress
		addrToSlotCount[s.ValidatorAddress]++
	}
	config = types.NormalizeSessionConfig(config, len(group))

	groupCopy := make([]types.SlotAssignment, len(group))
	copy(groupCopy, group)

	hostStats := make(map[uint32]*types.HostStats, len(group))
	for _, s := range group {
		hostStats[s.SlotID] = &types.HostStats{}
	}

	addrToSlots := make(map[string][]uint32, len(addrToSlotCount))
	for slot, addr := range slotToAddr {
		addrToSlots[addr] = append(addrToSlots[addr], slot)
	}
	for _, slots := range addrToSlots {
		slices.Sort(slots)
	}

	// Charge the one-time devshard creation fee at state initialization.
	if balance < config.CreateDevshardFee {
		return nil, fmt.Errorf("%w: create devshard fee %d exceeds escrow amount %d",
			types.ErrInsufficientBalance, config.CreateDevshardFee, balance)
	}
	initialBalance := balance - config.CreateDevshardFee

	sm := &StateMachine{
		state: &types.EscrowState{
			EscrowID:   escrowID,
			StateRootAndProtocolVersion: types.EffectiveStateRootAndProtocolVersion,
			Config:     config,
			Group:      groupCopy,
			Balance:    initialBalance,
			Fees:       config.CreateDevshardFee,
			Inferences: make(map[uint64]*types.InferenceRecord),
			HostStats:  hostStats,
			WarmKeys:   make(map[uint32]string),
		},
		verifier:           verifier,
		userAddress:        userAddress,
		slotToAddress:      slotToAddr,
		addressToSlotCount: addrToSlotCount,
		addressToSlots:     addrToSlots,
		totalSlots:         uint32(len(group)),
		committedEntries:   make(map[uint64][]byte),
		sealedNonces:       make(map[uint64]uint64),
		inferenceStore:     store,
	}
	for _, o := range opts {
		o(sm)
	}

	logging.Info("NewStateMachine", "subsystem", "state",
		"escrow_id", escrowID,
		"group_size", len(group),
		"state_root_and_protocol_version", sm.state.StateRootAndProtocolVersion,
		"balance", initialBalance,
		"create_devshard_fee", config.CreateDevshardFee,
		"token_price", config.TokenPrice,
		"vote_threshold", config.VoteThreshold,
		"validation_rate", config.ValidationRate,
		"user_address", userAddress,
	)

	return sm, nil
}

// ApplyDiff validates user signature and post_state_root, then applies the diff.
// Returns the computed state root.
func (sm *StateMachine) ApplyDiff(diff types.Diff) ([]byte, error) {
	// 1. Verify user signature (covers nonce, txs, escrow_id, post_state_root).
	diffContent := BuildDiffContent(sm.state.EscrowID, diff.Nonce, diff.Txs, diff.PostStateRoot)
	data, err := deterministicMarshal.Marshal(diffContent)
	if err != nil {
		return nil, fmt.Errorf("marshal diff content: %w", err)
	}

	recovered, err := sm.verifier.RecoverAddress(data, diff.UserSig)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", types.ErrInvalidUserSig, err)
	}
	if recovered != sm.userAddress {
		return nil, fmt.Errorf("%w: expected %s, got %s", types.ErrInvalidUserSig, sm.userAddress, recovered)
	}

	// 2. Apply txs and verify post_state_root atomically.
	sm.mu.Lock()
	defer sm.mu.Unlock()
	return sm.applyCore(diff.Nonce, diff.Txs, diff.PostStateRoot, "host")
}

// ApplyLocal applies txs without signature verification. Used by the user
// to compute the post_state_root before signing the diff.
func (sm *StateMachine) ApplyLocal(nonce uint64, txs []*types.DevshardTx) ([]byte, error) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	return sm.applyCore(nonce, txs, nil, "user")
}

// ApplyLocalBestEffort applies txs one by one, skipping any that fail.
// Returns the post-state root and the subset of txs that were applied.
// Used by the user to compose diffs from pending txs that may be stale.
func (sm *StateMachine) ApplyLocalBestEffort(nonce uint64, txs []*types.DevshardTx) ([]byte, []*types.DevshardTx, error) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	// Snapshot mutable state so fee charging and root computation remain atomic
	// with respect to this nonce, matching applyCore semantics.
	snap := sm.snapshotMutable()

	expectedNonce := sm.state.LatestNonce + 1
	if nonce != expectedNonce {
		return nil, nil, fmt.Errorf("%w: expected %d, got %d", types.ErrInvalidNonce, expectedNonce, nonce)
	}

	// Same pre-check as applyCore: at most one MsgStartInference, with id == nonce.
	startCount := 0
	for _, tx := range txs {
		if start := tx.GetStartInference(); start != nil {
			startCount++
			if start.InferenceId != nonce {
				return nil, nil, types.ErrInvalidInferenceID
			}
		}
	}
	if startCount > 1 {
		return nil, nil, types.ErrMultipleStartMsgs
	}

	// All applyTx implementations are check-first-mutate-last:
	// preconditions are validated before any state mutation, so a
	// failed tx leaves state unchanged. No per-tx snapshots needed.
	var applied []*types.DevshardTx
	for _, tx := range txs {
		if err := sm.applyTx(tx); err != nil {
			if tx.GetStartInference() != nil {
				sm.restoreMutable(snap)
				return nil, nil, fmt.Errorf("mandatory start inference: %w", err)
			}
			continue
		}
		applied = append(applied, tx)
	}

	// Charge per applied nonce only during the active phase.
	// NOTE: During the finalization round, the `txs` slice will contain a [types.MsgFinalizeRound] message,
	// the call to [StateMachine.applyTx] above will transition the state machine's phase to [types.PhaseFinalizing],
	// and this block will be skipped.
	if sm.state.Phase == types.PhaseActive {
		if sm.state.Balance < sm.state.Config.FeePerNonce {
			sm.restoreMutable(snap)
			return nil, nil, types.ErrInsufficientBalance
		}
		sm.state.Balance -= sm.state.Config.FeePerNonce
		sm.state.Fees += sm.state.Config.FeePerNonce
	}

	sm.state.LatestNonce = nonce

	if sm.state.Phase == types.PhaseFinalizing && sm.state.FinalizeNonce == 0 {
		sm.state.FinalizeNonce = nonce
	}
	if sm.state.Phase == types.PhaseFinalizing {
		deadlinePassed := sm.state.LatestNonce >= sm.state.FinalizeNonce+uint64(len(sm.state.Group))
		if deadlinePassed {
			sm.state.Phase = types.PhaseSettlement
			if err := sm.drainLiveIntoSealedAccLocked(sm.state.LatestNonce); err != nil {
				sm.restoreMutable(snap)
				return nil, nil, fmt.Errorf("drain live into sealed_acc: %w", err)
			}
		}
	}

	// Deterministically seal inferences whose grace gates have cleared, before
	// the root is computed, so the user's signed post_state_root commits to the
	// same seal the host will fold. Reads only state (nonce + ConfirmedAt clock).
	if sm.state.Phase == types.PhaseActive && shouldAutoSealAtNonce(sm.autoSealIntervalLocked(), nonce) {
		if _, _, err := sm.autoSealLocked("user", nonce); err != nil {
			sm.restoreMutable(snap)
			return nil, nil, fmt.Errorf("auto-seal: %w", err)
		}
	}

	root, err := sm.computeStateRootLocked()
	if err != nil {
		sm.restoreMutable(snap)
		return nil, nil, fmt.Errorf("compute state root: %w", err)
	}

	logging.Debug("applied diff (best-effort)", "subsystem", "state",
		"nonce", nonce, "applied", len(applied), "candidates", len(txs),
		"balance", sm.state.Balance,
		"group_size", len(sm.state.Group),
		"host_stats_count", len(sm.state.HostStats),
		"config_token_price", sm.state.Config.TokenPrice,
		"config_fee_per_nonce", sm.state.Config.FeePerNonce,
	)
	return root, applied, nil
}

// applyCore validates nonce, applies txs, updates nonce, and returns the state root.
// If postStateRoot is non-nil, the computed root must match; on mismatch the entire
// operation is rolled back (including nonce) and an error is returned.
func (sm *StateMachine) applyCore(nonce uint64, txs []*types.DevshardTx, postStateRoot []byte, side string) ([]byte, error) {
	// 1. Validate nonce.
	expectedNonce := sm.state.LatestNonce + 1
	if nonce != expectedNonce {
		return nil, fmt.Errorf("%w: expected %d, got %d", types.ErrInvalidNonce, expectedNonce, nonce)
	}

	// 2. Validate at most one MsgStartInference per diff, and inference_id == nonce.
	startCount := 0
	for _, tx := range txs {
		if start := tx.GetStartInference(); start != nil {
			startCount++
			if start.InferenceId != nonce {
				return nil, types.ErrInvalidInferenceID
			}
		}
	}
	if startCount > 1 {
		return nil, types.ErrMultipleStartMsgs
	}

	// 3. Snapshot mutable state for rollback on error.
	snap := sm.snapshotMutable()

	// 4. Apply each tx.
	for _, tx := range txs {
		if err := sm.applyTx(tx); err != nil {
			sm.restoreMutable(snap)
			return nil, err
		}
	}

	// 5. Charge per applied nonce only during the active phase.
	if sm.state.Phase == types.PhaseActive {
		if sm.state.Balance < sm.state.Config.FeePerNonce {
			sm.restoreMutable(snap)
			return nil, types.ErrInsufficientBalance
		}
		sm.state.Balance -= sm.state.Config.FeePerNonce
		sm.state.Fees += sm.state.Config.FeePerNonce
	}

	// 6. Update nonce.
	sm.state.LatestNonce = nonce

	// Track FinalizeNonce: the nonce at which finalization started.
	if sm.state.Phase == types.PhaseFinalizing && sm.state.FinalizeNonce == 0 {
		sm.state.FinalizeNonce = nonce
	}

	if sm.state.Phase == types.PhaseFinalizing {
		// Auto-transition Finalizing -> Settlement on deadline only.
		deadlinePassed := sm.state.LatestNonce >= sm.state.FinalizeNonce+uint64(len(sm.state.Group))
		if deadlinePassed {
			sm.state.Phase = types.PhaseSettlement
			// Under v2 composition, settling is the natural moment to drain
			// any record still live into the sealed accumulator. This keeps
			// the settlement payload size bounded (no live records on the
			// wire) and lets the chain recompute rest_hash from sealed_acc
			// alone. See devshard/docs/inferences-pruning.md \u00a71.4.
			if err := sm.drainLiveIntoSealedAccLocked(sm.state.LatestNonce); err != nil {
				sm.restoreMutable(snap)
				return nil, fmt.Errorf("drain live into sealed_acc: %w", err)
			}
		}
	}

	// 6b. Deterministically seal inferences whose grace gates have cleared,
	// folding them into SealedAcc before the root is computed so the seal is
	// part of post_state_root. The decision reads only state (nonce + the
	// ConfirmedAt-derived state clock), so user, host and replay all agree.
	var sealClockWin StateClockWindow
	if sm.state.Phase == types.PhaseActive && shouldAutoSealAtNonce(sm.autoSealIntervalLocked(), nonce) {
		var err error
		_, sealClockWin, err = sm.autoSealLocked(side, nonce)
		if err != nil {
			sm.restoreMutable(snap)
			return nil, fmt.Errorf("auto-seal: %w", err)
		}
	}

	// 7. Compute state root.
	root, err := sm.computeStateRootLocked()
	if err != nil {
		sm.restoreMutable(snap)
		return nil, fmt.Errorf("compute state root: %w", err)
	}

	// 8. Verify post_state_root if present. On mismatch, roll back everything.
	if len(postStateRoot) > 0 && !bytes.Equal(root, postStateRoot) {
		sm.logStateRootMismatchDiagnosticLocked(StateRootMismatchOpts{
			Side:          "devshardd",
			Nonce:         nonce,
			DiffPostState: postStateRoot,
			ComputedState: root,
			SealClock:     sealClockWin,
		})
		sm.restoreMutable(snap)
		return nil, fmt.Errorf("%w: diff %x, computed %x", types.ErrPostStateRootMismatch, postStateRoot, root)
	}

	logging.Debug("applied diff", "subsystem", "state", "nonce", nonce, "txs", len(txs))
	return root, nil
}

// LatestNonce returns the current nonce without deep-copying state.
func (sm *StateMachine) LatestNonce() uint64 {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return sm.state.LatestNonce
}

// Phase returns the current session phase.
func (sm *StateMachine) Phase() types.SessionPhase {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return sm.state.Phase
}

// Balance returns the current escrow balance.
func (sm *StateMachine) Balance() uint64 {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return sm.state.Balance
}

// Config returns a copy of the session config (a small value type). Use this
// instead of SnapshotState().Config to avoid deep-copying the inference map.
func (sm *StateMachine) Config() types.SessionConfig {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return sm.state.Config
}

// SnapshotState returns a deep copy of the current escrow state.
func (sm *StateMachine) SnapshotState() types.EscrowState {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return *cloneEscrowState(sm.state)
}

// SnapshotStateNoInferences returns a deep copy of the escrow state with the
// (potentially large) inference map omitted. All other fields, including the
// small per-slot maps, are copied. Use it for summary/state endpoints that do
// not render individual inference records, avoiding the cost of copying up to
// tens of thousands of them.
func (sm *StateMachine) SnapshotStateNoInferences() types.EscrowState {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	src := sm.state
	// Shallow struct copy; SealedAcc ([]byte) is shared deliberately: it is
	// only ever replaced wholesale (append to a nil slice), never mutated in
	// place, so readers of the snapshot see a stable value.
	s := *src
	s.Inferences = nil

	s.Group = make([]types.SlotAssignment, len(src.Group))
	copy(s.Group, src.Group)

	s.HostStats = make(map[uint32]*types.HostStats, len(src.HostStats))
	for k, v := range src.HostStats {
		cp := *v
		s.HostStats[k] = &cp
	}

	s.WarmKeys = make(map[uint32]string, len(src.WarmKeys))
	maps.Copy(s.WarmKeys, src.WarmKeys)

	return s
}

// ExportState returns a deep-copied pointer form used by recovery snapshots.
func (sm *StateMachine) ExportState() *types.EscrowState {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return cloneEscrowState(sm.state)
}

// RestoreState replaces the current escrow state with a deep copy from storage.
func (sm *StateMachine) RestoreState(state *types.EscrowState) {
	if state == nil {
		return
	}
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.state = cloneEscrowState(state)
	sm.rebuildCommittedEntriesLocked()
}

func cloneEscrowState(src *types.EscrowState) *types.EscrowState {
	s := *src

	// Deep copy Group.
	s.Group = make([]types.SlotAssignment, len(src.Group))
	copy(s.Group, src.Group)

	// Deep copy HostStats.
	s.HostStats = make(map[uint32]*types.HostStats, len(src.HostStats))
	for k, v := range src.HostStats {
		cp := *v
		s.HostStats[k] = &cp
	}

	// Deep copy WarmKeys.
	s.WarmKeys = make(map[uint32]string, len(src.WarmKeys))
	maps.Copy(s.WarmKeys, src.WarmKeys)

	// Deep copy Inferences.
	s.Inferences = copyInferences(src.Inferences)

	if len(src.SealedAcc) > 0 {
		s.SealedAcc = append([]byte(nil), src.SealedAcc...)
	}

	return &s
}

// SnapshotInferences returns a deep copy of just the inference map. Use it for
// endpoints that render the full inference list without paying to copy the
// rest of the escrow state.
func (sm *StateMachine) SnapshotInferences() map[uint64]*types.InferenceRecord {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return copyInferences(sm.state.Inferences)
}

// InferenceStatusCounts returns the total number of inferences and a per-status
// breakdown, computed under the read lock without deep-copying any records.
func (sm *StateMachine) InferenceStatusCounts() (int, map[types.InferenceStatus]int) {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	counts := make(map[types.InferenceStatus]int)
	for _, rec := range sm.state.Inferences {
		counts[rec.Status]++
	}
	return len(sm.state.Inferences), counts
}

// mutableSnapshot holds the mutable fields of EscrowState for rollback.
type mutableSnapshot struct {
	Balance       uint64
	Fees          uint64
	Phase         types.SessionPhase
	FinalizeNonce uint64
	LatestNonce   uint64
	Inferences    map[uint64]*types.InferenceRecord
	Committed     map[uint64][]byte
	HostStats     map[uint32]*types.HostStats
	WarmKeys      map[uint32]string
	SealedAcc     []byte
	SealedNonces  map[uint64]uint64
}

func (sm *StateMachine) snapshotMutable() mutableSnapshot {
	infCopy := copyInferences(sm.state.Inferences)

	hsCopy := make(map[uint32]*types.HostStats, len(sm.state.HostStats))
	for k, v := range sm.state.HostStats {
		cp := *v
		hsCopy[k] = &cp
	}

	warmCopy := make(map[uint32]string, len(sm.state.WarmKeys))
	maps.Copy(warmCopy, sm.state.WarmKeys)

	sealedNoncesCopy := make(map[uint64]uint64, len(sm.sealedNonces))
	maps.Copy(sealedNoncesCopy, sm.sealedNonces)

	return mutableSnapshot{
		Balance:       sm.state.Balance,
		Fees:          sm.state.Fees,
		Phase:         sm.state.Phase,
		FinalizeNonce: sm.state.FinalizeNonce,
		LatestNonce:   sm.state.LatestNonce,
		Inferences:    infCopy,
		Committed:     cloneCommittedInferenceEntries(sm.committedEntries),
		HostStats:     hsCopy,
		WarmKeys:      warmCopy,
		SealedAcc:     append([]byte(nil), sm.state.SealedAcc...),
		SealedNonces:  sealedNoncesCopy,
	}
}

func (sm *StateMachine) restoreMutable(snap mutableSnapshot) {
	sm.state.Balance = snap.Balance
	sm.state.Fees = snap.Fees
	sm.state.Phase = snap.Phase
	sm.state.FinalizeNonce = snap.FinalizeNonce
	sm.state.LatestNonce = snap.LatestNonce
	sm.state.Inferences = snap.Inferences
	sm.committedEntries = snap.Committed
	sm.state.HostStats = snap.HostStats
	sm.state.WarmKeys = snap.WarmKeys
	sm.state.SealedAcc = append([]byte(nil), snap.SealedAcc...)
	sm.sealedNonces = snap.SealedNonces
}

func (sm *StateMachine) isDuplicateInferenceID(id uint64) bool {
	if _, ok := sm.state.Inferences[id]; ok {
		return true
	}
	if _, ok := sm.committedEntries[id]; ok {
		return true
	}
	_, sealed := sm.sealedNonces[id]
	return sealed
}

// isInferenceEvictedFromLive reports whether id is known but no longer in the
// live RAM map (sealed into SealedAcc; may still be in sealedNonces).
func (sm *StateMachine) isInferenceEvictedFromLive(id uint64) bool {
	if _, live := sm.state.Inferences[id]; live {
		return false
	}
	if _, ok := sm.committedEntries[id]; ok {
		return true
	}
	_, sealed := sm.sealedNonces[id]
	return sealed
}

// ComputeStateRoot returns the current state root without modifying state.
func (sm *StateMachine) ComputeStateRoot() ([]byte, error) {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return sm.computeStateRootLocked()
}

// WarmKeys returns the current warm key bindings (shallow copy).
func (sm *StateMachine) WarmKeys() map[uint32]string {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	if len(sm.state.WarmKeys) == 0 {
		return nil
	}
	cp := make(map[uint32]string, len(sm.state.WarmKeys))
	maps.Copy(cp, sm.state.WarmKeys)
	return cp
}

// IsWarmKeyAddress returns true if addr is a known warm key for any slot.
func (sm *StateMachine) IsWarmKeyAddress(addr string) bool {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	for _, warmAddr := range sm.state.WarmKeys {
		if warmAddr == addr {
			return true
		}
	}
	return false
}

func (sm *StateMachine) applyTx(tx *types.DevshardTx) error {
	switch inner := tx.GetTx().(type) {
	case *types.DevshardTx_StartInference:
		return sm.applyStartInference(inner.StartInference)
	case *types.DevshardTx_ConfirmStart:
		return sm.applyConfirmStart(inner.ConfirmStart)
	case *types.DevshardTx_FinishInference:
		return sm.applyFinishInference(inner.FinishInference)
	case *types.DevshardTx_Validation:
		return sm.applyValidation(inner.Validation)
	case *types.DevshardTx_ValidationVote:
		return sm.applyValidationVote(inner.ValidationVote)
	case *types.DevshardTx_TimeoutInference:
		return sm.applyTimeout(inner.TimeoutInference)
	case *types.DevshardTx_RevealSeed:
		return sm.applyRevealSeed(inner.RevealSeed)
	case *types.DevshardTx_FinalizeRound:
		return sm.applyFinalizeRound()
	default:
		return types.ErrEmptyTx
	}
}

func (sm *StateMachine) applyStartInference(msg *types.MsgStartInference) error {
	if sm.state.Phase != types.PhaseActive {
		return types.ErrSessionFinalizing
	}

	// Duplicate inference ID guard.
	if sm.isDuplicateInferenceID(msg.InferenceId) {
		return types.ErrDuplicateInferenceID
	}

	// Executor slot: group[inference_id % len(group)].SlotID
	executorSlot := sm.state.Group[msg.InferenceId%uint64(len(sm.state.Group))].SlotID

	// Reserve cost: (input_length + max_tokens) * token_price
	reservedCost, err := tokenCost(msg.InputLength, msg.MaxTokens, sm.state.Config.TokenPrice)
	if err != nil {
		return err
	}
	if sm.state.Balance < reservedCost {
		return types.ErrInsufficientBalance
	}

	sm.state.Balance -= reservedCost

	rec := &types.InferenceRecord{
		Status:       types.StatusPending,
		ExecutorSlot: executorSlot,
		Model:        msg.Model,
		PromptHash:   msg.PromptHash,
		InputLength:  msg.InputLength,
		MaxTokens:    msg.MaxTokens,
		ReservedCost: reservedCost,
		StartedAt:    msg.StartedAt,
	}

	sm.state.Inferences[msg.InferenceId] = rec
	if err := sm.updateCommittedEntryLocked(msg.InferenceId, rec); err != nil {
		return err
	}
	logging.Debug("inference -> pending", "subsystem", "state",
		"inference_id", msg.InferenceId,
		"executor_slot", executorSlot,
		"model", msg.Model,
		"reserved_cost", reservedCost,
	)
	return nil
}

func (sm *StateMachine) applyConfirmStart(msg *types.MsgConfirmStart) error {
	rec, ok := sm.state.Inferences[msg.InferenceId]
	if !ok {
		if sm.isInferenceEvictedFromLive(msg.InferenceId) {
			return fmt.Errorf("%w: inference %d is sealed", types.ErrInvalidTransition, msg.InferenceId)
		}
		return fmt.Errorf("%w: inference %d", types.ErrInferenceNotFound, msg.InferenceId)
	}
	if rec.Status != types.StatusPending {
		return fmt.Errorf("%w: expected pending, got %d", types.ErrInvalidTransition, rec.Status)
	}

	// Verify executor receipt (includes confirmed_at from the executor's wall clock).
	receiptContent := &types.ExecutorReceiptContent{
		InferenceId: msg.InferenceId,
		PromptHash:  rec.PromptHash,
		Model:       rec.Model,
		InputLength: rec.InputLength,
		MaxTokens:   rec.MaxTokens,
		StartedAt:   rec.StartedAt,
		EscrowId:    sm.state.EscrowID,
		ConfirmedAt: msg.ConfirmedAt,
	}
	receiptData, err := deterministicMarshal.Marshal(receiptContent)
	if err != nil {
		return fmt.Errorf("marshal executor receipt: %w", err)
	}

	recovered, err := sm.verifier.RecoverAddress(receiptData, msg.ExecutorSig)
	if err != nil {
		return fmt.Errorf("%w: %v", types.ErrInvalidExecutorSig, err)
	}

	expectedAddr := sm.slotToAddress[rec.ExecutorSlot]
	if recovered != expectedAddr {
		if !sm.ResolveWarmKey(rec.ExecutorSlot, recovered, expectedAddr) {
			return fmt.Errorf("%w: expected executor %s (slot %d), got %s",
				types.ErrInvalidExecutorSig, expectedAddr, rec.ExecutorSlot, recovered)
		}
	}

	rec.Status = types.StatusStarted
	rec.ConfirmedAt = msg.ConfirmedAt
	logging.Debug("inference pending -> started", "subsystem", "state",
		"inference_id", msg.InferenceId,
		"executor_slot", rec.ExecutorSlot,
		"confirmed_at", msg.ConfirmedAt,
	)
	return sm.updateCommittedEntryLocked(msg.InferenceId, rec)
}

func (sm *StateMachine) applyFinishInference(msg *types.MsgFinishInference) error {
	rec, ok := sm.state.Inferences[msg.InferenceId]
	if !ok {
		if sm.isInferenceEvictedFromLive(msg.InferenceId) {
			return fmt.Errorf("%w: inference %d is sealed", types.ErrInvalidTransition, msg.InferenceId)
		}
		return fmt.Errorf("%w: inference %d", types.ErrInferenceNotFound, msg.InferenceId)
	}
	if rec.Status != types.StatusStarted {
		return fmt.Errorf("%w: expected started, got %d", types.ErrInvalidTransition, rec.Status)
	}

	// Verify executor slot.
	if msg.ExecutorSlot != rec.ExecutorSlot {
		return fmt.Errorf("%w: expected %d, got %d", types.ErrWrongExecutorSlot, rec.ExecutorSlot, msg.ExecutorSlot)
	}

	// Verify proposer signature from executor.
	cloned := proto.Clone(msg).(*types.MsgFinishInference)
	cloned.ProposerSig = nil
	if err := sm.verifyProposerSig(cloned, msg.ProposerSig, sm.slotToAddress[rec.ExecutorSlot], rec.ExecutorSlot); err != nil {
		return err
	}

	// Cross-session replay protection.
	if msg.EscrowId != sm.state.EscrowID {
		return fmt.Errorf("%w: expected %s, got %s", types.ErrEscrowIDMismatch, sm.state.EscrowID, msg.EscrowId)
	}

	// Compute actual cost.
	actualCost, err := tokenCost(msg.InputTokens, msg.OutputTokens, sm.state.Config.TokenPrice)
	if err != nil {
		return err
	}
	if actualCost > rec.ReservedCost {
		actualCost = rec.ReservedCost
	}

	// Release surplus.
	surplus := rec.ReservedCost - actualCost
	sm.state.Balance += surplus

	rec.Status = types.StatusFinished
	rec.ResponseHash = msg.ResponseHash
	rec.InputTokens = msg.InputTokens
	rec.OutputTokens = msg.OutputTokens
	rec.ActualCost = actualCost

	// Update host stats.
	sm.state.HostStats[rec.ExecutorSlot].Cost += actualCost

	logging.Debug("inference started -> finished", "subsystem", "state",
		"inference_id", msg.InferenceId,
		"executor_slot", msg.ExecutorSlot,
		"input_tokens", msg.InputTokens,
		"output_tokens", msg.OutputTokens,
		"actual_cost", actualCost,
	)
	return sm.updateCommittedEntryLocked(msg.InferenceId, rec)
}

func (sm *StateMachine) applyValidation(msg *types.MsgValidation) error {
	rec, ok := sm.state.Inferences[msg.InferenceId]
	if !ok {
		if sealNonce, sealed := sm.sealedNonces[msg.InferenceId]; sealed && sealNonce > 0 {
			return fmt.Errorf("%w: inference %d", types.ErrInferenceSealed, msg.InferenceId)
		}
		return fmt.Errorf("%w: inference %d", types.ErrInferenceNotFound, msg.InferenceId)
	}

	// Common pre-checks.
	if _, ok := sm.slotToAddress[msg.ValidatorSlot]; !ok {
		return fmt.Errorf("%w: slot %d", types.ErrSlotNotInGroup, msg.ValidatorSlot)
	}
	if msg.ValidatorSlot == rec.ExecutorSlot {
		return types.ErrSelfValidation
	}

	// Idempotent: duplicate validation from same address is always a no-op.
	if found, _ := sm.addressHasValidated(rec, msg.ValidatorSlot); found {
		return nil
	}

	// Status gate.
	switch rec.Status {
	case types.StatusFinished, types.StatusChallenged, types.StatusValidated, types.StatusInvalidated:
		// OK
	default:
		return fmt.Errorf("%w: expected finished or later, got %d", types.ErrInvalidTransition, rec.Status)
	}

	// Proposer sig + escrow_id (expensive, after dedup).
	cloned := proto.Clone(msg).(*types.MsgValidation)
	cloned.ProposerSig = nil
	if err := sm.verifyProposerSig(cloned, msg.ProposerSig, sm.slotToAddress[msg.ValidatorSlot], msg.ValidatorSlot); err != nil {
		return err
	}
	if msg.EscrowId != sm.state.EscrowID {
		return fmt.Errorf("%w: expected %s, got %s", types.ErrEscrowIDMismatch, sm.state.EscrowID, msg.EscrowId)
	}

	// Mutation: set bitmap, count vote weight.
	// TODO: only the validator's emitting slot is set here, while
	// applyValidationVote sets every slot owned by the voter address.
	// Consumers (collectValidationJobs and addressHasValidated) both use
	// "any slot of this address" semantics so
	// the asymmetry is benign, but the unified bitmap would be more
	// consistent. Changing it shifts state-machine output, so it requires a
	// coordinated upgrade.
	rec.ValidatedBy.Set(msg.ValidatorSlot)

	// Count vote weight for Finished state (tallies accumulate before any challenge).
	if rec.Status == types.StatusFinished {
		validatorAddr := sm.slotToAddress[msg.ValidatorSlot]
		weight := sm.addressToSlotCount[validatorAddr]
		if msg.Valid {
			rec.VotesValid += weight
		} else {
			rec.VotesInvalid += weight
			rec.Status = types.StatusChallenged
			// Obs row is not part of post_state_root; a storage blip must not fail
			// the tx (ApplyLocalBestEffort would drop it but keep the mutation).
			// Recovery rebuilds obs from the diff journal; see autoSealLocked.
			sm.persistLiveInferenceObsBestEffortLocked(msg.InferenceId, rec)
			logging.Debug("inference finished -> challenged", "subsystem", "state",
				"inference_id", msg.InferenceId,
				"validator_slot", msg.ValidatorSlot,
			)
		}
	}

	return sm.updateCommittedEntryLocked(msg.InferenceId, rec)
}

// addressHasValidated checks if the address owning slotID has any slot bit set in ValidatedBy.
func (sm *StateMachine) addressHasValidated(rec *types.InferenceRecord, slotID uint32) (bool, uint32) {
	addr := sm.slotToAddress[slotID]
	for _, slot := range sm.addressToSlots[addr] {
		if rec.ValidatedBy.IsSet(slot) {
			return true, slot
		}
	}
	return false, 0
}

func (sm *StateMachine) applyValidationVote(msg *types.MsgValidationVote) error {
	rec, ok := sm.state.Inferences[msg.InferenceId]
	if !ok {
		if sealNonce, sealed := sm.sealedNonces[msg.InferenceId]; sealed && sealNonce > 0 {
			return fmt.Errorf("%w: inference %d", types.ErrInferenceSealed, msg.InferenceId)
		}
		return fmt.Errorf("%w: inference %d", types.ErrInferenceNotFound, msg.InferenceId)
	}
	if _, ok := sm.slotToAddress[msg.VoterSlot]; !ok {
		return fmt.Errorf("%w: slot %d", types.ErrSlotNotInGroup, msg.VoterSlot)
	}

	// Skip already-resolved challenge votes (allows safe vote batching).
	if rec.Status == types.StatusValidated || rec.Status == types.StatusInvalidated {
		return nil
	}

	if rec.Status != types.StatusChallenged {
		return fmt.Errorf("%w: expected challenged, got %d", types.ErrInvalidTransition, rec.Status)
	}

	// Dedup: check ValidatedBy (unified bitmap for validators + voters).
	voterAddr := sm.slotToAddress[msg.VoterSlot]
	if found, existingSlot := sm.addressHasValidated(rec, msg.VoterSlot); found {
		return fmt.Errorf("%w: slot %d (address %s already participated via slot %d)",
			types.ErrDuplicateVote, msg.VoterSlot, voterAddr, existingSlot)
	}

	// Verify proposer signature from voter.
	clonedVV := proto.Clone(msg).(*types.MsgValidationVote)
	clonedVV.ProposerSig = nil
	if err := sm.verifyProposerSig(clonedVV, msg.ProposerSig, sm.slotToAddress[msg.VoterSlot], msg.VoterSlot); err != nil {
		return err
	}

	// Cross-session replay protection.
	if msg.EscrowId != sm.state.EscrowID {
		return fmt.Errorf("%w: expected %s, got %s", types.ErrEscrowIDMismatch, sm.state.EscrowID, msg.EscrowId)
	}

	// Mark ALL slots owned by this address in ValidatedBy (unified bitmap).
	weight := sm.addressToSlotCount[voterAddr]
	for _, slot := range sm.addressToSlots[voterAddr] {
		rec.ValidatedBy.Set(slot)
	}
	if msg.VoteValid {
		rec.VotesValid += weight
	} else {
		rec.VotesInvalid += weight
	}

	// VoteThreshold is frozen in state.Config at session creation (see VoteThreshold()).
	threshold := sm.state.Config.VoteThreshold
	if rec.VotesInvalid > threshold {
		rec.Status = types.StatusInvalidated
		// Refund cost.
		sm.state.HostStats[rec.ExecutorSlot].Invalid++
		hs := sm.state.HostStats[rec.ExecutorSlot]
		if hs.Cost < rec.ActualCost {
			hs.Cost = 0
		} else {
			hs.Cost -= rec.ActualCost
		}
		sm.state.Balance += rec.ActualCost
		logging.Debug("inference challenged -> invalidated", "subsystem", "state",
			"inference_id", msg.InferenceId,
			"votes_valid", rec.VotesValid,
			"votes_invalid", rec.VotesInvalid,
		)
	} else if rec.VotesValid > threshold {
		rec.Status = types.StatusValidated
		logging.Debug("inference challenged -> validated", "subsystem", "state",
			"inference_id", msg.InferenceId,
			"votes_valid", rec.VotesValid,
			"votes_invalid", rec.VotesInvalid,
		)
	}

	if rec.Status == types.StatusValidated || rec.Status == types.StatusInvalidated {
		// Same as challenge path: obs is observability-only, never consensus.
		sm.persistLiveInferenceObsBestEffortLocked(msg.InferenceId, rec)
	}

	return sm.updateCommittedEntryLocked(msg.InferenceId, rec)
}

func (sm *StateMachine) applyTimeout(msg *types.MsgTimeoutInference) error {
	rec, ok := sm.state.Inferences[msg.InferenceId]
	if !ok {
		if sm.isInferenceEvictedFromLive(msg.InferenceId) {
			return fmt.Errorf("%w: inference %d is sealed", types.ErrInvalidTransition, msg.InferenceId)
		}
		return fmt.Errorf("%w: inference %d", types.ErrInferenceNotFound, msg.InferenceId)
	}

	// Validate reason matches status.
	switch msg.Reason {
	case types.TimeoutReason_TIMEOUT_REASON_REFUSED:
		if rec.Status != types.StatusPending {
			return fmt.Errorf("%w: reason=refused requires pending, got %d", types.ErrInvalidTimeoutReason, rec.Status)
		}
	case types.TimeoutReason_TIMEOUT_REASON_EXECUTION:
		if rec.Status != types.StatusStarted {
			return fmt.Errorf("%w: reason=execution requires started, got %d", types.ErrInvalidTimeoutReason, rec.Status)
		}
	default:
		return fmt.Errorf("%w: unknown reason %v", types.ErrInvalidTimeoutReason, msg.Reason)
	}

	// Count accept votes, weighted by slots per address.
	// One signature from a multi-slot validator counts for all its slots.
	acceptCount := uint32(0)
	seenAddrs := make(map[string]bool, len(msg.Votes))
	for _, vote := range msg.Votes {
		// Group membership check.
		voterAddr, ok := sm.slotToAddress[vote.VoterSlot]
		if !ok {
			return fmt.Errorf("%w: slot %d", types.ErrSlotNotInGroup, vote.VoterSlot)
		}

		// Duplicate voter address detection (one vote per address).
		if seenAddrs[voterAddr] {
			return fmt.Errorf("%w: slot %d", types.ErrDuplicateVote, vote.VoterSlot)
		}
		seenAddrs[voterAddr] = true

		voteContent := &types.TimeoutVoteContent{
			EscrowId:    sm.state.EscrowID,
			InferenceId: msg.InferenceId,
			Reason:      msg.Reason,
			Accept:      vote.Accept,
		}
		voteData, err := deterministicMarshal.Marshal(voteContent)
		if err != nil {
			return fmt.Errorf("marshal timeout vote: %w", err)
		}

		recovered, err := sm.verifier.RecoverAddress(voteData, vote.Signature)
		if err != nil {
			return fmt.Errorf("%w: vote from slot %d: %v", types.ErrInvalidVoteSig, vote.VoterSlot, err)
		}

		if recovered != voterAddr {
			if !sm.ResolveWarmKey(vote.VoterSlot, recovered, voterAddr) {
				return fmt.Errorf("%w: vote from slot %d: expected %s, got %s",
					types.ErrInvalidVoteSig, vote.VoterSlot, voterAddr, recovered)
			}
		}

		if vote.Accept {
			acceptCount += sm.addressToSlotCount[voterAddr]
		}
	}

	// VoteThreshold is frozen in state.Config at session creation (see VoteThreshold()).
	threshold := sm.state.Config.VoteThreshold
	if acceptCount <= threshold {
		return fmt.Errorf("%w: need >%d accept votes, got %d", types.ErrInsufficientVotes, threshold, acceptCount)
	}

	rec.Status = types.StatusTimedOut
	sm.state.HostStats[rec.ExecutorSlot].Missed++

	// Release reserved cost back to escrow.
	sm.state.Balance += rec.ReservedCost

	logging.Debug("inference -> timed_out", "subsystem", "state",
		"inference_id", msg.InferenceId,
		"executor_slot", rec.ExecutorSlot,
		"reason", msg.Reason.String(),
	)
	return sm.updateCommittedEntryLocked(msg.InferenceId, rec)
}

func (sm *StateMachine) applyRevealSeed(msg *types.MsgRevealSeed) error {
	logging.Debug("ignoring deprecated reveal-seed tx",
		"subsystem", "state",
		"escrow_id", sm.state.EscrowID,
		"slot_id", msg.GetSlotId(),
	)
	return nil
}

func (sm *StateMachine) applyFinalizeRound() error {
	if sm.state.Phase != types.PhaseActive {
		return types.ErrAlreadyFinalizing
	}
	sm.state.Phase = types.PhaseFinalizing
	return nil
}

// BuildDiffContent creates the proto DiffContent from nonce, txs, escrowID, and postStateRoot for signing.
func BuildDiffContent(escrowID string, nonce uint64, txs []*types.DevshardTx, postStateRoot []byte) *types.DiffContent {
	return &types.DiffContent{
		Nonce:         nonce,
		Txs:           txs,
		EscrowId:      escrowID,
		PostStateRoot: postStateRoot,
	}
}

// verifyProposerSig verifies that sig was produced by expectedAddress over
// msgWithoutSig (the proto message with its proposer_sig field already zeroed).
// slotID is used for warm key resolution; pass math.MaxUint32 to skip warm key lookup.
func (sm *StateMachine) verifyProposerSig(msgWithoutSig proto.Message, sig []byte, expectedAddress string, slotID uint32) error {
	data, err := deterministicMarshal.Marshal(msgWithoutSig)
	if err != nil {
		return fmt.Errorf("marshal for proposer sig: %w", err)
	}

	recovered, err := sm.verifier.RecoverAddress(data, sig)
	if err != nil {
		return fmt.Errorf("%w: %v", types.ErrInvalidProposerSig, err)
	}

	if recovered != expectedAddress {
		if slotID != math.MaxUint32 && sm.ResolveWarmKey(slotID, recovered, expectedAddress) {
			return nil
		}
		return fmt.Errorf("%w: expected %s, got %s", types.ErrInvalidProposerSig, expectedAddress, recovered)
	}

	return nil
}

// ResolveWarmKey checks if recovered is an authorized warm key for the given slot.
// Returns true if the key is accepted (either cached or newly verified via bridge).
// On first successful resolution the binding is cached in state.
func (sm *StateMachine) ResolveWarmKey(slotID uint32, recovered, expected string) bool {
	if warm, ok := sm.state.WarmKeys[slotID]; ok {
		return warm == recovered
	}
	if sm.warmResolver == nil {
		return false
	}
	ok, err := sm.warmResolver(recovered, expected)
	if err != nil || !ok {
		return false
	}
	sm.state.WarmKeys[slotID] = recovered
	return true
}

// InjectWarmKeys adds warm key bindings to state without calling the resolver.
// Used during replay to restore bindings that were discovered during the original run.
// Existing bindings are not overwritten.
func (sm *StateMachine) InjectWarmKeys(delta map[uint32]string) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	for slotID, addr := range delta {
		if _, exists := sm.state.WarmKeys[slotID]; !exists {
			sm.state.WarmKeys[slotID] = addr
		}
	}
}

// CheckWarmKey checks if warmAddr is authorized to act on behalf of coldAddr
// without caching the result in state. Use for slot discovery at host startup
// to avoid mutating state before any diffs are applied.
func (sm *StateMachine) CheckWarmKey(warmAddr, coldAddr string) bool {
	if sm.warmResolver == nil {
		return false
	}
	ok, err := sm.warmResolver(warmAddr, coldAddr)
	return err == nil && ok
}

func (sm *StateMachine) TotalSlots() uint32 {
	return sm.totalSlots
}

// QuorumThreshold returns the minimum slot-weighted signature count for 2/3+1 quorum.
func (sm *StateMachine) QuorumThreshold() uint32 {
	return 2*sm.totalSlots/3 + 1
}

func (sm *StateMachine) SlotAddress(slotID uint32) string {
	return sm.slotToAddress[slotID]
}

func (sm *StateMachine) AddressSlotCount(addr string) uint32 {
	return sm.addressToSlotCount[addr]
}

// LiveInferenceIDs returns the set of inference ids currently in live state.
// The live set is bounded (in-flight plus in-grace), so this is cheap. The host
// uses it to detect which inferences a diff sealed (live before, gone after).
func (sm *StateMachine) LiveInferenceIDs() map[uint64]struct{} {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	out := make(map[uint64]struct{}, len(sm.state.Inferences))
	for id := range sm.state.Inferences {
		out[id] = struct{}{}
	}
	return out
}

// GetInference returns a copy of the inference record for the given ID.
func (sm *StateMachine) GetInference(id uint64) (types.InferenceRecord, bool) {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	rec, ok := sm.state.Inferences[id]
	if !ok {
		return types.InferenceRecord{}, false
	}
	return *rec, ok
}

// VoteThreshold returns the session's vote threshold.
func (sm *StateMachine) VoteThreshold() uint32 {
	return sm.state.Config.VoteThreshold
}

func SortedSlotIDs(group []types.SlotAssignment) []uint32 {
	ids := make([]uint32, len(group))
	for i, s := range group {
		ids[i] = s.SlotID
	}
	slices.Sort(ids)
	return ids
}
