package app

import (
	"bytes"
	"errors"
	"testing"

	"cosmossdk.io/log"
	abci "github.com/cometbft/cometbft/abci/types"
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

// fakeBeaconState is committed beacon state for the builder.
type fakeBeaconState struct {
	activeFrom int64 // heights > activeFrom are active; -1 = never
	pending    map[uint64]blstypes.PendingBeacon
}

func (f *fakeBeaconState) BeaconExtensionsActive(_ sdk.Context, h int64) bool {
	return f.activeFrom >= 0 && h > f.activeFrom
}

func (f *fakeBeaconState) GetPendingBeacon(_ sdk.Context, r uint64) (blstypes.PendingBeacon, bool) {
	p, ok := f.pending[r]
	return p, ok
}

// fakeShares serves one epoch's shares, or panics.
type fakeShares struct {
	shares *beacon.EpochShares
	panic  bool
}

func (f *fakeShares) Get(_ sdk.Context, epoch uint64) (*beacon.EpochShares, bool) {
	if f.panic {
		panic("provider exploded")
	}
	if f.shares == nil || f.shares.EpochID != epoch {
		return nil, false
	}
	return f.shares, true
}

// extFixture: slots [start,end] of a random key, with public keys.
func extFixture(t *testing.T, epoch uint64, start, end uint32) (*beacon.EpochShares, [][]byte) {
	t.Helper()
	s := &beacon.EpochShares{EpochID: epoch, SlotStart: start, SlotEnd: end}
	var pks [][]byte
	for i := start; i <= end; i++ {
		var e fr.Element
		_, err := e.SetRandom()
		require.NoError(t, err)
		be := e.Bytes()
		var le [32]byte
		for j := range be {
			le[j] = be[31-j]
		}
		s.Scalars = append(s.Scalars, le)
		pks = append(pks, blst.P2Generator().Mult(le[:], 255).ToAffine().Compress())
	}
	return s, pks
}

func pendingFor(t *testing.T, round, epoch uint64, totalSlots uint32) blstypes.PendingBeacon {
	t.Helper()
	msg, err := blsutil.BeaconMessage("gonka-test", epoch, round, bytes.Repeat([]byte{9}, 32))
	require.NoError(t, err)
	return blstypes.PendingBeacon{Round: round, KeyEpoch: epoch, Msg: msg[:], ITotalSlots: totalSlots}
}

func emptyCount(reason string) float64 {
	return promtest.ToFloat64(beacon.ExtendVoteEmpty.WithLabelValues(reason))
}

func TestBeaconExtensionBuilderSignsPendingMessage(t *testing.T) {
	shares, pks := extFixture(t, 4, 10, 15)
	state := &fakeBeaconState{activeFrom: 0, pending: map[uint64]blstypes.PendingBeacon{50: pendingFor(t, 50, 4, 100)}}
	h := newBeaconExtendVoteHandler(log.NewNopLogger(), beaconExtensionBuilder(log.NewNopLogger(), state,
		func() beacon.ShareProvider { return &fakeShares{shares: shares} }))

	signedBefore := promtest.ToFloat64(beacon.ExtendVoteSigned)
	resp, err := h(sdk.Context{}, &abci.RequestExtendVote{Height: 50})
	require.NoError(t, err)
	require.NotEmpty(t, resp.VoteExtension)
	require.Equal(t, signedBefore+1, promtest.ToFloat64(beacon.ExtendVoteSigned))
	require.Equal(t, float64(6), promtest.ToFloat64(beacon.ExtendVoteSlots))

	var ve blstypes.BeaconVoteExtension
	require.NoError(t, ve.Unmarshal(resp.VoteExtension))
	require.Equal(t, uint32(blskeeper.BeaconExtensionVersion), ve.Version)
	require.Equal(t, uint64(50), ve.Round)
	require.Equal(t, uint64(4), ve.KeyEpoch)
	require.Equal(t, []uint32{10, 11, 12, 13, 14, 15}, ve.SlotIndices)
	require.Len(t, ve.Signatures, 6*blsutil.SignatureLen)
	msg := state.pending[50].Msg
	for i := range ve.SlotIndices {
		require.True(t, blsutil.VerifyPartial(msg, ve.Signatures[i*48:(i+1)*48], pks[i]), "slot %d", ve.SlotIndices[i])
	}
	require.LessOrEqual(t, len(resp.VoteExtension), beaconExtensionMaxBytes(100))

	// Deterministic: a re-run produces the same bytes.
	again, err := h(sdk.Context{}, &abci.RequestExtendVote{Height: 50})
	require.NoError(t, err)
	require.Equal(t, resp.VoteExtension, again.VoteExtension)
}

func TestBeaconExtensionBuilderEmptyCases(t *testing.T) {
	shares, _ := extFixture(t, 4, 0, 3)
	pend := map[uint64]blstypes.PendingBeacon{50: pendingFor(t, 50, 4, 100)}
	run := func(state *fakeBeaconState, provider beacon.ShareProvider, height int64) []byte {
		t.Helper()
		h := newBeaconExtendVoteHandler(log.NewNopLogger(), beaconExtensionBuilder(log.NewNopLogger(), state,
			func() beacon.ShareProvider { return provider }))
		resp, err := h(sdk.Context{}, &abci.RequestExtendVote{Height: height})
		require.NoError(t, err, "ExtendVote never errors (R1)")
		return resp.VoteExtension
	}
	cases := []struct {
		name     string
		state    *fakeBeaconState
		provider beacon.ShareProvider
		height   int64
		reason   string
	}{
		{"before activation", &fakeBeaconState{activeFrom: -1, pending: pend}, &fakeShares{shares: shares}, 50, beacon.EmptyInactive},
		{"activation height itself", &fakeBeaconState{activeFrom: 50, pending: pend}, &fakeShares{shares: shares}, 50, beacon.EmptyInactive},
		{"no pending message", &fakeBeaconState{activeFrom: 0, pending: pend}, &fakeShares{shares: shares}, 51, beacon.EmptyNoPending},
		{"provider disabled", &fakeBeaconState{activeFrom: 0, pending: pend}, nil, 50, beacon.EmptyNoShares},
		{"no shares for the key epoch", &fakeBeaconState{activeFrom: 0, pending: pend}, &fakeShares{}, 50, beacon.EmptyNoShares},
		{"panicking provider", &fakeBeaconState{activeFrom: 0, pending: pend}, &fakeShares{panic: true}, 50, beacon.EmptyPanic},
		{"oversized for the key epoch", &fakeBeaconState{activeFrom: 0, pending: map[uint64]blstypes.PendingBeacon{50: pendingFor(t, 50, 4, 1)}},
			&fakeShares{shares: func() *beacon.EpochShares { s, _ := extFixture(t, 4, 0, 99); return s }()}, 50, beacon.EmptyOversize},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			before := emptyCount(c.reason)
			var provider beacon.ShareProvider
			if c.provider != nil {
				provider = c.provider
			}
			require.Empty(t, run(c.state, provider, c.height))
			require.Equal(t, before+1, emptyCount(c.reason))
			require.Zero(t, promtest.ToFloat64(beacon.ExtendVoteSlots))
		})
	}
}

