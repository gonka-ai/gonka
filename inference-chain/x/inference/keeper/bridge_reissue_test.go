package keeper_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"testing"

	wasmtypes "github.com/CosmWasm/wasmd/x/wasm/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/testutil"
	keepertest "github.com/productscience/inference/testutil/keeper"
	blstypes "github.com/productscience/inference/x/bls/types"
	"github.com/productscience/inference/x/inference/keeper"
	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
	"golang.org/x/crypto/sha3"
)

func TestBridgeWithdrawalRecordsReissueAuthorityOnlyOnInitialAdmission(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		name := "disabled"
		if enabled {
			name = "enabled"
		}
		t.Run(name, func(t *testing.T) {
			k, _, ctx, _ := setupKeeperWithMocks(t)
			ctx = ctx.WithChainID("gonka-reissue-test").WithBlockHeight(123).WithTxBytes([]byte("withdrawal"))
			bls := keepertest.NewMockBlsKeeper(gomock.NewController(t))
			k.BlsKeeper = bls
			creator := sdk.AccAddress([]byte("wrapped_contract____")).String()
			const destination = "0x1111111111111111111111111111111111111111"
			const token = "0x3333333333333333333333333333333333333333"
			k.SetWrappedTokenContract(ctx, types.BridgeWrappedTokenContract{
				ChainId: "ethereum", ContractAddress: token, WrappedContractAddress: creator,
			})
			k.SetBridgeContractAddress(ctx, types.BridgeContractAddress{ChainId: "ethereum", Address: destination})
			params := types.DefaultParams()
			if enabled {
				params.BridgeReissueParams = &types.BridgeReissueParams{AllowedTargets: []types.BridgeReissueTarget{
					{ChainId: "ethereum", ContractAddress: destination},
				}}
			}
			require.NoError(t, k.SetParams(ctx, params))
			bls.EXPECT().SetCurrentSigningEpochID(gomock.Any(), uint64(10))
			require.NoError(t, k.SetEffectiveEpochIndex(ctx, 10))
			k.SetEpochGroupData(ctx, types.EpochGroupData{EpochIndex: 10})
			ms := keeper.NewMsgServerWithWasmKeeper(k, mockWasmKeeper{
				GetContractInfoFn: func(_ context.Context, address sdk.AccAddress) *wasmtypes.ContractInfo {
					require.Equal(t, creator, address.String())
					return &wasmtypes.ContractInfo{CodeID: 1, Creator: creator}
				},
			})
			var captured blstypes.SigningData
			bls.EXPECT().RequestThresholdSignature(gomock.Any(), gomock.Any()).DoAndReturn(
				func(_ sdk.Context, data blstypes.SigningData) error { captured = data; return nil },
			)
			response, err := ms.RequestBridgeWithdrawal(ctx, &types.MsgRequestBridgeWithdrawal{
				Creator: creator, UserAddress: testutil.Creator, Amount: "500",
				DestinationAddress:       "0x2222222222222222222222222222222222222222",
				DestinationBridgeAddress: destination,
			})
			require.NoError(t, err)
			if enabled {
				require.Equal(t, []string{testutil.Creator, creator}, captured.ReissueSigners)
			} else {
				require.Empty(t, captured.ReissueSigners)
			}
			require.Equal(t, uint64(10), captured.CurrentEpochId)
			require.Equal(t, hashBridgeRequestIDForCancelTest(response.RequestId), captured.RequestId)
			source := sha256.Sum256([]byte(ctx.ChainID()))
			require.Equal(t, source[:], captured.ChainId)
			require.Len(t, captured.Data, 6)
			require.Equal(t, bytes.Repeat([]byte{0x22}, 20), captured.Data[2])
			require.Equal(t, bytes.Repeat([]byte{0x11}, 20), captured.Data[3])
			require.Equal(t, bytes.Repeat([]byte{0x33}, 20), captured.Data[4])
			require.Equal(t, byte(1), captured.Data[5][30])
			require.Equal(t, byte(244), captured.Data[5][31])
			pending, err := k.BridgeWithdrawalRefundsMap.Get(ctx, hex.EncodeToString(captured.RequestId))
			require.NoError(t, err)
			require.Equal(t, testutil.Creator, pending.UserAddress)
		})
	}
}

