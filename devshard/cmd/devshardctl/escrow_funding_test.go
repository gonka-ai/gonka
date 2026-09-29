package main

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
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

// The predicted cost is what the start nonce charges: the reservation plus the per-nonce fee. One below
// it the escrow refuses the request; at it the request starts.
func TestChatRequestCostIsExactlyWhatTheStartNonceCharges(t *testing.T) {
	// Test flow:
	// 1. Price a request on a config with a token price and a per-nonce fee.
	// 2. Start it on a real escrow funded one below the prediction, then at the prediction.
	// 3. The first is refused for balance, the second starts.
	config := testutil.DefaultConfig(3)
	config.TokenPrice = 3
	config.FeePerNonce = 1_000
	cost := chatRequestCost{inputLengthBytes: 400, maxTokens: testutil.TestMaxTokens}

	predicted, err := cost.startChargeOn(config)
	require.NoError(t, err)

	require.ErrorIs(t, applyStartInferenceWithBalance(t, config, cost, predicted-1), types.ErrInsufficientBalance,
		"an escrow one short of the predicted cost started the request, so the prediction is loose")
	require.NoError(t, applyStartInferenceWithBalance(t, config, cost, predicted),
		"an escrow holding the predicted cost refused the request, so the prediction undercharges")
}

func TestChatRequestCostReportsOverflowInsteadOfWrapping(t *testing.T) {
	// Test flow:
	// 1. Price a request whose reservation overflows, and one whose reservation fits but not with the fee.
	// 2. Compute the start charge.
	// 3. Both report an overflow instead of wrapping to a small number.
	testCases := []struct {
		name        string
		tokenPrice  uint64
		feePerNonce uint64
		cost        chatRequestCost
	}{
		{name: "reservation overflows", tokenPrice: 2, cost: chatRequestCost{inputLengthBytes: 1 << 63, maxTokens: 1 << 63}},
		{name: "fee overflows the reservation", tokenPrice: 1, feePerNonce: 1_000, cost: chatRequestCost{inputLengthBytes: math.MaxUint64 - 10, maxTokens: 10}},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			config := testutil.DefaultConfig(3)
			config.TokenPrice = testCase.tokenPrice
			config.FeePerNonce = testCase.feePerNonce

			_, err := testCase.cost.startChargeOn(config)

			require.ErrorIs(t, err, types.ErrCostOverflow)
		})
	}
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
	oneRequest, err := cost.startChargeOn(testutil.DefaultConfig(3))
	require.NoError(t, err)
	escrowRuntime := fundingTestRuntime(t, "6", 2*oneRequest)
	gateway := newFundingTestGateway(escrowRuntime)
	_, err = gateway.reserveRuntimeForModel(fundingTestModel, cost, nil)
	require.NoError(t, err)
	setEscrowBalance(t, escrowRuntime, oneRequest)

	_, err = gateway.reserveRuntimeForModel(fundingTestModel, cost, nil)

	require.NoError(t, err, "an escrow holding enough for this request was refused because its in-flight work was counted twice")
}

