package keeper

import (
	"bytes"
	"context"
	"encoding/hex"
	"testing"

	"cosmossdk.io/core/header"
	"cosmossdk.io/math"
	abci "github.com/cometbft/cometbft/abci/types"
	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	"github.com/consensys/gnark-crypto/ecc/bls12-381/fr"
	sdk "github.com/cosmos/cosmos-sdk/types"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"
	"github.com/stretchr/testify/require"
	blst "github.com/supranational/blst/bindings/go"

	"github.com/productscience/inference/x/bls/blsutil"
	"github.com/productscience/inference/x/bls/types"
)

const beaconTestChain = "gonka-beacon-test"

// --- fixtures ---------------------------------------------------------------

// beaconDKG is a degree-t polynomial over totalSlots slots: secrets[s] is
// slot s's key (f(s+1)), slotPKs[s] its compressed G2 public key.
type beaconDKG struct {
	secrets []fr.Element
	slotPKs [][]byte
	groupPK []byte
	degree  uint32
}

func g2Of(e fr.Element) []byte {
	le := leOf(e)
	return blst.P2Generator().Mult(le[:], 255).ToAffine().Compress()
}

func leOf(e fr.Element) [32]byte {
	be := e.Bytes()
	var le [32]byte
	for i := range be {
		le[i] = be[31-i]
	}
	return le
}

func newBeaconDKG(t *testing.T, totalSlots int, degree uint32) *beaconDKG {
	t.Helper()
	coeffs := make([]fr.Element, degree+1)
	for i := range coeffs {
		_, err := coeffs[i].SetRandom()
		require.NoError(t, err)
	}
	d := &beaconDKG{groupPK: g2Of(coeffs[0]), degree: degree}
	for s := 0; s < totalSlots; s++ {
		var x, acc fr.Element
		x.SetUint64(uint64(s) + 1)
		for i := len(coeffs) - 1; i >= 0; i-- {
			acc.Mul(&acc, &x)
			acc.Add(&acc, &coeffs[i])
		}
		d.secrets = append(d.secrets, acc)
		d.slotPKs = append(d.slotPKs, g2Of(acc))
	}
	return d
}

func (d *beaconDKG) scalars(start, end uint32) [][32]byte {
	out := make([][32]byte, 0, end-start+1)
	for s := start; s <= end; s++ {
		out = append(out, leOf(d.secrets[s]))
	}
	return out
}

// accounts are participant accounts whose bytes double as validator operator bytes.
func testAccount(i byte) sdk.AccAddress { return sdk.AccAddress(bytes.Repeat([]byte{i}, 20)) }
func testCons(i byte) sdk.ConsAddress   { return sdk.ConsAddress(bytes.Repeat([]byte{0xC0 | i}, 20)) }

// completedEpoch stores a COMPLETED epoch whose participants own the given ranges.
func completedEpoch(t *testing.T, k Keeper, ctx sdk.Context, epoch uint64, d *beaconDKG, ranges [][2]uint32) types.EpochBLSData {
	t.Helper()
	parts := make([]types.BLSParticipantInfo, len(ranges))
	for i, r := range ranges {
		parts[i] = types.BLSParticipantInfo{
			Address: testAccount(byte(i + 1)).String(), SlotStartIndex: r[0], SlotEndIndex: r[1],
			PercentageWeight: math.LegacyNewDecWithPrec(1, 1),
		}
	}
	data := types.EpochBLSData{
		EpochId: epoch, ITotalSlots: uint32(len(d.secrets)), TSlotsDegree: d.degree,
		DkgPhase: types.DKGPhase_DKG_PHASE_COMPLETED, GroupPublicKey: d.groupPK, SlotPublicKeys: d.slotPKs,
		Participants: parts,
	}
	require.NoError(t, k.SetEpochBLSDataBaseOnly(ctx, data))
	return data
}

// fakeStaking resolves consensus addresses and operators from maps.
type fakeStaking struct {
	byOper map[string]stakingtypes.Validator // operator bech32 -> validator
	byCons map[string]string                 // cons addr bytes -> operator bech32
	order  []string                          // bonded operators, by power
}

func newFakeStaking() *fakeStaking {
	return &fakeStaking{byOper: map[string]stakingtypes.Validator{}, byCons: map[string]string{}}
}

// add registers validator i (operator = testAccount(i)) with power and consensus address cons.
func (f *fakeStaking) add(i byte, power int64, cons sdk.ConsAddress) {
	oper := sdk.ValAddress(testAccount(i)).String()
	f.byOper[oper] = stakingtypes.Validator{
		OperatorAddress: oper,
		Status:          stakingtypes.Bonded,
		Tokens:          sdk.TokensFromConsensusPower(power, sdk.DefaultPowerReduction),
	}
	f.byCons[string(cons)] = oper
	f.order = append(f.order, oper)
}

