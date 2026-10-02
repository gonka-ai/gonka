package main

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// An escrow above balanceMinimumThreshold but below the price of the requests it is offered is not
// replaced by checkBalances, which only looks at the 1e6 floor, and reserveRuntimeForModel skips it
// without reporting depletion. With mainnet pricing (token_price 10, fee_per_nonce 1000, amount 7e8) a
// ~200 KB prompt costs ~2.04e6, so an escrow drained by such traffic to 2e6 answers every later one
// with 503 + Retry-After until heartbeat fees wear it under the floor or the next epoch rotation.
func TestStrandedBandEscrowIsReplacedWhenAFreshEscrowCouldFundTheRequest(t *testing.T) {
	const tokenPrice, feePerNonce = 10, 1_000
	stranded := gatewayTestRuntimeForLimits(t, "12", 2_000_000, 100)
	snapshot := stranded.proxy.sm.ExportState()
	snapshot.Config.TokenPrice = tokenPrice
	snapshot.Config.FeePerNonce = feePerNonce
	require.NoError(t, stranded.proxy.sm.RestoreState(snapshot))

	gateway, created, _ := gatewayTestDepletionGateway(t, stranded, withoutSettlement, func(settings *GatewaySettings) {
		settings.EscrowRotation.Models[0].Amount = 700_000_000
	})

	cost := chatRequestCost{promptTokens: 50_000, inputLengthBytes: 200_000, maxTokens: 4_096}
	charge, err := cost.startChargeOn(stranded.proxy.sm.Config())
	require.NoError(t, err)
	require.Greater(t, charge, stranded.proxy.sm.Balance(), "precondition: the request must cost more than the escrow holds")
	require.GreaterOrEqual(t, stranded.proxy.sm.Balance(), balanceMinimumThreshold, "precondition: the escrow must sit above the low-balance floor")
	require.LessOrEqual(t, charge, uint64(700_000_000), "precondition: a fresh rotation escrow could fund the request")

	_, err = gateway.reserveRuntimeForModel("m", cost, nil)
	var cannotFund *EscrowsCannotFundRequestError
	require.ErrorAs(t, err, &cannotFund, "the refused client still gets 503 + Retry-After")
	runBalanceTick(t, gateway, stranded.id)

	t.Logf("after a refused request and a balance tick: replacement escrows created=%d, stranded escrow active=%v balance=%d charge=%d",
		created.Load(), stranded.active.Load(), stranded.proxy.sm.Balance(), charge)
	require.GreaterOrEqual(t, created.Load(), int32(1),
		"an idle escrow that a fresh rotation escrow would outfund was left in rotation and no replacement was minted")
}

// A request larger than a whole rotation escrow is oversized, not a sign of depletion: replacing on it
// would mint a new escrow on every retry (docs/proxy-architecture.md). The fix must keep that.
func TestOversizedRequestDoesNotReplaceAnEscrow(t *testing.T) {
	stranded := gatewayTestRuntimeForLimits(t, "12", 2_000_000, 100)
	gateway, created, _ := gatewayTestDepletionGateway(t, stranded, withoutSettlement, func(settings *GatewaySettings) {
		settings.EscrowRotation.Models[0].Amount = 1_500_000
	})
	cost := chatRequestCost{promptTokens: 50_000, inputLengthBytes: 3_000_000, maxTokens: 4_096}

	_, err := gateway.reserveRuntimeForModel("m", cost, nil)
	var cannotFund *EscrowsCannotFundRequestError
	require.ErrorAs(t, err, &cannotFund)
	runBalanceTick(t, gateway, stranded.id)

	require.EqualValues(t, 0, created.Load(), "a request no fresh escrow could fund minted a replacement")
	require.True(t, stranded.active.Load(), "an oversized request took a funded escrow out of service")
}

// An escrow with requests in flight or a race refund pending has reservations off its balance; settling
// them returns the unused part, so refusing one request is not depletion and must not retire it.
func TestBusyEscrowShortOfARequestIsNotReplaced(t *testing.T) {
	for name, markBusy := range map[string]func(rt *devshardRuntime){
		"requests in flight":  func(rt *devshardRuntime) { rt.activeUserRequests.Store(3) },
		"race refund pending": func(rt *devshardRuntime) { rt.pendingRaceCleanup.Store(1) },
	} {
		t.Run(name, func(t *testing.T) {
			busy := gatewayTestRuntimeForLimits(t, "12", 2_000_000, 100)
			markBusy(busy)
			gateway, created, _ := gatewayTestDepletionGateway(t, busy, withoutSettlement, func(settings *GatewaySettings) {
				settings.EscrowRotation.Models[0].Amount = 700_000_000
			})
			cost := chatRequestCost{promptTokens: 50_000, inputLengthBytes: 3_000_000, maxTokens: 4_096}

			_, err := gateway.reserveRuntimeForModel("m", cost, nil)
			var cannotFund *EscrowsCannotFundRequestError
			require.ErrorAs(t, err, &cannotFund)
			runBalanceTick(t, gateway, busy.id)

			require.EqualValues(t, 0, created.Load(), "an escrow whose background work will refund it was replaced")
			require.True(t, busy.active.Load(), "a busy escrow was taken out of service over one request")
		})
	}
}

// Several idle escrows short of one request: one replacement is enough for the retry to land on, so only
// the poorest is retired and the others stay in service for cheaper requests.
func TestStrandedEscrowForRequestPicksOnlyThePoorest(t *testing.T) {
	poorer := gatewayTestRuntimeForLimits(t, "12", 1_500_000, 100)
	richer := gatewayTestRuntimeForLimits(t, "13", 2_000_000, 100)
	gateway, _, _ := gatewayTestDepletionGateway(t, poorer, withoutSettlement, func(settings *GatewaySettings) {
		settings.EscrowRotation.Models[0].Amount = 700_000_000
	})
	cost := chatRequestCost{promptTokens: 50_000, inputLengthBytes: 3_000_000, maxTokens: 4_096}

	require.Same(t, poorer, gateway.strandedEscrowForRequest("m", []*devshardRuntime{richer, poorer}, cost))
	require.Same(t, poorer, gateway.strandedEscrowForRequest("m", []*devshardRuntime{poorer, richer}, cost))
}

// A request that names no model is offered to escrows of every model; one refusal must not retire an
// escrow of a model the caller never asked for.
func TestStrandedEscrowForRequestIgnoresRequestsWithoutModel(t *testing.T) {
	stranded := gatewayTestRuntimeForLimits(t, "12", 2_000_000, 100)
	gateway, _, _ := gatewayTestDepletionGateway(t, stranded, withoutSettlement, func(settings *GatewaySettings) {
		settings.EscrowRotation.Models[0].Amount = 700_000_000
	})
	cost := chatRequestCost{promptTokens: 50_000, inputLengthBytes: 3_000_000, maxTokens: 4_096}

	require.Nil(t, gateway.strandedEscrowForRequest("", []*devshardRuntime{stranded}, cost))
}
