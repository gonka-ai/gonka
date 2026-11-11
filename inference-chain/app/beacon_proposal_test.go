package app

import (
	"bytes"
	"errors"
	"testing"
	"time"

	"cosmossdk.io/log"
	abci "github.com/cometbft/cometbft/abci/types"
	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	cmttypes "github.com/cometbft/cometbft/types"
	"github.com/consensys/gnark-crypto/ecc/bls12-381/fr"
	sdk "github.com/cosmos/cosmos-sdk/types"
	promtest "github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
	blst "github.com/supranational/blst/bindings/go"

	"github.com/productscience/inference/app/beacon"
	"github.com/productscience/inference/x/bls/blsutil"
	blskeeper "github.com/productscience/inference/x/bls/keeper"
	blstypes "github.com/productscience/inference/x/bls/types"
)

// --- fixture -------------------------------------------------------------------

const (
	propRound    = 100 // the round whose σ block 101 must carry
	propKeyEpoch = 4
)

var (
	valA = bytes.Repeat([]byte{0xA1}, 20) // slots 0-3
	valB = bytes.Repeat([]byte{0xB2}, 20) // slots 4-7
	valC = bytes.Repeat([]byte{0xC3}, 20) // slots 8-9
	valX = bytes.Repeat([]byte{0xEE}, 20) // no slots
)

// propChain is a fake x/bls: a 10-slot key, degree 4 (threshold 5).
type propChain struct {
	scalars  [][32]byte
	slotPKs  [][]byte
	groupPK  []byte
	pending  blstypes.PendingBeacon
	required map[int64]bool
	owners   map[string][2]uint32
	panicOn  bool
}

func newPropChain(t *testing.T) *propChain {
	t.Helper()
	const n, degree = 10, 4
	coeffs := make([]fr.Element, degree+1)
	for i := range coeffs {
		_, err := coeffs[i].SetRandom()
		require.NoError(t, err)
	}
	le := func(e fr.Element) [32]byte {
		be := e.Bytes()
		var out [32]byte
		for i := range be {
			out[i] = be[31-i]
		}
		return out
	}
	pk := func(s [32]byte) []byte { return blst.P2Generator().Mult(s[:], 255).ToAffine().Compress() }
	c := &propChain{groupPK: pk(le(coeffs[0])), required: map[int64]bool{propRound + 1: true},
		owners: map[string][2]uint32{string(valA): {0, 3}, string(valB): {4, 7}, string(valC): {8, 9}}}
	for s := 0; s < n; s++ {
		var x, acc fr.Element
		x.SetUint64(uint64(s) + 1)
		for i := degree; i >= 0; i-- {
			acc.Mul(&acc, &x)
			acc.Add(&acc, &coeffs[i])
		}
		c.scalars = append(c.scalars, le(acc))
		c.slotPKs = append(c.slotPKs, pk(le(acc)))
	}
	msg, err := blsutil.BeaconMessage("gonka-test", propKeyEpoch, propRound, bytes.Repeat([]byte{5}, 32))
	require.NoError(t, err)
	c.pending = blstypes.PendingBeacon{Round: propRound, KeyEpoch: propKeyEpoch, Msg: msg[:], ITotalSlots: n}
	return c
}

func (c *propChain) BeaconInjectionRound(_ sdk.Context, h int64) (blstypes.PendingBeacon, bool) {
	if c.panicOn {
		panic("chain exploded")
	}
	if c.required[h] {
		return c.pending, true
	}
	return blstypes.PendingBeacon{}, false
}

func (c *propChain) GetPendingBeacon(_ sdk.Context, r uint64) (blstypes.PendingBeacon, bool) {
	return c.pending, r == c.pending.Round
}