func (f *fakeStaking) GetValidator(_ context.Context, addr sdk.ValAddress) (stakingtypes.Validator, error) {
	if v, ok := f.byOper[addr.String()]; ok {
		return v, nil
	}
	return stakingtypes.Validator{}, stakingtypes.ErrNoValidatorFound
}

func (f *fakeStaking) GetValidatorByConsAddr(_ context.Context, c sdk.ConsAddress) (stakingtypes.Validator, error) {
	if oper, ok := f.byCons[string(c)]; ok {
		return f.byOper[oper], nil
	}
	return stakingtypes.Validator{}, stakingtypes.ErrNoValidatorFound
}

func (f *fakeStaking) GetBondedValidatorsByPower(context.Context) ([]stakingtypes.Validator, error) {
	out := make([]stakingtypes.Validator, 0, len(f.order))
	for _, o := range f.order {
		out = append(out, f.byOper[o])
	}
	return out, nil
}

func (f *fakeStaking) PowerReduction(context.Context) math.Int { return sdk.DefaultPowerReduction }

// beaconKeeper returns a keeper with staking wired, at height h with VE enabled from enableHeight.
func beaconKeeper(t *testing.T, staking *fakeStaking) (Keeper, sdk.Context) {
	t.Helper()
	k, ctx := setupTimingKeeper(t)
	if staking != nil {
		k.SetStakingKeeper(staking)
	}
	ctx = ctx.WithChainID(beaconTestChain)
	return k, ctx
}

// atBlock returns ctx for EndBlock(h): height, header hash and VE enable height.
func atBlock(ctx sdk.Context, h int64, enableHeight int64) sdk.Context {
	hash := bytes.Repeat([]byte{byte(h)}, 32)
	return ctx.WithBlockHeight(h).
		WithHeaderInfo(header.Info{Height: h, Hash: hash, ChainID: beaconTestChain}).
		WithConsensusParams(cmtproto.ConsensusParams{Abci: &cmtproto.ABCIParams{VoteExtensionsEnableHeight: enableHeight}}).
		WithEventManager(sdk.NewEventManager())
}

func hasEvent(ctx sdk.Context, typ string) bool {
	for _, e := range ctx.EventManager().Events() {
		if e.Type == typ {
			return true
		}
	}
	return false
}

// --- activation ---------------------------------------------------------------

func TestActivateBeaconIsIdempotentAndSeedsNewestCompleted(t *testing.T) {
	k, ctx := beaconKeeper(t, nil)
	d := newBeaconDKG(t, 8, 2)
	completedEpoch(t, k, ctx, 3, d, [][2]uint32{{0, 7}})
	completedEpoch(t, k, ctx, 4, d, [][2]uint32{{0, 7}})
	running := completedEpoch(t, k, ctx, 5, d, [][2]uint32{{0, 7}})
	running.DkgPhase = types.DKGPhase_DKG_PHASE_DEALING
	require.NoError(t, k.SetEpochBLSDataBaseOnly(ctx, running))

	_, ok := k.GetBeaconActivation(ctx)
	require.False(t, ok)
	require.False(t, k.BeaconExtensionsActive(ctx, 100), "inactive before activation")

	require.True(t, k.ActivateBeacon(ctx, 100, []uint64{5, 4, 3, 9}))
	a, ok := k.GetBeaconActivation(ctx)
	require.True(t, ok)
	require.Equal(t, uint64(100), a.ExtFrom)
	key, ok := k.GetBeaconKeyEpoch(ctx)
	require.True(t, ok)
	require.Equal(t, uint64(4), key, "newest COMPLETED/SIGNED, not the running DKG")

	require.False(t, k.BeaconExtensionsActive(ctx, 100), "U itself is precommitted by the old binary")
	require.True(t, k.BeaconExtensionsActive(ctx, 101))

	// Second run: unchanged.
	require.False(t, k.ActivateBeacon(ctx, 200, []uint64{3}))
	a, _ = k.GetBeaconActivation(ctx)
	require.Equal(t, uint64(100), a.ExtFrom)
	key, _ = k.GetBeaconKeyEpoch(ctx)
	require.Equal(t, uint64(4), key)
}

func TestActivateBeaconWithoutCompletedEpoch(t *testing.T) {
	k, ctx := beaconKeeper(t, nil)
	require.True(t, k.ActivateBeacon(ctx, 0, []uint64{1, 2}))
	_, ok := k.GetBeaconKeyEpoch(ctx)
	require.False(t, ok, "no DKG yet: no key")
	require.True(t, k.BeaconExtensionsActive(ctx, 1), "ext_from 0: active from the first block")
}

