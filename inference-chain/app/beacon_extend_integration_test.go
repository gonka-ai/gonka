package app_test

import (
	"crypto/sha256"
	"encoding/binary"
	"testing"
	"time"

	abci "github.com/cometbft/cometbft/abci/types"
	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	"github.com/consensys/gnark-crypto/ecc/bls12-381/fr"
	sdk "github.com/cosmos/cosmos-sdk/types"
	promtest "github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"

	"github.com/productscience/inference/app"
	"github.com/productscience/inference/app/beacon"
	"github.com/productscience/inference/app/beacon/sharefile"
	"github.com/productscience/inference/app/upgrades/v0_2_18"
	"github.com/productscience/inference/x/bls/blsutil"
	blstypes "github.com/productscience/inference/x/bls/types"
)

// blockHash is a deterministic 32-byte stand-in for CometBFT's block hash.
func blockHash(h int64) []byte {
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], uint64(h))
	s := sha256.Sum256(b[:])
	return s[:]
}

func finalizeWithHash(t *testing.T, testApp *app.App, h int64) {
	t.Helper()
	_, err := testApp.FinalizeBlock(&abci.RequestFinalizeBlock{Height: h, Hash: blockHash(h), Time: time.Unix(1_700_000_000+h, 0)})
	require.NoError(t, err)
	_, err = testApp.Commit()
	require.NoError(t, err)
}