func (c *propChain) ValidateBeaconExtensionStructure(_ sdk.Context, ext []byte, round uint64, cons []byte) (*blstypes.BeaconVoteExtension, error) {
	var ve blstypes.BeaconVoteExtension
	if err := ve.Unmarshal(ext); err != nil {
		return nil, err
	}
	if ve.Round != round || ve.KeyEpoch != c.pending.KeyEpoch {
		return nil, blskeeper.ErrBeaconExtensionRound
	}
	r, ok := c.owners[string(cons)]
	if !ok {
		return nil, blskeeper.ErrBeaconNoOwner
	}
	if !blskeeper.IsSlotRange(ve.SlotIndices, r[0], r[1]) || len(ve.Signatures) != 48*len(ve.SlotIndices) {
		return nil, blskeeper.ErrBeaconExtensionSlots
	}
	return &ve, nil
}

func (c *propChain) GetBeaconKeyMaterial(_ sdk.Context, e uint64) (blskeeper.BeaconKeyMaterial, error) {
	if e != propKeyEpoch {
		return blskeeper.BeaconKeyMaterial{}, errors.New("no such epoch")
	}
	return blskeeper.BeaconKeyMaterial{GroupPublicKey: c.groupPK, SlotPublicKeys: c.slotPKs, Threshold: 5}, nil
}

// ext is validator v's honest extension for the pending round.
func (c *propChain) ext(t *testing.T, v []byte) []byte {
	t.Helper()
	r := c.owners[string(v)]
	sigs, err := blsutil.SignPartials(c.pending.Msg, c.scalars[r[0]:r[1]+1])
	require.NoError(t, err)
	slots := make([]uint32, 0)
	for s := r[0]; s <= r[1]; s++ {
		slots = append(slots, s)
	}
	b, err := (&blstypes.BeaconVoteExtension{Version: 1, Round: propRound, KeyEpoch: propKeyEpoch, SlotIndices: slots, Signatures: sigs}).Marshal()
	require.NoError(t, err)
	return b
}

// expectedSigma is σ for the pending round (unique: any threshold subset).
func (c *propChain) expectedSigma(t *testing.T) []byte {
	t.Helper()
	sigs, err := blsutil.SignPartials(c.pending.Msg, c.scalars[:5])
	require.NoError(t, err)
	per := make([][]byte, 5)
	for i := range per {
		per[i] = sigs[i*48 : (i+1)*48]
	}
	s, err := blsutil.AggregatePartials([]uint32{0, 1, 2, 3, 4}, per)
	require.NoError(t, err)
	return s
}

type vote struct {
	addr  []byte
	power int64
	ext   []byte
	flag  cmtproto.BlockIDFlag
}

// commits returns the extended commit (as the proposer gets it) and the
// matching LastCommit (as validators see the proposal).
func commits(votes ...vote) (abci.ExtendedCommitInfo, abci.CommitInfo) {
	ec := abci.ExtendedCommitInfo{Round: 0}
	lc := abci.CommitInfo{Round: 0}
	for _, v := range votes {
		flag := v.flag
		if flag == 0 {
			flag = cmtproto.BlockIDFlagCommit
		}
		val := abci.Validator{Address: v.addr, Power: v.power}
		ec.Votes = append(ec.Votes, abci.ExtendedVoteInfo{Validator: val, VoteExtension: v.ext, ExtensionSignature: []byte{1}, BlockIdFlag: flag})
		lc.Votes = append(lc.Votes, abci.VoteInfo{Validator: val, BlockIdFlag: flag})
	}
	return ec, lc
}

func ctxStub(sdk.Context, abci.ExtendedCommitInfo) error { return nil }

func passThroughInner(_ sdk.Context, req *abci.RequestPrepareProposal) (*abci.ResponsePrepareProposal, error) {
	var out [][]byte
	var size int64
	for _, tx := range req.Txs {
		s := cmttypes.ComputeProtoSizeForTxs([]cmttypes.Tx{tx})
		if size+s > req.MaxTxBytes {
			break
		}
		size += s
		out = append(out, tx)
	}
	return &abci.ResponsePrepareProposal{Txs: out}, nil
}

func decodeOutcome(t *testing.T, tx []byte) *blstypes.InjectedBeacon {
	t.Helper()
	ib, err := blstypes.DecodeBeaconTx(tx)
	require.NoError(t, err)
	return ib
}

// --- collector -------------------------------------------------------------------

func newTestCollector(c *propChain) *beaconCollector {
	return newBeaconCollector(log.NewNopLogger(), c, func(int64) (sdk.Context, error) { return sdk.Context{}, nil })
}