// --- key switch (R9) ------------------------------------------------------------

func TestDKGCompletionIsIgnoredBeforeActivation(t *testing.T) {
	k, ctx := beaconKeeper(t, nil)
	k.onDKGCompleted(atBlock(ctx, 50, 2), 7)
	_, ok := k.GetBeaconKeyCandidate(ctx)
	require.False(t, ok, "pre-upgrade replay must not write beacon state")
}

func TestBeaconKeySwitchesAfterDelay(t *testing.T) {
	staking := newFakeStaking()
	staking.add(1, 10, testCons(1))
	k, ctx := beaconKeeper(t, staking)
	d1, d2 := newBeaconDKG(t, 8, 2), newBeaconDKG(t, 8, 2)
	completedEpoch(t, k, ctx, 1, d1, [][2]uint32{{0, 7}})
	completedEpoch(t, k, ctx, 2, d2, [][2]uint32{{0, 7}})
	require.True(t, k.ActivateBeacon(ctx, 0, []uint64{1}))

	const enable = 2
	const X = 20 // DKG 2 completes in EndBlock(X)
	delay := types.DefaultBeaconKeySwitchDelay

	keys := map[uint64]uint64{} // round -> key epoch, read right after EndBlock(round-1)
	keyOf := func(round uint64) uint64 {
		e, ok := keys[round]
		require.True(t, ok, "pending beacon for round %d", round)
		return e
	}
	switched := false
	for h := int64(10); h <= X+int64(delay)+3; h++ {
		c := atBlock(ctx, h, enable)
		if h == X {
			k.onDKGCompleted(c, 2) // inside the same EndBlock, before ProcessBlockBeacon
			cand, ok := k.GetBeaconKeyCandidate(c)
			require.True(t, ok)
			require.Equal(t, uint64(X)+1+delay, cand.FromRound)
		}
		k.ProcessBlockBeacon(c)
		p, ok := k.GetPendingBeacon(ctx, uint64(h)+1)
		require.True(t, ok, "round %d has a pending message (no gap at the switch)", h+1)
		keys[uint64(h)+1] = p.KeyEpoch
		if hasEvent(c, "inference.bls.EventBeaconKeySwitched") {
			require.False(t, switched)
			switched = true
			require.Equal(t, uint64(X)+delay, uint64(h), "switched in EndBlock(X+delay), effective from round X+1+delay")
		}
	}
	require.True(t, switched)
	for r := uint64(11); r <= X+delay; r++ {
		require.Equal(t, uint64(1), keyOf(r), "round %d keeps the old key", r)
	}
	for r := X + 1 + delay; r <= X+delay+4; r++ {
		require.Equal(t, uint64(2), keyOf(r), "round %d uses the new key", r)
	}
	_, ok := k.GetBeaconKeyCandidate(ctx)
	require.False(t, ok, "candidate consumed")

	// Slot owners moved to the new key epoch; the old epoch's index is gone.
	_, ok = k.GetBeaconSlotOwner(ctx, 2, testCons(1))
	require.True(t, ok)
	_, ok = k.GetBeaconSlotOwner(ctx, 1, testCons(1))
	require.False(t, ok)
}

func TestBeaconKeySwitchWithZeroDelay(t *testing.T) {
	k, ctx := beaconKeeper(t, nil)
	params, err := k.GetParams(ctx)
	require.NoError(t, err)
	params.BeaconKeySwitchDelay = 0
	require.NoError(t, k.SetParams(ctx, params))
	d := newBeaconDKG(t, 8, 2)
	completedEpoch(t, k, ctx, 1, d, [][2]uint32{{0, 7}})
	completedEpoch(t, k, ctx, 2, d, [][2]uint32{{0, 7}})
	require.True(t, k.ActivateBeacon(ctx, 0, []uint64{1}))

	c := atBlock(ctx, 30, 2)
	k.onDKGCompleted(c, 2)
	k.ProcessBlockBeacon(c)
	p, ok := k.GetPendingBeacon(ctx, 31)
	require.True(t, ok)
	require.Equal(t, uint64(2), p.KeyEpoch, "delay 0: the next round already uses the new key")
}

