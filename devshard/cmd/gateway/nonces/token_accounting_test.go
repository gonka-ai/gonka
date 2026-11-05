package nonces

import (
	"bytes"
	"crypto/sha256"
	"testing"
	"time"

	"devshard/cmd/gateway/accounting"
	"devshard/cmd/gateway/engine"
	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/state"
	"devshard/types"
)

const tokenEscrowID = "escrow-tokens"

type liveEscrow struct {
	machine *state.StateMachine
	signer  *signing.Secp256k1Signer
	hosts   []*signing.Secp256k1Signer
	group   []types.SlotAssignment
	nonce   uint64
}

func newLiveEscrow(t *testing.T, hostCount int) *liveEscrow {
	t.Helper()
	hosts := make([]*signing.Secp256k1Signer, hostCount)
	for index := range hosts {
		hosts[index] = testutil.MustGenerateKey(t)
	}
	group := testutil.MakeGroup(hosts)
	configuration := testutil.DefaultConfig(len(group))
	creator := testutil.MustGenerateKey(t)
	store := testutil.MustMemoryStore(t, tokenEscrowID, creator.Address(), configuration, group, 1<<40)

	machine, err := state.NewStateMachine(tokenEscrowID, configuration, group, 1<<40, creator.Address(),
		signing.NewSecp256k1Verifier(), store)
	if err != nil {
		t.Fatalf("NewStateMachine = %v, want a live escrow", err)
	}
	return &liveEscrow{machine: machine, signer: creator, hosts: hosts, group: group}
}

func (e *liveEscrow) apply(t *testing.T, txs ...*types.DevshardTx) {
	t.Helper()
	e.nonce++
	if _, err := e.machine.ApplyDiff(testutil.SignDiff(t, e.signer, tokenEscrowID, e.nonce, txs)); err != nil {
		t.Fatalf("ApplyDiff(nonce %d) = %v, want nil", e.nonce, err)
	}
}

func (e *liveEscrow) executorSlot(inferenceID uint64) int {
	return int(inferenceID % uint64(len(e.group)))
}

func (e *liveEscrow) start(t *testing.T, inputLength, maxTokens uint64) uint64 {
	t.Helper()
	inferenceID := e.nonce + 1
	e.apply(t, &types.DevshardTx{Tx: &types.DevshardTx_StartInference{StartInference: &types.MsgStartInference{
		InferenceId: inferenceID,
		PromptHash:  testutil.TestPromptHash[:],
		Model:       "llama",
		InputLength: inputLength,
		MaxTokens:   maxTokens,
		StartedAt:   int64(inferenceID) * 1000,
	}}})
	return inferenceID
}

func (e *liveEscrow) confirm(t *testing.T, inferenceID, inputLength, maxTokens uint64) {
	t.Helper()
	slot := e.executorSlot(inferenceID)
	receipt := testutil.SignExecutorReceipt(t, e.hosts[slot], tokenEscrowID, inferenceID,
		testutil.TestPromptHash[:], "llama", inputLength, maxTokens, int64(inferenceID)*1000, int64(inferenceID)*1000)
	e.apply(t, &types.DevshardTx{Tx: &types.DevshardTx_ConfirmStart{ConfirmStart: &types.MsgConfirmStart{
		InferenceId: inferenceID,
		ExecutorSig: receipt,
		ConfirmedAt: int64(inferenceID) * 1000,
	}}})
}

func (e *liveEscrow) finish(t *testing.T, inferenceID, inputLength, maxTokens, inputTokens, outputTokens uint64) {
	t.Helper()
	slot := e.executorSlot(inferenceID)
	e.confirm(t, inferenceID, inputLength, maxTokens)

	finish := &types.MsgFinishInference{
		InferenceId:  inferenceID,
		ResponseHash: bytes.Repeat([]byte{1}, sha256.Size),
		ServedHash:   bytes.Repeat([]byte{2}, sha256.Size),
		InputTokens:  inputTokens,
		OutputTokens: outputTokens,
		ExecutorSlot: uint32(slot),
		EscrowId:     tokenEscrowID,
	}
	finish.ProposerSig = testutil.SignProposerTx(t, e.hosts[slot], finish)
	e.apply(t, &types.DevshardTx{Tx: &types.DevshardTx_FinishInference{FinishInference: finish}})
}

