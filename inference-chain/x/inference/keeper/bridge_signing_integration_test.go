package keeper_test

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"math/big"
	"testing"

	"cosmossdk.io/math"
	storetypes "cosmossdk.io/store/types"
	bls12381 "github.com/consensys/gnark-crypto/ecc/bls12-381"
	"github.com/consensys/gnark-crypto/ecc/bls12-381/fp"
	hashToCurve "github.com/consensys/gnark-crypto/ecc/bls12-381/hash_to_curve"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/testutil"
	keepertest "github.com/productscience/inference/testutil/keeper"
	blskeeper "github.com/productscience/inference/x/bls/keeper"
	blstypes "github.com/productscience/inference/x/bls/types"
	inferencetypes "github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func bridgeTestG2(scalar int64) []byte {
	_, _, _, g2 := bls12381.Generators()
	var pk bls12381.G2Affine
	pk.ScalarMultiplication(&g2, big.NewInt(scalar))
	b := pk.Bytes()
	return b[:]
}

func bridgeTestHashPoint(hash []byte) bls12381.G1Affine {
	var u fp.Element
	u.SetBytes(hash)
	p := bls12381.MapToCurve1(&u)
	hashToCurve.G1Isogeny(&p.X, &p.Y)
	var out bls12381.G1Affine
	out.ClearCofactor(&p)
	return out
}

func bridgeTestSign(hash []byte, scalar int64) []byte {
	p := bridgeTestHashPoint(hash)
	var signature bls12381.G1Affine
	signature.ScalarMultiplication(&p, big.NewInt(scalar))
	b := signature.Bytes()
	return b[:]
}

func TestBridgeFallbackCompletesRealSignature(t *testing.T) {
	k, ms, ctx, mocks := setupKeeperWithMocks(t)
	ctx = ctx.WithBlockHeight(100).WithChainID("review-chain")
	setBridgeSigningFallbackEpochs(t, k, ctx)
	blsK := k.BlsKeeper.(blskeeper.Keeper)
	participants := []blstypes.BLSParticipantInfo{
		{Address: testutil.Creator, SlotStartIndex: 0, SlotEndIndex: 0},
		{Address: testutil.Validator, SlotStartIndex: 1, SlotEndIndex: 1},
		{Address: testutil.Validator2, SlotStartIndex: 2, SlotEndIndex: 2},
	}
	require.NoError(t, blsK.SetEpochBLSData(ctx, blstypes.EpochBLSData{
		EpochId: 9, DkgPhase: blstypes.DKGPhase_DKG_PHASE_SIGNED,
		ITotalSlots: 3, TSlotsDegree: 1, Participants: participants,
		GroupPublicKey: bridgeTestG2(17), SlotPublicKeys: [][]byte{bridgeTestG2(22), bridgeTestG2(27), bridgeTestG2(32)},
	}))
	bridge := "0x2222222222222222222222222222222222222222"
	k.SetBridgeContractAddress(ctx, inferencetypes.BridgeContractAddress{ChainId: "ethereum", Address: bridge})
	signer := sdk.MustAccAddressFromBech32(testutil.Creator)
	mocks.AccountKeeper.EXPECT().HasAccount(ctx, signer).Return(true)
	mocks.BankViewKeeper.EXPECT().SpendableCoin(ctx, signer, inferencetypes.BaseCoin).Return(sdk.NewCoin(inferencetypes.BaseCoin, math.NewInt(1000)))
	mocks.BankKeeper.EXPECT().SendCoinsFromAccountToModule(ctx, signer, inferencetypes.BridgeEscrowAccName, gomock.Any(), "bridge_escrow").Return(nil)
	resp, err := ms.RequestBridgeMint(ctx, &inferencetypes.MsgRequestBridgeMint{
		Creator: testutil.Creator, Amount: "100", ChainId: "ethereum",
		DestinationAddress: "0x3333333333333333333333333333333333333333", DestinationBridgeAddress: bridge,
	})
	require.NoError(t, err)
	require.Equal(t, uint64(9), resp.EpochIndex)
	requests, err := blsK.ListActiveSigningRequests(ctx, 9)
	require.NoError(t, err)
	require.Len(t, requests, 1)
	request := requests[0]
	require.Equal(t, uint64(9), binary.BigEndian.Uint64(request.EncodedData[:8]))
	for i, scalar := range []int64{22, 27} {
		require.NoError(t, blsK.AddPartialSignature(ctx, request.RequestId, []uint32{uint32(i)}, bridgeTestSign(request.MessageHash, scalar), participants[i].Address))
	}
	result, err := blsK.GetSigningStatus(ctx, request.RequestId)
	require.NoError(t, err)
	require.Equal(t, blstypes.ThresholdSigningStatus_THRESHOLD_SIGNING_STATUS_COMPLETED, result.Status)
	var signature bls12381.G1Affine
	require.NoError(t, signature.Unmarshal(result.FinalSignature))
	var pk bls12381.G2Affine
	require.NoError(t, pk.Unmarshal(bridgeTestG2(17)))
	hashPoint := bridgeTestHashPoint(result.MessageHash)
	hashPoint.Neg(&hashPoint)
	_, _, _, g2 := bls12381.Generators()
	valid, err := bls12381.PairingCheck([]bls12381.G1Affine{signature, hashPoint}, []bls12381.G2Affine{g2, pk})
	require.NoError(t, err)
	require.True(t, valid)
	t.Log("Fallback mint completed with two real partial signatures, verified against epoch 9 group key")
}

