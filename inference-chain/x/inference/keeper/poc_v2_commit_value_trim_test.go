package keeper_test

import (
	"strings"
	"testing"

	"cosmossdk.io/collections"
	storetypes "cosmossdk.io/store/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/stretchr/testify/require"

	"github.com/productscience/inference/testutil"
	"github.com/productscience/inference/x/inference/types"
)

func storeCommitKey(participant string) collections.Triple[int64, sdk.AccAddress, string] {
	return collections.Join3(int64(100), sdk.MustAccAddressFromBech32(participant), mainnetPoCModelID)
}

func commitAndDistribute(t *testing.T, ctx sdk.Context, ms types.MsgServer, creator string) (commitGas, distGas storetypes.Gas) {
	t.Helper()
	cctx := ctx.WithGasMeter(storetypes.NewInfiniteGasMeter())
	_, err := ms.PoCV2StoreCommit(cctx, &types.MsgPoCV2StoreCommit{
		Creator:                  creator,
		PocStageStartBlockHeight: 100,
		Entries:                  []*types.PoCV2CommitEntry{makePoCV2CommitEntry(mainnetPoCModelID, 3, 7)},
	})
	require.NoError(t, err)
	dctx := ctx.WithBlockHeight(160).WithGasMeter(storetypes.NewInfiniteGasMeter())
	_, err = ms.MLNodeWeightDistribution(dctx, &types.MsgMLNodeWeightDistribution{
		Creator:                  creator,
		PocStageStartBlockHeight: 100,
		Entries: []*types.MLNodeDistributionEntry{{
			ModelId: mainnetPoCModelID,
			Weights: []*types.MLNodeWeight{{NodeId: "node-1", Weight: 1}, {NodeId: "node-2", Weight: 2}},
		}},
	})
	require.NoError(t, err)
	return cctx.GasMeter().GasConsumed(), dctx.GasMeter().GasConsumed()
}

func fullCommit(creator string) types.PoCV2StoreCommit {
	return types.PoCV2StoreCommit{
		ParticipantAddress:       creator,
		PocStageStartBlockHeight: 100,
		Count:                    3,
		RootHash:                 makePoCV2CommitEntry(mainnetPoCModelID, 3, 7).RootHash,
		CommitBlockHeight:        110,
		ModelId:                  mainnetPoCModelID,
		TreeDepth:                24,
	}
}

func fullDistribution(creator string) types.MLNodeWeightDistribution {
	return types.MLNodeWeightDistribution{
		ParticipantAddress:       creator,
		PocStageStartBlockHeight: 100,
		Weights:                  []*types.MLNodeWeight{{NodeId: "node-1", Weight: 1}, {NodeId: "node-2", Weight: 2}},
		ModelId:                  mainnetPoCModelID,
	}
}

// Stored values keep only what the key does not hold; every reader gets the full record back.
func TestPoCV2StoreCommitAndDistribution_ValuesDropKeyFields(t *testing.T) {
	k, ctx, ms := setupPoCV2StoreCommitTest(t, 110, nil, mainnetPoCModelID)
	commitGas, distGas := commitAndDistribute(t, ctx, ms, testutil.Executor)
	t.Logf("PoCV2StoreCommit gas %d, MLNodeWeightDistribution gas %d", commitGas, distGas)

	rawCommit, err := k.PoCV2StoreCommits.Get(ctx, storeCommitKey(testutil.Executor))
	require.NoError(t, err)
	wantRaw := fullCommit(testutil.Executor)
	wantRaw.ParticipantAddress, wantRaw.PocStageStartBlockHeight, wantRaw.ModelId = "", 0, ""
	require.Equal(t, wantRaw, rawCommit)
	rawDist, err := k.MLNodeWeightDistributions.Get(ctx, storeCommitKey(testutil.Executor))
	require.NoError(t, err)
	require.Equal(t, types.MLNodeWeightDistribution{Weights: fullDistribution(testutil.Executor).Weights}, rawDist)

	key := commitKey(testutil.Executor, mainnetPoCModelID)
	commits, err := k.GetAllPoCV2StoreCommitsForStage(ctx, 100)
	require.NoError(t, err)
	require.Equal(t, map[types.PoCParticipantModelKey]types.PoCV2StoreCommit{key: fullCommit(testutil.Executor)}, commits)
	dists, err := k.GetAllMLNodeWeightDistributionsForStage(ctx, 100)
	require.NoError(t, err)
	require.Equal(t, map[types.PoCParticipantModelKey]types.MLNodeWeightDistribution{key: fullDistribution(testutil.Executor)}, dists)

	qd, err := k.AllMLNodeWeightDistributionsForStage(ctx, &types.QueryAllMLNodeWeightDistributionsForStageRequest{PocStageStartBlockHeight: 100})
	require.NoError(t, err)
	require.Equal(t, []*types.MLNodeWeightDistributionWithAddress{{
		ParticipantAddress: testutil.Executor,
		ModelId:            mainnetPoCModelID,
		Weights:            fullDistribution(testutil.Executor).Weights,
	}}, qd.Distributions)

	// The trimmed commit still drives the next-commit checks.
	_, err = ms.PoCV2StoreCommit(ctx.WithBlockHeight(111), &types.MsgPoCV2StoreCommit{
		Creator:                  testutil.Executor,
		PocStageStartBlockHeight: 100,
		Entries:                  []*types.PoCV2CommitEntry{makePoCV2CommitEntry(mainnetPoCModelID, 3, 8)},
	})
	require.ErrorContains(t, err, "count must increase")
}