// An escrow that can pay the reservation but not the per-nonce fee would fail the start nonce and read
// as exhausted over one request, so it must not be offered that request.
func TestReserveRuntimeSkipsAnEscrowShortOfThePerNonceFee(t *testing.T) {
	// Test flow:
	// 1. Fund an idle escrow with the reservation alone and a busy one with the reservation plus the fee.
	// 2. Reserve an escrow for the request.
	// 3. The busy escrow is chosen, because the idle one cannot pay the fee.
	const feePerNonce = 1_000
	cost := fundingTestCost()
	reserved, err := cost.startChargeOn(testutil.DefaultConfig(3))
	require.NoError(t, err)
	feeShortEscrow := fundingTestRuntime(t, "6", reserved)
	setEscrowFeePerNonce(t, feeShortEscrow, feePerNonce)
	fundedEscrow := fundingTestRuntime(t, "12", reserved+feePerNonce)
	setEscrowFeePerNonce(t, fundedEscrow, feePerNonce)
	fundedEscrow.activeUserRequests.Store(5)
	gateway := newFundingTestGateway(feeShortEscrow, fundedEscrow)

	chosen, err := gateway.reserveRuntimeForModel(fundingTestModel, cost, nil)

	require.NoError(t, err)
	require.Equal(t, "12", chosen.id, "the gateway routed to an escrow that cannot pay the per-nonce fee")
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

func setEscrowFeePerNonce(t *testing.T, rt *devshardRuntime, feePerNonce uint64) {
	t.Helper()
	snapshot := rt.proxy.sm.ExportState()
	snapshot.Config.FeePerNonce = feePerNonce
	require.NoError(t, rt.proxy.sm.RestoreState(snapshot))
}

// setEscrowBalance moves the escrow's balance the way a landed reservation does.
func setEscrowBalance(t *testing.T, rt *devshardRuntime, balance uint64) {
	t.Helper()
	snapshot := rt.proxy.sm.ExportState()
	snapshot.Balance = balance
	require.NoError(t, rt.proxy.sm.RestoreState(snapshot))
}

func stubSpendableBalance(t *testing.T, spendable uint64, queryError error) {
	t.Helper()
	saved := gatewaySpendableBalance
	gatewaySpendableBalance = func(*Gateway, context.Context, string) (uint64, error) {
		return spendable, queryError
	}
	t.Cleanup(func() { gatewaySpendableBalance = saved })
}

func TestEnsureCanFundEscrowRequiresTheAmountPlusTheFee(t *testing.T) {
	_, feeAmount := gatewayTxFee()
	for _, testCase := range []struct {
		name       string
		spendable  uint64
		queryError error
		isRefused  bool
	}{
		{name: "amount_plus_fee_is_enough", spendable: 500_000_000 + feeAmount},
		{name: "one_short_of_the_fee_is_refused", spendable: 500_000_000 + feeAmount - 1, isRefused: true},
		{name: "an_unanswered_balance_query_does_not_block_creation", queryError: errors.New("bank query unimplemented")},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			stubSpendableBalance(t, testCase.spendable, testCase.queryError)

			err := (&Gateway{}).ensureCanFundEscrow(t.Context(), "gonka1creator", 500_000_000)

			if testCase.isRefused {
				require.ErrorIs(t, err, errEscrowFundingInsufficient)
				return
			}
			require.NoError(t, err)
		})
	}
}

func TestPrepareBridgeEscrowsSharesTheFundsAcrossModelsRoundRobin(t *testing.T) {
	store, err := NewGatewayStore(filepath.Join(t.TempDir(), "gateway.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	settings := GatewaySettings{
		ChainREST:               "http://node:1317",
		PublicAPI:               "http://api:9000",
		DefaultModel:            "first",
		DefaultRequestMaxTokens: 1000,
		MaxConcurrentRequests:   2,
		EscrowRotation: EscrowRotationSettings{
			Enabled:           true,
			SettlementEnabled: true,
			Models: []EscrowRotationModelSettings{
				{ModelID: "first", TempCount: 3, TargetCount: 3, Amount: 1000, PrivateKeyEnv: "DEVSHARD_PRIVATE_KEY"},
				{ModelID: "second", TempCount: 3, TargetCount: 3, Amount: 1000, PrivateKeyEnv: "DEVSHARD_PRIVATE_KEY"},
				{ModelID: "third", TempCount: 3, TargetCount: 3, Amount: 1000, PrivateKeyEnv: "DEVSHARD_PRIVATE_KEY"},
			},
		},
	}.WithTuningDefaults()
	require.NoError(t, store.Initialize(settings, nil))

	var createdMutex sync.Mutex
	createdByModel := map[string]int{}
	fundedEscrows := 4
	saved := gatewayCreateRotationEscrow
	gatewayCreateRotationEscrow = func(_ *Gateway, _ context.Context, _ GatewaySettings, model EscrowRotationModelSettings, _ string, _ uint64) (*CreateDevshardEscrowResult, error) {
		createdMutex.Lock()
		defer createdMutex.Unlock()
		if fundedEscrows == 0 {
			return nil, errEscrowFundingInsufficient
		}
		fundedEscrows--
		createdByModel[model.ModelID]++
		return &CreateDevshardEscrowResult{EscrowID: 1, TxHash: "OK"}, nil
	}
	t.Cleanup(func() { gatewayCreateRotationEscrow = saved })

	gateway := &Gateway{store: store, rotationBreakers: make(map[string]*rotationBreaker)}
	gateway.prepareBridgeEscrows(t.Context(), ChainPhaseSnapshot{EpochIndex: 10}, settings)

	require.Equal(t, map[string]int{"first": 2, "second": 1, "third": 1}, createdByModel,
		"four funded escrows must be spread one per model per pass, not spent on the first model")
}