func TestBeaconKeyNeverMovesBackwards(t *testing.T) {
	k, ctx := beaconKeeper(t, nil)
	d := newBeaconDKG(t, 8, 2)
	for _, e := range []uint64{3, 4, 5} {
		completedEpoch(t, k, ctx, e, d, [][2]uint32{{0, 7}})
	}
	require.True(t, k.ActivateBeacon(ctx, 0, []uint64{4}))
	c := atBlock(ctx, 10, 2)
	k.onDKGCompleted(c, 3) // older than the key
	k.onDKGCompleted(c, 4) // equal to the key
	_, ok := k.GetBeaconKeyCandidate(ctx)
	require.False(t, ok)

	k.onDKGCompleted(c, 5)
	k.onDKGCompleted(c, 3) // late, older than the candidate
	cand, ok := k.GetBeaconKeyCandidate(ctx)
	require.True(t, ok)
	require.Equal(t, uint64(5), cand.Epoch)
}

// A FAILED DKG never becomes a candidate: the real completion path marks it
// failed and returns before the beacon hook. The old key keeps signing.
func TestFailedDKGKeepsTheOldKey(t *testing.T) {
	k, ctx := beaconKeeper(t, nil)
	d := newBeaconDKG(t, 8, 2)
	completedEpoch(t, k, ctx, 1, d, [][2]uint32{{0, 7}})
	require.True(t, k.ActivateBeacon(ctx, 0, []uint64{1}))

	failing := completedEpoch(t, k, ctx, 2, d, [][2]uint32{{0, 3}, {4, 7}})
	failing.DkgPhase = types.DKGPhase_DKG_PHASE_DISPUTING
	failing.GroupPublicKey, failing.SlotPublicKeys = nil, nil
	failing.DealerParts = []*types.DealerPartStorage{{}, {}} // nobody dealt
	failing.VerificationSubmissions = []*types.VerificationVectorSubmission{{}, {}}
	require.NoError(t, k.SetEpochBLSDataBaseOnly(ctx, failing))

	c := atBlock(ctx, 40, 2)
	require.NoError(t, k.CompleteDKG(c, &failing))
	stored, err := k.GetEpochBLSData(ctx, 2)
	require.NoError(t, err)
	require.Equal(t, types.DKGPhase_DKG_PHASE_FAILED, stored.DkgPhase)
	_, ok := k.GetBeaconKeyCandidate(ctx)
	require.False(t, ok)

	k.ProcessBlockBeacon(c)
	p, ok := k.GetPendingBeacon(ctx, 41)
	require.True(t, ok)
	require.Equal(t, uint64(1), p.KeyEpoch)
}

// --- pending message --------------------------------------------------------------

func TestPendingBeaconWrittenOnlyWhenEnabledAndKeyed(t *testing.T) {
	k, ctx := beaconKeeper(t, nil)
	d := newBeaconDKG(t, 12, 3)

	// Not activated: nothing.
	k.ProcessBlockBeacon(atBlock(ctx, 10, 2))
	_, ok := k.GetPendingBeacon(ctx, 11)
	require.False(t, ok)

	require.True(t, k.ActivateBeacon(ctx, 0, nil))
	// Activated, no key yet: nothing (Finalize records MISS_NO_KEY in Step 5).
	k.ProcessBlockBeacon(atBlock(ctx, 11, 2))
	_, ok = k.GetPendingBeacon(ctx, 12)
	require.False(t, ok)

	completedEpoch(t, k, ctx, 7, d, [][2]uint32{{0, 11}})
	k.onDKGCompleted(atBlock(ctx, 12, 2), 7)
	for h := int64(12); h <= 12+int64(types.DefaultBeaconKeySwitchDelay); h++ {
		k.ProcessBlockBeacon(atBlock(ctx, h, 2))
	}
	round := uint64(13 + types.DefaultBeaconKeySwitchDelay)
	_, ok = k.GetPendingBeacon(ctx, round-1)
	require.False(t, ok, "before the key switch there is no key")
	p, ok := k.GetPendingBeacon(ctx, round)
	require.True(t, ok)

	// msg is the blsutil vector for this chain, key epoch, round and hash(round-1).
	want, err := blsutil.BeaconMessage(beaconTestChain, 7, round, bytes.Repeat([]byte{byte(round - 1)}, 32))
	require.NoError(t, err)
	require.Equal(t, want[:], p.Msg)
	require.Equal(t, uint64(7), p.KeyEpoch)
	require.Equal(t, uint32(12), p.ITotalSlots)

	// VE not enabled yet at H+1: nothing for that round.
	k.ProcessBlockBeacon(atBlock(ctx, 40, 50))
	_, ok = k.GetPendingBeacon(ctx, 41)
	require.False(t, ok, "H+1 < VoteExtensionsEnableHeight")
	k.ProcessBlockBeacon(atBlock(ctx, 49, 50))
	_, ok = k.GetPendingBeacon(ctx, 50)
	require.True(t, ok, "H+1 == VoteExtensionsEnableHeight")
	k.ProcessBlockBeacon(atBlock(ctx, 60, 0))
	_, ok = k.GetPendingBeacon(ctx, 61)
	require.False(t, ok, "vote extensions disabled")
}

