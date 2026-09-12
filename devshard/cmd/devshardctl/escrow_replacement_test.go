package main

import (
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestGatewayCheckBalancesBroadcastsOneReplacementForAnUnconfirmedCreate(t *testing.T) {
	gateway, depletedRuntime := newReplacementTestGateway(t, withoutSettlement)
	broadcasts := stubCreateOnChainUnconfirmed(t)

	runBalanceTick(t, gateway, depletedRuntime.id)
	runBalanceTick(t, gateway, depletedRuntime.id)

	require.EqualValues(t, 1, broadcasts.Load(), "a second balance tick broadcast another replacement for the same depleted escrow")
}

func TestGatewayCheckBalancesTakesADepletedEscrowOutOfServiceBeforeItsReplacementExists(t *testing.T) {
	gateway, depletedRuntime := newReplacementTestGateway(t, withoutSettlement)
	stubCreateOnChainFailingBeforeBroadcast(t)

	runBalanceTick(t, gateway, depletedRuntime.id)

	require.False(t, depletedRuntime.active.Load(), "a depleted escrow kept taking inferences while its replacement failed")
	require.False(t, devshardIDs(t, gateway.store)[depletedRuntime.id].Active, "a depleted escrow stayed saved active while its replacement failed")
}

func TestGatewayCheckBalancesDoesNotRetryAFailedReplacement(t *testing.T) {
	gateway, depletedRuntime := newReplacementTestGateway(t, withoutSettlement)
	attempts := stubCreateOnChainFailingBeforeBroadcast(t)

	runBalanceTick(t, gateway, depletedRuntime.id)
	runBalanceTick(t, gateway, depletedRuntime.id)

	require.EqualValues(t, 1, attempts.Load(), "a failed replacement was attempted again for an escrow already out of service")
}

func TestGatewayCheckBalancesMintsNothingWhileTheDeactivationIsUnsaved(t *testing.T) {
	gateway, depletedRuntime := newReplacementTestGateway(t, withoutSettlement)
	attempts := stubCreateOnChainFailingBeforeBroadcast(t)
	rejectDevshardColumnUpdate(t, gateway.store, depletedRuntime.id, "active", 0)

	runBalanceTick(t, gateway, depletedRuntime.id)

	require.EqualValues(t, 0, attempts.Load(), "a replacement was attempted although the depleted escrow is still saved active")
	require.True(t, depletedRuntime.active.Load(), "the depleted escrow stopped taking inferences although its deactivation is unsaved")
}

func TestGatewayCheckBalancesTakesTheEscrowOutOfServiceOnceItsDeactivationIsSaved(t *testing.T) {
	gateway, depletedRuntime := newReplacementTestGateway(t, withoutSettlement)
	attempts := stubCreateOnChainFailingBeforeBroadcast(t)
	allowDeactivation := rejectDevshardColumnUpdate(t, gateway.store, depletedRuntime.id, "active", 0)
	runBalanceTick(t, gateway, depletedRuntime.id)
	allowDeactivation()

	runBalanceTick(t, gateway, depletedRuntime.id)

	require.False(t, depletedRuntime.active.Load(), "an unsaved deactivation kept the depleted escrow in service after the store recovered")
	require.EqualValues(t, 1, attempts.Load(), "the replacement was not attempted exactly once after the deactivation was saved")
}

func TestGatewayCheckBalancesMintsNothingWhileTheSettlementMarkIsUnsaved(t *testing.T) {
	gateway, depletedRuntime := newReplacementTestGateway(t)
	attempts := stubCreateOnChainFailingBeforeBroadcast(t)
	rejectDevshardColumnUpdate(t, gateway.store, depletedRuntime.id, "settlement_pending", 1)

	runBalanceTick(t, gateway, depletedRuntime.id)

	require.EqualValues(t, 0, attempts.Load(), "a replacement was attempted although the settlement mark of the depleted escrow is unsaved")
	require.True(t, devshardIDs(t, gateway.store)[depletedRuntime.id].Active, "the depleted escrow was saved inactive without its settlement mark")
	require.True(t, depletedRuntime.active.Load(), "the depleted escrow stopped taking inferences although its settlement mark is unsaved")
}

func TestGatewayBalanceExhaustedTakesTheEscrowOutOfServiceWhenItsReplacementFails(t *testing.T) {
	gateway, depletedRuntime := newReplacementTestGateway(t, withoutSettlement)
	stubCreateOnChainFailingBeforeBroadcast(t)
	depletedRuntime.proxy.redundancy = &Redundancy{}
	gateway.attachEscrowChecker(depletedRuntime)

	depletedRuntime.proxy.redundancy.onBalanceExhausted()
	waitForReplacementIdle(t, gateway, depletedRuntime.id)

	require.False(t, depletedRuntime.active.Load(), "an exhausted escrow kept taking inferences while its replacement failed")
}

func TestGatewayCheckBalancesSwapsADepletedEscrowForARegularReplacement(t *testing.T) {
	gateway, depletedRuntime := newReplacementTestGateway(t, withoutSettlement)
	stubCreateOnChain(t, "TXCONFIRMED", 99)

	runBalanceTick(t, gateway, depletedRuntime.id)

	replacement, isPersisted := devshardIDs(t, gateway.store)["99"]
	require.True(t, isPersisted, "the replacement escrow was not persisted")
	require.Equal(t, rotationRoleRegular, replacement.RotationRole, "the replacement escrow was not persisted as regular")
	require.False(t, depletedRuntime.active.Load(), "the depleted escrow kept taking inferences after its replacement was persisted")
	require.Empty(t, pendingCommitments(t, gateway.store), "the confirmed replacement left its intent pending")
}

func TestGatewayScheduleDepletedEscrowReplacementIgnoresADeactivatedReplacedEscrow(t *testing.T) {
	gateway, depletedRuntime := newReplacementTestGateway(t, withoutSettlement)
	broadcasts := stubCreateOnChain(t, "TXCONFIRMED", 99)
	runBalanceTick(t, gateway, depletedRuntime.id)
	require.EqualValues(t, 1, broadcasts.Load(), "the depleted escrow was not replaced by the balance tick")

	gateway.scheduleDepletedEscrowReplacement(depletedRuntime.id, depletedRuntime.model, "balance_exhausted")
	waitForReplacementIdle(t, gateway, depletedRuntime.id)

	require.EqualValues(t, 1, broadcasts.Load(), "a late trigger minted a second replacement for a deactivated escrow")
}

func TestGatewayScheduleDepletedEscrowReplacementIgnoresARetiredReplacedEscrow(t *testing.T) {
	gateway, depletedRuntime := newReplacementTestGateway(t, withoutSettlement)
	broadcasts := stubCreateOnChain(t, "TXCONFIRMED", 99)
	runBalanceTick(t, gateway, depletedRuntime.id)
	require.EqualValues(t, 1, broadcasts.Load(), "the depleted escrow was not replaced by the balance tick")
	require.True(t, gateway.retireRuntime(depletedRuntime.id, "settled"), "the replaced escrow was not retired")

	gateway.scheduleDepletedEscrowReplacement(depletedRuntime.id, depletedRuntime.model, "balance_exhausted")
	waitForReplacementIdle(t, gateway, depletedRuntime.id)

	require.EqualValues(t, 1, broadcasts.Load(), "a late trigger minted a second replacement for a retired escrow")
}

func TestGatewayScheduleDepletedEscrowReplacementSettlesADeactivatedEscrowOnce(t *testing.T) {
	depletedRuntime := gatewayTestRuntimeForLimits(t, "12", balanceMinimumThreshold-1, nonceDeactivationLimit-1)
	gateway, _, settled := gatewayTestDepletionGateway(t, depletedRuntime)
	runBalanceTick(t, gateway, depletedRuntime.id)
	waitForSettlementFinished(t, gateway, depletedRuntime.id, settled)

	gateway.scheduleDepletedEscrowReplacement(depletedRuntime.id, depletedRuntime.model, "balance_exhausted")
	waitForReplacementIdle(t, gateway, depletedRuntime.id)

	require.False(t, isSettlementInFlight(gateway, depletedRuntime.id), "a late trigger started settling an escrow that was already settled")
	require.EqualValues(t, 1, settled.Load(), "a late trigger settled an escrow that was already settled")
}

// newReplacementTestGateway builds a rotating gateway whose escrow "12" sits at the nonce limit, with the real replacement create and a stub runtime builder.
func newReplacementTestGateway(t *testing.T, modifySettings ...func(*GatewaySettings)) (*Gateway, *devshardRuntime) {
	t.Helper()
	depletedRuntime := gatewayTestRuntimeForLimits(t, "12", balanceMinimumThreshold, nonceDeactivationLimit)
	gateway, _, _ := gatewayTestDepletionGateway(t, depletedRuntime, modifySettings...)
	stubRuntimeBuilder(t)
	saved := gatewayCreateDepletionEscrow
	gatewayCreateDepletionEscrow = nil
	t.Cleanup(func() { gatewayCreateDepletionEscrow = saved })
	return gateway, depletedRuntime
}

func withoutSettlement(settings *GatewaySettings) {
	settings.EscrowRotation.SettlementEnabled = false
}

func pendingCommitments(t *testing.T, store *GatewayStore) []GatewayEscrowCommitment {
	t.Helper()
	commitments, err := store.LoadCommitments()
	require.NoError(t, err)
	return commitments
}

// rejectDevshardColumnUpdate makes the store refuse writing the value to the escrow's column and returns a function that lifts the refusal.
func rejectDevshardColumnUpdate(t *testing.T, store *GatewayStore, escrowID, column string, rejectedValue int) func() {
	t.Helper()
	_, err := store.db.Exec(fmt.Sprintf(`
		CREATE TRIGGER reject_%[2]s_update
		BEFORE UPDATE OF %[2]s ON gateway_devshards
		WHEN NEW.id = '%[1]s' AND NEW.%[2]s = %[3]d
		BEGIN
			SELECT RAISE(ABORT, 'update rejected by test');
		END`, escrowID, column, rejectedValue))
	require.NoError(t, err)
	return func() {
		_, err := store.db.Exec(fmt.Sprintf(`DROP TRIGGER reject_%s_update`, column))
		require.NoError(t, err)
	}
}

// runBalanceTick runs one balance check and waits until any replacement it started has finished.
func runBalanceTick(t *testing.T, gateway *Gateway, escrowID string) {
	t.Helper()
	gateway.checkBalances()
	waitForReplacementIdle(t, gateway, escrowID)
}

func waitForReplacementIdle(t *testing.T, gateway *Gateway, escrowID string) {
	t.Helper()
	require.Eventually(t, func() bool {
		gateway.replenishmentMu.Lock()
		defer gateway.replenishmentMu.Unlock()
		_, isInFlight := gateway.replenishmentInFlight[escrowID]
		return !isInFlight
	}, escrowWriteRetries*escrowWriteRetryBackoff+2*time.Second, 10*time.Millisecond, "replacement for escrow %s did not finish", escrowID)
}

func waitForSettlementFinished(t *testing.T, gateway *Gateway, escrowID string, settled *atomic.Int32) {
	t.Helper()
	require.Eventually(t, func() bool {
		record, _, err := gateway.store.GetDevshard(escrowID)
		return settled.Load() == 1 && err == nil && !record.SettlementPending && !isSettlementInFlight(gateway, escrowID)
	}, time.Second, 10*time.Millisecond, "settlement of escrow %s did not finish", escrowID)
}

func isSettlementInFlight(gateway *Gateway, escrowID string) bool {
	gateway.settlementMu.Lock()
	defer gateway.settlementMu.Unlock()
	_, isSettling := gateway.settlementInFlight[escrowID]
	return isSettling
}

// stubCreateOnChainUnconfirmed broadcasts the create and then fails, as when the tx lands but its result never arrives.
func stubCreateOnChainUnconfirmed(t *testing.T) *atomic.Int32 {
	t.Helper()
	var broadcasts atomic.Int32
	stubCreateOnChainWith(t, func(onPrepared func(string) error) (*CreateDevshardEscrowResult, error) {
		if err := onPrepared("TXUNCONFIRMED"); err != nil {
			return nil, err
		}
		broadcasts.Add(1)
		return nil, errors.New("wait for tx: context deadline exceeded")
	})
	return &broadcasts
}

func stubCreateOnChainFailingBeforeBroadcast(t *testing.T) *atomic.Int32 {
	t.Helper()
	var attempts atomic.Int32
	stubCreateOnChainWith(t, func(func(string) error) (*CreateDevshardEscrowResult, error) {
		attempts.Add(1)
		return nil, errors.New("account info: rpc unavailable")
	})
	return &attempts
}
