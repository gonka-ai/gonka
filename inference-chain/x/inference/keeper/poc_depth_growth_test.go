package keeper_test

import (
	"encoding/hex"
	"encoding/json"
	"os"
	"testing"

	storetypes "cosmossdk.io/store/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/testutil"
	"github.com/productscience/inference/x/inference/keeper"
	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
)

func depthGrowthEntries(t *testing.T, model string) []*types.PoCV2CommitEntry {
	t.Helper()
	data, err := os.ReadFile("../../../testutil/testdata/poc_depth_growth.json")
	require.NoError(t, err)
	var fixtures []struct {
		Count uint32
		Root  string
		Depth uint32
	}
	require.NoError(t, json.Unmarshal(data, &fixtures))
	out := make([]*types.PoCV2CommitEntry, 0, len(fixtures))
	for _, f := range fixtures {
		root, err := hex.DecodeString(f.Root)
		require.NoError(t, err)
		out = append(out, &types.PoCV2CommitEntry{ModelId: model, Count: f.Count, RootHash: root, TreeDepth: f.Depth})
	}
	return out
}

func TestDepthGrowthRealHandlers(t *testing.T) {
	for _, challenge := range []bool{false, true} {
		name := "regular"
		if challenge {
			name = "challenge"
		}
		t.Run(name, func(t *testing.T) {
			var k keeper.Keeper
			var ctx sdk.Context
			var ms types.MsgServer
			models := []string{testPoCModelID, testPoCModelID2}
			stage := int64(100)
			if challenge {
				k, ctx, _ = setupChallengeCreate(t, 110)
				seedOpenChallenge(t, k, ctx, stage)
				for _, m := range models {
					k.SetModel(ctx, &types.Model{Id: m})
				}
				require.NoError(t, k.SetActiveParticipants(ctx, types.ActiveParticipants{EpochId: 2, Participants: []*types.ActiveParticipant{{Index: testutil.Executor, Models: models}}}))
				ms = keeper.NewMsgServerImpl(k)
			} else {
				k, ctx, ms = setupPoCV2StoreCommitTest(t, 110, nil, models...)
			}
			submit := func(at sdk.Context, entries ...*types.PoCV2CommitEntry) error {
				if challenge {
					_, err := ms.PoCChallengeStoreCommit(at, &types.MsgPoCChallengeStoreCommit{Creator: testutil.Executor, PocStageStartBlockHeight: stage, Entries: entries})
					return err
				}
				_, err := ms.PoCV2StoreCommit(at, &types.MsgPoCV2StoreCommit{Creator: testutil.Executor, PocStageStartBlockHeight: stage, Entries: entries})
				return err
			}
			query := func() map[string]types.PoCV2StoreCommit {
				if !challenge {
					commits, err := k.GetAllPoCV2StoreCommitsForStage(ctx, stage)
					require.NoError(t, err)
					out := map[string]types.PoCV2StoreCommit{}
					for key, c := range commits {
						out[key.ModelID] = c
					}
					return out
				}
				commits, err := k.ListChallengeCommits(ctx, testutil.Executor)
				require.NoError(t, err)
				out := map[string]types.PoCV2StoreCommit{}
				for _, c := range commits {
					out[c.ModelId] = c
				}
				return out
			}
			a, b := depthGrowthEntries(t, models[0]), depthGrowthEntries(t, models[1])
			for i := range a {
				ctx = ctx.WithBlockHeight(110 + int64(i)).WithGasMeter(storetypes.NewGasMeter(1_000_000_000))
				require.NoError(t, submit(ctx, a[i], b[i]))
				for _, entry := range []*types.PoCV2CommitEntry{a[i], b[i]} {
					got := query()[entry.ModelId]
					require.Equal(t, entry.Count, got.Count)
					require.Equal(t, entry.RootHash, got.RootHash)
					require.Equal(t, entry.TreeDepth, got.TreeDepth)
					if !challenge {
						response, err := k.PoCV2StoreCommit(ctx, &types.QueryPoCV2StoreCommitRequest{PocStageStartBlockHeight: stage, ParticipantAddress: testutil.Executor, ModelId: entry.ModelId})
						require.NoError(t, err)
						require.True(t, response.Found)
						require.Equal(t, entry.Count, response.Count)
						require.Equal(t, entry.RootHash, response.RootHash)
						require.Equal(t, entry.TreeDepth, response.TreeDepth)
					} else {
						response, err := k.OpenPoCChallenges(ctx, &types.QueryOpenPoCChallengesRequest{})
						require.NoError(t, err)
						require.Len(t, response.Challenges, 1)
						found := false
						for _, commit := range response.Challenges[0].Commits {
							if commit.ModelId == entry.ModelId {
								found = true
								require.Equal(t, entry.Count, commit.Count)
								require.Equal(t, entry.RootHash, commit.RootHash)
								require.Equal(t, entry.TreeDepth, commit.TreeDepth)
							}
						}
						require.True(t, found)
					}

				}

				next := *a[i]
				next.Count++
				require.ErrorContains(t, submit(ctx, &next), "only one commit per block")
			}

			before := query()
			for _, bad := range []struct {
				depth, count uint32
				message      string
			}{{31, 7, "must not decrease"}, {0, 7, "must be in"}, {33, 7, "must be in"}, {32, 6, "count must increase"}} {
				ctx = ctx.WithBlockHeight(ctx.BlockHeight() + 1).WithGasMeter(storetypes.NewGasMeter(1_000_000_000))
				good, invalid := *a[len(a)-1], *b[len(b)-1]
				good.Count++
				invalid.Count = bad.count
				invalid.TreeDepth = bad.depth
				require.ErrorContains(t, submit(ctx, &good, &invalid), bad.message)
				require.Equal(t, before, query())
			}
			require.Error(t, submit(ctx, a[len(a)-1], a[len(a)-1]))

			if challenge {
				_, err := ms.PoCChallengeStoreCommit(ctx, &types.MsgPoCChallengeStoreCommit{Creator: testutil.Executor, PocStageStartBlockHeight: stage + 1, Entries: []*types.PoCV2CommitEntry{a[0]}})
				require.Error(t, err)
			}
		})
	}
}