func (e *liveEscrow) timeOut(t *testing.T, inferenceID uint64) {
	t.Helper()
	executor := e.executorSlot(inferenceID)
	votes := make([]*types.TimeoutVote, 0, len(e.hosts))
	for slot, host := range e.hosts {
		if slot == executor {
			continue
		}
		vote := testutil.SignTimeoutVote(t, host, tokenEscrowID, inferenceID, types.TimeoutReason_TIMEOUT_REASON_EXECUTION, true)
		vote.VoterSlot = uint32(slot)
		votes = append(votes, vote)
	}
	e.apply(t, &types.DevshardTx{Tx: &types.DevshardTx_TimeoutInference{TimeoutInference: &types.MsgTimeoutInference{
		InferenceId: inferenceID,
		Reason:      types.TimeoutReason_TIMEOUT_REASON_EXECUTION,
		Votes:       votes,
	}}})
}

// Test flow:
//  1. Start one inference on a live escrow without confirming or finishing it.
//  2. Report that nonce as a burn (ghost) and observe the escrow's state through the ledger.
//  3. Assert the folded totals show zero in-flight nonces.
//  4. Assert the ghost disposition counts exactly the one burned nonce.
func TestABurnIsNotCountedAsInFlight(t *testing.T) {
	t.Parallel()
	escrow := newLiveEscrow(t, 3)
	burned := escrow.start(t, 60, 64)

	totals := observedTotalsWithBurn(t, escrow, burned)

	if totals.InFlight != 0 {
		t.Fatalf("in_flight = %d for a burned nonce, want 0", totals.InFlight)
	}
	if got := totals.Dispositions[accounting.DispositionGhost]; got != 1 {
		t.Fatalf("ghost disposition = %d, want the one nonce the gateway spent on nobody", got)
	}
}

// observedBy is the ledger reading one escrow the way the sweep does, after whatever the gateway itself reported about the nonces.
func observedBy(t *testing.T, escrow *liveEscrow, reported func(book *accounting.Book)) accounting.ParticipantRecord {
	t.Helper()
	service, err := accounting.NewService(accounting.Settings{Now: func() time.Time { return time.Unix(0, 0).UTC() }})
	if err != nil {
		t.Fatalf("NewService(): %v", err)
	}
	t.Cleanup(func() { _ = service.Close() })
	ledger := recorderFor(service)

	snapshot := escrow.machine.SnapshotState()
	if err := service.Book.OpenEscrow(accounting.EscrowMetadata{
		EscrowID:      tokenEscrowID,
		Model:         "llama",
		CreationEpoch: 7,
		Slots:         snapshot.Group,
	}); err != nil {
		t.Fatalf("OpenEscrow(): %v", err)
	}
	if reported != nil {
		reported(service.Book)
	}
	ledger.observeEscrowState(tokenEscrowID, snapshot)

	return foldRecords(service.Book.Query(accounting.QueryFilter{}))
}

// streamed is the gateway reporting an attempt that produced tokens, which is where output tokens come from.
func streamed(t *testing.T, nonce uint64, tokens int64) func(*accounting.Book) {
	t.Helper()
	return func(book *accounting.Book) {
		if err := book.RecordRace(tokenEscrowID, []accounting.Attempt{{
			Nonce: nonce, RequestID: "request-1", Sent: true,
			Usage: accounting.UsageWinner, OutputTokens: tokens,
		}}); err != nil {
			t.Fatalf("RecordRace(): %v", err)
		}
	}
}

func observedTotalsWithBurn(t *testing.T, escrow *liveEscrow, burned uint64) accounting.ParticipantRecord {
	t.Helper()
	return observedBy(t, escrow, func(book *accounting.Book) {
		if err := book.RecordGhost(tokenEscrowID, burned, "participant_window_full_no_send"); err != nil {
			t.Fatalf("RecordGhost(): %v", err)
		}
	})
}

func foldRecords(records []accounting.ParticipantRecord) accounting.ParticipantRecord {
	var total accounting.ParticipantRecord
	for _, record := range records {
		total.CountedNonces += record.CountedNonces
		total.EstimatedInput += record.EstimatedInput
		total.MaxTokens += record.MaxTokens
		total.InputTokens += record.InputTokens
		total.OutputTokens += record.OutputTokens
		total.ReservedCost += record.ReservedCost
		total.InFlight += record.InFlight
		if total.Dispositions == nil {
			total.Dispositions = map[accounting.Disposition]uint64{}
		}
		for disposition, count := range record.Dispositions {
			total.Dispositions[disposition] += count
		}
	}
	return total
}

