package keeper_test

import (
	"fmt"
	"testing"

	storetypes "cosmossdk.io/store/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/x/group"
	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

// A vote on an existing bridge tx checks the voter with one Has; its gas
// must not grow with the number of earlier confirmations.
func TestValidateBridgeExchange_VoteGasFlatInPriorConfirmations(t *testing.T) {
	voteGas := func(prior int) uint64 {
		k, ctx, mocks, validator, _ := setupBridgeValidate(t)
		btx := &types.BridgeTransaction{
			ChainId:         "ethereum",
			ContractAddress: "0xabc",
			OwnerAddress:    "gonka1owner",
			Amount:          "100",
			BlockNumber:     "1000",
			ReceiptIndex:    "1",
			ReceiptsRoot:    "0xroot",
			EpochIndex:      335,
			Status:          types.BridgeTransactionStatus_BRIDGE_PENDING,
		}
		k.SetBridgeTransaction(ctx, btx)
		for i := 0; i < prior; i++ {
			other := sdk.AccAddress([]byte(fmt.Sprintf("prior-validator-%04d", i))).String()
			require.NoError(t, k.AddBridgeTransactionValidator(ctx, btx, other))
		}
		mocks.GroupKeeper.EXPECT().GroupMembers(gomock.Any(), gomock.Any()).Return(
			&group.QueryGroupMembersResponse{
				Members: []*group.GroupMember{{
					GroupId: 1,
					Member:  &group.Member{Address: validator, Weight: "10"},
				}},
			}, nil,
		).AnyTimes()
		msg := &types.MsgBridgeExchange{
			Validator:       validator,
			OriginChain:     "ethereum",
			ContractAddress: "0xabc",
			OwnerAddress:    "gonka1owner",
			Amount:          "100",
			BlockNumber:     "1000",
			ReceiptIndex:    "1",
			ReceiptsRoot:    "0xroot",
		}
		gctx := ctx.WithGasMeter(storetypes.NewInfiniteGasMeter())
		res, err := k.ValidateBridgeExchange(gctx, msg)
		require.NoError(t, err)
		require.False(t, res.IsCreate)
		return gctx.GasMeter().GasConsumed()
	}

	base := voteGas(0)
	for _, n := range []int{20, 54} {
		got := voteGas(n)
		t.Logf("prior confirmations %d: vote validation gas %d (no prior: %d)", n, got, base)
		require.Equal(t, base, got, "vote gas grows with prior confirmations")
	}
}
