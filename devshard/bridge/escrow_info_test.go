package bridge

import (
	"encoding/hex"
	"testing"

	inferencetypes "github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
)

func TestEscrowInfoFromQuery_MapsSessionTimeouts(t *testing.T) {
	appHash := []byte{1, 2, 3, 4}
	got, err := EscrowInfoFromQuery(42, &inferencetypes.DevshardEscrow{
		Creator:                   "gonka1creator",
		AppHash:                   hex.EncodeToString(appHash),
		Slots:                     []string{"h0", "h1"},
		ModelId:                   "test-model",
		Amount:                    99,
		TokenPrice:                7,
		CreateDevshardFee:         12_345,
		FeePerNonce:               19,
		InferenceSealGraceNonces:  9,
		InferenceSealGraceSeconds: 77,
		AutoSealEveryNNonces:      21,
		ValidationRate:            7_777,
		VoteThresholdFactor:       67,
		RefusalTimeout:            5,
		ExecutionTimeout:          17,
		EpochIndex:                3,
		Settled:                   true,
	})
	require.NoError(t, err)
	require.Equal(t, "42", got.EscrowID)
	require.Equal(t, uint64(99), got.Amount)
	require.Equal(t, "gonka1creator", got.CreatorAddress)
	require.Equal(t, appHash, got.AppHash)
	require.Equal(t, []string{"h0", "h1"}, got.Slots)
	require.Equal(t, "test-model", got.ModelID)
	require.Equal(t, uint64(7), got.TokenPrice)
	require.Equal(t, uint64(12_345), got.CreateDevshardFee)
	require.Equal(t, uint64(19), got.FeePerNonce)
	require.Equal(t, uint32(9), got.InferenceSealGraceNonces)
	require.Equal(t, uint32(77), got.InferenceSealGraceSeconds)
	require.Equal(t, uint32(21), got.AutoSealEveryNNonces)
	require.Equal(t, uint32(7_777), got.ValidationRate)
	require.Equal(t, uint32(67), got.VoteThresholdFactor)
	require.Equal(t, int64(5), got.RefusalTimeout)
	require.Equal(t, int64(17), got.ExecutionTimeout)
	require.Equal(t, uint64(3), got.EpochID)
	require.True(t, got.Settled)
}

func TestEscrowInfoFromQuery_NilEscrow(t *testing.T) {
	_, err := EscrowInfoFromQuery(1, nil)
	require.Error(t, err)
}