// The BLS keeper's cryptographic state machine is covered separately. These
// cases pin the bridge authorization boundary and ensure no caller-supplied
// destination or asset payload can reach it.
func TestReissueBridgeSignatureDestinationAndOwner(t *testing.T) {
	for _, operation := range []string{"MINT_OPERATION", "WITHDRAW_OPERATION"} {
		for _, scenario := range []string{"owner", "second withdrawal signer", "outsider", "legacy", "disabled", "wrong target chain", "wrong target address", "unregistered", "wrong source chain", "malformed payload", "wrong operation"} {
			t.Run(operation+"/"+scenario, func(t *testing.T) {
				k, _, ctx, mocks := setupKeeperWithMocks(t)
				ctx = ctx.WithChainID("gonka-reissue-test")
				bls := keepertest.NewMockBlsKeeper(gomock.NewController(t))
				k.BlsKeeper = bls
				ms := keeper.NewMsgServerImpl(k)
				const requestID = "req_bridge_reissue"
				hash := hashBridgeRequestIDForCancelTest(requestID)
				source := sha256.Sum256([]byte(ctx.ChainID()))
				operationHasher := sha3.NewLegacyKeccak256()
				operationHasher.Write([]byte(operation))
				chain := make([]byte, 32)
				chain[31] = 1
				bridge := bytes.Repeat([]byte{0x11}, 20)
				amount := make([]byte, 32)
				amount[31] = 100
				data := [][]byte{chain, operationHasher.Sum(nil), bytes.Repeat([]byte{0x22}, 20), bridge}
				if operation == "WITHDRAW_OPERATION" {
					data = append(data, bytes.Repeat([]byte{0x33}, 20))
				}
				data = append(data, amount)
				request := &blstypes.ThresholdSigningRequest{RequestId: hash, ChainId: source[:], Data: data, ReissueSigners: []string{testutil.Creator}, Status: blstypes.ThresholdSigningStatus_THRESHOLD_SIGNING_STATUS_COMPLETED}
				creator := testutil.Creator
				allowed := types.BridgeReissueTarget{ChainId: "ethereum", ContractAddress: "0x" + hex.EncodeToString(bridge)}
				params := types.DefaultParams()
				params.BridgeReissueParams = &types.BridgeReissueParams{AllowedTargets: []types.BridgeReissueTarget{allowed}}
				if scenario != "unregistered" {
					k.SetBridgeContractAddress(ctx, types.BridgeContractAddress{ChainId: allowed.ChainId, Address: allowed.ContractAddress})
				}
				switch scenario {
				case "second withdrawal signer":
					if operation == "MINT_OPERATION" {
						t.Skip("mint has one original signer")
					}
					request.ReissueSigners = append(request.ReissueSigners, testutil.Requester)
					creator = testutil.Requester
				case "outsider":
					creator = testutil.Requester
				case "legacy":
					request.ReissueSigners = nil
				case "disabled":
					params.BridgeReissueParams = nil
				case "wrong target chain":
					request.Data[0][31] = 137
				case "wrong target address":
					request.Data[3] = bytes.Repeat([]byte{0x44}, 20)
				case "wrong source chain":
					request.ChainId = bytes.Repeat([]byte{0x55}, 32)
				case "malformed payload":
					request.Data[2] = []byte{1}
				case "wrong operation":
					request.Data[1] = bytes.Repeat([]byte{0x66}, 32)
				}
				require.NoError(t, k.SetParams(ctx, params))
				creatorAddr, err := sdk.AccAddressFromBech32(creator)
				require.NoError(t, err)
				mocks.AccountKeeper.EXPECT().HasAccount(gomock.Any(), creatorAddr).Return(true)
				bls.EXPECT().GetSigningStatus(gomock.Any(), hash).Return(request, nil)
				accept := scenario == "owner" || scenario == "second withdrawal signer"
				if accept {
					bls.EXPECT().SetCurrentSigningEpochID(gomock.Any(), uint64(10))
					require.NoError(t, k.SetEffectiveEpochIndex(ctx, 10))
					k.SetEpochGroupData(ctx, types.EpochGroupData{EpochIndex: 10})
					bls.EXPECT().ReissueThresholdSignature(gomock.Any(), hash, uint64(10)).Return(nil)
				}
				resp, err := ms.ReissueBridgeSignature(ctx, &types.MsgReissueBridgeSignature{Creator: creator, RequestId: requestID})
				if !accept {
					require.Error(t, err)
					require.Nil(t, resp)
					return
				}
				require.NoError(t, err)
				require.Equal(t, uint64(10), resp.EpochIndex)
				// No BankKeeper expectation is installed: any fund movement fails this test.
			})
		}
	}
}

func TestBridgeRefundRejectsPreviouslyCompletedSignatureInTerminalFailureState(t *testing.T) {
	for _, status := range []blstypes.ThresholdSigningStatus{blstypes.ThresholdSigningStatus_THRESHOLD_SIGNING_STATUS_FAILED, blstypes.ThresholdSigningStatus_THRESHOLD_SIGNING_STATUS_EXPIRED} {
		t.Run(status.String(), func(t *testing.T) {
			k, _, ctx, _ := setupKeeperWithMocks(t)
			bls := keepertest.NewMockBlsKeeper(gomock.NewController(t))
			k.BlsKeeper = bls
			id := bytes.Repeat([]byte{0x77}, 32)
			bls.EXPECT().GetSigningStatus(gomock.Any(), id).Return(&blstypes.ThresholdSigningRequest{Status: status, LastCompletedSignature: []byte{1}}, nil)
			refunded, err := k.ProcessAutoRefundForFailedBridgeOperation(ctx, id, "expired reissue")
			require.ErrorContains(t, err, "completed signature")
			require.False(t, refunded)
		})
	}
}
