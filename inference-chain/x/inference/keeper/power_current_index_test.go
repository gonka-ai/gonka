package keeper_test

import (
	"fmt"
	"testing"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
)

func TestGetCurrentEpochGroupIndex_MatchesGroupWithoutDecoding(t *testing.T) {
	k, _, ctx, _ := setupKeeperWithMocks(t)
	sctx := sdk.UnwrapSDKContext(ctx)

	_, err := k.GetCurrentEpochGroupIndex(sctx)
	require.ErrorIs(t, err, types.ErrEffectiveEpochNotFound)

	require.NoError(t, k.SetEffectiveEpochIndex(sctx, 439))
	_, err = k.GetCurrentEpochGroupIndex(sctx)
	require.ErrorIs(t, err, types.ErrEpochGroupDataNotFound)
	_, err = k.GetCurrentEpochGroup(sctx)
	require.ErrorIs(t, err, types.ErrEpochGroupDataNotFound)

	// Root group sized like mainnet epoch 439: 28 weights, 3 sub-models.
	egd := types.EpochGroupData{EpochIndex: 439, PocStartBlockHeight: 6768004,
		SubGroupModels: []string{"MiniMaxAI/MiniMax-M2.7", "deepseek-ai/DeepSeek-V4-Flash-0731", "zai-org/GLM-5.3-Flash"}}
	for i := 0; i < 28; i++ {
		var nodes []*types.MLNodeInfo
		for j := 0; j < 12; j++ {
			nodes = append(nodes, &types.MLNodeInfo{NodeId: fmt.Sprintf("node-%d-%d", i, j), PocWeight: 123456})
		}
		egd.ValidationWeights = append(egd.ValidationWeights, &types.ValidationWeight{
			MemberAddress: sdk.AccAddress(fmt.Appendf(nil, "participant-%08d", i)).String(),
			Weight:        123456, Reputation: 100, ConfirmationWeight: 123456, VotingPower: 123456,
			MlNodes: nodes,
		})
	}
	k.SetEpochGroupData(sctx, egd)
	raw, err := egd.Marshal()
	require.NoError(t, err)

	group, err := k.GetCurrentEpochGroup(sctx)
	require.NoError(t, err)
	idx, err := k.GetCurrentEpochGroupIndex(sctx)
	require.NoError(t, err)
	require.Equal(t, group.GroupData.EpochIndex, idx)

	before, _ := checkGas(t, sctx, func(c sdk.Context) error { _, err := k.GetCurrentEpochGroup(c); return err })
	after, _ := checkGas(t, sctx, func(c sdk.Context) error { _, err := k.GetCurrentEpochGroupIndex(c); return err })
	t.Logf("root EGD %d B: GetCurrentEpochGroup %d gas, GetCurrentEpochGroupIndex %d gas", len(raw), before, after)
	require.Less(t, after, before)
}
