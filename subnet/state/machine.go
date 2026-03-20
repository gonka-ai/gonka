package state

import (
	"bytes"
	"fmt"
	"maps"
	"math"
	"slices"
	"sync"

	"google.golang.org/protobuf/proto"

	"subnet/logging"
	"subnet/signing"
	"subnet/types"
)

// Assumed validation rate (bps) for unrevealed seed penalty. 10000 = 100%.
const penaltyValidationRate = 10000

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
// HostStats, RevealedSeeds, WarmKeys, Balance, Phase, nonces).
// Immutable lookup maps (slotToAddress, etc.) are safe to read without locking.
type StateMachine struct {
	mu          sync.RWMutex
	state       *types.EscrowState
	verifier    signing.Verifier
	userAddress string

	// Lookup maps derived from group at construction time.
	slotToAddress      map[uint32]string
	addressToSlotCount map[string]uint32
	addressToSlots     map[string][]uint32 // address -> sorted slot IDs
	totalSlots         uint32

	warmResolver WarmKeyResolver // optional, nil = no warm key support
}

// SMOption configures optional StateMachine behavior.
type SMOption func(*StateMachine)

// WithWarmKeyResolver sets a callback for warm key verification.
func WithWarmKeyResolver(r WarmKeyResolver) SMOption {
	return func(sm *StateMachine) { sm.warmResolver = r }
}

func NewStateMachine(
	escrowID string,
	config types.SessionConfig,
	group []types.SlotAssignment,
	balance uint64,
	userAddress string,
	verifier signing.Verifier,
	opts ...SMOption,
) *StateMachine {
	slotToAddr := make(map[uint32]string, len(group))
	addrToSlotCount := make(map[string]uint32, len(group))
	for _, s := range group {
		slotToAddr[s.SlotID] = s.ValidatorAddress
		addrToSlotCount[s.ValidatorAddress]++
	}

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

	sm := &StateMachine{
		state: &types.EscrowState{
			EscrowID:      escrowID,
			Config:        config,
			Group:         groupCopy,
			Balance:       balance,
			Inferences:    make(map[uint64]*types.InferenceRecord),
			HostStats:     hostStats,
			RevealedSeeds: make(map[uint32]int64),
			WarmKeys:      make(map[uint32]string),
		},
		verifier:           verifier,
		userAddress:        userAddress,
		slotToAddress:      slotToAddr,
		addressToSlotCount: addrToSlotCount,
		addressToSlots:     addrToSlots,
		totalSlots:         uint32(len(group)),
	}
	for _, o := range opts {
		o(sm)
	}

	logging.Info("NewStateMachine", "subsystem", "state",
		"escrow_id", escrowID,
		"group_size", len(group),
		"balance", balance,
		"token_price", config.TokenPrice,
		"vote_threshold", config.VoteThreshold,
		"user_address", userAddress,
	)

	return sm
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
	return sm.applyCore(diff.Nonce, diff.Txs, diff.PostStateRoot)
}

// ApplyLocal applies txs without signature verification. Used by the user
// to compute the post_state_root before signing the diff.
func (sm *StateMachine) ApplyLocal(nonce uint64, txs []*types.SubnetTx) ([]byte, error) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	return sm.applyCore(nonce, txs, nil)
}

// ApplyLocalBestEffort applies txs one by one, skipping any that fail.
// Returns the post-state root and the subset of txs that were applied.
// Used by the user to compose diffs from pending txs that may be stale.
func (sm *StateMachine) ApplyLocalBestEffort(nonce uint64, txs []*types.SubnetTx) ([]byte, []*types.SubnetTx, error) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

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
	var applied []*types.SubnetTx
	for _, tx := range txs {
		if err := sm.applyTx(tx); err != nil {
			continue
		}
		applied = append(applied, tx)
	}

	sm.state.LatestNonce = nonce

	if sm.state.Phase == types.PhaseFinalizing && sm.state.FinalizeNonce == 0 {
		sm.state.FinalizeNonce = nonce
	}
	if sm.state.Phase == types.PhaseFinalizing {
		sm.recomputeCompliance()
		sm.penalizeUnrevealedSeeds()
		allRevealed := sm.allUniqueAddressesRevealed()
		deadlinePassed := sm.state.LatestNonce >= sm.state.FinalizeNonce+uint64(len(sm.state.Group))
		if allRevealed || deadlinePassed {
			sm.state.Phase = types.PhaseSettlement
		}
	}

	root, err := ComputeStateRoot(sm.state.Balance, sm.state.HostStats, sm.state.Inferences, sm.state.Phase, sm.state.WarmKeys)
	if err != nil {
		return nil, nil, fmt.Errorf("compute state root: %w", err)
	}

	logging.Debug("applied diff (best-effort)", "subsystem", "state",
		"nonce", nonce, "applied", len(applied), "candidates", len(txs),
		"balance", sm.state.Balance,
		"group_size", len(sm.state.Group),
		"host_stats_count", len(sm.state.HostStats),
		"config_token_price", sm.state.Config.TokenPrice,
	)
	return root, applied, nil
}

