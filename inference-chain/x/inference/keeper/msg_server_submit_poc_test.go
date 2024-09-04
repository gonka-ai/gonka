package keeper_test

import (
	keepertest "github.com/productscience/inference/testutil/keeper"
	"github.com/productscience/inference/x/inference/types"
	"testing"
)

func TestSubmitPoC(t *testing.T) {
	k, ctx, mocks := keepertest.InferenceKeeperReturningMock(t)
	_ = mocks
	ms := setupMsgServerWithKeeper(k)

	resp, err := ms.SubmitPoC(ctx, &types.MsgSubmitPoC{
		BlockHeight: 240,
		Nonce:       []string{"helloworld"},
	})
	if err != nil {
		println(err)
	}

	ctx.BlockHeight()

	_ = resp
}
