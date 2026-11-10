package app_test

import (
	"bytes"
	"os"
	"testing"
	"time"

	abci "github.com/cometbft/cometbft/abci/types"
	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	"github.com/consensys/gnark-crypto/ecc/bls12-381/fr"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/stretchr/testify/require"
	blst "github.com/supranational/blst/bindings/go"

	"github.com/productscience/inference/app"
	"github.com/productscience/inference/app/beacon"
	"github.com/productscience/inference/app/beacon/sharefile"
	"github.com/productscience/inference/app/upgrades/v0_2_18"
	blstypes "github.com/productscience/inference/x/bls/types"
)

func g2Compressed(e fr.Element) []byte {
	be := e.Bytes()
	var le [32]byte
	for i := range be {
		le[i] = be[31-i]
	}
	return blst.P2Generator().Mult(le[:], 255).ToAffine().Compress()
}

// TestBeaconShares_EndToEndWithCommittedState drives the real app: committed
// x/bls state holds a completed epoch, the share file is written exactly as the
// dAPI does (sharefile.WriteAtomic), and ExtendVote at the VE height verifies
// the shares against that state while still returning an empty extension.
func TestBeaconShares_EndToEndWithCommittedState(t *testing.T) {
	testApp := createTestApp(t)
	t.Cleanup(func() { require.NoError(t, testApp.Close()) })
	provider, ok := testApp.BeaconShares.(*beacon.FileShareProvider)
	require.True(t, ok, "provider is on by default")
	statusOf := func(epoch uint64) beacon.Status {
		for _, st := range provider.Statuses() {
			if st.EpochID == epoch {
				return st
			}
		}
		return beacon.Status{}
	}
	waitState := func(epoch uint64, state string) beacon.Status {
		t.Helper()
		require.Eventually(t, func() bool { return statusOf(epoch).State == state }, 10*time.Second, 20*time.Millisecond,
			"epoch %d never became %s (now %+v)", epoch, state, statusOf(epoch))
		return statusOf(epoch)
	}

	// Degree-2 polynomial over 8 slots; participant "owner" holds [3,7].
	const epochID = 3
	const totalSlots = 8
	coeffs := make([]fr.Element, 3)
	for i := range coeffs {
		_, err := coeffs[i].SetRandom()
		require.NoError(t, err)
	}
	secrets := make([]fr.Element, totalSlots)
	slotPKs := make([][]byte, totalSlots)
	for s := range secrets {
		var x fr.Element
		x.SetUint64(uint64(s) + 1)
		for i := len(coeffs) - 1; i >= 0; i-- {
			secrets[s].Mul(&secrets[s], &x)
			secrets[s].Add(&secrets[s], &coeffs[i])
		}
		slotPKs[s] = g2Compressed(secrets[s])
	}
	groupPK := g2Compressed(coeffs[0])

	// The slot owner is the genesis validator, and this node signs as it (M2).
	seedCtx := testApp.NewUncachedContext(false, cmtproto.Header{Height: 2, ChainID: testApp.ChainID()})
	vals, err := testApp.StakingKeeper.GetAllValidators(seedCtx)
	require.NoError(t, err)
	require.NotEmpty(t, vals)
	valOper, err := sdk.ValAddressFromBech32(vals[0].OperatorAddress)
	require.NoError(t, err)
	owner := sdk.AccAddress(valOper).String()
	ownerCons, err := vals[0].GetConsAddr()
	require.NoError(t, err)
	testApp.SetBeaconSelfForTest(ownerCons)

	require.NoError(t, testApp.BlsKeeper.SetEpochBLSDataBaseOnly(seedCtx, blstypes.EpochBLSData{
		EpochId:        epochID,
		ITotalSlots:    totalSlots,
		TSlotsDegree:   2,
		DkgPhase:       blstypes.DKGPhase_DKG_PHASE_COMPLETED,
		GroupPublicKey: groupPK,
		SlotPublicKeys: slotPKs,
		ValidDealers:   []bool{true, true},
		Participants: []blstypes.BLSParticipantInfo{
			{Address: "gonka1other", SlotStartIndex: 0, SlotEndIndex: 2},
			{Address: owner, SlotStartIndex: 3, SlotEndIndex: 7},
		},
	}))
	testApp.BlsKeeper.SetCurrentSigningEpochID(seedCtx, epochID)
	enableHeight, _, err := v0_2_18.EnableVoteExtensions(seedCtx, testApp.ConsensusParamsKeeper.ParamsStore, 2)
	require.NoError(t, err)
	finalizeAndCommit(t, testApp, 2, nil)
	finalizeAndCommit(t, testApp, 3, nil)

	shares := make([][sharefile.ScalarLen]byte, 0, 5)
	for s := 3; s <= 7; s++ {
		shares = append(shares, secrets[s].Bytes())
	}
	file, err := sharefile.New(testApp.ChainID(), epochID, owner, 3, 7, groupPK, shares, time.Now())
	require.NoError(t, err)
	path, written, err := sharefile.WriteAtomic(provider.Dir(), file)
	require.NoError(t, err)
	require.True(t, written)
	provider.Reload()

	// A node signing as another validator never uses these shares (M2). The
	// background verifier judges the file against the committed version the
	// PreBlocker recorded; ExtendVote only reads memory (review L5).
	testApp.SetBeaconSelfForTest(bytes.Repeat([]byte{0xEE}, 20))
	ext, err := testApp.ExtendVote(seedCtx, &abci.RequestExtendVote{Height: enableHeight})
	require.NoError(t, err)
	require.Empty(t, ext.VoteExtension)
	st := waitState(epochID, "invalid")
	require.Contains(t, st.Reason, "this node signs as")

	// The right node: the file is reloaded (rewritten, so it is judged again).
	testApp.SetBeaconSelfForTest(ownerCons)
	require.NoError(t, os.Remove(path))
	provider.Reload()
	_, written, err = sharefile.WriteAtomic(provider.Dir(), file)
	require.NoError(t, err)
	require.True(t, written)
	provider.Reload()

	waitState(epochID, "valid")
	ext, err = testApp.ExtendVote(seedCtx, &abci.RequestExtendVote{Height: enableHeight})
	require.NoError(t, err)
	require.Empty(t, ext.VoteExtension, "Step 2 keeps extensions empty")
	require.Equal(t, []beacon.Status{{EpochID: epochID, SlotStart: 3, SlotEnd: 7, State: "valid"}}, provider.Statuses())

	// The shares served are the file's, in blst (little-endian) order, and each
	// matches its committed slot public key.
	got, ok := provider.Get(testApp.NewUncachedContext(false, cmtproto.Header{Height: enableHeight, ChainID: testApp.ChainID()}), epochID)
	require.True(t, ok)
	for i, sc := range got.Scalars {
		require.NoError(t, beacon.VerifyShare(sc, slotPKs[3+i]))
	}

	// A file for an epoch that committed state does not have (yet) is never
	// served, and is retried rather than rejected (review L1).
	wrong, err := sharefile.New(testApp.ChainID(), epochID+1, owner, 3, 7, groupPK, shares, time.Now())
	require.NoError(t, err)
	_, _, err = sharefile.WriteAtomic(provider.Dir(), wrong)
	require.NoError(t, err)
	provider.Reload()
	_, ok = provider.Get(testApp.NewUncachedContext(false, cmtproto.Header{Height: enableHeight, ChainID: testApp.ChainID()}), epochID+1)
	require.False(t, ok, "epoch without committed BLS data")
	require.Eventually(t, func() bool { return statusOf(epochID+1).Reason != "" }, 10*time.Second, 20*time.Millisecond)
	require.Equal(t, "unverified", statusOf(epochID+1).State)
	require.Contains(t, statusOf(epochID+1).Reason, "has no BLS data")
}

// TestBeaconShares_DisabledByConfig: [bls-beacon] enabled = false leaves no provider.
func TestBeaconShares_DisabledByConfig(t *testing.T) {
	testApp := createTestAppWithOptions(t, map[string]interface{}{app.FlagBeaconEnabled: false})
	require.Nil(t, testApp.BeaconShares)
	require.NoError(t, testApp.Close(), "Close works without a provider")
}
