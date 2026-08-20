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
// The spread straddles W_conf deliberately, so both branches of the producer
// rule run in one scenario: slot 2 is ~100 blocks under the floor and carries
// it, while slot 3 is ~900 under and declines to.
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
	// is what (C-quorum) counts and what the gateway's collectors aggregate into
	// a divergence surface. The log deliberately holds one shared reference
	// height instead, so the audit ring is the only plane where the spread is
	// visible at all.
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

	// All three branches of the producer rule, read off the applied records.
	//
	// Slot 2 is inside W_conf of the floor, so its confirms carry it rather than
	// its own tip: stamping the raw tip is what the verifier refuses, and this is
	// the assertion that the shipping executor picks the satisfiable height.
	//
	// Slot 3 is ~900 blocks under the floor, past any plausible chain advance, so
	// it takes the other branch and omits. That is the escape that stops a
	// poisoned or dead-branch height being repeated under every honest signature
	// — and the inference still completes, which is the point: omission costs the
	// roster one height claim, never a served request.
	//
	// Nonce 1 is the bootstrap: the floor is still empty, because a mainnet-scale
	// height needs corroboration before it becomes the escrow's logical time, so
	// the executor stamps its own tip and the cross-signer gap below the start is
	// accepted rather than blamed.
	recs := snap.Inferences
	top := uint64(divergedOracleHeights[0])

	// Bootstrap, inferences 1–3: a mainnet-scale height needs corroboration
	// before it becomes the escrow's logical time, so F is empty here and every
	// executor stamps its own follower. The whole spread lands in the log, one
	// stamp per signer, and nothing is rejected — which is also how the floor
	// then seeds, at the height the roster vouches for rather than at the highest
	// number anyone claimed.
	require.Equal(t, top, recs[1].StartedAtHeight)
	for i, id := range []uint64{1, 2, 3} {
		require.Equalf(t, uint64(divergedOracleHeights[i+1]), recs[id].ConfirmedAtHeight,
			"inference %d predates the floor, so slot %d stamps its own tip", id, i+1)
	}
	floor, _, known := sm.HeightSyncFloorAsOf(4)
	require.True(t, known)
	require.Equal(t, uint64(divergedOracleHeights[2]), floor,
		"three distinct signers seed the floor at the corroborated height, not at the maximum")

	// Past the bootstrap, the two branches of the producer rule split by
	// distance. Slot 2 is ~100 blocks under the floor and carries it: stamping
	// the raw tip is what the verifier refuses, so this is the assertion that the
	// shipping executor picks the satisfiable height.
	require.Equal(t, 2, hostIdxForNonce(6))
	require.Equal(t, top, recs[6].ConfirmedAtHeight,
		"within W_conf of the floor, an executor carries it rather than its own tip")

	// Slot 3 is ~1000 blocks under, past any plausible chain advance, so it takes
	// the other branch and omits. That escape is what stops a poisoned or
	// dead-branch height being repeated under every honest signature — and the
	// inference still completes, which is the point: omission costs the roster
	// one height claim, never a served request.
	for _, id := range []uint64{7, 11} {
		require.Equalf(t, 3, hostIdxForNonce(id), "nonce %d must be served by the most-behind slot", id)
		require.Zerof(t, recs[id].ConfirmedAtHeight,
			"slot 3 sits at %d, out of reach of the floor: it omits instead of carrying",
			divergedOracleHeights[3])
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
