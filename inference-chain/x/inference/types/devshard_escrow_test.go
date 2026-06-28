package types_test

import (
	"testing"

	"github.com/cosmos/gogoproto/proto"
	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
)

func TestDevshardEscrow_ProtoRoundTrip_Fees(t *testing.T) {
	orig := &types.DevshardEscrow{
		Id:                7,
		Creator:           "gonka1creator",
		Amount:            1_000,
		Slots:             []string{"a", "b"},
		EpochIndex:        3,
		AppHash:           "hash",
		Settled:           false,
		TokenPrice:        2,
		ModelId:           "m1",
		CreateDevshardFee: 10_000,
		FeePerNonce:       1_000,
	}
	bz, err := proto.Marshal(orig)
	require.NoError(t, err)

	var decoded types.DevshardEscrow
	require.NoError(t, proto.Unmarshal(bz, &decoded))
	require.Equal(t, orig, &decoded)
}

func TestDevshardEscrow_LegacyBytes_FeesAbsentDecodeZero(t *testing.T) {
	legacy := &types.DevshardEscrow{
		Id:         1,
		Creator:    "gonka1creator",
		Amount:     100,
		Slots:      []string{"s"},
		EpochIndex: 1,
		AppHash:    "h",
		TokenPrice: 1,
		ModelId:    "m",
	}
	bz, err := proto.Marshal(legacy)
	require.NoError(t, err)

	var withFees types.DevshardEscrow
	require.NoError(t, proto.Unmarshal(bz, &withFees))
	require.Equal(t, uint64(0), withFees.CreateDevshardFee)
	require.Equal(t, uint64(0), withFees.FeePerNonce)
}
