package keeper_test

import (
	"testing"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	keepertest "github.com/productscience/inference/testutil/keeper"
	"github.com/productscience/inference/testutil"
	"github.com/productscience/inference/x/inference/types"
)

// A missing preserved snapshot is treated as "no nodes preserved for this episode".
// The filter then excludes every participant from the inference-during-PoC pool; the
// downstream GetRandomMemberForModel returns a typed not-found error rather than the
// transient codes.Unavailable the earlier hard-fail produced.
func TestGetRandomExecutorWithoutPreservedSnapshotDoesNotHardFail(t *testing.T) {
	k, ctx := keepertest.InferenceKeeper(t)
	sdkCtx := sdk.UnwrapSDKContext(ctx).WithBlockHeight(150)

	require.NoError(t, k.SetEpoch(sdkCtx, &types.Epoch{Index: 1, PocStartBlockHeight: 100}))
	require.NoError(t, k.SetEffectiveEpochIndex(sdkCtx, 1))
	require.NoError(t, k.SetActiveConfirmationPoCEvent(sdkCtx, types.ConfirmationPoCEvent{
		EpochIndex:    1,
		TriggerHeight: 140,
		Phase:         types.ConfirmationPoCPhase_CONFIRMATION_POC_GENERATION,
	}))
	require.NoError(t, k.SetActiveParticipants(sdkCtx, types.ActiveParticipants{
		EpochId: 1,
		Participants: []*types.ActiveParticipant{
			{
				Index:  testutil.Executor,
				Models: []string{"model-a"},
				MlNodes: []*types.ModelMLNodes{
					{
						MlNodes: []*types.MLNodeInfo{
							{NodeId: "node-1", PocWeight: 10},
						},
					},
				},
			},
		},
	}))

	_, err := k.GetRandomExecutor(sdkCtx, &types.QueryGetRandomExecutorRequest{Model: "model-a"})
	require.Error(t, err)
	require.NotEqual(t, codes.Unavailable, status.Code(err),
		"missing preserved snapshot should not surface codes.Unavailable to clients")
}
