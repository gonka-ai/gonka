package heightsync_test

import (
	"math"
	"testing"

	"github.com/stretchr/testify/require"

	"devshard/heightsync"
)

// claim is one signer's stamp. The signer matters: the raise rule counts
// distinct identities, so tests must say who claimed what.
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
	f.Observe(1, []heightsync.FloorClaim{claim(0, 50, nil)}) // no hash is not a claim (H38)
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

	// The per-signer claims behind the raise rule must be copied too, or a
	// rolled-back trial apply leaves corroboration behind that the committed
	// log never saw.
	cp.Observe(3, []heightsync.FloorClaim{claim(1, 5_000, hash)})
	f.Observe(4, []heightsync.FloorClaim{claim(1, 5_000, hash)})
	h, _, _ = f.AsOf(5)
	require.Equal(t, uint64(10), h,
		"one signer's jump is uncorroborated here: the clone's second claim did not leak")
}

// TestFloorIndex_LoneImplausibleClaimDoesNotMoveTheFloor is step 2's core case.
//
// One participant stamping an absurd height used to set the escrow's logical
// time to it permanently: the floor was a running maximum over any signer, so a
// single claim of 1<<40 put the bar past anything an honest oracle would ever
// report. Nothing halted after step 1 — carriers lift, omission stays legal —
// but every derived quantity became nonsense and L6 could not refute a height
// no chain will reach, so no verifier could ever settle it.
func TestFloorIndex_LoneImplausibleClaimDoesNotMoveTheFloor(t *testing.T) {
	f := heightsync.NewFloorIndex()
	good, poison := []byte{0xaa}, []byte{0xbb}
	f.Observe(1, []heightsync.FloorClaim{claim(0, 100, good)})

	marks := f.Observe(2, []heightsync.FloorClaim{claim(1, math.MaxUint64/2, poison)})

	h, hash, known := f.AsOf(3)
	require.True(t, known)
	require.Equal(t, uint64(100), h, "an uncorroborated claim does not become the escrow's logical time")
	require.Equal(t, good, hash)

	require.Len(t, marks, 1, "the attempt is evidence, not a silent clamp")
	require.Equal(t, heightsync.MarkFloorOutOfBand, marks[0].Kind)
	require.Equal(t, uint32(1), marks[0].Slot, "attributed at the moment of the damage")
	require.Equal(t, uint64(2), marks[0].Nonce)

	// Liveness is the other half: the escrow keeps advancing normally.
	require.Empty(t, f.Observe(3, []heightsync.FloorClaim{claim(0, 101, good)}))
	h, _, _ = f.AsOf(4)
	require.Equal(t, uint64(101), h, "the next honest diff is accepted and still moves the floor")
}

// TestFloorIndex_UnaidedRaiseStopsAtWConf pins the shape of the bound. Ordinary
// advance is unaffected — a cadence of one turnover every Interval keeps honest
// steps orders of magnitude inside the window — so a host ack at the live tip
// still establishes the turn's reference height. Sequencer heartbeats do not.
func TestFloorIndex_UnaidedRaiseStopsAtWConf(t *testing.T) {
	hash := []byte{0xaa}
	w := heightsync.DefaultConfirmWindowBlocks

	exact := heightsync.NewFloorIndex()
	exact.Observe(1, []heightsync.FloorClaim{claim(0, 100, hash)})
	exact.Observe(2, []heightsync.FloorClaim{claim(0, 100+w, hash)})
	h, _, _ := exact.AsOf(3)
	require.Equal(t, 100+w, h, "the window itself is reachable unaided")

	over := heightsync.NewFloorIndex()
	over.Observe(1, []heightsync.FloorClaim{claim(0, 100, hash)})
	over.Observe(2, []heightsync.FloorClaim{claim(0, 100+w+1, hash)})
	h, _, _ = over.AsOf(3)
	require.Equal(t, uint64(100), h, "one block past it needs someone else to agree")
}

// TestFloorIndex_QuorumAdmitsTheJumpOneSignerCannot keeps the bound from
// becoming a liveness problem. A roster whose chain really did jump — an oracle
// recovering from a stall, or an escrow bootstrapping on mainnet heights — moves
// the floor as soon as its members agree, because the rule asks for
// corroboration rather than for small steps.
func TestFloorIndex_QuorumAdmitsTheJumpOneSignerCannot(t *testing.T) {
	f := heightsync.NewFloorIndex()
	hash := []byte{0xaa}
	f.Observe(1, []heightsync.FloorClaim{claim(0, 100, hash)})

	f.Observe(2, []heightsync.FloorClaim{claim(0, 8_000_000, hash)})
	h, _, _ := f.AsOf(3)
	require.Equal(t, uint64(100), h, "one signer alone cannot")

	f.Observe(3, []heightsync.FloorClaim{claim(1, 8_000_000, hash)})
	h, _, _ = f.AsOf(4)
	require.Equal(t, uint64(8_000_000), h, "two distinct signers holding it can")
}