func TestPendingBeaconSweep(t *testing.T) {
	k, ctx := beaconKeeper(t, nil)
	d := newBeaconDKG(t, 8, 2)
	completedEpoch(t, k, ctx, 1, d, [][2]uint32{{0, 7}})
	require.True(t, k.ActivateBeacon(ctx, 0, []uint64{1}))
	for h := int64(1); h <= 40; h++ {
		k.ProcessBlockBeacon(atBlock(ctx, h, 2))
	}
	var rounds []uint64
	store := k.storeService.OpenKVStore(ctx)
	it, err := store.Iterator(types.BeaconPendingPrefix, types.BeaconPendingKey(1<<62))
	require.NoError(t, err)
	for ; it.Valid(); it.Next() {
		var p types.PendingBeacon
		require.NoError(t, p.Unmarshal(it.Value()))
		rounds = append(rounds, p.Round)
	}
	it.Close()
	require.Len(t, rounds, beaconPendingKeepRounds+1, "bounded: last %d rounds plus the next", beaconPendingKeepRounds)
	require.Equal(t, uint64(41), rounds[len(rounds)-1])
	require.Equal(t, uint64(41-beaconPendingKeepRounds), rounds[0])
}

// --- slot owners and coverage ------------------------------------------------------

func TestBeaconSlotOwnerResolvesThroughStaking(t *testing.T) {
	staking := newFakeStaking()
	staking.add(1, 10, testCons(1))
	staking.add(2, 10, testCons(2))
	k, ctx := beaconKeeper(t, staking)
	d := newBeaconDKG(t, 10, 3)
	completedEpoch(t, k, ctx, 4, d, [][2]uint32{{0, 5}, {6, 9}})
	require.True(t, k.ActivateBeacon(ctx, 0, []uint64{4}))

	o, ok := k.GetBeaconSlotOwner(ctx, 4, testCons(2))
	require.True(t, ok)
	require.Equal(t, uint32(6), o.SlotStart)
	require.Equal(t, uint32(9), o.SlotEnd)
	require.Equal(t, testAccount(2).String(), o.ParticipantAddress)
	require.Equal(t, []byte(testCons(2)), o.ConsAddr, "filled in on read")

	// Participant account bytes == validator operator bytes (epochgroup).
	require.Equal(t, []byte(testAccount(2)), []byte(sdk.ValAddress(testAccount(2))))

	// Consensus key rotation: a new consensus address of the same validator
	// maps to the same slots; the old one no longer resolves.
	staking.byCons[string(testCons(9))] = staking.byCons[string(testCons(2))]
	delete(staking.byCons, string(testCons(2)))
	o, ok = k.GetBeaconSlotOwner(ctx, 4, testCons(9))
	require.True(t, ok)
	require.Equal(t, uint32(6), o.SlotStart)
	_, ok = k.GetBeaconSlotOwner(ctx, 4, testCons(2))
	require.False(t, ok)

	_, ok = k.GetBeaconSlotOwner(ctx, 4, testCons(7))
	require.False(t, ok, "unknown validator")
	_, ok = k.GetBeaconSlotOwner(ctx, 5, testCons(1))
	require.False(t, ok, "not the key epoch")

	kNoStaking, ctx2 := beaconKeeper(t, nil)
	_, ok = kNoStaking.GetBeaconSlotOwner(ctx2, 4, testCons(1))
	require.False(t, ok, "no staking keeper: no resolution")
}

func TestWorstCaseBeaconSlots(t *testing.T) {
	m := func(p int64, s uint32) BeaconCoverageMember { return BeaconCoverageMember{Power: p, Slots: s} }
	// Three equal validators: dropping one removes exactly 1/3 of the power,
	// which would leave no >2/3 majority, so none can be dropped.
	require.Equal(t, uint32(90), WorstCaseBeaconSlots(30, []BeaconCoverageMember{m(10, 30), m(10, 30), m(10, 30)}))
	// Four equal validators: one can be dropped (1/4 < 1/3).
	require.Equal(t, uint32(75), WorstCaseBeaconSlots(40, []BeaconCoverageMember{m(10, 25), m(10, 25), m(10, 25), m(10, 25)}))
	// Skewed: a low-power validator holds most slots and can be dropped.
	require.Equal(t, uint32(20), WorstCaseBeaconSlots(100, []BeaconCoverageMember{m(5, 80), m(50, 10), m(45, 10)}))
	// Greedy drops the most slots per power first, skipping one that would cross 1/3.
	require.Equal(t, uint32(10), WorstCaseBeaconSlots(100, []BeaconCoverageMember{m(30, 60), m(3, 30), m(67, 10)}))
	require.Equal(t, uint32(0), WorstCaseBeaconSlots(0, nil))
}

