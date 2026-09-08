package heightsync_test

import (
	"math"
	"testing"

	"github.com/stretchr/testify/require"

	"devshard/heightsync"
)

// claim is one signer's stamp. The signer matters: only host-signed claims raise
// the floor, and the entry keeps the identity for L6 attribution, so tests must
// say who claimed what.
func claim(signer uint32, height uint64, hash []byte) heightsync.FloorClaim {
	return heightsync.FloorClaim{Signer: signer, Height: height, Hash: hash}
}

func TestFloorIndex_AsOfExcludesTheQueriedNonce(t *testing.T) {
	f := heightsync.NewFloorIndex()
	hash := []byte{0xaa}
	f.Observe(5, []heightsync.FloorClaim{claim(0, 100, hash)})

	h, _, known := f.AsOf(5)
	require.True(t, known)
	require.Zero(t, h, "F(m) is the floor *below* m, so a stamp is never judged against itself")

	h, gotHash, known := f.AsOf(6)
	require.True(t, known)
	require.Equal(t, uint64(100), h)
	require.Equal(t, hash, gotHash, "the floor carries the hash of the block that set it")
}

func TestFloorIndex_RunningMaxIsMonotoneInNonce(t *testing.T) {
	f := heightsync.NewFloorIndex()
	hash := []byte{0xaa}
	f.Observe(1, []heightsync.FloorClaim{claim(0, 10, hash)})
	f.Observe(2, []heightsync.FloorClaim{claim(0, 30, hash)})
	f.Observe(3, []heightsync.FloorClaim{claim(0, 20, hash)}) // below the running max: no effect
	f.Observe(4, []heightsync.FloorClaim{claim(0, 40, hash)})

	for _, tc := range []struct{ query, want uint64 }{
		{1, 0}, {2, 10}, {3, 30}, {4, 30}, {5, 40}, {99, 40},
	} {
		h, _, known := f.AsOf(tc.query)
		require.True(t, known)
		require.Equalf(t, tc.want, h, "F(%d)", tc.query)
	}
	require.Equal(t, 3, f.Len(), "a height below the running max adds no entry")
}

func TestFloorIndex_IgnoresAbsentStamps(t *testing.T) {
	f := heightsync.NewFloorIndex()
	f.Observe(1, []heightsync.FloorClaim{claim(0, 50, nil)}) // no hash is not a claim (spec §14)
	f.Observe(2, []heightsync.FloorClaim{claim(0, 0, []byte{0xaa})})

	h, _, known := f.AsOf(10)
	require.True(t, known)
	require.Zero(t, h)
	require.Zero(t, f.Len())
}

func TestFloorIndex_PrunedRangeIsUnknownNotHigher(t *testing.T) {
	// The window is bounded, so an old enough nonce falls off the front. The
	// answer there must be "unknown" so callers skip the check: substituting the
	// oldest retained floor would reject honest stamps that predate it.
	f := heightsync.NewFloorIndex()
	hash := []byte{0xaa}
	for i := uint64(1); i <= heightsync.DefaultFloorWindow+10; i++ {
		f.Observe(i, []heightsync.FloorClaim{claim(0, i*10, hash)})
	}
	require.Equal(t, heightsync.DefaultFloorWindow, f.Len(), "the index stays bounded")

	_, _, known := f.AsOf(2)
	require.False(t, known, "a pruned range is unknowable, not a higher floor")

	h, _, known := f.AsOf(heightsync.DefaultFloorWindow + 11)
	require.True(t, known)
	require.Equal(t, uint64((heightsync.DefaultFloorWindow+10)*10), h)
}

func TestFloorIndex_CloneIsIndependent(t *testing.T) {
	f := heightsync.NewFloorIndex()
	hash := []byte{0xaa}
	f.Observe(1, []heightsync.FloorClaim{claim(0, 10, hash)})

	cp := f.Clone()
	cp.Observe(2, []heightsync.FloorClaim{claim(0, 99, hash)})

	h, _, _ := f.AsOf(3)
	require.Equal(t, uint64(10), h, "trial-apply must not leak into committed state")
	h, _, _ = cp.AsOf(3)
	require.Equal(t, uint64(99), h)
}

