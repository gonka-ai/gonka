package scheduler

import (
	"errors"
	"testing"

	"devshard/cmd/gateway/chain"
	"devshard/types"

	"github.com/stretchr/testify/require"
)

const escalationMaxNonce = 1_700

// escalationBounds are the cutoff routing stops at and the bound an escalation may go on to, for a group of groupSize.
func escalationBounds(groupSize int) (cutoff, bound uint64) {
	hostsCap := types.MaxActiveNonce(escalationMaxNonce, groupSize)
	margin := min(nonceInFlightMargin, hostsCap/nonceCeilingPerMaxMargin)
	return hostsCap - margin, hostsCap - margin/2
}

func retiredEscrow(latestNonce uint64) Escrow {
	return Escrow{
		ID:      "escrow-retired",
		Model:   modelA,
		Session: &fakeSession{latestNonce: latestNonce, slots: slotsOf("escrow-retired", 4)},
	}
}

// Test flow:
//  1. Register an escrow retired at its nonce cap and still held by a race, with its latest nonce just below the escalation bound, and one exactly at it.
//  2. Pin an escalation, and a client request, to each.
//  3. Assert only the escalation below the bound gets the escrow; the others hear ErrEscrowGone.
func TestAPinnedEscalationMayUseAnEscrowRetiredAtItsNonceCapBelowTheBound(t *testing.T) {
	_, bound := escalationBounds(4)
	snapshot := chain.PhaseSnapshot{MaxNonce: escalationMaxNonce}
	escalation := RequestProfile{Model: modelA, Escrow: "escrow-retired", Escalation: true}
	clientPin := RequestProfile{Model: modelA, Escrow: "escrow-retired"}

	for _, tc := range []struct {
		name        string
		latestNonce uint64
		profile     RequestProfile
		served      bool
	}{
		{"escalation below the bound", bound - 1, escalation, true},
		{"escalation at the bound", bound, escalation, false},
		{"client pin below the bound", bound - 1, clientPin, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			scheduler, escrows, _ := newScheduler()
			escrows.retired = map[string]Escrow{"escrow-retired": retiredEscrow(tc.latestNonce)}

			picked, err := pickWith(scheduler, tc.profile, snapshot)

			if tc.served {
				require.NoError(t, err)
				require.Equal(t, "escrow-retired", picked.ID)
				return
			}
			require.ErrorIs(t, err, ErrEscrowGone)
		})
	}
}

// Test flow:
//  1. Pin an escalation to an escrow that is not a candidate and was not retired at its nonce cap.
//  2. Assert the pick hears ErrEscrowGone: only a nonce-cap retirement is exempt.
func TestAPinnedEscalationOnAnEscrowRetiredForAnotherReasonIsGone(t *testing.T) {
	_, bound := escalationBounds(4)
	scheduler, escrows, _ := newScheduler()
	escrows.retired = map[string]Escrow{"escrow-other": retiredEscrow(bound - 1)}

	_, err := pickWith(scheduler, RequestProfile{Model: modelA, Escrow: "escrow-retired", Escalation: true}, chain.PhaseSnapshot{MaxNonce: escalationMaxNonce})

	require.ErrorIs(t, err, ErrEscrowGone)
}

