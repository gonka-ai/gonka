package keeper_test

import (
	"testing"

	"go.uber.org/mock/gomock"

	sdk "github.com/cosmos/cosmos-sdk/types"
	keepertest "github.com/productscience/inference/testutil/keeper"
	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
)

func TestParticipantsWithBalances(t *testing.T) {
	k, ctx, mocks := keepertest.InferenceKeeperReturningMocks(t)

	addr1 := sdk.AccAddress([]byte("addr1_______________"))
	addr2 := sdk.AccAddress([]byte("addr2_______________"))

	k.Participants.Set(ctx, addr1, types.Participant{Address: addr1.String(), InferenceUrl: "http://node1"})
	k.Participants.Set(ctx, addr2, types.Participant{Address: addr2.String(), InferenceUrl: "http://node2"})

	mocks.BankViewKeeper.EXPECT().IterateAllBalances(gomock.Any(), gomock.Any()).DoAndReturn(
		func(_ interface{}, cb func(sdk.AccAddress, sdk.Coin) bool) {
			cb(addr1, sdk.NewInt64Coin("ngonka", 1000))
			cb(addr2, sdk.NewInt64Coin("ngonka", 2000))
		},
	)

	resp, err := k.ParticipantsWithBalances(ctx, &types.QueryParticipantsWithBalancesRequest{})
	require.NoError(t, err)
	require.Len(t, resp.Participants, 2)
}