// TestFloorIndex_CarriesCannotCorroborate closes the laundering route. Lifting
// to the floor is what the producer rule demands of a lagging party, so carries
// are everywhere; if they counted as agreement, one signer could raise the floor
// unaided and then have the whole roster ratify it by obeying the rule.
func TestFloorIndex_CarriesCannotCorroborate(t *testing.T) {
	f := heightsync.NewFloorIndex()
	good, poison := []byte{0xaa}, []byte{0xbb}
	f.Observe(1, []heightsync.FloorClaim{claim(0, 100, good)})
	f.Observe(2, []heightsync.FloorClaim{claim(1, 9_000_000, poison)})

	// Slots 2 and 3 do exactly what L0 asks: they carry F(m) = 100.
	f.Observe(3, []heightsync.FloorClaim{claim(2, 100, good), claim(3, 100, good)})

	h, _, _ := f.AsOf(4)
	require.Equal(t, uint64(100), h, "obeying the producer rule must never ratify a poisoned height")
}

// TestFloorIndex_BootstrapSeedsFromCorroborationNotFromTheFirstStamp records the
// one behaviour change the bound imposes on an honest escrow: on real mainnet
// heights the first stamp is thousands of blocks above an empty floor, so it no
// longer seeds F on its own. Sequencer heartbeats never seed it (rule 3). The
// floor arrives with host acks — two host signers at the live height, which is
// Q for NewFloorIndex — and no honest party is marked for it.
func TestFloorIndex_BootstrapSeedsFromCorroborationNotFromTheFirstStamp(t *testing.T) {
	f := heightsync.NewFloorIndex()
	hash := []byte{0xaa}

	marks := f.Observe(1, []heightsync.FloorClaim{
		claim(heightsync.SequencerSigner, 8_000_000, hash),
	})
	h, _, _ := f.AsOf(2)
	require.Zero(t, h, "an escrow with no logical time yet has no floor to defend")
	require.Empty(t, marks, "the sequencer's first honest heartbeat is not an anomaly")

	marks = f.Observe(2, []heightsync.FloorClaim{claim(0, 7_999_998, hash)})
	h, _, _ = f.AsOf(3)
	require.Zero(t, h, "one host past W_conf of an empty floor cannot jump; sequencer does not fill Q")
	require.Empty(t, marks)

	marks = f.Observe(3, []heightsync.FloorClaim{claim(1, 7_999_998, hash)})
	h, _, _ = f.AsOf(4)
	require.Equal(t, uint64(7_999_998), h,
		"the floor is the height the host roster vouches for, so it seeds at the corroborated one")
	require.Empty(t, marks)
}

// TestRefStamp_CoversEveryDiffResidentHeight pins the single-semantics rule: the
// log has one kind of height, so every message that can carry one is a reference
// stamp. Heartbeats and acks were once excluded as first-party own-tip claims;
// those readings now live in the envelope instead (spec §14).
func TestRefStamp_CoversEveryDiffResidentHeight(t *testing.T) {
	hash := []byte{0xaa}

	h, gotHash, ok := heightsync.RefStamp(hbTx(1, 50, 3, hash, nil))
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

	// Absence is keyed on the hash, never on a zero height (H38).
	_, _, ok = heightsync.RefStamp(ackTxAt(1, 4, 50, nil))
	require.False(t, ok)
}

func TestRefProducingNonce_PerMessageBasis(t *testing.T) {
	hash := []byte{0xaa}

	// Sequencer-composed legs are produced at the nonce they land at.
	m, ok := heightsync.RefProducingNonce(9, startTxAt(9, 50, hash))
	require.True(t, ok)
	require.Equal(t, uint64(9), m)

	m, ok = heightsync.RefProducingNonce(9, hbTx(9, 50, 3, hash, nil))
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

// TestFloorIndex_SequencerStampsNeverRaise is H89: MsgHeartbeat / MsgStartInference
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
	require.Equal(t, uint64(180), h, "a host ack at that H does raise (unaided, inside W_conf)")
}

// TestFloorIndex_SequencerDoesNotFillQuorum is H90: sequencer + one host cannot
// jump past W_conf. Q is host-only. A future/unmined envelope is rule 1 (L6),
// not an Observe input — matching stamp and envelope still compose; the oracle
// refuses the pair later.
func TestFloorIndex_SequencerDoesNotFillQuorum(t *testing.T) {
	hash := []byte{0xaa}
	f := heightsync.NewFloorIndex()
	f.Observe(1, []heightsync.FloorClaim{claim(0, 100, hash)})

	f.Observe(2, []heightsync.FloorClaim{
		claim(heightsync.SequencerSigner, 8_000_000, hash),
		claim(0, 8_000_000, hash),
	})
	h, _, _ := f.AsOf(3)
	require.Equal(t, uint64(100), h, "sequencer + one host is not Q")

	f.Observe(3, []heightsync.FloorClaim{claim(1, 8_000_000, hash)})
	h, _, _ = f.AsOf(4)
	require.Equal(t, uint64(8_000_000), h, "two host signers holding it can jump")
}

func TestFloorConfigFor_HostOnlyQuorumClampedToRoster(t *testing.T) {
	cfg := heightsync.DefaultHeartbeatConfig()
	one := heightsync.FloorConfigFor(1, cfg)
	require.Equal(t, 1, one.Quorum, "a one-slot escrow must be able to seed F")

	three := heightsync.FloorConfigFor(3, cfg)
	require.Equal(t, 2, three.Quorum)

	unset := heightsync.NewFloorIndex()
	unset.Observe(1, []heightsync.FloorClaim{claim(0, 100, []byte{0xaa})})
	unset.Observe(2, []heightsync.FloorClaim{claim(0, 8_000_000, []byte{0xaa})})
	h, _, _ := unset.AsOf(3)
	require.Equal(t, uint64(100), h, "NewFloorIndex still defaults Q to 2")
}