// TestFloorIndex_HostClaimRaisesAtAnyDistance pins the raise rule after W_conf
// and Q were withdrawn: a host-signed claim above the standing floor becomes the
// escrow's logical time however far above it is.
//
// This is deliberate, not a regression. Bounding the raise made the floor the
// defence against a fabricated height, which it cannot be: the floor only sees
// heights that are already in the log. They get there through an exchange whose
// envelope was admitted, and admission is where implausibility is answered —
// past |Δ| > D the sender owes Strong proof (§8/§15), and L5a marks the band
// until Strong lands. A height nobody can prove is therefore attributable at its
// origin, which is what the bound was trying and failing to do.
func TestFloorIndex_HostClaimRaisesAtAnyDistance(t *testing.T) {
	f := heightsync.NewFloorIndex()
	good, far := []byte{0xaa}, []byte{0xbb}
	f.Observe(1, []heightsync.FloorClaim{claim(0, 100, good)})

	f.Observe(2, []heightsync.FloorClaim{claim(1, math.MaxUint64/2, far)})

	h, hash, known := f.AsOf(3)
	require.True(t, known)
	require.Equal(t, uint64(math.MaxUint64/2), h)
	require.Equal(t, far, hash)

	p, known := f.PointAsOf(3)
	require.True(t, known)
	require.Equal(t, uint32(1), p.Author, "the entry names the claimant, so L6 blames it and not the carriers")
	require.Equal(t, uint64(2), p.Nonce)
}

// TestFloorIndex_PoisonedLowFloorRecovers is the case the withdrawn bound made
// unrecoverable, and the reason it went.
//
// The first participant to stamp a fresh escrow claims H=1 — a real height, an
// honest hash, just ancient. F becomes 1. Every honest host is at the live tip,
// which is far more than W_conf above 1, so under the old rule not one of them
// could raise the floor, nothing lowers it, and the escrow's logical time was
// pinned at 1 for the rest of the session by a single message.
func TestFloorIndex_PoisonedLowFloorRecovers(t *testing.T) {
	f := heightsync.NewFloorIndex()
	old, live := []byte{0xaa}, []byte{0xbb}

	f.Observe(1, []heightsync.FloorClaim{claim(0, 1, old)})
	h, _, _ := f.AsOf(2)
	require.Equal(t, uint64(1), h, "the first host-signed claim seeds F, however stale")

	f.Observe(2, []heightsync.FloorClaim{claim(1, 10_000, live)})
	h, hash, _ := f.AsOf(3)
	require.Equal(t, uint64(10_000), h, "an honest host at the real tip repairs the escrow's clock alone")
	require.Equal(t, live, hash)
}

// TestFloorIndex_CarryOfTheFloorAddsNoEntry: lifting to F(m) is what the producer
// rule asks of a lagging party, so carries are everywhere. They are not raises —
// a carry equals the standing floor — so they leave the index untouched and the
// entry keeps naming whoever originated the height (L6).
func TestFloorIndex_CarryOfTheFloorAddsNoEntry(t *testing.T) {
	f := heightsync.NewFloorIndex()
	hash := []byte{0xaa}
	f.Observe(1, []heightsync.FloorClaim{claim(0, 100, hash)})

	f.Observe(2, []heightsync.FloorClaim{claim(2, 100, hash), claim(3, 100, hash)})

	require.Equal(t, 1, f.Len())
	p, _ := f.PointAsOf(3)
	require.Equal(t, uint32(0), p.Author, "the carriers did not take over authorship of the height")
}

// TestFloorIndex_BootstrapSeedsFromTheFirstHostStamp: on real mainnet heights the
// first stamp is millions of blocks above an empty floor and seeds F on its own.
// A sequencer heartbeat never does (rule 3), which is why an escrow's first
// logical time always arrives from a host.
func TestFloorIndex_BootstrapSeedsFromTheFirstHostStamp(t *testing.T) {
	f := heightsync.NewFloorIndex()
	hash := []byte{0xaa}

	f.Observe(1, []heightsync.FloorClaim{
		claim(heightsync.SequencerSigner, 8_000_000, hash),
	})
	h, _, _ := f.AsOf(2)
	require.Zero(t, h, "a user stamp is not a height source, so it cannot seed the escrow's clock")

	f.Observe(2, []heightsync.FloorClaim{claim(0, 7_999_998, hash)})
	h, _, _ = f.AsOf(3)
	require.Equal(t, uint64(7_999_998), h, "the first host-signed stamp seeds F")
}