// applyCore validates nonce, applies txs, updates nonce, and returns the state root.
// If postStateRoot is non-nil, the computed root must match; on mismatch the entire
// operation is rolled back (including nonce) and an error is returned.
func (sm *StateMachine) applyCore(nonce uint64, txs []*types.SubnetTx, postStateRoot []byte) ([]byte, error) {
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

	// 5. Update nonce.
	sm.state.LatestNonce = nonce

	// Track FinalizeNonce: the nonce at which finalization started.
	if sm.state.Phase == types.PhaseFinalizing && sm.state.FinalizeNonce == 0 {
		sm.state.FinalizeNonce = nonce
	}

	if sm.state.Phase == types.PhaseFinalizing {
		sm.recomputeCompliance()
		sm.penalizeUnrevealedSeeds()

		// Auto-transition Finalizing -> Settlement.
		allRevealed := sm.allUniqueAddressesRevealed()
		deadlinePassed := sm.state.LatestNonce >= sm.state.FinalizeNonce+uint64(len(sm.state.Group))
		if allRevealed || deadlinePassed {
			sm.state.Phase = types.PhaseSettlement
		}
	}

	// 6. Compute state root.
	root, err := ComputeStateRoot(sm.state.Balance, sm.state.HostStats, sm.state.Inferences, sm.state.Phase, sm.state.WarmKeys)
	if err != nil {
		return nil, fmt.Errorf("compute state root: %w", err)
	}

	// 7. Verify post_state_root if present. On mismatch, roll back everything.
	if len(postStateRoot) > 0 && !bytes.Equal(root, postStateRoot) {
		logging.Error("state root mismatch diagnostic",
			"subsystem", "state",
			"nonce", nonce,
			"balance", sm.state.Balance,
			"group_size", len(sm.state.Group),
			"host_stats_count", len(sm.state.HostStats),
			"inferences_count", len(sm.state.Inferences),
			"phase", sm.state.Phase,
			"warm_keys_count", len(sm.state.WarmKeys),
			"config_token_price", sm.state.Config.TokenPrice,
			"config_vote_threshold", sm.state.Config.VoteThreshold,
			"config_validation_rate", sm.state.Config.ValidationRate,
			"escrow_id", sm.state.EscrowID,
		)
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

// SnapshotState returns a deep copy of the current escrow state.
func (sm *StateMachine) SnapshotState() types.EscrowState {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	s := *sm.state

	// Deep copy Group.
	s.Group = make([]types.SlotAssignment, len(sm.state.Group))
	copy(s.Group, sm.state.Group)

	// Deep copy HostStats.
	s.HostStats = make(map[uint32]*types.HostStats, len(sm.state.HostStats))
	for k, v := range sm.state.HostStats {
		cp := *v
		s.HostStats[k] = &cp
	}

	// Deep copy RevealedSeeds.
	s.RevealedSeeds = make(map[uint32]int64, len(sm.state.RevealedSeeds))
	maps.Copy(s.RevealedSeeds, sm.state.RevealedSeeds)

	// Deep copy WarmKeys.
	s.WarmKeys = make(map[uint32]string, len(sm.state.WarmKeys))
	maps.Copy(s.WarmKeys, sm.state.WarmKeys)

	// Deep copy Inferences.
	s.Inferences = copyInferences(sm.state.Inferences)

	return s
}

// mutableSnapshot holds the mutable fields of EscrowState for rollback.
type mutableSnapshot struct {
	Balance       uint64
	Phase         types.SessionPhase
	FinalizeNonce uint64
	LatestNonce   uint64
	Inferences    map[uint64]*types.InferenceRecord
	HostStats     map[uint32]*types.HostStats
	RevealedSeeds map[uint32]int64
	WarmKeys      map[uint32]string
}

func (sm *StateMachine) snapshotMutable() mutableSnapshot {
	infCopy := copyInferences(sm.state.Inferences)

	hsCopy := make(map[uint32]*types.HostStats, len(sm.state.HostStats))
	for k, v := range sm.state.HostStats {
		cp := *v
		hsCopy[k] = &cp
	}

	seedsCopy := make(map[uint32]int64, len(sm.state.RevealedSeeds))
	maps.Copy(seedsCopy, sm.state.RevealedSeeds)

	warmCopy := make(map[uint32]string, len(sm.state.WarmKeys))
	maps.Copy(warmCopy, sm.state.WarmKeys)

	return mutableSnapshot{
		Balance:       sm.state.Balance,
		Phase:         sm.state.Phase,
		FinalizeNonce: sm.state.FinalizeNonce,
		LatestNonce:   sm.state.LatestNonce,
		Inferences:    infCopy,
		HostStats:     hsCopy,
		RevealedSeeds: seedsCopy,
		WarmKeys:      warmCopy,
	}
}

func (sm *StateMachine) restoreMutable(snap mutableSnapshot) {
	sm.state.Balance = snap.Balance
	sm.state.Phase = snap.Phase
	sm.state.FinalizeNonce = snap.FinalizeNonce
	sm.state.LatestNonce = snap.LatestNonce
	sm.state.Inferences = snap.Inferences
	sm.state.HostStats = snap.HostStats
	sm.state.RevealedSeeds = snap.RevealedSeeds
	sm.state.WarmKeys = snap.WarmKeys
}

// ComputeStateRoot returns the current state root without modifying state.
func (sm *StateMachine) ComputeStateRoot() ([]byte, error) {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return ComputeStateRoot(sm.state.Balance, sm.state.HostStats, sm.state.Inferences, sm.state.Phase, sm.state.WarmKeys)
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

func (sm *StateMachine) applyTx(tx *types.SubnetTx) error {
	switch inner := tx.GetTx().(type) {
	case *types.SubnetTx_StartInference:
		return sm.applyStartInference(inner.StartInference)
	case *types.SubnetTx_ConfirmStart:
		return sm.applyConfirmStart(inner.ConfirmStart)
	case *types.SubnetTx_FinishInference:
		return sm.applyFinishInference(inner.FinishInference)
	case *types.SubnetTx_Validation:
		return sm.applyValidation(inner.Validation)
	case *types.SubnetTx_ValidationVote:
		return sm.applyValidationVote(inner.ValidationVote)
	case *types.SubnetTx_TimeoutInference:
		return sm.applyTimeout(inner.TimeoutInference)
	case *types.SubnetTx_RevealSeed:
		return sm.applyRevealSeed(inner.RevealSeed)
	case *types.SubnetTx_FinalizeRound:
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
	if _, exists := sm.state.Inferences[msg.InferenceId]; exists {
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
	logging.Debug("new inference", "subsystem", "state", "inference_id", msg.InferenceId, "executor_slot", executorSlot)
	return nil
}

func (sm *StateMachine) applyConfirmStart(msg *types.MsgConfirmStart) error {
	rec, ok := sm.state.Inferences[msg.InferenceId]
	if !ok {
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
	return nil
}

func (sm *StateMachine) applyFinishInference(msg *types.MsgFinishInference) error {
	rec, ok := sm.state.Inferences[msg.InferenceId]
	if !ok {
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

	return nil
}

func (sm *StateMachine) applyValidation(msg *types.MsgValidation) error {
	rec, ok := sm.state.Inferences[msg.InferenceId]
	if !ok {
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
		}
	}

	return nil
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

	// Check majority using VoteThreshold from config.
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
	} else if rec.VotesValid > threshold {
		rec.Status = types.StatusValidated
	}

	return nil
}

func (sm *StateMachine) applyTimeout(msg *types.MsgTimeoutInference) error {
	rec, ok := sm.state.Inferences[msg.InferenceId]
	if !ok {
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

	// Check threshold using VoteThreshold from config.
	threshold := sm.state.Config.VoteThreshold
	if acceptCount <= threshold {
		return fmt.Errorf("%w: need >%d accept votes, got %d", types.ErrInsufficientVotes, threshold, acceptCount)
	}

	rec.Status = types.StatusTimedOut
	sm.state.HostStats[rec.ExecutorSlot].Missed++

	// Release reserved cost back to escrow.
	sm.state.Balance += rec.ReservedCost

	return nil
}

func (sm *StateMachine) applyRevealSeed(msg *types.MsgRevealSeed) error {
	// Guard: must be in PhaseFinalizing. Rejected in Active (too early) and Settlement (too late).
	if sm.state.Phase == types.PhaseSettlement {
		return types.ErrSessionSettlement
	}
	if sm.state.Phase != types.PhaseFinalizing {
		return types.ErrSessionNotFinalizing
	}

	// Verify slot is in group.
	revealerAddr, ok := sm.slotToAddress[msg.SlotId]
	if !ok {
		return fmt.Errorf("%w: slot %d", types.ErrSlotNotInGroup, msg.SlotId)
	}

	// Verify proposer signature from slot owner.
	clonedRS := proto.Clone(msg).(*types.MsgRevealSeed)
	clonedRS.ProposerSig = nil
	if err := sm.verifyProposerSig(clonedRS, msg.ProposerSig, revealerAddr, msg.SlotId); err != nil {
		return err
	}

	// Cross-session replay protection.
	if msg.EscrowId != sm.state.EscrowID {
		return fmt.Errorf("%w: expected %s, got %s", types.ErrEscrowIDMismatch, sm.state.EscrowID, msg.EscrowId)
	}

	// Verify seed signature recovers to slot owner (proves honest derivation).
	seedAddr, err := sm.verifier.RecoverAddress([]byte(sm.state.EscrowID), msg.Signature)
	if err != nil {
		return fmt.Errorf("%w: %v", types.ErrInvalidSeedSig, err)
	}
	if seedAddr != revealerAddr {
		boundWarm, ok := sm.state.WarmKeys[msg.SlotId]
		if !ok || boundWarm != seedAddr {
			return fmt.Errorf("%w: expected %s or bound warm key, got %s",
				types.ErrInvalidSeedSig, revealerAddr, seedAddr)
		}
	}

	// Dedup by address: check if any slot owned by same address already revealed.
	for _, slot := range slices.Sorted(maps.Keys(sm.state.RevealedSeeds)) {
		if sm.slotToAddress[slot] == revealerAddr {
			return fmt.Errorf("%w: address %s already revealed via slot %d", types.ErrDuplicateSeedReveal, revealerAddr, slot)
		}
	}

	// Derive seed from signature.
	seed, err := DeriveSeed(msg.Signature)
	if err != nil {
		return err
	}

	// Store seed. Compliance is computed later in recomputeCompliance.
	sm.state.RevealedSeeds[msg.SlotId] = seed

	return nil
}

// recomputeCompliance recalculates RequiredValidations and CompletedValidations
// for all revealed seeds. Called during PhaseFinalizing so that MsgFinishInference
// arriving in the same diff as MsgRevealSeed is counted.
func (sm *StateMachine) recomputeCompliance() {
	for revealSlot, seed := range sm.state.RevealedSeeds {
		revealerAddr := sm.slotToAddress[revealSlot]
		validatorSlotCount := sm.addressToSlotCount[revealerAddr]
		requiredValidations := uint32(0)
		completedValidations := uint32(0)

		for _, infID := range slices.Sorted(maps.Keys(sm.state.Inferences)) {
			rec := sm.state.Inferences[infID]
			switch rec.Status {
			case types.StatusFinished, types.StatusChallenged, types.StatusValidated, types.StatusInvalidated:
			default:
				continue
			}

			executorAddr := sm.slotToAddress[rec.ExecutorSlot]
			if executorAddr == revealerAddr {
				continue
			}

			executorSlotCount := sm.addressToSlotCount[executorAddr]
			if ShouldValidate(seed, infID, validatorSlotCount, executorSlotCount, sm.totalSlots, sm.state.Config.ValidationRate) {
				requiredValidations++
				for _, vSlot := range sm.addressToSlots[revealerAddr] {
					if rec.ValidatedBy.IsSet(vSlot) {
						completedValidations++
						break
					}
				}
			}
		}

		for _, slot := range sm.addressToSlots[revealerAddr] {
			if hs, ok := sm.state.HostStats[slot]; ok {
				hs.RequiredValidations = requiredValidations
				hs.CompletedValidations = completedValidations
			}
		}
	}
}

// penalizeUnrevealedSeeds sets RequiredValidations for unrevealed hosts.
// Mirrors applyRevealSeed with penaltyValidationRate. CompletedValidations stays 0.
func (sm *StateMachine) penalizeUnrevealedSeeds() {
	revealedAddrs := make(map[string]bool, len(sm.state.RevealedSeeds))
	for slot := range sm.state.RevealedSeeds {
		revealedAddrs[sm.slotToAddress[slot]] = true
	}

	for addr, slots := range sm.addressToSlots {
		if revealedAddrs[addr] {
			continue
		}

		validatorSlotCount := sm.addressToSlotCount[addr]
		rate := float64(penaltyValidationRate) / 10000.0
		var probSum float64
		for _, infID := range slices.Sorted(maps.Keys(sm.state.Inferences)) {
			rec := sm.state.Inferences[infID]
			switch rec.Status {
			case types.StatusFinished, types.StatusChallenged, types.StatusValidated, types.StatusInvalidated:
			default:
				continue
			}
			executorAddr := sm.slotToAddress[rec.ExecutorSlot]
			if executorAddr == addr {
				continue
			}
			executorSlotCount := sm.addressToSlotCount[executorAddr]
			if sm.totalSlots <= executorSlotCount {
				continue
			}
			p := rate * float64(validatorSlotCount) / float64(sm.totalSlots-executorSlotCount)
			if p > 1.0 {
				p = 1.0
			}
			probSum += p
		}

		required := uint32(math.Ceil(probSum))
		for _, slot := range slots {
			if hs, ok := sm.state.HostStats[slot]; ok {
				hs.RequiredValidations = required
			}
		}
	}
}

func (sm *StateMachine) applyFinalizeRound() error {
	if sm.state.Phase != types.PhaseActive {
		return types.ErrAlreadyFinalizing
	}
	sm.state.Phase = types.PhaseFinalizing
	return nil
}

// allUniqueAddressesRevealed returns true when every unique address in the
// group has revealed a seed.
func (sm *StateMachine) allUniqueAddressesRevealed() bool {
	revealedAddrs := make(map[string]bool, len(sm.state.RevealedSeeds))
	for slot := range sm.state.RevealedSeeds {
		revealedAddrs[sm.slotToAddress[slot]] = true
	}
	return len(revealedAddrs) == len(sm.addressToSlots)
}

// BuildDiffContent creates the proto DiffContent from nonce, txs, escrowID, and postStateRoot for signing.
func BuildDiffContent(escrowID string, nonce uint64, txs []*types.SubnetTx, postStateRoot []byte) *types.DiffContent {
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

func (sm *StateMachine) SlotAddress(slotID uint32) string {
	return sm.slotToAddress[slotID]
}

func (sm *StateMachine) AddressSlotCount(addr string) uint32 {
	return sm.addressToSlotCount[addr]
}

// IsSlotRevealed returns true if the given slot has already revealed its seed.
func (sm *StateMachine) IsSlotRevealed(slotID uint32) bool {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	_, ok := sm.state.RevealedSeeds[slotID]
	return ok
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

// RevealedSlots returns a shallow copy of the revealed seeds map.
func (sm *StateMachine) RevealedSlots() map[uint32]int64 {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	if len(sm.state.RevealedSeeds) == 0 {
		return nil
	}
	cp := make(map[uint32]int64, len(sm.state.RevealedSeeds))
	maps.Copy(cp, sm.state.RevealedSeeds)
	return cp
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