func TestCollectorBuildsSigmaFromPartials(t *testing.T) {
	c := newPropChain(t)
	col := newTestCollector(c)
	col.start(t.Context())
	t.Cleanup(col.close)
	count := func(r string) float64 { return promtest.ToFloat64(beacon.CollectorPartials.WithLabelValues(r)) }
	invalid, notOwner, valid := count(partialInvalid), count(partialNotOwner), count(partialValid)

	col.Add(propRound, valX, c.ext(t, valA))     // not an owner
	col.Add(propRound, valC, []byte{0xde, 0xad}) // garbage
	col.Add(propRound, valA, c.ext(t, valA))     // 4 slots
	col.Add(propRound, valA, c.ext(t, valA))     // duplicate
	col.Add(propRound, valC, nil)                // empty: ignored by Add
	col.Add(propRound, valB, c.ext(t, valB))     // 8 slots ≥ 5
	require.Eventually(t, func() bool { _, _, ok := col.Sigma(propRound, propKeyEpoch); return ok }, 5*time.Second, 5*time.Millisecond)

	sigma, hint, _ := col.Sigma(propRound, propKeyEpoch)
	require.Equal(t, c.expectedSigma(t), sigma, "σ is unique")
	require.True(t, blsutil.VerifySignature(c.pending.Msg, sigma, c.groupPK))
	require.Equal(t, []byte{0xff, 0x00}, hint, "slots 0-7 contributed")
	_, _, ok := col.Sigma(propRound, propKeyEpoch+1)
	require.False(t, ok, "another key epoch")
	_, _, ok = col.Sigma(propRound+1, propKeyEpoch)
	require.False(t, ok)
	require.Equal(t, notOwner+1, count(partialNotOwner))
	require.Equal(t, invalid+1, count(partialInvalid))
	require.Equal(t, valid+2, count(partialValid))
}

func TestCollectorDropsWhenFullAndIgnoresUnknownRounds(t *testing.T) {
	c := newPropChain(t)
	col := newTestCollector(c) // worker not started: the queue fills up
	dropped := promtest.ToFloat64(beacon.CollectorPartials.WithLabelValues("dropped"))
	start := time.Now()
	for i := 0; i < collectorQueueSize+10; i++ {
		col.Add(propRound, valA, []byte{1})
	}
	require.Less(t, time.Since(start), time.Second, "Add never blocks")
	require.Equal(t, dropped+10, promtest.ToFloat64(beacon.CollectorPartials.WithLabelValues("dropped")))

	col2 := newTestCollector(c)
	col2.process(collectItem{height: propRound + 7, addr: valA, ext: c.ext(t, valA)})
	_, _, ok := col2.Sigma(propRound+7, propKeyEpoch)
	require.False(t, ok, "no pending beacon for that round")

	var nilCol *beaconCollector
	nilCol.Add(1, valA, []byte{1}) // no-op
	_, _, ok = nilCol.Sigma(1, 1)
	require.False(t, ok)
}

// --- PrepareProposal -----------------------------------------------------------------

func prepare(t *testing.T, c *propChain, col *beaconCollector, req *abci.RequestPrepareProposal) [][]byte {
	t.Helper()
	h := newBeaconPrepareProposalHandler(log.NewNopLogger(), c, col, passThroughInner)
	resp, err := h(sdk.Context{}, req)
	require.NoError(t, err)
	return resp.Txs
}

func TestPrepareProposalSigmaFromCommit(t *testing.T) {
	c := newPropChain(t)
	ec, _ := commits(vote{valA, 10, c.ext(t, valA), 0}, vote{valB, 10, c.ext(t, valB), 0}, vote{valC, 5, nil, 0})
	mempool := [][]byte{[]byte("tx1"), []byte("tx2")}
	txs := prepare(t, c, nil, &abci.RequestPrepareProposal{Height: propRound + 1, MaxTxBytes: 1 << 20, Txs: mempool, LocalLastCommit: ec})
	require.Len(t, txs, 3)
	s := decodeOutcome(t, txs[0]).GetSigma()
	require.NotNil(t, s)
	require.Equal(t, uint64(propRound), s.Round)
	require.Equal(t, uint64(propKeyEpoch), s.KeyEpoch)
	require.Equal(t, c.expectedSigma(t), s.Sigma)
	require.Equal(t, mempool, txs[1:])
}

