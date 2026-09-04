package scenarios

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"devshard/heightsync"
	"devshard/types"
)

// A roster spread hundreds of blocks apart, far past D = 2. Nothing here is
// misbehaviour: a host that fell behind its own chain follower looks exactly
// like this, and the escrow has to keep serving through it.
//
// Slot 3 sits ~1000 blocks under the rest, which used to be past W_conf and so
// took the producer rule's omit branch. That branch is gone with W_conf, and
// this spread is what proves it: slot 3 now carries the floor like everyone
// else, and a lone host claim seeds the floor at any distance.
var divergedOracleHeights = []int64{1000, 998, 900, 5}

// TestHeightSync_E2E_WideDivergenceNeverBlocksInferences runs sustained
// inference traffic across a roster whose tips disagree by ~1000 blocks, over
// the real HTTP stack with real producer stamping and real diff validation.
//
// The state-level test writes stamps by hand; this one proves the shipping
// producers choose stamps a verifier accepts. That is the part that regressed
// before: the user stamps starts from the roster maximum while each executor
// confirms from its own follower, so a rule comparing those two directly turned
// every lagging host into an HTTP 500 and halted the escrow.
func TestHeightSync_E2E_WideDivergenceNeverBlocksInferences(t *testing.T) {
	ctx := context.Background()

	hostOracles := make([]*staticOracle, len(divergedOracleHeights))
	for i, h := range divergedOracleHeights {
		hostOracles[i] = staticOracleWith(h, []byte{0xd0, byte(i), byte(h & 0xff)})
	}
	st, _ := setupFourHostHTTPHeightSyncCourier(t, hostOracles)
	seedHTTPSession(t, st.Session)
	params := defaultInferenceParams()

	// Twelve nonces: two full sync turns (1–4, 8–11) with the omit window
	// between them, and every slot serving as executor three times. Each
	// SendInference is start + confirm + finish through validate-then-apply on
	// the sequencer and on the receiving host, so a single divergence-driven
	// INVALID anywhere in that path fails here.
	const nonces = 12
	for n := uint64(1); n <= nonces; n++ {
		_, err := st.Session.SendInference(ctx, params)
		require.NoErrorf(t, err, "nonce=%d served by slot %d must not fail on height divergence",
			n, hostIdxForNonce(n))
		syncHostsFromSession(t, st)
	}

	sm := st.Session.StateMachine()
	require.Equal(t, uint64(nonces), sm.LatestNonce())
	snap := sm.SnapshotState()

	// Divergence must survive the round trip rather than being clamped to one
	// roster-wide number — and the place it survives is the **envelope**, not the
	// log. Each host's response-leg anchor carries its own first-party tip, which
	// is what the gateway's collectors aggregate into a divergence surface. The
	// log deliberately holds one shared reference height instead, so the audit
	// ring is the only plane where the spread is visible at all.
	seen := make(map[int64]bool)
	for i, srv := range st.Servers {
		ar := srv.HeightSyncAuditRing()
		require.NotNil(t, ar)
		var sawOwn bool
		for _, a := range ar.List(st.HostAddrs[i]) {
			if a.Direction != "response" {
				continue
			}
			seen[a.MainnetHeight] = true
			if a.MainnetHeight == divergedOracleHeights[i] {
				sawOwn = true
			}
		}
		require.Truef(t, sawOwn, "host %d must attest its own tip %d, not a roster-wide value",
			i, divergedOracleHeights[i])
	}
	require.True(t, seen[divergedOracleHeights[0]] && seen[divergedOracleHeights[3]],
		"both ends of the spread must reach the log for the dispute layer to have a case")

	// Both branches of the producer rule, read off the applied records.
	//
	// A **host** always has a branch to take: its own tip when that is above the
	// floor, the floor itself when it is behind. There is no distance escape any
	// more, so the most-behind slot in the roster still writes a height.
	//
	// The **sequencer** is the one party that can end up with nothing to write:
	// before any host has stamped, there is no F to carry and the user has no
	// oracle of its own (§10.3.1). Nonces 1–2 are exactly that window.
	recs := snap.Inferences

	// Bootstrap. F is empty at nonces 1 and 2, so the start leg goes out
	// unstamped rather than carrying the courier's cached roster maximum — the
	// courier is not a height source. Each executor still stamps first-party, so
	// the spread itself lands in the log and nothing is rejected.
	for _, id := range []uint64{1, 2} {
		require.Zerof(t, recs[id].StartedAtHeight,
			"inference %d predates any host stamp, so the sequencer omits", id)
		require.Equalf(t, uint64(divergedOracleHeights[hostIdxForNonce(id)]), recs[id].ConfirmedAtHeight,
			"slot %d stamps its own tip while F is still empty", hostIdxForNonce(id))
	}

	// One host claim is the whole raise rule. Slot 1's confirm at nonce 1 seeds
	// F at 998 on its own — no corroborating second signer, no cap on the jump
	// from 0, because the height was already bounded at envelope admission.
	floor, _, known := sm.HeightSyncFloorAsOf(3)
	require.True(t, known)
	require.Equal(t, uint64(divergedOracleHeights[1]), floor,
		"a lone host confirm seeds F at any distance")

	// Nonce 3 is served by slot 3, ~993 blocks under that floor. It **carries**
	// F rather than omitting: this is the branch W_conf used to divert, and
	// diverting it is what silenced a lagging host for the rest of a session.
	require.Equal(t, 3, hostIdxForNonce(3))
	require.Equal(t, floor, recs[3].ConfirmedAtHeight,
		"the most-behind slot carries the floor instead of declining to stamp")
	require.Equal(t, floor, recs[3].StartedAtHeight,
		"once F exists the sequencer stamps exactly F, never its courier tip")

	// Slot 0's confirm at nonce 4 is the last raise: 1000 is the roster's real
	// tip, so F settles there and every later stamp on both legs equals it. The
	// confirm rides a later diff, which is why the floor answers 1000 from nonce
	// 6 rather than 5 — L0 judges each stamp against the floor at its own
	// producing nonce, so the pipelining costs nobody a verdict.
	require.Equal(t, 0, hostIdxForNonce(4))
	top := uint64(divergedOracleHeights[0])
	floor, _, known = sm.HeightSyncFloorAsOf(6)
	require.True(t, known)
	require.Equal(t, top, floor)
	for _, id := range []uint64{6, 7, 8, 9, 10, 11} {
		require.Equalf(t, top, recs[id].StartedAtHeight, "inference %d start", id)
		require.Equalf(t, top, recs[id].ConfirmedAtHeight,
			"inference %d: slot %d stamps the settled floor", id, hostIdxForNonce(id))
	}

	// Lag is not blame. A host hundreds of blocks behind never authored a false
	// claim, so nothing may be attributed to it; the only marks divergence may
	// produce here are L5a admission records, which phase F settles with a
	// Strong proof rather than by refusing the exchange.
	//
	// The L4 assertion is the sharper one. Each ack carries max(anchor, F(m))
	// while the anchor beside it carries the host's raw tip, so the two differ by
	// design on every lagging host — and a receiver that had kept the old strict
	// equality would name all of them originators of a dispute.
	for i, srv := range st.Servers {
		ml := srv.HeightSyncMarks()
		if ml == nil {
			continue
		}
		require.Falsef(t, ml.HasKind(heightsync.MarkDisputeOriginator),
			"host %d must not be named originator of a dispute it did not cause", i)
		require.Falsef(t, ml.HasKind(heightsync.MarkDisputeCarrier),
			"host %d must not see a carrier contradiction on an honest exchange", i)
		require.Falsef(t, ml.HasKind(heightsync.MarkHeightUnbacked),
			"host %d must not see an unbacked stamp: the sequencer wrote F or nothing", i)
	}

	require.Equal(t, types.PhaseActive, snap.Phase,
		"a roster ~1000 blocks apart must leave the escrow serving")
	require.Len(t, snap.Inferences, nonces)

	// Every inference has settled except the last, whose finish leg still rides
	// the pending diff. Completion is the assertion that matters: a stamp the
	// verifier refused would leave its inference stuck mid-flight instead.
	var finished int
	for _, rec := range snap.Inferences {
		if rec.Status == types.StatusFinished {
			finished++
		}
	}
	require.Equal(t, nonces-1, finished,
		"an executor's height gap must not strand an inference short of finish")
}
