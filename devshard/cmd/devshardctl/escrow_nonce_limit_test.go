package main

import (
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	devshardpkg "devshard"
)

type signalingMaxNonce struct {
	read     chan struct{}
	readOnce sync.Once
}

func (provider *signalingMaxNonce) MaxNonce() uint32 {
	provider.readOnce.Do(func() { close(provider.read) })
	return 1_000_000
}

// The first balance check runs as the gateway starts, so the chain max nonce must reach the gateway before it.
func TestNewManagedGatewayChecksBalancesWithTheChainMaxNonceFromTheStart(t *testing.T) {
	store, err := NewGatewayStore(filepath.Join(t.TempDir(), "gateway.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	settings := GatewaySettings{DefaultModel: "m", EscrowRotation: EscrowRotationSettings{Enabled: true}}
	require.NoError(t, store.Initialize(settings, nil))
	maxNonce := &signalingMaxNonce{read: make(chan struct{})}

	gateway := NewManagedGateway(nil, NewGatewayLimiter(0, 0), settings, t.TempDir(), store, nil, nil, nil, maxNonce)
	t.Cleanup(gateway.stopEscrowRotator)

	requireClosedWithin(t, maxNonce.read, "the first balance check ran without the chain max nonce")
}

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

func TestGatewayChooseRuntimeRoutesBelowTheDefaultLimitUntilTheChainMaxNonceIsKnown(t *testing.T) {
	escrowRuntime := gatewayTestRuntimeForLimits(t, "6", balanceMinimumThreshold, nonceDeactivationLimit-1)
	gateway := NewGateway([]*devshardRuntime{escrowRuntime}, NewGatewayLimiter(0, 0), "m")

	chosen, err := gateway.reserveRuntimeForModel("m", 5)

	require.NoError(t, err, "an escrow below the default limit was skipped as spent before the chain max nonce was known")
	require.Equal(t, "6", chosen.id)
}

func TestGatewayCheckBalancesKeepsAnEscrowBelowTheChainLimit(t *testing.T) {
	escrowRuntime := gatewayTestRuntimeForLimits(t, "12", balanceMinimumThreshold, nonceDeactivationLimit)
	gateway, created, _ := gatewayTestDepletionGateway(t, escrowRuntime)
	gateway.maxNonce = devshardpkg.StaticMaxNonce(1_000_000)

	runBalanceTick(t, gateway, escrowRuntime.id)

	require.EqualValues(t, 0, created.Load(), "an escrow below the chain limit was replaced as spent")
	require.True(t, escrowRuntime.active.Load(), "an escrow below the chain limit was taken out of service")
}

// An escrow past the default limit may still be far below the chain's max_nonce, so it is not retired before that value is known.
func TestGatewayCheckBalancesKeepsAnEscrowAtTheDefaultLimitUntilTheChainMaxNonceIsKnown(t *testing.T) {
	escrowRuntime := gatewayTestRuntimeForLimits(t, "12", balanceMinimumThreshold, nonceDeactivationLimit)
	gateway, created, _ := gatewayTestDepletionGateway(t, escrowRuntime, withoutSettlement)

	runBalanceTick(t, gateway, escrowRuntime.id)

	require.EqualValues(t, 0, created.Load(), "an escrow was replaced for its nonce before the chain max nonce was known")
	require.True(t, escrowRuntime.active.Load(), "an escrow was taken out of service for its nonce before the chain max nonce was known")
}

func TestGatewayChooseRuntimeReplacesNothingUntilTheChainMaxNonceIsKnown(t *testing.T) {
	escrowRuntime := gatewayTestRuntimeForLimits(t, "12", balanceMinimumThreshold, nonceDeactivationLimit)
	gateway, created, _ := gatewayTestDepletionGateway(t, escrowRuntime, withoutSettlement)

	_, _ = gateway.reserveRuntimeForModel("m", 5)
	waitForReplacementIdle(t, gateway, escrowRuntime.id)

	require.EqualValues(t, 0, created.Load(), "routing replaced an escrow for its nonce before the chain max nonce was known")
	require.True(t, escrowRuntime.active.Load(), "routing took an escrow out of service for its nonce before the chain max nonce was known")
}

// A chain max_nonce too small for the margin must not wrap the limit around to a nonce no escrow ever reaches.
func TestEscrowNonceLimitKeepsTheHostActiveCapWhenNoMarginIsLeft(t *testing.T) {
	require.EqualValues(t, 146, escrowNonceLimit(150, 3), "150 - (3+1) leaves no room for the 200 in-flight margin")
}
