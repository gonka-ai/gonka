package keeper

import (
	"testing"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/x/group"
	"github.com/productscience/inference/testutil"
	"github.com/stretchr/testify/require"
)

func TestBridgeRequiredPower(t *testing.T) {
	// Nobody removed: a majority of the epoch-start weight, as before.
	require.Equal(t, int64(51), bridgeRequiredPower(100, 100))
	// Mainnet epoch 418: 419861 of 841326 left; the third-of-start floor (280443)
	// is above the live majority (209931).
	require.Equal(t, int64(280443), bridgeRequiredPower(841326, 419861))
	// Most of the group removed: the floor holds.
	require.Equal(t, int64(34), bridgeRequiredPower(100, 10))
	// x/group never counts above the epoch-start weight.
	require.Equal(t, int64(51), bridgeRequiredPower(100, 150))
}

func TestLiveVotedPower_IgnoresVotersRemovedAfterVoting(t *testing.T) {
	sdk.GetConfig().SetBech32PrefixForAccount("gonka", "gonka")
	a := testutil.Validator
	b := testutil.Validator2
	removed := sdk.AccAddress([]byte("removed_voter_______")).String()
	members := []*group.GroupMember{
		{GroupId: 1, Member: &group.Member{Address: a, Weight: "20"}},
		{GroupId: 1, Member: &group.Member{Address: b, Weight: "20"}},
	}
	require.Equal(t, int64(20), liveVotedPower([]string{a, removed}, members))
	require.Equal(t, int64(40), liveVotedPower([]string{a, b, removed}, members))
}