// TestBlockBeacon_ExtensionsAcrossTheStep3Upgrade drives the real app through
// the plan's activation scenario (rule R6, E < U):
//   - vote extensions are enabled at E = 4 while the beacon is not active yet;
//   - the Step 3 upgrade runs at U = 6 (ActivateBlockBeacon): ext_from = U, the
//     beacon key is seeded from the completed DKG, the switch delay is set;
//   - ExtendVote(U) is empty (old-binary semantics), ExtendVote(U+1) carries
//     one partial per owned slot over PendingBeacon(U+1) that verifies against
//     the committed slot public keys and passes the structural check;
//   - VerifyVoteExtension accepts it; a second activation changes nothing.
func TestBlockBeacon_ExtensionsAcrossTheStep3Upgrade(t *testing.T) {
	testApp := createTestApp(t) // height 1 committed; genesis activated the beacon (ext_from 0)
	t.Cleanup(func() { require.NoError(t, testApp.Close()) })
	provider := testApp.BeaconShares.(*beacon.FileShareProvider)

	// A completed epoch whose slots [3,7] belong to the genesis validator.
	const epochID, totalSlots, degree = 3, 8, 2
	coeffs := make([]fr.Element, degree+1)
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
	seed := testApp.NewUncachedContext(false, cmtproto.Header{Height: 2, ChainID: testApp.ChainID()})
	vals, err := testApp.StakingKeeper.GetAllValidators(seed)
	require.NoError(t, err)
	valOper, err := sdk.ValAddressFromBech32(vals[0].OperatorAddress)
	require.NoError(t, err)
	owner := sdk.AccAddress(valOper).String()
	ownerCons, err := vals[0].GetConsAddr()
	require.NoError(t, err)
	testApp.SetBeaconSelfForTest(ownerCons)

	require.NoError(t, testApp.BlsKeeper.SetEpochBLSDataBaseOnly(seed, blstypes.EpochBLSData{
		EpochId: epochID, ITotalSlots: totalSlots, TSlotsDegree: degree,
		DkgPhase: blstypes.DKGPhase_DKG_PHASE_COMPLETED, GroupPublicKey: g2Compressed(coeffs[0]), SlotPublicKeys: slotPKs,
		ValidDealers: []bool{true},
		Participants: []blstypes.BLSParticipantInfo{
			{Address: sdk.AccAddress(make([]byte, 20)).String(), SlotStartIndex: 0, SlotEndIndex: 2},
			{Address: owner, SlotStartIndex: 3, SlotEndIndex: 7},
		},
	}))
	testApp.BlsKeeper.SetCurrentSigningEpochID(seed, epochID)
	// The chain predates Step 3: no beacon activation in state.
	seed.KVStore(testApp.GetKey(blstypes.StoreKey)).Delete(blstypes.BeaconActivationKey)
	enable, _, err := v0_2_18.EnableVoteExtensions(seed, testApp.ConsensusParamsKeeper.ParamsStore, 2)
	require.NoError(t, err)
	require.Equal(t, int64(4), enable)

	for h := int64(2); h <= 5; h++ {
		finalizeWithHash(t, testApp, h)
	}
	committed := func() sdk.Context {
		return testApp.NewUncachedContext(false, cmtproto.Header{Height: 6, ChainID: testApp.ChainID()})
	}
	_, ok := testApp.BlsKeeper.GetPendingBeacon(committed(), 5)
	require.False(t, ok, "no pending beacon before the Step 3 activation, even with VE on")

	// --- the upgrade at U = 6 ---
	const U = 6
	upgradeCtx := testApp.NewUncachedContext(false, cmtproto.Header{Height: U, ChainID: testApp.ChainID()})
	activated, err := v0_2_18.ActivateBlockBeacon(upgradeCtx, testApp.BlsKeeper)
	require.NoError(t, err)
	require.True(t, activated)
	act, ok := testApp.BlsKeeper.GetBeaconActivation(committed())
	require.True(t, ok)
	require.Equal(t, uint64(U), act.ExtFrom)
	key, ok := testApp.BlsKeeper.GetBeaconKeyEpoch(committed())
	require.True(t, ok)
	require.Equal(t, uint64(epochID), key, "seeded from the completed DKG")
	params, err := testApp.BlsKeeper.GetParams(committed())
	require.NoError(t, err)
	require.Equal(t, blstypes.DefaultBeaconKeySwitchDelay, params.BeaconKeySwitchDelay)

	// Idempotent: a second run changes nothing.
	again, err := v0_2_18.ActivateBlockBeacon(testApp.NewUncachedContext(false, cmtproto.Header{Height: U + 3}), testApp.BlsKeeper)
	require.NoError(t, err)
	require.False(t, again)
	act, _ = testApp.BlsKeeper.GetBeaconActivation(committed())
	require.Equal(t, uint64(U), act.ExtFrom)

	// ExtendVote(U): the old binary precommitted U; the new one stays empty too.
	inactive := promtest.ToFloat64(beacon.ExtendVoteEmpty.WithLabelValues(beacon.EmptyInactive))
	ext, err := testApp.ExtendVote(seed, &abci.RequestExtendVote{Height: U, Hash: blockHash(U)})
	require.NoError(t, err)
	require.Empty(t, ext.VoteExtension)
	require.Equal(t, inactive+1, promtest.ToFloat64(beacon.ExtendVoteEmpty.WithLabelValues(beacon.EmptyInactive)))

	finalizeWithHash(t, testApp, U) // EndBlock(U) writes PendingBeacon(U+1)
	pending, ok := testApp.BlsKeeper.GetPendingBeacon(committed(), U+1)
	require.True(t, ok)
	require.Equal(t, uint64(epochID), pending.KeyEpoch)
	wantMsg, err := blsutil.BeaconMessage(testApp.ChainID(), epochID, U+1, blockHash(U))
	require.NoError(t, err)
	require.Equal(t, wantMsg[:], pending.Msg, "m_{U+1} binds the hash of block U")

	// The background verifier accepts the shares once it sees a committed version.
	finalizeWithHash(t, testApp, U+1) // PreBlocker records committed height U
	// Share file for the key epoch, as the dAPI writes it. Written after the
	// test's direct state writes, so the background verifier never reads a
	// committed version while the test mutates the live store.
	shares := make([][sharefile.ScalarLen]byte, 0, 5)
	for s := 3; s <= 7; s++ {
		shares = append(shares, secrets[s].Bytes())
	}
	file, err := sharefile.New(testApp.ChainID(), epochID, owner, 3, 7, g2Compressed(coeffs[0]), shares, time.Now())
	require.NoError(t, err)
	_, _, err = sharefile.WriteAtomic(provider.Dir(), file)
	require.NoError(t, err)
	provider.Reload()
	require.Eventually(t, func() bool {
		for _, st := range provider.Statuses() {
			if st.EpochID == epochID && st.State == "valid" {
				return true
			}
		}
		return false
	}, 10*time.Second, 20*time.Millisecond)

	// ExtendVote(U+2): partials over PendingBeacon(U+2).
	round := int64(U + 2)
	ext, err = testApp.ExtendVote(seed, &abci.RequestExtendVote{Height: round, Hash: blockHash(round)})
	require.NoError(t, err)
	require.NotEmpty(t, ext.VoteExtension)

	ctx := committed()
	ve, err := testApp.BlsKeeper.ValidateBeaconExtensionStructure(ctx, ext.VoteExtension, uint64(round), ownerCons)
	require.NoError(t, err, "structure check that FinalizeBeacon will apply")
	require.Equal(t, []uint32{3, 4, 5, 6, 7}, ve.SlotIndices)
	pending, ok = testApp.BlsKeeper.GetPendingBeacon(ctx, uint64(round))
	require.True(t, ok)
	for i, slot := range ve.SlotIndices {
		require.True(t, blsutil.VerifyPartial(pending.Msg, ve.Signatures[i*48:(i+1)*48], slotPKs[slot]), "slot %d", slot)
	}

	// Peers accept it (R2: size cap only).
	vr, err := testApp.VerifyVoteExtension(&abci.RequestVerifyVoteExtension{
		Height: round, Hash: blockHash(round), ValidatorAddress: ownerCons, VoteExtension: ext.VoteExtension,
	})
	require.NoError(t, err)
	require.Equal(t, abci.ResponseVerifyVoteExtension_ACCEPT, vr.Status)

	// The beacon key query reflects the state.
	resp, err := testApp.BlsKeeper.BeaconKey(ctx, &blstypes.QueryBeaconKeyRequest{})
	require.NoError(t, err)
	require.True(t, resp.Activated)
	require.Equal(t, uint64(U), resp.Activation.ExtFrom)
	require.Equal(t, uint64(epochID), resp.KeyEpoch)
}
