package host

import (
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	"devshard"
	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/state"
	"devshard/storage"
	"devshard/stub"
	"devshard/types"
)

const benchLiveInferences = 10_000

// BenchmarkCollectValidationJobs times one validation-job collection against
// 10_000 live inferences, half of them finished work this host owes a
// validation for. The other half are still pending, so the old walk copies
// and scans them too.
//
//   - snapshot: the previous collector, one deep copy of the escrow state
//     then a walk of every live inference.
//   - owed: the current collector, a walk of the owed set only.
func BenchmarkCollectValidationJobs(b *testing.B) {
	h := benchValidationHost(b, benchLiveInferences)
	if got := len(h.sm.OwedValidationIDs()); got != benchLiveInferences/2 {
		b.Fatalf("owed set = %d, want %d", got, benchLiveInferences/2)
	}
	h.mu.Lock()
	before := jobIDSet(h.collectValidationJobsSnapshot())
	clear(h.validating)
	after := jobIDSet(h.collectValidationJobs())
	clear(h.validating)
	h.mu.Unlock()
	if len(before) != benchLiveInferences/2 || len(before) != len(after) {
		b.Fatalf("collectors disagree: snapshot %d, owed %d", len(before), len(after))
	}
	for id := range before {
		if _, ok := after[id]; !ok {
			b.Fatalf("owed collector missed inference %d", id)
		}
	}

	b.Run("snapshot", func(b *testing.B) {
		benchCollect(b, h, (*Host).collectValidationJobsSnapshot)
	})
	b.Run("owed", func(b *testing.B) {
		benchCollect(b, h, (*Host).collectValidationJobs)
	})
}

func benchCollect(b *testing.B, h *Host, collect func(*Host) []validateJob) {
	b.Helper()
	b.ReportAllocs()
	b.ResetTimer()
	var sink int
	for i := 0; i < b.N; i++ {
		h.mu.Lock()
		clear(h.validating)
		jobs := collect(h)
		h.mu.Unlock()
		sink += len(jobs)
	}
	b.StopTimer()
	if sink != b.N*benchLiveInferences/2 {
		b.Fatalf("collected %d jobs over %d iterations, want %d each", sink, b.N, benchLiveInferences/2)
	}
}

var benchMarshal = proto.MarshalOptions{Deterministic: true}

// benchValidationHost builds a two-slot host and a live map of n inferences.
// Odd ids finish on the other slot, so this host owes every one of them
// (validation rate 10000, one validator slot, one other slot). Even ids stay
// pending. Seal grace stays beyond the setup, so all n records stay live.
func benchValidationHost(b *testing.B, n int) *Host {
	b.Helper()
	validator, err := signing.GenerateKey()
	if err != nil {
		b.Fatal(err)
	}
	executor, err := signing.GenerateKey()
	if err != nil {
		b.Fatal(err)
	}
	user, err := signing.GenerateKey()
	if err != nil {
		b.Fatal(err)
	}
	group := []types.SlotAssignment{
		{SlotID: 0, ValidatorAddress: validator.Address()},
		{SlotID: 1, ValidatorAddress: executor.Address()},
	}
	config := types.SessionConfig{
		TokenPrice:                1,
		VoteThreshold:             1,
		ValidationRate:            10000,
		ExecutionTimeout:          1_000_000,
		InferenceSealGraceNonces:  1_000_000,
		InferenceSealGraceSeconds: 1_000_000,
	}
	const (
		escrowID = "escrow-bench"
		balance  = 1 << 40
	)
	store := storage.NewMemory()
	if err := store.CreateSession(storage.CreateSessionParams{
		EscrowID: escrowID, EpochID: 1, Version: testutil.RuntimeTestVersion,
		CreatorAddr: user.Address(), Config: config, Group: group, InitialBalance: balance,
	}); err != nil {
		b.Fatal(err)
	}
	sm, err := state.NewStateMachine(escrowID, config, group, balance, user.Address(),
		signing.NewSecp256k1Verifier(), store)
	if err != nil {
		b.Fatal(err)
	}
	h, err := NewHost(sm, validator, stub.NewInferenceEngine(), escrowID, group, nil,
		WithGrace(10), WithValidator(stub.NewValidationEngine()), WithEpochID(1))
	if err != nil {
		b.Fatal(err)
	}

	// A diff carries at most one start, and that start's id is the nonce, so
	// the n live ids are n separate diffs. Confirms and finishes batch after.
	for id := uint64(1); id <= uint64(n); id++ {
		benchApplyChunks(b, sm, id, []*types.DevshardTx{testutil.StartTx(id)})
	}
	var confirms, finishes []*types.DevshardTx
	for id := uint64(1); id <= uint64(n); id += 2 {
		confirms = append(confirms, benchConfirmTx(b, executor, escrowID, id))
		finishes = append(finishes, benchFinishTx(b, executor, escrowID, id))
	}
	nonce := benchApplyChunks(b, sm, uint64(n)+1, confirms)
	benchApplyChunks(b, sm, nonce, finishes)

	h.validationLifecycleMu.Lock()
	h.validationQueue = make(chan validateJob, defaultValidationQueueSize)
	h.validationLifecycleMu.Unlock()
	return h
}