func TestBeaconSlotCoverageWarning(t *testing.T) {
	// Balanced: 4 validators x 25 slots, threshold 51 → worst case 75: no warning.
	staking := newFakeStaking()
	for i := byte(1); i <= 4; i++ {
		staking.add(i, 10, testCons(i))
	}
	k, ctx := beaconKeeper(t, staking)
	d := newBeaconDKG(t, 100, 50)
	completedEpoch(t, k, ctx, 1, d, [][2]uint32{{0, 24}, {25, 49}, {50, 74}, {75, 99}})
	c := ctx.WithEventManager(sdk.NewEventManager())
	require.True(t, k.ActivateBeacon(c, 0, []uint64{1}))
	require.False(t, hasEvent(c, "inference.bls.EventBeaconSlotCoverageWarning"))

	// Skewed: a validator with 5% of power holds 80% of slots; one participant
	// with slots has no validator at all.
	staking = newFakeStaking()
	staking.add(1, 5, testCons(1))
	staking.add(2, 50, testCons(2))
	staking.add(3, 45, testCons(3))
	k, ctx = beaconKeeper(t, staking)
	completedEpoch(t, k, ctx, 1, d, [][2]uint32{{0, 79}, {80, 89}, {90, 94}, {95, 99}})
	c = ctx.WithEventManager(sdk.NewEventManager())
	require.True(t, k.ActivateBeacon(c, 0, []uint64{1}))
	var warn *types.EventBeaconSlotCoverageWarning
	for _, e := range c.EventManager().Events() {
		if e.Type == "inference.bls.EventBeaconSlotCoverageWarning" {
			msg, err := sdk.ParseTypedEvent(abciEvent(e))
			require.NoError(t, err)
			warn = msg.(*types.EventBeaconSlotCoverageWarning)
		}
	}
	require.NotNil(t, warn)
	require.Equal(t, uint32(51), warn.ThresholdSlots)
	require.Equal(t, uint32(15), warn.WorstCaseSlots, "drop the 5%-power validator: 10+5 slots remain")
	require.Equal(t, uint32(5), warn.SlotsWithoutValidator)
}

// --- extension structure --------------------------------------------------------------

// signedExtension builds the extension the app's ExtendVote would build.
func signedExtension(t *testing.T, p types.PendingBeacon, d *beaconDKG, start, end uint32) []byte {
	t.Helper()
	sigs, err := blsutil.SignPartials(p.Msg, d.scalars(start, end))
	require.NoError(t, err)
	slots := make([]uint32, 0, end-start+1)
	for s := start; s <= end; s++ {
		slots = append(slots, s)
	}
	b, err := (&types.BeaconVoteExtension{Version: 1, Round: p.Round, KeyEpoch: p.KeyEpoch, SlotIndices: slots, Signatures: sigs}).Marshal()
	require.NoError(t, err)
	return b
}

func structureFixture(t *testing.T) (Keeper, sdk.Context, *beaconDKG, types.PendingBeacon) {
	t.Helper()
	staking := newFakeStaking()
	staking.add(1, 10, testCons(1))
	staking.add(2, 10, testCons(2))
	staking.add(3, 10, testCons(3)) // validator without slots
	k, ctx := beaconKeeper(t, staking)
	d := newBeaconDKG(t, 10, 4)
	completedEpoch(t, k, ctx, 6, d, [][2]uint32{{0, 5}, {6, 9}})
	require.True(t, k.ActivateBeacon(ctx, 0, []uint64{6}))
	k.ProcessBlockBeacon(atBlock(ctx, 99, 2))
	p, ok := k.GetPendingBeacon(ctx, 100)
	require.True(t, ok)
	return k, ctx, d, p
}

// Two validators' extensions: every per-slot partial verifies with the
// keeper's pairing check (the Step 5 path); one flipped bit fails.
func TestBeaconExtensionPartialsVerify(t *testing.T) {
	k, ctx, d, p := structureFixture(t)
	data, err := k.GetEpochBLSData(ctx, 6)
	require.NoError(t, err)
	for _, v := range []struct {
		cons       sdk.ConsAddress
		start, end uint32
	}{{testCons(1), 0, 5}, {testCons(2), 6, 9}} {
		ve, err := k.ValidateBeaconExtensionStructure(ctx, signedExtension(t, p, d, v.start, v.end), 100, v.cons)
		require.NoError(t, err)
		for i, slot := range ve.SlotIndices {
			sig := ve.Signatures[i*48 : (i+1)*48]
			require.True(t, k.verifyBLSPartialSignatureBlst(sig, p.Msg, &data, []uint32{slot}), "slot %d", slot)
			require.True(t, blsutil.VerifyPartial(p.Msg, sig, d.slotPKs[slot]))
		}
		require.True(t, k.verifyBLSPartialSignatureBlst(ve.Signatures, p.Msg, &data, ve.SlotIndices), "all slots at once")
		bad := append([]byte(nil), ve.Signatures...)
		bad[len(bad)-1] ^= 1
		require.False(t, k.verifyBLSPartialSignatureBlst(bad, p.Msg, &data, ve.SlotIndices), "bit flip")
	}
}

