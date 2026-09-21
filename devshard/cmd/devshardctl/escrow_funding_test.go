package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/state"
	"devshard/types"
)

// The gateway prices the body it holds, but the proxy rewrites that body for streaming before the
// host hashes it. The margin exists to cover the difference; this fails the day a rewrite outgrows
// it, including one that grows with the prompt rather than by a fixed amount.
func TestChatRequestCostCoversTheBodyTheProxyActuallySends(t *testing.T) {
	testCases := []struct {
		name   string
		prompt string
	}{
		{name: "short prompt", prompt: "count to three"},
		{name: "long prompt", prompt: strings.Repeat("summarise this paragraph. ", 4_000)},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			withForceUpstreamStreaming(t, true)
			clientBody, err := json.Marshal(map[string]any{
				"model":    fundingTestModel,
				"messages": []map[string]string{{"role": "user", "content": testCase.prompt}},
			})
			require.NoError(t, err)

			gatewayBody, gatewayRequest, err := normalizeChatRequestForModel(clientBody, fundingTestModel)
			require.NoError(t, err)
			upstreamBody, _, err := normalizeUpstreamChatRequest(gatewayBody)
			require.NoError(t, err)

			cost := newChatRequestCost(gatewayBody, gatewayRequest, estimatePromptTokens(gatewayBody))

			require.GreaterOrEqual(t, cost.inputLengthBytes, uint64(len(upstreamBody)),
				"the gateway priced fewer bytes than the host will hash, so its routing decision underpays")
		})
	}
}

// The predicted cost is the reservation and nothing else. One below it the escrow refuses the
// request outright; at it the reservation lands and only the per-nonce fee is short, which is the
// signal that marks the escrow exhausted — so the pre-filter must not swallow that case.
func TestChatRequestCostIsExactlyTheReservation(t *testing.T) {
	config := testutil.DefaultConfig(3)
	config.TokenPrice = 3
	config.FeePerNonce = 1_000
	cost := chatRequestCost{inputLengthBytes: 400, maxTokens: testutil.TestMaxTokens}

	predicted, err := cost.reservedOn(config)
	require.NoError(t, err)

	require.ErrorIs(t, applyStartInferenceWithBalance(t, config, cost, predicted-1), types.ErrRequestExceedsBalance,
		"an escrow one short of the predicted cost took the reservation, so the prediction is loose")
	atPrediction := applyStartInferenceWithBalance(t, config, cost, predicted)
	require.ErrorIs(t, atPrediction, types.ErrInsufficientBalance)
	require.NotErrorIs(t, atPrediction, types.ErrRequestExceedsBalance,
		"the reservation itself was refused, so the escrow never reaches the fee that marks it exhausted")
}

func TestChatRequestCostReportsOverflowInsteadOfWrapping(t *testing.T) {
	config := testutil.DefaultConfig(3)
	config.TokenPrice = 2

	_, err := chatRequestCost{inputLengthBytes: 1 << 63, maxTokens: 1 << 63}.reservedOn(config)

	require.ErrorIs(t, err, types.ErrCostOverflow)
}

// An escrow whose price cannot even be computed can never pay it, so it must not be offered work.
func TestEscrowCannotFundARequestPricedBeyondUint64(t *testing.T) {
	escrowRuntime := fundingTestRuntime(t, "6", 1_000_000)

	require.False(t, escrowCanFund(escrowRuntime, chatRequestCost{inputLengthBytes: 1 << 63, maxTokens: 1 << 63}))
}

// applyStartInferenceWithBalance runs the priced request against a real escrow funded with balance,
// so the assertions ride on the state machine's own arithmetic rather than a copy of it.
func applyStartInferenceWithBalance(t *testing.T, config types.SessionConfig, cost chatRequestCost, balance uint64) error {
	t.Helper()

	hosts := []*signing.Secp256k1Signer{
		testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t),
	}
	user := testutil.MustGenerateKey(t)
	group := testutil.MakeGroup(hosts)
	sm, err := state.NewStateMachine("escrow-1", config, group, balance, user.Address(), signing.NewSecp256k1Verifier(),
		testutil.MustMemoryStore(t, "escrow-1", user.Address(), config, group, balance))
	require.NoError(t, err)

	_, err = sm.ApplyDiff(testutil.SignDiff(t, user, "escrow-1", 1, []*types.DevshardTx{{
		Tx: &types.DevshardTx_StartInference{StartInference: &types.MsgStartInference{
			InferenceId: 1,
			PromptHash:  []byte("prompt"),
			Model:       "m",
			InputLength: cost.inputLengthBytes,
			MaxTokens:   cost.maxTokens,
			StartedAt:   1000,
		}},
	}}))
	return err
}

