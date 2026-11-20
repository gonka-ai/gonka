package keeper_test

import (
	"context"
	"encoding/hex"
	"fmt"
	"math/big"
	"testing"

	"cosmossdk.io/math"
	curve "github.com/consensys/gnark-crypto/ecc/bls12-381"
	"github.com/consensys/gnark-crypto/ecc/bls12-381/fp"
	"github.com/consensys/gnark-crypto/ecc/bls12-381/hash_to_curve"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/testutil"
	blskeeper "github.com/productscience/inference/x/bls/keeper"
	blstypes "github.com/productscience/inference/x/bls/types"
	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func TestBridgeRefundRejectedWhenPartialsExposed(t *testing.T) {
	for _, attempts := range []uint32{1, 2} {
		for _, automatic := range []bool{false, true} {
			t.Run(fmt.Sprintf("attempts_%d/automatic_%t", attempts, automatic), func(t *testing.T) {
				k, ms, ctx, mocks := setupKeeperWithMocks(t)
				ctx = ctx.WithBlockHeight(100).WithChainID("refund-test-chain").WithTxBytes([]byte(t.Name()))
				blsK, ok := k.BlsKeeper.(blskeeper.Keeper)
				require.True(t, ok)
				params, err := blsK.GetParams(ctx)
				require.NoError(t, err)
				params.MaxSigningAttempts = attempts
				require.NoError(t, blsK.SetParams(ctx, params))

				// Two of three slots can sign; only the first slot publishes a share.
				const epoch = uint64(700)
				_, _, _, g2 := curve.Generators()
				var group curve.G2Affine
				group.ScalarMultiplication(&g2, big.NewInt(7))
				groupBytes := group.Bytes()
				addresses := []string{testutil.Creator, testutil.Requester, sdk.AccAddress([]byte("third-refund-signer!!")).String()}
				epochData := blstypes.EpochBLSData{
					EpochId:        epoch,
					ITotalSlots:    3,
					TSlotsDegree:   1,
					DkgPhase:       blstypes.DKGPhase_DKG_PHASE_SIGNED,
					GroupPublicKey: groupBytes[:],
				}
				for i, address := range addresses {
					var publicKey curve.G2Affine
					publicKey.ScalarMultiplication(&g2, big.NewInt(7+3*int64(i+1)))
					encoded := publicKey.Bytes()
					epochData.SlotPublicKeys = append(epochData.SlotPublicKeys, encoded[:])
					epochData.Participants = append(epochData.Participants, blstypes.BLSParticipantInfo{
						Address: address, SlotStartIndex: uint32(i), SlotEndIndex: uint32(i),
					})
				}
				require.NoError(t, blsK.SetEpochBLSData(ctx, epochData))
				require.NoError(t, k.SetEffectiveEpochIndex(ctx, epoch))
				k.SetEpochGroupData(ctx, types.EpochGroupData{EpochIndex: epoch})
				const bridge = "0x1111111111111111111111111111111111111111"
				_, err = ms.RegisterBridgeAddresses(ctx, &types.MsgRegisterBridgeAddresses{
					Authority: k.GetAuthority(), ChainName: "ethereum", Addresses: []string{bridge},
				})
				require.NoError(t, err)

				owner, err := sdk.AccAddressFromBech32(testutil.Creator)
				require.NoError(t, err)
				mocks.AccountKeeper.EXPECT().HasAccount(gomock.Any(), gomock.Any()).Return(true).AnyTimes()
				mocks.BankViewKeeper.EXPECT().SpendableCoin(gomock.Any(), owner, types.BaseCoin).
					Return(sdk.NewInt64Coin(types.BaseCoin, 1000))
				coins := sdk.NewCoins(sdk.NewInt64Coin(types.BaseCoin, 100))
				locked := math.ZeroInt()
				mocks.BankKeeper.EXPECT().SendCoinsFromAccountToModule(gomock.Any(), owner, types.BridgeEscrowAccName, coins, "bridge_escrow").
					DoAndReturn(func(_ context.Context, _ sdk.AccAddress, _ string, amount sdk.Coins, _ string) error {
						locked = locked.Add(amount.AmountOf(types.BaseCoin))
						return nil
					})
				releaseCalls := 0
				mocks.BankKeeper.EXPECT().SendCoinsFromModuleToAccount(gomock.Any(), types.BridgeEscrowAccName, owner, coins, "bridge_release").
					DoAndReturn(func(_ context.Context, _ string, _ sdk.AccAddress, amount sdk.Coins, _ string) error {
						releaseCalls++
						locked = locked.Sub(amount.AmountOf(types.BaseCoin))
						return nil
					}).AnyTimes()
				response, err := ms.RequestBridgeMint(ctx, &types.MsgRequestBridgeMint{
					Creator: testutil.Creator, Amount: "100", ChainId: "ethereum",
					DestinationAddress: "0x3333333333333333333333333333333333333333", DestinationBridgeAddress: bridge,
				})
				require.NoError(t, err)
				requestID := hashBridgeRequestIDForCancelTest(response.RequestId)
				requestKey := hex.EncodeToString(requestID)
				pending, err := k.BridgeMintRefundsMap.Get(ctx, requestKey)
				require.NoError(t, err)
				request, err := blsK.GetSigningStatus(ctx, requestID)
				require.NoError(t, err)
				require.True(t, locked.Equal(math.NewInt(100)))

				// Use the same message mapping and slot secret as the signing implementation.
				var u fp.Element
				u.SetBytes(request.MessageHash)
				point := curve.MapToCurve1(&u)
				hash_to_curve.G1Isogeny(&point.X, &point.Y)
				var message, signature curve.G1Affine
				message.ClearCofactor(&point)
				signature.ScalarMultiplication(&message, big.NewInt(10))
				encodedSignature := signature.Bytes()
				_, err = blskeeper.NewMsgServerImpl(blsK).SubmitPartialSignature(ctx, &blstypes.MsgSubmitPartialSignature{
					Creator: addresses[0], RequestId: requestID, SlotIndices: []uint32{0}, PartialSignature: encodedSignature[:],
				})
				require.NoError(t, err)
				published, err := blsK.GetSigningStatus(ctx, requestID)
				require.NoError(t, err)
				require.Len(t, published.PartialSignatures, 1)

				for attempt := uint32(0); attempt < attempts; attempt++ {
					current, err := blsK.GetSigningStatus(ctx, requestID)
					require.NoError(t, err)
					ctx = ctx.WithBlockHeight(current.DeadlineBlockHeight)
					require.NoError(t, blsK.ProcessThresholdSigningDeadlines(ctx))
				}
				expired, err := blsK.GetSigningStatus(ctx, requestID)
				require.NoError(t, err)
				require.Equal(t, blstypes.ThresholdSigningStatus_THRESHOLD_SIGNING_STATUS_EXPIRED, expired.Status)
				if attempts > 1 {
					require.Empty(t, expired.PartialSignatures, "retry must have cleared the published share")
				}

				if automatic {
					var closeRetry bool
					closeRetry, err = k.ProcessAutoRefundForFailedBridgeOperation(ctx, requestID, "deadline expired")
					require.False(t, closeRetry)
				} else {
					_, err = ms.CancelBridgeOperation(ctx, &types.MsgCancelBridgeOperation{
						Creator: testutil.Creator, RequestId: response.RequestId,
					})
				}
				require.Zero(t, releaseCalls, "exposed signing requests must never release escrow")
				require.ErrorContains(t, err, "partial signatures have been exposed")
				require.True(t, locked.Equal(math.NewInt(100)), "escrow must remain locked")
				stillPending, err := k.BridgeMintRefundsMap.Get(ctx, requestKey)
				require.NoError(t, err)
				require.Equal(t, pending, stillPending)
				unchanged, err := blsK.GetSigningStatus(ctx, requestID)
				require.NoError(t, err)
				require.Equal(t, expired, unchanged)
			})
		}
	}
}
