package accounting

import (
	"context"
	"testing"

	"devshard/types"

	"github.com/stretchr/testify/require"
)

// A burned nonce is never sent, yet the gateway raises a timeout on it and the chain counts the miss
// when the group accepts. The ledger has to be able to hold that, or the cross-check compares the
// chain's misses against a population that excludes them by construction.
func TestATimeoutOnABurnedNonceIsRecorded(t *testing.T) {
	tracker := newTestTracker(t)
	registerEscrow(t, tracker, "e1", 7, "m")

	require.NoError(t, tracker.RecordDiff("e1", 2, true))
	require.NoError(t, tracker.RecordGhost("e1", 2, PhaseNormal, QuarantineNone, NoSendPoCUnavailable, "", true))
	require.NoError(t, tracker.RecordTimeout(TimeoutRecord{
		EscrowID: "e1", Nonce: 2, Kind: TimeoutRefused, Phase: PhaseNormal, Outcome: TimeoutApplied,
	}), "a timeout on a burned nonce must be accepted")

	record := recordFor(t, tracker, "p0")
	require.Equal(t, uint64(1), record.TimeoutOutcomes[TimeoutApplied],
		"the applied timeout must reach the population the cross-check compares against")
}

// The invariant it relaxes still holds everywhere else: a nonce that was neither sent nor burned has
// no business carrying a timeout, and accepting one would let a bug write history that never happened.
func TestATimeoutOnANonceThatWasNeitherSentNorBurnedIsRefused(t *testing.T) {
	tracker := newTestTracker(t)
	registerEscrow(t, tracker, "e1", 7, "m")

	require.NoError(t, tracker.RecordDiff("e1", 2, true))
	err := tracker.RecordTimeout(TimeoutRecord{
		EscrowID: "e1", Nonce: 2, Kind: TimeoutRefused, Phase: PhaseNormal, Outcome: TimeoutApplied,
	})
	require.Error(t, err, "a timeout without a send or a burn behind it must be refused")
}

// A burn with no timeout behind it must retire on the spot. Holding it would leak a live entry per
// burn for an outcome that is never coming.
func TestABurnWithNoTimeoutComingRetiresAtOnce(t *testing.T) {
	tracker := newTestTracker(t)
	registerEscrow(t, tracker, "e1", 7, "m")

	require.NoError(t, tracker.RecordDiff("e1", 2, true))
	require.NoError(t, tracker.RecordGhost("e1", 2, PhaseNormal, QuarantineNone, NoSendPoCUnavailable, "", false))

	err := tracker.RecordTimeout(TimeoutRecord{
		EscrowID: "e1", Nonce: 2, Kind: TimeoutRefused, Phase: PhaseNormal, Outcome: TimeoutApplied,
	})
	require.Error(t, err, "a burn that declared no timeout must not stay live waiting for one")
}

// The whole point of the fix, asserted as the number it exists to move: burns whose timeouts the group
// accepted become chain misses, and the cross-check has to see them on both sides. Before the fix the
// ledger could not hold them at all, so this difference was the gateway's own policy reported as a
// disagreement with the chain.
func TestBurnsTheGroupTimedOutBalanceTheCrossCheck(t *testing.T) {
	tracker := newTestTracker(t)
	registerEscrow(t, tracker, "e1", 7, "m")

	const applied, cleared = 3, 5
	nonce := uint64(0)
	burn := func(timeoutApplied bool) {
		nonce += 2 // slot 0 of a two-slot group
		require.NoError(t, tracker.RecordDiff("e1", nonce, true))
		require.NoError(t, tracker.RecordGhost("e1", nonce, PhaseNormal, QuarantineNone, NoSendPoCUnavailable, "", true))
		outcome := TimeoutInsufficientVotes
		if timeoutApplied {
			outcome = TimeoutApplied
		}
		require.NoError(t, tracker.RecordTimeout(TimeoutRecord{
			EscrowID: "e1", Nonce: nonce, Kind: TimeoutRefused, Phase: PhaseNormal, Outcome: outcome,
		}))
	}
	for range applied {
		burn(true)
	}
	for range cleared {
		burn(false)
	}

	// The chain counts a miss for each timeout the group accepted, and only those.
	require.NoError(t, tracker.RecordProtocol("e1", 2, 0, ProtocolTimeoutApplied, types.HostStats{Missed: applied}))

	record := recordFor(t, tracker, "p0")
	require.Equal(t, uint64(applied), record.ProtocolMisses, "the chain counted one miss per accepted timeout")
	require.Equal(t, uint64(applied), record.TimeoutOutcomes[TimeoutApplied],
		"the ledger must hold the same population the chain counted")
	require.Zero(t, record.CrossChecks.ErrorCount,
		"a burn the group timed out is the gateway's own doing, not a disagreement with the chain")
}

// Holding a burned nonce live until its timeout arrives is bounded by that timeout, not open-ended: a
// gateway that dies between the burn and the raise must not leave the entry behind forever. Retiring
// the escrow releases it, which is the same sweep every other unfinished nonce depends on.
func TestABurnAwaitingATimeoutDoesNotOutliveItsEscrow(t *testing.T) {
	tracker := newTestTracker(t)
	registerEscrow(t, tracker, "e1", 7, "m")

	require.NoError(t, tracker.RecordDiff("e1", 2, true))
	require.NoError(t, tracker.RecordGhost("e1", 2, PhaseNormal, QuarantineNone, NoSendPoCUnavailable, "", true))
	require.NotEmpty(t, liveNonceCount(t, tracker, "e1"), "the burn waits for the timeout it declared")

	purged, err := tracker.PurgeEpoch(context.Background(), 7)
	require.NoError(t, err)
	require.Positive(t, purged, "the sweep must reach an escrow holding a burn that never got its timeout")
	require.Zero(t, liveNonceCount(t, tracker, "e1"), "nothing may survive the sweep")
}

func liveNonceCount(t *testing.T, tracker *Tracker, escrowID string) int {
	t.Helper()
	tracker.mu.Lock()
	defer tracker.mu.Unlock()
	escrow := tracker.escrows[escrowID]
	if escrow == nil {
		return 0
	}
	return len(escrow.Live)
}
