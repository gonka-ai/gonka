package poc

import (
	"testing"

	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
)

// Mainnet guardian kx9mca3x: operator and account forms of the same key.
const (
	guardianValoper = "gonkavaloper1kx9mca3xm8u8ypzfuhmxey66u0ufxhs70mtf0e"
	guardianAcc     = "gonka1kx9mca3xm8u8ypzfuhmxey66u0ufxhs7nm6wc5"
	otherAcc        = "gonka1qwfrtz9c7kcrfkrrlne2pkcye74mj6ce33xdkl"
)

func TestIsGuardianAddress(t *testing.T) {
	params := &types.GenesisGuardianParams{GuardianAddresses: []string{guardianValoper}}
	require.True(t, isGuardianAddress(params, guardianAcc))
	require.False(t, isGuardianAddress(params, otherAcc))
	require.False(t, isGuardianAddress(nil, guardianAcc))
	require.False(t, isGuardianAddress(params, "not-an-address"))
}

func TestCanVoteInModel(t *testing.T) {
	vp := map[string]map[string]int64{
		"model-a": {otherAcc: 10},
		"model-b": {otherAcc: 0},
	}
	require.True(t, canVoteInModel(vp, "model-a", otherAcc, false))
	require.False(t, canVoteInModel(vp, "model-b", otherAcc, false))
	require.False(t, canVoteInModel(vp, "model-c", otherAcc, false))
	require.True(t, canVoteInModel(vp, "model-b", guardianAcc, true))
}

func TestSplitByVotingPowerKeepsGuardKeys(t *testing.T) {
	vp := map[string]map[string]int64{"model-a": {otherAcc: 10}}
	items := []participantWork{
		{address: "p1", modelId: "model-a"},
		{address: "p2", modelId: "model-b"},
	}
	kept, dropped := splitByVotingPower(items, vp, otherAcc, false)
	require.Equal(t, []participantWork{items[0]}, kept)
	require.Equal(t, []string{earlyShareKey("p2", "model-b")}, dropped)

	kept, dropped = splitByVotingPower(items, vp, guardianAcc, true)
	require.Equal(t, items, kept)
	require.Empty(t, dropped)
}
