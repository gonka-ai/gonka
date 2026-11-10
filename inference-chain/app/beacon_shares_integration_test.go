package app_test

import (
	"testing"
	"time"

	abci "github.com/cometbft/cometbft/abci/types"
	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	"github.com/consensys/gnark-crypto/ecc/bls12-381/fr"
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
	provider, ok := testApp.BeaconShares.(*beacon.FileShareProvider)
	require.True(t, ok, "provider is on by default")

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
	const owner = "gonka1owner"

	seedCtx := testApp.NewUncachedContext(false, cmtproto.Header{Height: 2, ChainID: testApp.ChainID()})
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
	_, written, err := sharefile.WriteAtomic(provider.Dir(), file)
	require.NoError(t, err)
	require.True(t, written)
	provider.Reload()

	ext, err := testApp.ExtendVote(seedCtx, &abci.RequestExtendVote{Height: enableHeight})
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

	// A file whose shares belong to someone else's range is rejected.
	wrong, err := sharefile.New(testApp.ChainID(), epochID+1, owner, 3, 7, groupPK, shares, time.Now())
	require.NoError(t, err)
	_, _, err = sharefile.WriteAtomic(provider.Dir(), wrong)
	require.NoError(t, err)
	provider.Reload()
	_, ok = provider.Get(testApp.NewUncachedContext(false, cmtproto.Header{Height: enableHeight, ChainID: testApp.ChainID()}), epochID+1)
	require.False(t, ok, "epoch without committed BLS data")

	require.NoError(t, testApp.Close())
}

// TestBeaconShares_DisabledByConfig: [bls-beacon] enabled = false leaves no provider.
func TestBeaconShares_DisabledByConfig(t *testing.T) {
	testApp := createTestAppWithOptions(t, map[string]interface{}{app.FlagBeaconEnabled: false})
	require.Nil(t, testApp.BeaconShares)
	require.NoError(t, testApp.Close(), "Close works without a provider")
}