func TestPrepareProposalSigmaFromCollector(t *testing.T) {
	c := newPropChain(t)
	col := newTestCollector(c)
	col.process(collectItem{height: propRound, addr: valA, ext: c.ext(t, valA)})
	col.process(collectItem{height: propRound, addr: valB, ext: c.ext(t, valB)})
	// The LocalLastCommit alone is below threshold: the collector's σ (built from
	// partials it received, e.g. late precommits) is used anyway.
	ec, _ := commits(vote{valA, 10, c.ext(t, valA), 0}, vote{valB, 10, nil, 0})
	txs := prepare(t, c, col, &abci.RequestPrepareProposal{Height: propRound + 1, MaxTxBytes: 1 << 20, LocalLastCommit: ec})
	require.Equal(t, c.expectedSigma(t), decodeOutcome(t, txs[0]).GetSigma().Sigma)
}

func TestPrepareProposalProofOfMiss(t *testing.T) {
	c := newPropChain(t)
	ec, _ := commits(vote{valA, 10, c.ext(t, valA), 0}, vote{valB, 10, nil, 0}, vote{valC, 5, []byte{1, 2, 3}, 0})
	before := promtest.ToFloat64(beacon.ProposalOutcome.WithLabelValues("miss"))
	txs := prepare(t, c, nil, &abci.RequestPrepareProposal{Height: propRound + 1, MaxTxBytes: 1 << 20, LocalLastCommit: ec})
	m := decodeOutcome(t, txs[0]).GetMiss()
	require.NotNil(t, m)
	require.Equal(t, uint64(propRound), m.Round)
	want, err := ec.Marshal()
	require.NoError(t, err)
	require.Equal(t, want, m.ExtendedCommit, "the LocalLastCommit, unmodified")
	require.Equal(t, before+1, promtest.ToFloat64(beacon.ProposalOutcome.WithLabelValues("miss")))
}

func TestPrepareProposalReservedTxBudget(t *testing.T) {
	c := newPropChain(t)
	ec, _ := commits(vote{valA, 10, c.ext(t, valA), 0}, vote{valB, 10, c.ext(t, valB), 0})
	base := prepare(t, c, nil, &abci.RequestPrepareProposal{Height: propRound + 1, MaxTxBytes: 1 << 20, LocalLastCommit: ec})
	beaconSize := cmttypes.ComputeProtoSizeForTxs([]cmttypes.Tx{base[0]})
	tx := bytes.Repeat([]byte{7}, 100)
	txSize := cmttypes.ComputeProtoSizeForTxs([]cmttypes.Tx{tx})

	// Room for the beacon tx and exactly one mempool tx.
	txs := prepare(t, c, nil, &abci.RequestPrepareProposal{Height: propRound + 1, MaxTxBytes: beaconSize + txSize + txSize/2,
		Txs: [][]byte{tx, tx, tx}, LocalLastCommit: ec})
	require.Len(t, txs, 2, "mempool txs are trimmed, not the beacon tx")
	require.True(t, blstypes.IsBeaconTx(txs[0]))

	// Smaller than the beacon tx: it is proposed alone.
	txs = prepare(t, c, nil, &abci.RequestPrepareProposal{Height: propRound + 1, MaxTxBytes: beaconSize - 1, Txs: [][]byte{tx}, LocalLastCommit: ec})
	require.Len(t, txs, 1)
	require.True(t, blstypes.IsBeaconTx(txs[0]))

	// A pass-through error keeps [beaconTx], never raw req.Txs.
	failing := func(sdk.Context, *abci.RequestPrepareProposal) (*abci.ResponsePrepareProposal, error) {
		return nil, errors.New("decode failure")
	}
	h := newBeaconPrepareProposalHandler(log.NewNopLogger(), c, nil, failing)
	resp, err := h(sdk.Context{}, &abci.RequestPrepareProposal{Height: propRound + 1, MaxTxBytes: 1 << 20, Txs: [][]byte{tx}, LocalLastCommit: ec})
	require.NoError(t, err)
	require.Len(t, resp.Txs, 1)
	require.True(t, blstypes.IsBeaconTx(resp.Txs[0]))

	// A mempool tx carrying the beacon prefix never reaches the block.
	spoof := append(append([]byte(nil), blstypes.BeaconTxPrefix...), 1, 2, 3)
	txs = prepare(t, c, nil, &abci.RequestPrepareProposal{Height: propRound + 1, MaxTxBytes: 1 << 20, Txs: [][]byte{spoof, tx}, LocalLastCommit: ec})
	require.Len(t, txs, 2)
	require.Equal(t, tx, txs[1])
}

