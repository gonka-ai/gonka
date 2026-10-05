package keeper_test

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"testing"

	sdk "github.com/cosmos/cosmos-sdk/types"
	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
	authztypes "github.com/cosmos/cosmos-sdk/x/authz"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/productscience/inference/testutil"
	"github.com/productscience/inference/x/inference/keeper"
	"github.com/productscience/inference/x/inference/types"
)

// setupClaim prepares a claimable epoch-100 settle record for testutil.Creator; payErr is
// what every bank transfer of the payout returns.
func setupClaim(t *testing.T, payErr error) (keeper.Keeper, types.MsgServer, sdk.Context) {
	k, ms, ctx, mocks := setupKeeperWithMocks(t)
	account := NewMockAccount(testutil.Creator)
	seedBytes := make([]byte, 8)
	binary.BigEndian.PutUint64(seedBytes, 1)
	signature, err := account.key.Sign(seedBytes)
	require.NoError(t, err)

	k.SetEpoch(ctx, &types.Epoch{Index: 100, PocStartBlockHeight: 1000})
	k.SetEpoch(ctx, &types.Epoch{Index: 101, PocStartBlockHeight: 2000})
	require.NoError(t, k.SetEffectiveEpochIndex(ctx, 101))
	for _, epoch := range []uint64{100, 101} {
		k.SetEpochGroupData(ctx, types.EpochGroupData{
			EpochIndex:        epoch,
			EpochGroupId:      epoch,
			ValidationWeights: []*types.ValidationWeight{{MemberAddress: testutil.Creator, Weight: 10}},
		})
		k.SetActiveParticipants(ctx, types.ActiveParticipants{
			EpochId:      epoch,
			Participants: []*types.ActiveParticipant{{Index: testutil.Creator}},
		})
	}
	addr := sdk.MustAccAddressFromBech32(testutil.Creator)
	require.NoError(t, k.Participants.Set(ctx, addr, types.Participant{Index: testutil.Creator, Address: testutil.Creator, Status: types.ParticipantStatus_ACTIVE}))
	require.NoError(t, k.SetSettleAmount(ctx, types.SettleAmount{
		Participant:   testutil.Creator,
		EpochIndex:    100,
		WorkCoins:     1000,
		RewardCoins:   500,
		SeedSignature: hex.EncodeToString(signature),
	}))
	require.NoError(t, k.SetEpochPerformanceSummary(ctx, types.EpochPerformanceSummary{EpochIndex: 100, ParticipantId: testutil.Creator}))
	require.NoError(t, k.SeedEpochGroupValidationEntries(ctx, types.EpochGroupValidations{
		Participant: testutil.Creator, EpochIndex: 100, ValidatedInferences: []string{"inference1"},
	}))

	mocks.AccountKeeper.EXPECT().HasAccount(gomock.Any(), addr).Return(true).AnyTimes()
	mocks.AccountKeeper.EXPECT().GetAccount(gomock.Any(), addr).Return(account).AnyTimes()
	mocks.AuthzKeeper.EXPECT().GranterGrants(gomock.Any(), gomock.Any()).Return(&authztypes.QueryGranterGrantsResponse{}, nil).AnyTimes()
	mocks.BankKeeper.EXPECT().SendCoinsFromModuleToAccount(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Return(payErr).AnyTimes()
	mocks.BankKeeper.EXPECT().SendCoinsFromModuleToModule(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Return(payErr).AnyTimes()
	mocks.StreamVestingKeeper.EXPECT().AddVestedRewards(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
	return k, ms, ctx
}

// A paid claim deletes the settle record, so it must not write the attempt height first.
func TestClaimRewards_PaidClaimDoesNotRewriteSettleRecord(t *testing.T) {
	_, ms, ctx := setupClaim(t, nil)
	var trace bytes.Buffer
	ctx = ctx.WithBlockHeight(100)
	ctx.MultiStore().SetTracer(&trace)
	resp, err := ms.ClaimRewards(ctx, &types.MsgClaimRewards{Creator: testutil.Creator, EpochIndex: 100, Seed: 1})
	ctx.MultiStore().SetTracer(nil)
	require.NoError(t, err)
	require.Equal(t, "Rewards claimed successfully", resp.Result)

	settleKey := append(types.SettleAmountPrefix.Bytes(), sdk.MustAccAddressFromBech32(testutil.Creator)...)
	writes, deletes := 0, 0
	for _, line := range bytes.Split(trace.Bytes(), []byte("\n")) {
		var op struct{ Operation, Key string }
		if json.Unmarshal(line, &op) != nil {
			continue
		}
		key, _ := base64.StdEncoding.DecodeString(op.Key)
		if !bytes.Equal(key, settleKey) {
			continue
		}
		switch op.Operation {
		case "write":
			writes++
		case "delete":
			deletes++
		}
	}
	require.Equal(t, 0, writes)
	require.Equal(t, 1, deletes)
}

// A claim whose payout fails keeps the record with the attempt height, and a retry
// inside the 30-block window is rate limited.
func TestClaimRewards_FailedClaimRecordsAttempt(t *testing.T) {
	k, ms, ctx := setupClaim(t, sdkerrors.ErrInsufficientFunds)
	resp, err := ms.ClaimRewards(ctx.WithBlockHeight(100), &types.MsgClaimRewards{Creator: testutil.Creator, EpochIndex: 100, Seed: 1})
	require.NoError(t, err)
	require.Equal(t, uint64(0), resp.Amount)
	require.NotEqual(t, "Rewards claimed successfully", resp.Result)

	settle, found := k.GetSettleAmount(ctx, testutil.Creator)
	require.True(t, found)
	require.Equal(t, int64(100), settle.LastClaimAttempt)

	resp, err = ms.ClaimRewards(ctx.WithBlockHeight(110), &types.MsgClaimRewards{Creator: testutil.Creator, EpochIndex: 100, Seed: 1})
	require.NoError(t, err)
	require.Equal(t, "Claim rate limited", resp.Result)
}