func TestBridgeSigningEpochSelectionGas(t *testing.T) {
	k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)
	blsK := k.BlsKeeper.(blskeeper.Keeper)
	data := blstypes.EpochBLSData{EpochId: 10, DkgPhase: blstypes.DKGPhase_DKG_PHASE_SIGNED, GroupPublicKey: bridgeTestG2(17), ITotalSlots: 100, TSlotsDegree: 50}

	for i := 0; i < 100; i++ {
		data.SlotPublicKeys = append(data.SlotPublicKeys, bridgeTestG2(17))
	}
	for i := 0; i < 20; i++ {
		address := fmt.Sprintf("participant-%d", i)
		data.Participants = append(data.Participants, blstypes.BLSParticipantInfo{Address: address, SlotStartIndex: uint32(i * 5), SlotEndIndex: uint32(i*5 + 4)})
		part := &blstypes.DealerPartStorage{DealerAddress: address}
		for j := 0; j < 51; j++ {
			part.Commitments = append(part.Commitments, bridgeTestG2(17))
		}
		for j := 0; j < 20; j++ {
			shares := &blstypes.EncryptedSharesForParticipant{}
			for s := 0; s < 5; s++ {
				shares.EncryptedShares = append(shares.EncryptedShares, bytes.Repeat([]byte{1}, 128))
			}
			part.ParticipantShares = append(part.ParticipantShares, shares)
		}
		data.DealerParts = append(data.DealerParts, part)
	}

	parts := data.DealerParts
	data.DealerParts = nil
	require.NoError(t, blsK.SetEpochBLSData(ctx, data))
	sparseCtx, _ := ctx.CacheContext()
	sparseCtx = sparseCtx.WithGasMeter(storetypes.NewInfiniteGasMeter())
	_, err := k.GetBridgeSigningEpochID(sparseCtx, 10)
	require.NoError(t, err)
	data.DealerParts = parts
	require.NoError(t, blsK.SetEpochBLSData(ctx, data))
	request := blstypes.SigningData{CurrentEpochId: 10, RequestId: bytes.Repeat([]byte{4}, 32), ChainId: bytes.Repeat([]byte{2}, 32)}
	before, _ := ctx.CacheContext()
	before = before.WithGasMeter(storetypes.NewInfiniteGasMeter())
	require.NoError(t, blsK.RequestThresholdSignature(before, request))
	after, _ := ctx.CacheContext()
	after = after.WithGasMeter(storetypes.NewInfiniteGasMeter())
	_, err = k.GetBridgeSigningEpochID(after, 10)
	require.NoError(t, err)

	require.Equal(t, sparseCtx.GasMeter().GasConsumed(), after.GasMeter().GasConsumed())
	require.NoError(t, blsK.RequestThresholdSignature(after, request))
	require.Less(t, after.GasMeter().GasConsumed(), before.GasMeter().GasConsumed()*110/100)
	t.Logf("20 dealers / 100 slots: existing BLS request gas=%d, with new selector=%d, multiplier=%.2f", before.GasMeter().GasConsumed(), after.GasMeter().GasConsumed(), float64(after.GasMeter().GasConsumed())/float64(before.GasMeter().GasConsumed()))
}