// An idle escrow is the cheapest by load, but routing to one that cannot pay for the reservation
// sends the request nowhere. Funding has to outrank load.
func TestReserveRuntimeSkipsAnEscrowThatCannotPayForTheRequest(t *testing.T) {
	poorEscrow := fundingTestRuntime(t, "6", 100)
	richEscrow := fundingTestRuntime(t, "12", 1_000_000)
	richEscrow.activeUserRequests.Store(5)
	gateway := newFundingTestGateway(poorEscrow, richEscrow)

	chosen, err := gateway.reserveRuntimeForModel(fundingTestModel, fundingTestCost(), nil)

	require.NoError(t, err)
	require.Equal(t, "12", chosen.id, "the gateway routed to an escrow that cannot fund the reservation")
}

// A fleet that is merely out of money recovers on the next replacement or settlement, so the client
// is told to come back rather than handed a failure.
func TestReserveRuntimeAsksForARetryWhenNoEscrowCanPay(t *testing.T) {
	gateway := newFundingTestGateway(fundingTestRuntime(t, "6", 100), fundingTestRuntime(t, "12", 100))

	_, err := gateway.reserveRuntimeForModel(fundingTestModel, fundingTestCost(), nil)

	var cannotFund *EscrowsCannotFundRequestError
	require.ErrorAs(t, err, &cannotFund)
	require.Equal(t, 2, cannotFund.EscrowsRefused)
	require.Equal(t, http.StatusServiceUnavailable, gatewayStatusCodeForError(err))
}

// An escrow already serving requests has had their reservations taken off its balance. Counting the
// same money a second time would drop the busiest escrows at exactly the load they exist to carry.
func TestReserveRuntimeStillPicksAnEscrowServingWorkItCanAfford(t *testing.T) {
	cost := fundingTestCost()
	oneRequest, err := cost.reservedOn(testutil.DefaultConfig(3))
	require.NoError(t, err)
	escrowRuntime := fundingTestRuntime(t, "6", 2*oneRequest)
	gateway := newFundingTestGateway(escrowRuntime)
	_, err = gateway.reserveRuntimeForModel(fundingTestModel, cost, nil)
	require.NoError(t, err)
	setEscrowBalance(t, escrowRuntime, oneRequest)

	_, err = gateway.reserveRuntimeForModel(fundingTestModel, cost, nil)

	require.NoError(t, err, "an escrow holding enough for this request was refused because its in-flight work was counted twice")
}

// A balance short only of the per-nonce fee is what marks an escrow exhausted, so such an escrow must
// still be offered the request instead of being filtered out before it can raise that signal.
func TestReserveRuntimePicksAnEscrowShortOnlyOfThePerNonceFee(t *testing.T) {
	cost := fundingTestCost()
	reserved, err := cost.reservedOn(testutil.DefaultConfig(3))
	require.NoError(t, err)
	gateway := newFundingTestGateway(fundingTestRuntime(t, "6", reserved))

	chosen, err := gateway.reserveRuntimeForModel(fundingTestModel, cost, nil)

	require.NoError(t, err, "an escrow that can pay the reservation was filtered out over the per-nonce fee")
	require.Equal(t, "6", chosen.id)
}

// The client-facing count must name every escrow that was asked, whichever stage refused it.
func TestRefusedEscrowCountSpansBothTheFilterAndTheWalk(t *testing.T) {
	filtered := &EscrowsCannotFundRequestError{EscrowsRefused: 2, wrapped: types.ErrRequestExceedsBalance}

	merged := withRefusedEscrowCount(filtered, 1)

	var counted *EscrowsCannotFundRequestError
	require.ErrorAs(t, merged, &counted)
	require.Equal(t, 3, counted.EscrowsRefused, "the escrows the walk asked were dropped from the count")
	require.ErrorIs(t, merged, types.ErrRequestExceedsBalance)
}

func TestRefusedEscrowCountLeavesAnErrorNoEscrowRefused(t *testing.T) {
	cause := errors.New("no devshard runtimes available for new inferences")

	require.Equal(t, cause, withRefusedEscrowCount(cause, 0))
}

func TestRefusedEscrowCountKeepsARateLimitAsItsOwnAnswer(t *testing.T) {
	cause := &EscrowParticipantRateLimitError{}

	require.Equal(t, error(cause), withRefusedEscrowCount(cause, 1))
}

const fundingTestModel = "Qwen/Test"

func fundingTestCost() chatRequestCost {
	return chatRequestCost{promptTokens: 1, inputLengthBytes: 400, maxTokens: testutil.TestMaxTokens}
}

func fundingTestRuntime(t *testing.T, escrowID string, balance uint64) *devshardRuntime {
	t.Helper()
	escrowRuntime := gatewayTestRuntimeForLimits(t, escrowID, balance, 0)
	escrowRuntime.model = fundingTestModel
	return escrowRuntime
}

func newFundingTestGateway(runtimes ...*devshardRuntime) *Gateway {
	return NewGateway(runtimes, NewGatewayLimiter(0, 0), fundingTestModel)
}

// setEscrowBalance moves the escrow's balance the way a landed reservation does.
func setEscrowBalance(t *testing.T, rt *devshardRuntime, balance uint64) {
	t.Helper()
	snapshot := rt.proxy.sm.ExportState()
	snapshot.Balance = balance
	require.NoError(t, rt.proxy.sm.RestoreState(snapshot))
}
