package keeper_test

import (
	"testing"

	"cosmossdk.io/errors"
	"github.com/stretchr/testify/require"

	keepertest "github.com/productscience/inference/testutil/keeper"
	"github.com/productscience/inference/x/bls/keeper"
	"github.com/productscience/inference/x/bls/types"
)

func TestRequestThresholdSignatureMsgDeprecated(t *testing.T) {
	k, ctx := keepertest.BlsKeeper(t)
	ms := keeper.NewMsgServerImpl(k)

	msg := &types.MsgRequestThresholdSignature{
		Creator:        "gonka1hgt9lxxxwpsnc3yn2nheqqy9a8vlcjwvgzpve2",
		CurrentEpochId: 1,
		ChainId:        make([]byte, 32),
		RequestId:      []byte("request-id-32-bytes-----------"),
		Data:           [][]byte{make([]byte, 32)},
	}

	resp, err := ms.RequestThresholdSignature(ctx, msg)

	require.Nil(t, resp)
	require.True(t, errors.IsOf(err, types.ErrDeprecated), "err=%v", err)
	require.Contains(t, err.Error(), "threshold signature request message is deprecated")

	_, statusErr := k.GetSigningStatus(ctx, msg.RequestId)
	require.Error(t, statusErr)
}