// Test flow:
//  1. Keep an escrow past the cutoff among the candidates, below the escalation bound, with a hook recording exhaustion.
//  2. Pin an escalation and a client request to it, then an escalation with the chain's max_nonce unknown.
//  3. Assert the escalation is served and the exhaustion still reported, while the client pin and the escalation under the fallback ceiling are refused as capped.
func TestAPinnedEscalationMayGoPastTheCutoffOnALiveEscrow(t *testing.T) {
	cutoff, bound := escalationBounds(4)
	require.Less(t, cutoff, bound-1)
	scheduler, _, _ := newScheduler(candidate{id: "escrow-1", weight: 1, latestNonce: bound - 1})
	var exhausted []string
	scheduler.onEscrowExhausted = func(escrowID string, _ ExhaustionReason) { exhausted = append(exhausted, escrowID) }
	snapshot := chain.PhaseSnapshot{MaxNonce: escalationMaxNonce}

	picked, err := pickWith(scheduler, RequestProfile{Model: modelA, Escrow: "escrow-1", Escalation: true}, snapshot)
	require.NoError(t, err)
	require.Equal(t, "escrow-1", picked.ID)
	require.Equal(t, []string{"escrow-1"}, exhausted)

	_, err = pickWith(scheduler, RequestProfile{Model: modelA, Escrow: "escrow-1"}, snapshot)
	require.ErrorIs(t, err, ErrNoEscrowCapacity)

	unknownCap, _, _ := newScheduler(candidate{id: "escrow-1", weight: 1, latestNonce: fallbackNonceCeiling})
	_, err = pickWith(unknownCap, RequestProfile{Model: modelA, Escrow: "escrow-1", Escalation: true}, chain.PhaseSnapshot{})
	require.ErrorIs(t, err, ErrNoEscrowCapacity)
}

// Test flow:
//  1. Build a dispatcher whose live escrow hold refuses, as a retired escrow's does, and whose escalation hold admits.
//  2. Queue an escalation whose nonce lands just below the bound, and one whose nonce lands on it.
//  3. Assert the first is served on the escalation hold and the second hears ErrEscrowGone with nothing committed.
func TestADispatcherHoldsARetiredEscrowForEscalationsBelowTheBound(t *testing.T) {
	_, bound := escalationBounds(2)
	for _, tc := range []struct {
		name   string
		nonce  uint64
		served bool
	}{
		{"below the bound", bound - 1, true},
		{"at the bound", bound, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var escalationHolds int
			test := newHarness(t, harnessConfig{
				holdStart:      true,
				escrowHold:     func() (func(), bool) { return nil, false },
				escalationHold: func() (func(), bool) { escalationHolds++; return func() {}, true },
			})
			test.snapshots.set(chain.PhaseSnapshot{MaxNonce: escalationMaxNonce})
			test.session.nonce = tc.nonce - 1
			queued := test.submitCosting(t, RequestProfile{Model: modelA, Escrow: escrowA, Escalation: true, Params: "payload"}, test.clock.Now())
			test.dispatcher.start()

			result := awaitReply(t, queued)

			_, _, commits := test.session.report()
			if tc.served {
				wantAssignment(t, result, []string{hostA, hostB}[tc.nonce%2], tc.nonce)
				require.Equal(t, 1, escalationHolds)
				return
			}
			require.ErrorIs(t, result.err, ErrEscrowGone)
			require.Empty(t, commits)
			require.Zero(t, escalationHolds)
		})
	}
}

// Test flow:
//  1. Build a dispatcher whose live escrow hold refuses and whose escalation hold admits.
//  2. Queue a new request and an escalation together, below the bound.
//  3. Assert the new request hears ErrEscrowGone and the escalation is served: a new request never lands on a retired escrow.
func TestARetiredEscrowServesItsEscalationsButNoNewRequest(t *testing.T) {
	_, bound := escalationBounds(2)
	test := newHarness(t, harnessConfig{
		holdStart:      true,
		escrowHold:     func() (func(), bool) { return nil, false },
		escalationHold: func() (func(), bool) { return func() {}, true },
	})
	test.snapshots.set(chain.PhaseSnapshot{MaxNonce: escalationMaxNonce})
	test.session.nonce = bound - 10
	fresh := test.submit(t, test.clock.Now())
	escalation := test.submitCosting(t, RequestProfile{Model: modelA, Escrow: escrowA, Escalation: true, Params: "payload"}, test.clock.Now())
	test.dispatcher.start()

	freshResult, escalationResult := awaitReply(t, fresh), awaitReply(t, escalation)

	require.ErrorIs(t, freshResult.err, ErrEscrowGone)
	require.NoError(t, escalationResult.err)
	_, _, commits := test.session.report()
	require.Len(t, commits, 1)
	require.False(t, errors.Is(escalationResult.err, ErrEscrowGone))
}