func TestPrepareProposalWhenNotRequired(t *testing.T) {
	c := newPropChain(t)
	tx := []byte("tx")
	spoof := append(append([]byte(nil), blstypes.BeaconTxPrefix...), 9)
	txs := prepare(t, c, nil, &abci.RequestPrepareProposal{Height: propRound + 5, MaxTxBytes: 1 << 20, Txs: [][]byte{spoof, tx}})
	require.Equal(t, [][]byte{tx}, txs, "no beacon tx, spoofed prefix filtered")

	// A panicking chain degrades to the plain pass-through (R1).
	c.panicOn = true
	txs = prepare(t, c, nil, &abci.RequestPrepareProposal{Height: propRound + 1, MaxTxBytes: 1 << 20, Txs: [][]byte{tx}})
	require.Equal(t, [][]byte{tx}, txs)
}

// --- ProcessProposal -----------------------------------------------------------------

func process(c *propChain, validate voteExtValidator, txs [][]byte, lc abci.CommitInfo, height int64) abci.ResponseProcessProposal_ProposalStatus {
	h := newBeaconProcessProposalHandler(log.NewNopLogger(), c, validate)
	resp, err := h(sdk.Context{}, &abci.RequestProcessProposal{Height: height, Txs: txs, ProposedLastCommit: lc})
	if err != nil {
		panic(err)
	}
	return resp.Status
}

func sigmaTx(t *testing.T, round, epoch uint64, sigma []byte) []byte {
	t.Helper()
	tx, err := blstypes.EncodeBeaconTx(&blstypes.InjectedBeacon{Outcome: &blstypes.InjectedBeacon_Sigma{Sigma: &blstypes.BeaconSigma{Round: round, KeyEpoch: epoch, Sigma: sigma}}})
	require.NoError(t, err)
	return tx
}

func missTx(t *testing.T, round, epoch uint64, ec abci.ExtendedCommitInfo) []byte {
	t.Helper()
	b, err := ec.Marshal()
	require.NoError(t, err)
	tx, err := blstypes.EncodeBeaconTx(&blstypes.InjectedBeacon{Outcome: &blstypes.InjectedBeacon_Miss{Miss: &blstypes.BeaconMissProof{Round: round, KeyEpoch: epoch, ExtendedCommit: b}}})
	require.NoError(t, err)
	return tx
}

