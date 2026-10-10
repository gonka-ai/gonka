package keeper

import (
	"math"
	"testing"

	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
)

func TestTotalForfeitedWorkCoins(t *testing.T) {
	participants := []types.Participant{
		{Status: types.ParticipantStatus_ACTIVE, CoinBalance: 100},
		{Status: types.ParticipantStatus_INVALID, CoinBalance: 200},
		{Status: types.ParticipantStatus_INACTIVE, CoinBalance: 300},
		{Status: types.ParticipantStatus_UNSPECIFIED, CoinBalance: 400},
		{Status: types.ParticipantStatus_INVALID, CoinBalance: 0},
		{Status: types.ParticipantStatus_INACTIVE, CoinBalance: -500},
	}

	total, err := totalForfeitedWorkCoins(participants)
	require.NoError(t, err)
	require.Equal(t, int64(500), total)
}

func TestTotalForfeitedWorkCoinsRejectsOverflow(t *testing.T) {
	participants := []types.Participant{
		{Status: types.ParticipantStatus_INVALID, CoinBalance: math.MaxInt64},
		{Status: types.ParticipantStatus_INACTIVE, CoinBalance: 1},
	}

	_, err := totalForfeitedWorkCoins(participants)
	require.ErrorContains(t, err, "exceeds int64")
}
