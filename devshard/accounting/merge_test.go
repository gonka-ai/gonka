package accounting

import (
	"testing"

	"github.com/stretchr/testify/require"

	"devshard/types"
)

// These cover the merge logic itself, without Postgres, so the rules stay
// checked when the container-backed tests are skipped.

func TestSaturatingSubClampsAtZero(t *testing.T) {
	require.Equal(t, uint64(3), saturatingSub(5, 2))
	require.Equal(t, uint64(0), saturatingSub(2, 5), "a peer's rows must never be cancelled")
	require.Equal(t, uint64(0), saturatingSub(4, 4))
}

func TestContributionMinusKeepsOnlyOwnShare(t *testing.T) {
	key := CounterKey{SlotID: 1, Disposition: DispositionGhost}
	other := CounterKey{SlotID: 0, Disposition: DispositionFinishedUsed}

	total := newEscrowContribution()
	total.addCounter(key, 5)
	total.addCounter(other, 2)

	peer := newEscrowContribution()
	peer.addCounter(key, 3)

	mine := total.minus(peer)
	require.Equal(t, uint64(2), mine.counters[key], "the peer's three stay attributed to the peer")
	require.Equal(t, uint64(2), mine.counters[other], "a key no peer holds is published whole")

	// A key whose local total dropped below the peer baseline (reclassification)
	// must disappear rather than go negative.
	shrunk := newEscrowContribution()
	shrunk.addCounter(key, 1)
	require.NotContains(t, shrunk.minus(peer).counters, key)
}

func TestContributionFromBlobIsIdempotentAcrossReplay(t *testing.T) {
	blob := escrowBlob{
		Counters: []counterBlob{{Key: CounterKey{SlotID: 1, Disposition: DispositionGhost}, Count: 4}},
	}
	peer := newEscrowContribution()
	peer.addCounter(CounterKey{SlotID: 1, Disposition: DispositionGhost}, 1)

	first := contributionFromBlob(blob).minus(peer)
	second := contributionFromBlob(blob).minus(peer)
	require.Equal(t, first.counters, second.counters,
		"replaying an unchanged snapshot must publish the same absolute value")
}

// TestViewDerivesPerSlotTotalsFromNonceSets pins the read side of the set
// layout: the per-slot numbers the query path consumes are counted from the
// nonce sets, on top of whatever the pre-set layout left behind.
func TestViewDerivesPerSlotTotalsFromNonceSets(t *testing.T) {
	escrow := &escrowState{
		Meta:      EscrowMetadata{EscrowID: "e", Model: "m"},
		HostStats: map[uint32]types.HostStats{},
		Counters:  map[CounterKey]uint64{},
		ProtocolOnly: map[uint64]uint32{
			3: 1,
			5: 1,
			8: 0,
		},
		Challenge: map[uint64]challengeRecord{
			10: {Slot: 1},
			12: {Slot: 1, Resolved: true},
			14: {Slot: 0},
		},
		Invalid: map[uint64]uint32{20: 1, 22: 1},
		Live:    map[uint64]*nonceState{},
	}

	view := escrow.view("e")
	require.Equal(t, uint64(2), view.counters[CounterKey{SlotID: 1, Disposition: DispositionProtocolOnly}])
	require.Equal(t, uint64(1), view.counters[CounterKey{SlotID: 0, Disposition: DispositionProtocolOnly}])
	require.Equal(t, uint64(1), view.challengeBySlot[1], "a resolved challenge is no longer unresolved")
	require.Equal(t, uint64(1), view.challengeBySlot[0])
	require.Equal(t, uint64(2), view.invalidBySlot[1])
}

