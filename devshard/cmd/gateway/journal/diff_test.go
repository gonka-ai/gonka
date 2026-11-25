package journal

import (
	"testing"

	"github.com/stretchr/testify/require"

	"devshard/cmd/gateway/accounting"
	"devshard/cmd/gateway/internal/logcapture"
	"devshard/types"
)

// composedDiff is an inference's diff carrying a valid and an invalid verdict and an applied timeout.
func composedDiff() *types.Diff {
	return &types.Diff{Nonce: 9, Txs: []*types.DevshardTx{
		{Tx: &types.DevshardTx_Validation{Validation: &types.MsgValidation{InferenceId: 4, ValidatorSlot: 1, Valid: true}}},
		{Tx: &types.DevshardTx_Validation{Validation: &types.MsgValidation{InferenceId: 5, ValidatorSlot: 0, Valid: false}}},
		{Tx: &types.DevshardTx_TimeoutInference{TimeoutInference: &types.MsgTimeoutInference{InferenceId: 6}}},
		{Tx: &types.DevshardTx_StartInference{StartInference: &types.MsgStartInference{InferenceId: 9}}},
	}}
}

// Test flow:
//  1. Build a `composedDiff` (an inference carrying two validations and an applied timeout).
//  2. Convert it to facts via `diffFacts`.
//  3. Assert the returned facts match the expected sequence, in the diff's own order.
func TestADiffIsReadIntoFactsInOrder(t *testing.T) {
	require.Equal(t, []DiffFact{
		{Kind: DiffFactValidation, Nonce: 4, ValidatorSlot: 1},
		{Kind: DiffFactValidation, Nonce: 5, ValidatorSlot: 0},
		{Kind: DiffFactInvalidVerdict, Nonce: 5, ValidatorSlot: 0},
		{Kind: DiffFactAppliedTimeout, Nonce: 6},
	}, diffFacts(composedDiff()))
}

// Test flow:
//  1. Build a journal with a `ledgerSpy` and a `composedDiff`.
//  2. Record the diff via `DiffComposed`, then immediately clear the diff's `Txs`.
//  3. Flush the journal.
//  4. Assert the ledger spy received the facts as copies, unaffected by clearing `Txs`.
func TestAComposedDiffReachesTheLedgerAsFacts(t *testing.T) {
	ledger := &ledgerSpy{}
	events := newJournal(t, Settings{Lines: &logcapture.Recorder{}, Ledger: ledger})
	diff := composedDiff()

	events.DiffComposed("escrow-1", diff)
	diff.Txs = nil
	events.Flush()

	require.Equal(t, []string{
		"diff escrow-1 1 4 1",
		"diff escrow-1 1 5 0",
		"diff escrow-1 2 5 0",
		"diff escrow-1 3 6 0",
	}, ledger.arrived())
}

// Test flow:
//  1. Build a journal with a `ledgerSpy`.
//  2. Record a diff via `DiffComposed` that carries no ledger fact (only a `StartInference` transaction).
//  3. Assert `events.accepted` stays zero under the session lock.
func TestADiffWithNoLedgerFactQueuesNothing(t *testing.T) {
	events := newJournal(t, Settings{Lines: &logcapture.Recorder{}, Ledger: &ledgerSpy{}})

	events.DiffComposed("escrow-1", &types.Diff{Nonce: 9, Txs: []*types.DevshardTx{
		{Tx: &types.DevshardTx_StartInference{StartInference: &types.MsgStartInference{InferenceId: 9}}},
	}})

	events.mu.Lock()
	defer events.mu.Unlock()
	require.Zero(t, events.accepted, "a diff with no ledger fact must not take a queue slot under the session lock")
}

// Test flow:
//  1. Build a journal with a `ledgerSpy`.
//  2. Record a warmup probe attempt via `ProbeRecorded`.
//  3. Flush the journal.
//  4. Assert the ledger spy received the probe fact.
func TestAWarmupProbeReachesTheLedger(t *testing.T) {
	ledger := &ledgerSpy{}
	events := newJournal(t, Settings{Lines: &logcapture.Recorder{}, Ledger: ledger})

	events.ProbeRecorded("escrow-1", accounting.Attempt{Nonce: 7, Terminal: accounting.TerminalWarmupProbe})
	events.Flush()

	require.Equal(t, []string{"probe escrow-1 7 " + accounting.TerminalWarmupProbe}, ledger.arrived())
}

