package warmup

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	"devshard/cmd/gateway/registry"
	"devshard/user"
)

type recordingWarmupNarrator struct {
	calls         []string
	probeErrors   []error
	catchUpErrors []error
}

func (n *recordingWarmupNarrator) WarmupFoundNoNonce(escrowID string, probeErr error) {
	n.calls = append(n.calls, "no nonce "+escrowID)
	n.probeErrors = append(n.probeErrors, probeErr)
}

func (n *recordingWarmupNarrator) EscrowWarmed(escrowID, model string, nonce uint64, served bool, catchUpErr error) {
	n.calls = append(n.calls, fmt.Sprintf("warmed %s %s nonce %d served %t", escrowID, model, nonce, served))
	n.catchUpErrors = append(n.catchUpErrors, catchUpErr)
}

func (n *recordingWarmupNarrator) WarmupLedgerOpenFailed(escrowID string, err error) {
	n.calls = append(n.calls, fmt.Sprintf("ledger open failed %s: %v", escrowID, err))
}

func (n *recordingWarmupNarrator) WarmupVoted(escrowID string, nonce uint64, action, reason string) {
	n.calls = append(n.calls, fmt.Sprintf("voted %s nonce %d %s %s", escrowID, nonce, action, reason))
}

// Test flow:
//  1. Build a `Prober` whose probe commits no nonce and returns no error.
//  2. Warm one escrow through it with a recording narrator attached.
//  3. Assert the narrator recorded a single "no nonce" call for the escrow.
//  4. Assert the recorded probe error is nil.
func TestAProbeThatCommittedNothingIsNarratedWithTheErrorItHad(t *testing.T) {
	narrator := &recordingWarmupNarrator{}
	warmup := &Prober{
		escrows: &stubEscrows{session: stubSession{}, live: true},
		probe: func(context.Context, registry.EscrowSession, user.InferenceParams, func()) (uint64, bool, error) {
			return 0, false, nil
		},
		catchUp: func(context.Context, registry.EscrowSession) error { return nil },
		stop:    make(chan struct{}),
		now:     warmupClock(),
	}
	warmup.SetNarrator(narrator)

	warmup.warm("escrow-1", "model-a")

	require.Equal(t, []string{"no nonce escrow-1"}, narrator.calls)
	require.Equal(t, []error{nil}, narrator.probeErrors)
}

// Test flow:
//  1. Build a warmup under test that serves its probe and attach a recording narrator.
//  2. Warm one escrow.
//  3. Assert the narrator recorded it as warmed, naming the model, nonce, and served flag.
//  4. Assert the recorded catch-up error is nil.
func TestAServedProbeIsNarratedAsWarmed(t *testing.T) {
	narrator := &recordingWarmupNarrator{}
	warmup, _, _ := newWarmupUnderTest(stubSession{}, nil)
	warmup.SetNarrator(narrator)

	warmup.warm("escrow-1", "model-a")

	require.Equal(t, []string{"warmed escrow-1 model-a nonce 1 served true"}, narrator.calls)
	require.Equal(t, []error{nil}, narrator.catchUpErrors)
}

// Test flow:
//  1. Build a refused warmup whose poster answers a refused vote with a timeout-collection error.
//  2. Warm one escrow with a recording narrator attached.
//  3. Assert the narrator recorded the vote's action and reason before recording the escrow as warmed but not served.
func TestAFailedWarmupVoteIsNarratedWithItsAction(t *testing.T) {
	narrator := &recordingWarmupNarrator{}
	warmup := refusedWarmup(&stubPoster{vote: "refused", err: errors.New("collect timeout votes")}, &spyTimeouts{}, true)
	warmup.SetNarrator(narrator)

	warmup.warm("escrow-1", "model-a")

	require.Equal(t, []string{
		"voted escrow-1 nonce 7 failed timeout_collection_error",
		"warmed escrow-1 model-a nonce 7 served false",
	}, narrator.calls)
}

// Test flow:
//  1. Build a `Prober` whose ledger refuses to open.
//  2. Open the ledger for one escrow with a recording narrator attached.
//  3. Assert the narrator recorded the ledger-open failure with the escrow ID and the refusal's error text.
func TestALedgerThatRefusesTheEscrowIsNarrated(t *testing.T) {
	narrator := &recordingWarmupNarrator{}
	warmup := &Prober{ledger: &spyLedger{openRefusal: errors.New("ledger closed")}, epochs: stubEpochs{epoch: 3}, now: warmupClock()}
	warmup.SetNarrator(narrator)

	warmup.openLedger("escrow-1", "model-a", stubSession{})

	require.Equal(t, []string{"ledger open failed escrow-1: ledger closed"}, narrator.calls)
}

// Test flow:
//  1. Call `SetNarrator` on a nil `*Prober`.
//  2. Assert it does not panic.
func TestBindingANarratorToAnAbsentWarmupDoesNothing(t *testing.T) {
	var warmup *Prober

	require.NotPanics(t, func() { warmup.SetNarrator(&recordingWarmupNarrator{}) })
}