func TestViewAddsPreSetLayoutTotals(t *testing.T) {
	escrow := &escrowState{
		Meta:            EscrowMetadata{EscrowID: "e", Model: "m"},
		HostStats:       map[uint32]types.HostStats{},
		Counters:        map[CounterKey]uint64{},
		ProtocolOnly:    map[uint64]uint32{3: 1},
		Challenge:       map[uint64]challengeRecord{10: {Slot: 1}},
		Invalid:         map[uint64]uint32{20: 1},
		ChallengeBySlot: map[uint32]uint64{1: 2},
		InvalidBySlot:   map[uint32]uint64{1: 3},
		Live:            map[uint64]*nonceState{},
	}

	view := escrow.view("e")
	require.Equal(t, uint64(3), view.challengeBySlot[1], "carried total plus the derived one")
	require.Equal(t, uint64(4), view.invalidBySlot[1])
}

// TestChallengeResolutionIsMonotonic covers the flag that replaced deleting the
// entry: repeated verdicts must not reopen or double-close a challenge.
func TestChallengeResolutionIsMonotonic(t *testing.T) {
	escrow := &escrowState{Challenge: map[uint64]challengeRecord{}}

	escrow.openChallenge(7, 1)
	escrow.openChallenge(7, 1)
	require.Len(t, escrow.Challenge, 1)
	require.False(t, escrow.Challenge[7].Resolved)

	require.Equal(t, uint32(1), escrow.resolveChallenge(7, 9), "the challenged slot wins over the fallback")
	require.True(t, escrow.Challenge[7].Resolved)

	// A challenge verdict arriving after resolution must not reopen it.
	escrow.openChallenge(7, 1)
	require.True(t, escrow.Challenge[7].Resolved)

	// An unknown nonce falls back to the verdict's slot and records nothing.
	require.Equal(t, uint32(9), escrow.resolveChallenge(99, 9))
	require.NotContains(t, escrow.Challenge, uint64(99))
}

func TestRecordInvalidCountsNonceOnce(t *testing.T) {
	escrow := &escrowState{
		Challenge: map[uint64]challengeRecord{},
		Invalid:   map[uint64]uint32{},
	}
	escrow.openChallenge(7, 1)

	escrow.recordInvalid(7, 3)
	escrow.recordInvalid(7, 3)
	require.Equal(t, map[uint64]uint32{7: 1}, escrow.Invalid,
		"a repeated verdict is the same nonce, and the challenged slot decides attribution")

	// A nonce already counted under the pre-set layout stays out of the set.
	escrow.InvalidLegacy = map[uint64]struct{}{9: {}}
	escrow.recordInvalid(9, 0)
	require.NotContains(t, escrow.Invalid, uint64(9))
}

func TestBlobRoundTripPreservesNonceSets(t *testing.T) {
	escrow := &escrowState{
		Meta: EscrowMetadata{
			EscrowID:             "e",
			CreationEpoch:        4,
			Model:                "m",
			Phase:                EscrowActive,
			RefusalTimeout:       60,
			ExecutionTimeout:     1200,
			TimeoutBufferSeconds: 5,
			Slots: []types.SlotAssignment{
				{SlotID: 0, ValidatorAddress: "p0"},
				{SlotID: 1, ValidatorAddress: "p1"},
			},
		},
		HostStats:     map[uint32]types.HostStats{},
		Counters:      map[CounterKey]uint64{},
		ProtocolOnly:  map[uint64]uint32{3: 1, 8: 0},
		Challenge:     map[uint64]challengeRecord{10: {Slot: 1}, 12: {Slot: 0, Resolved: true}},
		Invalid:       map[uint64]uint32{20: 1},
		InvalidLegacy: map[uint64]struct{}{5: {}},
		Live:          map[uint64]*nonceState{},
	}

	raw, err := encodeEscrowBlob(blobFromEscrow(escrow))
	require.NoError(t, err)
	decoded, err := decodeEscrowBlob(raw)
	require.NoError(t, err)

	tr := &Tracker{escrows: map[string]*escrowState{}}
	require.NoError(t, applyLoadedEscrow(tr, decoded))
	loaded := tr.escrows["e"]
	require.Equal(t, escrow.ProtocolOnly, loaded.ProtocolOnly)
	require.Equal(t, escrow.Challenge, loaded.Challenge)
	require.Equal(t, escrow.Invalid, loaded.Invalid)
	require.Equal(t, escrow.InvalidLegacy, loaded.InvalidLegacy)
}