// Test flow:
//  1. Start three inferences on a live escrow: one that finishes, one that stays confirmed and running, and one that times out after being confirmed.
//  2. Report the finished nonce's streamed output tokens and observe the escrow's state.
//  3. Assert counted nonces equal 1, matching only the nonce the chain counted tokens for.
//  4. Assert input and output tokens match the finish's own numbers.
//  5. Assert estimated input and max tokens cover only the finished nonce's reservation, not the unfinished ones.
//  6. Assert reserved cost is non-zero, since it covers every started nonce regardless of how it ended.
func TestTheGivenAndCountedTokensCoverTheSameNonces(t *testing.T) {
	t.Parallel()
	escrow := newLiveEscrow(t, 3)

	finished := escrow.start(t, 200_000, 4_096)
	stillRunning := escrow.start(t, 200_000, 4_096)
	timedOut := escrow.start(t, 200_000, 4_096)
	escrow.finish(t, finished, 200_000, 4_096, 27_438, 4_096)
	escrow.confirm(t, stillRunning, 200_000, 4_096)
	escrow.confirm(t, timedOut, 200_000, 4_096)
	escrow.timeOut(t, timedOut)

	totals := observedBy(t, escrow, streamed(t, finished, 4_096))

	if totals.CountedNonces != 1 {
		t.Fatalf("counted_nonces = %d, want the one nonce the chain counted tokens for", totals.CountedNonces)
	}
	if totals.InputTokens != 27_438 || totals.OutputTokens != 4_096 {
		t.Fatalf("input_tokens/output_tokens = %d/%d, want the prompt the finish carried and the answer the gateway counted",
			totals.InputTokens, totals.OutputTokens)
	}
	if totals.EstimatedInput != 50_000 || totals.MaxTokens != 4_096 {
		t.Fatalf("estimated_input_tokens/max_tokens = %d/%d, want only the finished nonce's reservation: an "+
			"unfinished nonce on the given side makes every ratio against the counted side meaningless",
			totals.EstimatedInput, totals.MaxTokens)
	}
	if totals.ReservedCost == 0 {
		t.Fatal("reserved_cost lost the nonces that never finished; the money side covers every started nonce")
	}
}

// Test flow:
//  1. Start and finish one inference on a live escrow, then report its streamed output tokens and observe the escrow's state.
//  2. Assert the input-estimate bias is exactly 1, since the prompt estimate matched the finish exactly.
//  3. Assert the output-reservation-use ratio matches the finished answer's share of its max-tokens reservation.
func TestTheTokenRatiosReadTrueOverOneFinishedNonce(t *testing.T) {
	t.Parallel()
	escrow := newLiveEscrow(t, 3)

	finished := escrow.start(t, 109_752, 4_096)
	escrow.finish(t, finished, 109_752, 4_096, 27_438, 320)

	totals := observedBy(t, escrow, streamed(t, finished, 320))

	if bias := float64(totals.InputTokens) / float64(totals.EstimatedInput); bias != 1 {
		t.Fatalf("input_estimate_bias = %v, want 1 for a prompt the estimate got exactly right", bias)
	}
	if use := float64(totals.OutputTokens) / float64(totals.MaxTokens); use != 320.0/4096.0 {
		t.Fatalf("output_reservation_use = %v, want the share of the reservation the answer used", use)
	}
}

// Test flow:
//  1. Record a race with a losing attempt that only counted logprob tokens and a winning attempt with its own usage-reported completion tokens.
//  2. Assert the folded output tokens equal the sum of the loser's counted stream and the winner's reported usage.
func TestARaceReportsTheTokensItsAttemptsStreamed(t *testing.T) {
	t.Parallel()
	escrow := newLiveEscrow(t, 3)
	ledger := newOpenedLedger(t, escrow)

	ledger.RecordRace(engine.RaceOutcome{
		RequestID: "request-1",
		EscrowID:  tokenEscrowID,
		Model:     "llama",
		Attempts: []engine.AttemptOutcome{
			{Nonce: 1, SendTime: time.Unix(1, 0), LogprobTokens: 834, Terminal: engine.TerminalLost},
			{Nonce: 2, SendTime: time.Unix(1, 0), UsageCompletionTokens: 4_096, Terminal: engine.TerminalWon},
		},
	})

	totals := foldRecords(ledger.service.Book.Query(accounting.QueryFilter{}))
	if totals.OutputTokens != 4_930 {
		t.Fatalf("output_tokens = %d, want the 834 counted off the loser's stream and the winner's 4096",
			totals.OutputTokens)
	}
}