// A provider returning shares that do not cover a contiguous range of the key
// epoch never produces an extension (defence in depth; the provider checks this).
func TestBeaconExtensionBuilderRejectsInconsistentShares(t *testing.T) {
	shares, _ := extFixture(t, 4, 0, 3)
	shares.SlotEnd = 9 // 4 scalars for 10 slots
	state := &fakeBeaconState{activeFrom: 0, pending: map[uint64]blstypes.PendingBeacon{50: pendingFor(t, 50, 4, 100)}}
	before := emptyCount(beacon.EmptyNotOwner)
	h := newBeaconExtendVoteHandler(log.NewNopLogger(), beaconExtensionBuilder(log.NewNopLogger(), state,
		func() beacon.ShareProvider { return &fakeShares{shares: shares} }))
	resp, err := h(sdk.Context{}, &abci.RequestExtendVote{Height: 50})
	require.NoError(t, err)
	require.Empty(t, resp.VoteExtension)
	require.Equal(t, before+1, emptyCount(beacon.EmptyNotOwner))
}

func TestBeaconSizeCapCoversTheKeyEpoch(t *testing.T) {
	params := func(n uint32, err error) func(sdk.Context) (blstypes.Params, error) {
		return func(sdk.Context) (blstypes.Params, error) {
			p := blstypes.DefaultParams()
			p.ITotalSlots = n
			return p, err
		}
	}
	state := &fakeBeaconState{pending: map[uint64]blstypes.PendingBeacon{7: {Round: 7, ITotalSlots: 1000}}}
	// The key epoch has more slots than the current parameter: its size wins.
	require.Equal(t, beaconExtensionMaxBytes(1000), beaconSizeCap(params(100, nil), state)(sdk.Context{}, 7))
	// No pending message for the height: the parameter.
	require.Equal(t, beaconExtensionMaxBytes(100), beaconSizeCap(params(100, nil), state)(sdk.Context{}, 8))
	// Fewer slots in the key epoch than the parameter: the parameter.
	state.pending[9] = blstypes.PendingBeacon{Round: 9, ITotalSlots: 10}
	require.Equal(t, beaconExtensionMaxBytes(100), beaconSizeCap(params(100, nil), state)(sdk.Context{}, 9))
	// Params unreadable: the pending message alone, else the default.
	require.Equal(t, beaconExtensionMaxBytes(1000), beaconSizeCap(params(0, errors.New("x")), state)(sdk.Context{}, 7))
	require.Equal(t, beaconExtensionMaxBytes(0), beaconSizeCap(params(0, errors.New("x")), state)(sdk.Context{}, 8))
}