func TestProcessProposalAccepts(t *testing.T) {
	c := newPropChain(t)
	const ACCEPT = abci.ResponseProcessProposal_ACCEPT
	full, fullLC := commits(vote{valA, 10, c.ext(t, valA), 0}, vote{valB, 10, c.ext(t, valB), 0})
	short, shortLC := commits(vote{valA, 10, c.ext(t, valA), 0}, vote{valB, 10, nil, 0}, vote{valC, 5, []byte{0xba, 0xd0}, 0})
	garbage := []byte{0xff, 0x00, 0x13}

	require.Equal(t, ACCEPT, process(c, ctxStub, [][]byte{sigmaTx(t, propRound, propKeyEpoch, c.expectedSigma(t)), garbage}, fullLC, propRound+1),
		"valid σ; Txs[1:] are never decoded")
	require.Equal(t, ACCEPT, process(c, ctxStub, [][]byte{missTx(t, propRound, propKeyEpoch, short)}, shortLC, propRound+1),
		"proof of miss below threshold, with a garbage extension counted as zero (R2)")
	require.Equal(t, ACCEPT, process(c, ctxStub, [][]byte{garbage}, abci.CommitInfo{}, propRound+5), "not required, no beacon tx")
	require.Equal(t, ACCEPT, process(c, ctxStub, nil, abci.CommitInfo{}, propRound+5), "empty block, not required")

	// What PrepareProposal builds is what ProcessProposal accepts.
	for _, ec := range []abci.ExtendedCommitInfo{full, short} {
		lc := abci.CommitInfo{Round: ec.Round}
		for _, v := range ec.Votes {
			lc.Votes = append(lc.Votes, abci.VoteInfo{Validator: v.Validator, BlockIdFlag: v.BlockIdFlag})
		}
		txs := prepare(t, c, nil, &abci.RequestPrepareProposal{Height: propRound + 1, MaxTxBytes: 1 << 20, LocalLastCommit: ec})
		require.Equal(t, ACCEPT, process(c, ctxStub, txs, lc, propRound+1))
	}
}

func TestProcessProposalRejects(t *testing.T) {
	c := newPropChain(t)
	const REJECT = abci.ResponseProcessProposal_REJECT
	full, fullLC := commits(vote{valA, 10, c.ext(t, valA), 0}, vote{valB, 10, c.ext(t, valB), 0})
	short, shortLC := commits(vote{valA, 10, c.ext(t, valA), 0}, vote{valB, 10, nil, 0})
	sigma := c.expectedSigma(t)
	otherMsg, err := blsutil.SignPartials(bytes.Repeat([]byte{1}, 32), c.scalars[:1])
	require.NoError(t, err)
	reason := func(r string) float64 {
		return promtest.ToFloat64(beacon.ProcessProposal.WithLabelValues("reject", r))
	}

	cases := []struct {
		name     string
		txs      [][]byte
		lc       abci.CommitInfo
		height   int64
		validate voteExtValidator
		reason   string
	}{
		{"no beacon tx when required", [][]byte{[]byte("tx")}, fullLC, propRound + 1, ctxStub, "missing"},
		{"empty block when required", nil, fullLC, propRound + 1, ctxStub, "missing"},
		{"σ over another message", [][]byte{sigmaTx(t, propRound, propKeyEpoch, otherMsg)}, fullLC, propRound + 1, ctxStub, "bad_sigma"},
		{"σ bytes garbage", [][]byte{sigmaTx(t, propRound, propKeyEpoch, []byte{1, 2})}, fullLC, propRound + 1, ctxStub, "bad_sigma"},
		{"σ for another round", [][]byte{sigmaTx(t, propRound-1, propKeyEpoch, sigma)}, fullLC, propRound + 1, ctxStub, "round_or_epoch"},
		{"σ with another key epoch", [][]byte{sigmaTx(t, propRound, propKeyEpoch+1, sigma)}, fullLC, propRound + 1, ctxStub, "round_or_epoch"},
		{"proof of miss with enough slots", [][]byte{missTx(t, propRound, propKeyEpoch, full)}, fullLC, propRound + 1, ctxStub, "miss_disproved"},
		{"proof of miss with a vote dropped", [][]byte{missTx(t, propRound, propKeyEpoch, abci.ExtendedCommitInfo{Votes: short.Votes[:1]})}, shortLC, propRound + 1, ctxStub, "commit_mismatch"},
		{"proof of miss with a flag changed", [][]byte{missTx(t, propRound, propKeyEpoch, func() abci.ExtendedCommitInfo {
			ec := short
			ec.Votes = append([]abci.ExtendedVoteInfo(nil), short.Votes...)
			ec.Votes[1].BlockIdFlag = cmtproto.BlockIDFlagAbsent
			return ec
		}())}, shortLC, propRound + 1, ctxStub, "commit_mismatch"},
		{"proof of miss with a power changed", [][]byte{missTx(t, propRound, propKeyEpoch, func() abci.ExtendedCommitInfo {
			ec := short
			ec.Votes = append([]abci.ExtendedVoteInfo(nil), short.Votes...)
			ec.Votes[0].Validator.Power = 99
			return ec
		}())}, shortLC, propRound + 1, ctxStub, "commit_mismatch"},
		{"proof of miss with a stripped/forged extension", [][]byte{missTx(t, propRound, propKeyEpoch, short)}, shortLC, propRound + 1,
			func(sdk.Context, abci.ExtendedCommitInfo) error {
				return errors.New("failed to verify validator vote extension signature")
			}, "ext_signatures"},
		{"proof of miss for another round", [][]byte{missTx(t, propRound+1, propKeyEpoch, short)}, shortLC, propRound + 1, ctxStub, "round_or_epoch"},
		{"proof of miss that doesn't decode", [][]byte{func() []byte {
			tx, _ := blstypes.EncodeBeaconTx(&blstypes.InjectedBeacon{Outcome: &blstypes.InjectedBeacon_Miss{Miss: &blstypes.BeaconMissProof{Round: propRound, KeyEpoch: propKeyEpoch, ExtendedCommit: []byte{0xff, 0xff}}}})
			return tx
		}()}, shortLC, propRound + 1, ctxStub, "decode"},
		{"truncated beacon tx", [][]byte{append(append([]byte(nil), blstypes.BeaconTxPrefix...), 0xff, 0xff)}, fullLC, propRound + 1, ctxStub, "decode"},
		{"beacon tx when not required", [][]byte{sigmaTx(t, propRound, propKeyEpoch, sigma)}, fullLC, propRound + 5, ctxStub, "not_required"},
		{"prefix on another tx", [][]byte{sigmaTx(t, propRound, propKeyEpoch, sigma), sigmaTx(t, propRound, propKeyEpoch, sigma)}, fullLC, propRound + 1, ctxStub, "prefix_elsewhere"},
		{"beacon tx not first", [][]byte{[]byte("tx"), sigmaTx(t, propRound, propKeyEpoch, sigma)}, fullLC, propRound + 1, ctxStub, "prefix_elsewhere"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before := reason(tc.reason)
			require.Equal(t, REJECT, process(c, tc.validate, tc.txs, tc.lc, tc.height))
			require.Equal(t, before+1, reason(tc.reason))
		})
	}

	// A panic rejects, never an error (R1).
	c.panicOn = true
	require.Equal(t, REJECT, process(c, ctxStub, [][]byte{[]byte("tx")}, fullLC, propRound+1))
}