// TestRefStamp_CoversEveryDiffResidentHeight pins the single-semantics rule: the
// log has one kind of height, so every message that can carry one is a reference
// stamp. Heartbeats and acks were once excluded as first-party own-tip claims;
// those readings now live in the envelope instead (spec §14).
func TestRefStamp_CoversEveryDiffResidentHeight(t *testing.T) {
	hash := []byte{0xaa}

	h, gotHash, ok := heightsync.RefStamp(hbTx(50, 3, hash, nil))
	require.True(t, ok, "a heartbeat height is a reference height")
	require.Equal(t, uint64(50), h)
	require.Equal(t, hash, gotHash)

	h, _, ok = heightsync.RefStamp(ackTxAt(1, 4, 50, hash))
	require.True(t, ok, "an ack height is a reference height")
	require.Equal(t, uint64(50), h)

	h, gotHash, ok = heightsync.RefStamp(confirmTxAt(7, 50, hash))
	require.True(t, ok)
	require.Equal(t, uint64(50), h)
	require.Equal(t, hash, gotHash)

	// Absence is keyed on the hash, never on a zero height (spec §14).
	_, _, ok = heightsync.RefStamp(ackTxAt(1, 4, 50, nil))
	require.False(t, ok)
}

func TestRefProducingNonce_PerMessageBasis(t *testing.T) {
	hash := []byte{0xaa}

	// Sequencer-composed legs are produced at the nonce they land at.
	m, ok := heightsync.RefProducingNonce(9, startTxAt(9, 50, hash))
	require.True(t, ok)
	require.Equal(t, uint64(9), m)

	m, ok = heightsync.RefProducingNonce(9, hbTx(50, 3, hash, nil))
	require.True(t, ok)
	require.Equal(t, uint64(9), m)

	// A confirm or finish is produced when its inference was prepared, which is
	// what inference_id records, so the basis needs no extra wire field.
	m, ok = heightsync.RefProducingNonce(9, confirmTxAt(3, 50, hash))
	require.True(t, ok)
	require.Equal(t, uint64(3), m)

	m, ok = heightsync.RefProducingNonce(9, finishTxAt(3, 50, hash))
	require.True(t, ok)
	require.Equal(t, uint64(3), m)

	// An ack's producer had applied through ref_nonce inclusive — it is answering
	// the heartbeat there — so the basis is one past it, folding in that
	// heartbeat's own stamp. Landing later, after the floor has moved, costs an
	// honest host nothing.
	m, ok = heightsync.RefProducingNonce(9, ackTxAt(3, 4, 50, hash))
	require.True(t, ok)
	require.Equal(t, uint64(4), m)
}

// TestFloorIndex_SequencerStampsNeverRaise: MsgHeartbeat / MsgStartInference
// are user-signed. Observe ignores them as raises on apply and on a second fold
// that stands in for gossip (same claims, no envelope).
func TestFloorIndex_SequencerStampsNeverRaise(t *testing.T) {
	hash := []byte{0xaa}
	f := heightsync.NewFloorIndex()
	f.Observe(1, []heightsync.FloorClaim{claim(0, 100, hash)})

	f.Observe(2, []heightsync.FloorClaim{
		claim(heightsync.SequencerSigner, 180, hash),
	})
	h, _, _ := f.AsOf(3)
	require.Equal(t, uint64(100), h, "a heartbeat above F does not raise")

	gossip := f.Clone()
	gossip.Observe(2, []heightsync.FloorClaim{
		claim(heightsync.SequencerSigner, 180, hash),
	})
	gh, _, _ := gossip.AsOf(3)
	require.Equal(t, h, gh, "gossip of the same Diff yields the same F")

	f.Observe(3, []heightsync.FloorClaim{claim(1, 180, hash)})
	h, _, _ = f.AsOf(4)
	require.Equal(t, uint64(180), h, "a host ack at that H does raise")
}