// R2: VerifyVoteExtension never judges content. A real extension, garbage and
// an extension from a validator with no slots are accepted alike; only
// oversized payloads are rejected, and each verdict is counted.
func TestBeaconVerifyVoteExtensionAcceptsAnyContentWithinCap(t *testing.T) {
	shares, _ := extFixture(t, 4, 0, 9)
	state := &fakeBeaconState{activeFrom: 0, pending: map[uint64]blstypes.PendingBeacon{50: pendingFor(t, 50, 4, 100)}}
	ev := newBeaconExtendVoteHandler(log.NewNopLogger(), beaconExtensionBuilder(log.NewNopLogger(), state,
		func() beacon.ShareProvider { return &fakeShares{shares: shares} }))
	real, err := ev(sdk.Context{}, &abci.RequestExtendVote{Height: 50})
	require.NoError(t, err)

	verify := newBeaconVerifyVoteExtensionHandler(log.NewNopLogger(),
		beaconSizeCap(func(sdk.Context) (blstypes.Params, error) { return blstypes.DefaultParams(), nil }, state))
	accepted := func(reason string) float64 {
		return promtest.ToFloat64(beacon.VerifyVoteExt.WithLabelValues("accept", reason))
	}
	within := accepted("within_cap")
	for name, ext := range map[string][]byte{
		"real partials":                    real.VoteExtension,
		"garbage":                          {0xde, 0xad},
		"from a validator with no slots":   real.VoteExtension, // Verify cannot and does not tell
		"wrong round inside a valid proto": mustMarshal(t, &blstypes.BeaconVoteExtension{Version: 1, Round: 999}),
	} {
		resp, err := verify(sdk.Context{}, &abci.RequestVerifyVoteExtension{Height: 50, ValidatorAddress: []byte{1}, VoteExtension: ext})
		require.NoError(t, err, name)
		require.Equal(t, abci.ResponseVerifyVoteExtension_ACCEPT, resp.Status, name)
	}
	require.Equal(t, within+4, accepted("within_cap"))

	rejectedBefore := promtest.ToFloat64(beacon.VerifyVoteExt.WithLabelValues("reject", "oversized"))
	resp, err := verify(sdk.Context{}, &abci.RequestVerifyVoteExtension{Height: 50, VoteExtension: make([]byte, beaconExtensionMaxBytes(100)+1)})
	require.NoError(t, err)
	require.Equal(t, abci.ResponseVerifyVoteExtension_REJECT, resp.Status)
	require.Equal(t, rejectedBefore+1, promtest.ToFloat64(beacon.VerifyVoteExt.WithLabelValues("reject", "oversized")))
}

func mustMarshal(t *testing.T, ve *blstypes.BeaconVoteExtension) []byte {
	t.Helper()
	b, err := ve.Marshal()
	require.NoError(t, err)
	return b
}