func heightAckTx() *types.DevshardTx {
	return &types.DevshardTx{Tx: &types.DevshardTx_HeightAck{HeightAck: &types.MsgHeightAck{}}}
}

// Test flow:
//  1. Build each kind of diff the session composes without an inference, plus an inference carrying a height ack.
//  2. Convert each to facts via `diffFacts`.
//  3. Assert each yields its other facts in order and then one service fact naming what spent its nonce.
func TestADiffWithNoInferenceIsReadAsAServiceNonce(t *testing.T) {
	testCases := []struct {
		name  string
		txs   []*types.DevshardTx
		facts []DiffFact
	}{
		{
			name: "heartbeat span opening a forced turn",
			txs: []*types.DevshardTx{
				{Tx: &types.DevshardTx_ForceHeightSyncTurn{ForceHeightSyncTurn: &types.MsgForceHeightSyncTurn{}}},
				{Tx: &types.DevshardTx_Heartbeat{Heartbeat: &types.MsgHeartbeat{}}},
			},
			facts: []DiffFact{{Kind: DiffFactServiceNonce, Nonce: 12, Purpose: accounting.ServiceHeartbeat}},
		},
		{
			name: "ack round flushing a validation",
			txs: []*types.DevshardTx{
				heightAckTx(),
				{Tx: &types.DevshardTx_Validation{Validation: &types.MsgValidation{InferenceId: 4, ValidatorSlot: 1, Valid: true}}},
			},
			facts: []DiffFact{
				{Kind: DiffFactValidation, Nonce: 4, ValidatorSlot: 1},
				{Kind: DiffFactServiceNonce, Nonce: 12, Purpose: accounting.ServiceHeartbeat},
			},
		},
		{
			name: "timeout round carrying a pending ack",
			txs: []*types.DevshardTx{
				{Tx: &types.DevshardTx_TimeoutInference{TimeoutInference: &types.MsgTimeoutInference{InferenceId: 6}}},
				heightAckTx(),
			},
			facts: []DiffFact{
				{Kind: DiffFactAppliedTimeout, Nonce: 6},
				{Kind: DiffFactServiceNonce, Nonce: 12, Purpose: accounting.ServiceTimeout},
			},
		},
		{
			name:  "error miss round",
			txs:   []*types.DevshardTx{{Tx: &types.DevshardTx_ErrorMiss{ErrorMiss: &types.MsgErrorMiss{InferenceId: 6}}}},
			facts: []DiffFact{{Kind: DiffFactServiceNonce, Nonce: 12, Purpose: accounting.ServiceErrorMiss}},
		},
		{
			name:  "finalize round",
			txs:   []*types.DevshardTx{{Tx: &types.DevshardTx_FinalizeRound{FinalizeRound: &types.MsgFinalizeRound{}}}},
			facts: []DiffFact{{Kind: DiffFactServiceNonce, Nonce: 12, Purpose: accounting.ServiceFinalize}},
		},
		{
			name:  "round carrying only a transaction the ledger ignores",
			txs:   []*types.DevshardTx{{Tx: &types.DevshardTx_ConfirmStart{ConfirmStart: &types.MsgConfirmStart{InferenceId: 6}}}},
			facts: []DiffFact{{Kind: DiffFactServiceNonce, Nonce: 12, Purpose: accounting.ServiceFlush}},
		},
		{
			name:  "empty round",
			facts: []DiffFact{{Kind: DiffFactServiceNonce, Nonce: 12, Purpose: accounting.ServiceFlush}},
		},
		{
			name: "inference carrying a transaction the ledger ignores",
			txs: []*types.DevshardTx{
				{Tx: &types.DevshardTx_StartInference{StartInference: &types.MsgStartInference{InferenceId: 12}}},
				{Tx: &types.DevshardTx_ConfirmStart{ConfirmStart: &types.MsgConfirmStart{InferenceId: 6}}},
			},
		},
		{
			name: "inference carrying a height ack",
			txs: []*types.DevshardTx{
				{Tx: &types.DevshardTx_StartInference{StartInference: &types.MsgStartInference{InferenceId: 12}}},
				heightAckTx(),
			},
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			require.Equal(t, testCase.facts, diffFacts(&types.Diff{Nonce: 12, Txs: testCase.txs}))
		})
	}
}
