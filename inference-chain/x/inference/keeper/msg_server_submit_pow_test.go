package keeper_test

import (
	"github.com/cosmos/cosmos-sdk/x/auth/ante/testutil"
	"github.com/golang/mock/gomock"
	keepertest "github.com/productscience/inference/testutil/keeper"
	"github.com/productscience/inference/x/inference/types"
	"testing"
)

func TestSubmitPow(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	mockAccountKeeper := testutil.NewMockAccountKeeper(mockCtrl)
	k, ctx := keepertest.InferenceKeeperWithMockAccountKeeper(t, mockAccountKeeper)
	ms := setupMsgServerWithKeeper(k)

	resp, err := ms.SubmitPow(ctx, &types.MsgSubmitPow{
		BlockHeight: 240,
		Nonce:       []string{"helloworld"},
	})
	if err != nil {
		println(err)
	}

	ctx.BlockHeight()

	_ = resp
}