// TEST-ONLY withholding switch: no beacon tx even when required, and the
// other validators reject such a proposal (the next proposer takes over).
func TestPrepareProposalWithholdSwitch(t *testing.T) {
	c := newPropChain(t)
	ec, lc := commits(vote{valA, 10, c.ext(t, valA), 0}, vote{valB, 10, c.ext(t, valB), 0})
	tx := []byte("tx")
	before := promtest.ToFloat64(beacon.ProposalOutcome.WithLabelValues("withheld"))
	h := newBeaconPrepareProposalHandler(log.NewNopLogger(), c, nil, passThroughInner, true)
	resp, err := h(sdk.Context{}, &abci.RequestPrepareProposal{Height: propRound + 1, MaxTxBytes: 1 << 20, Txs: [][]byte{tx}, LocalLastCommit: ec})
	require.NoError(t, err)
	require.Equal(t, [][]byte{tx}, resp.Txs, "σ was available but withheld")
	require.Equal(t, before+1, promtest.ToFloat64(beacon.ProposalOutcome.WithLabelValues("withheld")))
	require.Equal(t, abci.ResponseProcessProposal_REJECT, process(c, ctxStub, resp.Txs, lc, propRound+1))

	// Not required: the switch changes nothing.
	resp, err = h(sdk.Context{}, &abci.RequestPrepareProposal{Height: propRound + 5, MaxTxBytes: 1 << 20, Txs: [][]byte{tx}})
	require.NoError(t, err)
	require.Equal(t, [][]byte{tx}, resp.Txs)
	require.Equal(t, abci.ResponseProcessProposal_ACCEPT, process(c, ctxStub, resp.Txs, abci.CommitInfo{}, propRound+5))
}