func TestValidateBeaconExtensionStructure(t *testing.T) {
	k, ctx, d, p := structureFixture(t)
	good := signedExtension(t, p, d, 0, 5)
	_, err := k.ValidateBeaconExtensionStructure(ctx, good, 100, testCons(1))
	require.NoError(t, err)

	mutate := func(f func(ve *types.BeaconVoteExtension)) []byte {
		var ve types.BeaconVoteExtension
		require.NoError(t, ve.Unmarshal(good))
		f(&ve)
		b, err := ve.Marshal()
		require.NoError(t, err)
		return b
	}
	cases := map[string]struct {
		ext   []byte
		round uint64
		cons  sdk.ConsAddress
		want  error
	}{
		"wrong version": {mutate(func(ve *types.BeaconVoteExtension) { ve.Version = 2 }), 100, testCons(1), ErrBeaconExtensionVersion},
		"wrong round":   {mutate(func(ve *types.BeaconVoteExtension) { ve.Round = 101 }), 100, testCons(1), ErrBeaconExtensionRound},
		"wrong epoch":   {mutate(func(ve *types.BeaconVoteExtension) { ve.KeyEpoch = 5 }), 100, testCons(1), ErrBeaconExtensionEpoch},
		"subset of the range": {mutate(func(ve *types.BeaconVoteExtension) {
			ve.SlotIndices = ve.SlotIndices[:5]
			ve.Signatures = ve.Signatures[:5*48]
		}), 100, testCons(1), ErrBeaconExtensionSlots},
		"superset of the range": {mutate(func(ve *types.BeaconVoteExtension) {
			ve.SlotIndices = append(ve.SlotIndices, 6)
			ve.Signatures = append(ve.Signatures, ve.Signatures[:48]...)
		}), 100, testCons(1), ErrBeaconExtensionSlots},
		"another's range":         {signedExtension(t, p, d, 6, 9), 100, testCons(1), ErrBeaconExtensionSlots},
		"odd signature length":    {mutate(func(ve *types.BeaconVoteExtension) { ve.Signatures = ve.Signatures[:len(ve.Signatures)-1] }), 100, testCons(1), ErrBeaconExtensionSigLen},
		"validator with no slots": {good, 100, testCons(3), ErrBeaconNoOwner},
		"unknown validator":       {good, 100, testCons(8), ErrBeaconNoOwner},
		"no pending round":        {good, 55, testCons(1), ErrBeaconNoPending},
		"oversized":               {make([]byte, MaxBeaconExtensionBytes(10)+1), 100, testCons(1), ErrBeaconExtensionSize},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := k.ValidateBeaconExtensionStructure(ctx, c.ext, c.round, c.cons)
			require.ErrorIs(t, err, c.want)
		})
	}
	_, err = k.ValidateBeaconExtensionStructure(ctx, []byte{0xff, 0xff, 0xff}, 100, testCons(1))
	require.ErrorContains(t, err, "decode", "garbage")
}

func TestMaxBeaconExtensionBytes(t *testing.T) {
	require.Equal(t, 1000*64+512, MaxBeaconExtensionBytes(1000))
	require.Equal(t, 4096, MaxBeaconExtensionBytes(1))
	require.Equal(t, MaxBeaconExtensionBytes(types.DefaultParams().ITotalSlots), MaxBeaconExtensionBytes(0))

	// A real extension for a validator owning every slot fits with room to spare.
	p := types.PendingBeacon{Round: 1 << 40, KeyEpoch: 1 << 40, Msg: bytes.Repeat([]byte{1}, 32)}
	d := newBeaconDKG(t, 1000, 3)
	ext := signedExtension(t, p, d, 0, 999)
	require.Less(t, len(ext), MaxBeaconExtensionBytes(1000))
}