// Records written before the change carry every field and read back unchanged.
func TestPoCV2StoreCommitAndDistribution_ReadFullLegacyValues(t *testing.T) {
	k, ctx, _ := setupPoCV2StoreCommitTest(t, 110, nil, mainnetPoCModelID)
	require.NoError(t, k.PoCV2StoreCommits.Set(ctx, storeCommitKey(testutil.Executor), fullCommit(testutil.Executor)))
	require.NoError(t, k.MLNodeWeightDistributions.Set(ctx, storeCommitKey(testutil.Executor), fullDistribution(testutil.Executor)))
	require.NoError(t, k.SetPoCV2StoreCommit(ctx, fullCommit(testutil.Executor2)))
	require.NoError(t, k.SetMLNodeWeightDistribution(ctx, fullDistribution(testutil.Executor2)))

	commits, err := k.GetAllPoCV2StoreCommitsForStage(ctx, 100)
	require.NoError(t, err)
	require.Equal(t, map[types.PoCParticipantModelKey]types.PoCV2StoreCommit{
		commitKey(testutil.Executor, mainnetPoCModelID):  fullCommit(testutil.Executor),
		commitKey(testutil.Executor2, mainnetPoCModelID): fullCommit(testutil.Executor2),
	}, commits)
	dists, err := k.GetAllMLNodeWeightDistributionsForStage(ctx, 100)
	require.NoError(t, err)
	require.Equal(t, map[types.PoCParticipantModelKey]types.MLNodeWeightDistribution{
		commitKey(testutil.Executor, mainnetPoCModelID):  fullDistribution(testutil.Executor),
		commitKey(testutil.Executor2, mainnetPoCModelID): fullDistribution(testutil.Executor2),
	}, dists)
}

// Upper-case bech32 is accepted by AccAddressFromBech32; such a record keeps every field.
func TestPoCV2StoreCommitAndDistribution_NonCanonicalAddressKeepsFields(t *testing.T) {
	k, ctx, _ := setupPoCV2StoreCommitTest(t, 110, nil, mainnetPoCModelID)
	up := strings.ToUpper(testutil.Executor)
	require.NoError(t, k.SetPoCV2StoreCommit(ctx, fullCommit(up)))
	require.NoError(t, k.SetMLNodeWeightDistribution(ctx, fullDistribution(up)))

	raw, err := k.PoCV2StoreCommits.Get(ctx, storeCommitKey(testutil.Executor))
	require.NoError(t, err)
	require.Equal(t, fullCommit(up), raw)
	dists, err := k.GetAllMLNodeWeightDistributionsForStage(ctx, 100)
	require.NoError(t, err)
	require.Equal(t, fullDistribution(up), dists[commitKey(testutil.Executor, mainnetPoCModelID)])
}

// A record with nothing outside the key would encode to an empty value; it keeps every field.
func TestPoCV2StoreCommitAndDistribution_EmptyRecordKeepsFields(t *testing.T) {
	k, ctx, _ := setupPoCV2StoreCommitTest(t, 110, nil, mainnetPoCModelID)
	c := types.PoCV2StoreCommit{ParticipantAddress: testutil.Executor, PocStageStartBlockHeight: 100, ModelId: mainnetPoCModelID}
	d := types.MLNodeWeightDistribution{ParticipantAddress: testutil.Executor, PocStageStartBlockHeight: 100, ModelId: mainnetPoCModelID}
	require.NoError(t, k.SetPoCV2StoreCommit(ctx, c))
	require.NoError(t, k.SetMLNodeWeightDistribution(ctx, d))
	commits, err := k.GetAllPoCV2StoreCommitsForStage(ctx, 100)
	require.NoError(t, err)
	require.Equal(t, c, commits[commitKey(testutil.Executor, mainnetPoCModelID)])
	dists, err := k.GetAllMLNodeWeightDistributionsForStage(ctx, 100)
	require.NoError(t, err)
	require.Equal(t, d, dists[commitKey(testutil.Executor, mainnetPoCModelID)])
}
