package main

import (
	"testing"

	"github.com/stretchr/testify/require"

	devshardpkg "devshard"
)

// The chain allows a million nonces, so an escrow past the old 19 800 default keeps serving.
func TestGatewayChooseRuntimeRoutesPastTheOldDefaultOnceTheChainAllowsIt(t *testing.T) {
	escrowRuntime := gatewayTestRuntimeForLimits(t, "6", balanceMinimumThreshold, nonceDeactivationLimit)
	gateway := NewGateway([]*devshardRuntime{escrowRuntime}, NewGatewayLimiter(0, 0), "m")
	gateway.maxNonce = devshardpkg.StaticMaxNonce(1_000_000)

	chosen, err := gateway.reserveRuntimeForModel("m", 5)

	require.NoError(t, err, "an escrow far below the chain max nonce was skipped as spent")
	require.Equal(t, "6", chosen.id)
}

// Hosts stop taking new work group size plus one nonces short of max_nonce, and the gateway stops its in-flight margin before that.
func TestGatewayChooseRuntimeStopsAnInFlightMarginShortOfTheHostActiveCap(t *testing.T) {
	spentRuntime := gatewayTestRuntimeForLimits(t, "6", balanceMinimumThreshold, 999_796)
	availableRuntime := gatewayTestRuntimeForLimits(t, "12", balanceMinimumThreshold, 999_795)
	require.EqualValues(t, 3, spentRuntime.proxy.sm.TotalSlots(), "the nonces above assume a three-slot group: 1_000_000 - (3+1) - 200")
	gateway := NewGateway([]*devshardRuntime{spentRuntime, availableRuntime}, NewGatewayLimiter(0, 0), "m")
	gateway.maxNonce = devshardpkg.StaticMaxNonce(1_000_000)

	chosen, err := gateway.reserveRuntimeForModel("m", 5)

	require.NoError(t, err, "an escrow below the host active cap was skipped as spent")
	require.Equal(t, "12", chosen.id, "an escrow within the in-flight margin of the host active cap still took inferences")
}

func TestGatewayChooseRuntimeUsesTheDefaultLimitUntilTheChainMaxNonceIsKnown(t *testing.T) {
	escrowRuntime := gatewayTestRuntimeForLimits(t, "6", balanceMinimumThreshold, nonceDeactivationLimit)
	gateway := NewGateway([]*devshardRuntime{escrowRuntime}, NewGatewayLimiter(0, 0), "m")

	_, err := gateway.reserveRuntimeForModel("m", 5)

	require.ErrorContains(t, err, "skipped: high_nonce=1", "an escrow at the default limit took inferences before the chain max nonce was known")
}

func TestGatewayCheckBalancesKeepsAnEscrowBelowTheChainLimit(t *testing.T) {
	escrowRuntime := gatewayTestRuntimeForLimits(t, "12", balanceMinimumThreshold, nonceDeactivationLimit)
	gateway, created, _ := gatewayTestDepletionGateway(t, escrowRuntime)
	gateway.maxNonce = devshardpkg.StaticMaxNonce(1_000_000)

	runBalanceTick(t, gateway, escrowRuntime.id)

	require.EqualValues(t, 0, created.Load(), "an escrow below the chain limit was replaced as spent")
	require.True(t, escrowRuntime.active.Load(), "an escrow below the chain limit was taken out of service")
}