func benchApplyChunks(b *testing.B, sm *state.StateMachine, nonce uint64, txs []*types.DevshardTx) uint64 {
	b.Helper()
	const chunk = 1000
	for len(txs) > 0 {
		n := chunk
		if n > len(txs) {
			n = len(txs)
		}
		_, applied, err := sm.ApplyLocalBestEffort(nonce, txs[:n])
		if err != nil {
			b.Fatalf("nonce %d: %v", nonce, err)
		}
		if len(applied) != n {
			b.Fatalf("nonce %d: applied %d of %d txs", nonce, len(applied), n)
		}
		nonce++
		txs = txs[n:]
	}
	return nonce
}

func benchConfirmTx(b *testing.B, executor *signing.Secp256k1Signer, escrowID string, id uint64) *types.DevshardTx {
	b.Helper()
	const confirmedAt = int64(2000)
	content := &types.ExecutorReceiptContent{
		InferenceId: id,
		PromptHash:  testutil.TestPromptHash[:],
		Model:       "llama",
		InputLength: 100,
		MaxTokens:   50,
		StartedAt:   1000,
		EscrowId:    escrowID,
		ConfirmedAt: confirmedAt,
	}
	sig := benchSign(b, executor, content)
	return &types.DevshardTx{Tx: &types.DevshardTx_ConfirmStart{ConfirmStart: &types.MsgConfirmStart{
		InferenceId: id, ExecutorSig: sig, ConfirmedAt: confirmedAt,
	}}}
}

func benchFinishTx(b *testing.B, executor *signing.Secp256k1Signer, escrowID string, id uint64) *types.DevshardTx {
	b.Helper()
	msg := &types.MsgFinishInference{
		InferenceId: id, ResponseHash: []byte("response-hash-32-bytes-padding!!"),
		InputTokens: 80, OutputTokens: 40, ExecutorSlot: 1, EscrowId: escrowID,
	}
	msg.ProposerSig = benchSign(b, executor, msg)
	return &types.DevshardTx{Tx: &types.DevshardTx_FinishInference{FinishInference: msg}}
}

func benchSign(b *testing.B, signer *signing.Secp256k1Signer, msg proto.Message) []byte {
	b.Helper()
	data, err := benchMarshal.Marshal(msg)
	if err != nil {
		b.Fatal(err)
	}
	sig, err := signer.Sign(data)
	if err != nil {
		b.Fatal(err)
	}
	return sig
}

func jobIDSet(jobs []validateJob) map[uint64]struct{} {
	out := make(map[uint64]struct{}, len(jobs))
	for _, job := range jobs {
		out[job.inferenceID] = struct{}{}
	}
	return out
}

// collectValidationJobsSnapshot is the selection collectValidationJobs did
// before the owed set: one deep copy of the escrow state, then a walk of
// every live inference. The mempool is scanned once per candidate, which is
// what that version did.
func (h *Host) collectValidationJobsSnapshot() []validateJob {
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
	if !devshard.CanValidateEpoch(h.validator, h.epochID) {
		return nil
	}

	st := h.sm.SnapshotState()
	now := time.Now()
	available := cap(q) - len(q)
	if available <= 0 {
		return nil
	}
	for id, until := range h.validationRetryAt {
		rec := st.Inferences[id]
		if !until.After(now) || !h.inferenceValidatable(rec) {
			delete(h.validationRetryAt, id)
		}
	}
	var jobs []validateJob

	for infID, rec := range st.Inferences {
		if !devshard.CanValidate(h.validator, rec.Model) {
			continue
		}
		if rec.Status != types.StatusFinished && rec.Status != types.StatusChallenged {
			continue
		}
		if h.slotIDs[rec.ExecutorSlot] {
			continue
		}
		participated := false
		for slot := range h.slotIDs {
			if rec.ValidatedBy.IsSet(slot) {
				participated = true
				break
			}
		}
		if participated {
			continue
		}
		if _, ok := h.validating[infID]; ok {
			continue
		}
		if now.Before(h.validationRetryAt[infID]) {
			continue
		}
		if h.hasMempoolValidationOrVote(infID) {
			continue
		}

		flow := validationFlowChallenged
		if rec.Status == types.StatusFinished {
			executorSlots := h.sm.AddressSlotCount(h.slotToAddr[rec.ExecutorSlot])
			if !state.ShouldValidate(h.ownSeed, infID, uint32(len(h.slotIDs)), executorSlots, h.sm.TotalSlots(), st.Config.ValidationRate) {
				continue
			}
			flow = validationFlowShouldValidate
		}
		h.validating[infID] = struct{}{}
		jobs = append(jobs, validateJob{
			inferenceID:     infID,
			validatorSlot:   h.sortedSlots[0],
			flow:            flow,
			model:           rec.Model,
			promptHash:      rec.PromptHash,
			responseHash:    rec.ResponseHash,
			inputTokens:     rec.InputTokens,
			outputTokens:    rec.OutputTokens,
			escrowID:        h.escrowID,
			executorAddress: h.slotToAddr[rec.ExecutorSlot],
			epochID:         h.epochID,
		})
		available--
		if available == 0 {
			break
		}
	}
	return jobs
}