func TestBeaconQueries(t *testing.T) {
	k, ctx, _, p := structureFixture(t)
	resp, err := k.BeaconKey(ctx, &types.QueryBeaconKeyRequest{})
	require.NoError(t, err)
	require.True(t, resp.KeySet)
	require.Equal(t, uint64(6), resp.KeyEpoch)
	require.True(t, resp.Activated)
	require.False(t, resp.HasCandidate)

	pr, err := k.PendingBeacon(ctx, &types.QueryPendingBeaconRequest{Round: 100})
	require.NoError(t, err)
	require.Equal(t, hex.EncodeToString(p.Msg), hex.EncodeToString(pr.Pending.Msg))
	_, err = k.PendingBeacon(ctx, &types.QueryPendingBeaconRequest{Round: 7})
	require.Error(t, err)
	_, err = k.BeaconKey(ctx, nil)
	require.Error(t, err)
}

func abciEvent(e sdk.Event) abci.Event { return abci.Event(e) }

func TestBeaconKeySwitchDelayParam(t *testing.T) {
	require.Equal(t, types.DefaultBeaconKeySwitchDelay, types.DefaultParams().BeaconKeySwitchDelay)
	p := types.DefaultParams()
	p.BeaconKeySwitchDelay = 0
	require.NoError(t, p.Validate(), "0 = switch on the next block")
	p.BeaconKeySwitchDelay = types.MaxBeaconKeySwitchDelay
	require.NoError(t, p.Validate())
	p.BeaconKeySwitchDelay = types.MaxBeaconKeySwitchDelay + 1
	require.Error(t, p.Validate())
}

// Step 4: the beacon tx is required in block H iff the beacon is activated,
// H > inj_from and round H-1 has a pending message.
func TestBeaconInjectionRound(t *testing.T) {
	k, ctx := beaconKeeper(t, nil)
	d := newBeaconDKG(t, 8, 2)
	completedEpoch(t, k, ctx, 1, d, [][2]uint32{{0, 7}})

	_, ok := k.BeaconInjectionRound(ctx, 50)
	require.False(t, ok, "not activated")

	require.True(t, k.ActivateBeacon(ctx, 40, []uint64{1}))
	a, _ := k.GetBeaconActivation(ctx)
	require.Equal(t, uint64(40), a.InjFrom)
	for h := int64(38); h <= 45; h++ {
		k.ProcessBlockBeacon(atBlock(ctx, h, 2)) // pending for rounds 39..46
	}
	_, ok = k.BeaconInjectionRound(ctx, 40)
	require.False(t, ok, "block inj_from itself (old-binary proposal)")
	p, ok := k.BeaconInjectionRound(ctx, 41)
	require.True(t, ok)
	require.Equal(t, uint64(40), p.Round)
	_, ok = k.BeaconInjectionRound(ctx, 60)
	require.False(t, ok, "no pending message for round 59")
}

func TestGetBeaconKeyMaterial(t *testing.T) {
	k, ctx := beaconKeeper(t, nil)
	d := newBeaconDKG(t, 8, 2)
	completedEpoch(t, k, ctx, 1, d, [][2]uint32{{0, 7}})
	km, err := k.GetBeaconKeyMaterial(ctx, 1)
	require.NoError(t, err)
	require.Equal(t, uint32(3), km.Threshold)
	require.Equal(t, d.groupPK, km.GroupPublicKey)
	require.Len(t, km.SlotPublicKeys, 8)

	_, err = k.GetBeaconKeyMaterial(ctx, 2)
	require.Error(t, err, "no such epoch")
	dealing := completedEpoch(t, k, ctx, 3, d, [][2]uint32{{0, 7}})
	dealing.DkgPhase = types.DKGPhase_DKG_PHASE_DEALING
	require.NoError(t, k.SetEpochBLSDataBaseOnly(ctx, dealing))
	_, err = k.GetBeaconKeyMaterial(ctx, 3)
	require.Error(t, err, "DKG not final")
}

// blsutil.AggregatePartials matches the keeper's threshold-signing aggregation.
func TestAggregatePartialsMatchesKeeper(t *testing.T) {
	k, _ := beaconKeeper(t, nil)
	d := newBeaconDKG(t, 8, 2)
	msg := bytes.Repeat([]byte{4}, 32)
	sigs, err := blsutil.SignPartials(msg, d.scalars(2, 6))
	require.NoError(t, err)
	slots := []uint32{2, 3, 4, 5, 6}
	per := make([][]byte, len(slots))
	for i := range slots {
		per[i] = sigs[i*48 : (i+1)*48]
	}
	got, err := blsutil.AggregatePartials(slots, per)
	require.NoError(t, err)
	want, err := k.aggregateBLSPartialSignaturesBlst([]types.PartialSignature{{SlotIndices: slots, Signature: sigs}})
	require.NoError(t, err)
	require.Equal(t, want, got)
	require.True(t, k.verifyFinalSignatureBlst(got, msg, d.groupPK))
}
