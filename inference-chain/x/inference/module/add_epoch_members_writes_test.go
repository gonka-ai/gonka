package inference

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	storetypes "cosmossdk.io/store/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/stretchr/testify/require"

	"github.com/productscience/inference/x/inference/types"
)

// addEpochMembers reads and writes each EpochGroupData record a fixed number of times,
// however many members the epoch has; the stored result is the same as member-by-member.
func TestAddEpochMembersGroupDataAccessIsFlat(t *testing.T) {
	small := addEpochMembersRun(t, 3)
	large := addEpochMembersRun(t, 30)
	require.Equal(t, small.reads, large.reads)
	require.Equal(t, small.writes, large.writes)

	require.Len(t, large.root.ValidationWeights, 30)
	require.Len(t, large.root.MemberSeedSignatures, 30)
	require.Equal(t, int64(30*10), large.root.TotalWeight)
	for i, vw := range large.root.ValidationWeights {
		require.Equal(t, large.members[i], vw.MemberAddress)
		require.Equal(t, large.members[i], large.root.MemberSeedSignatures[i].MemberAddress)
	}
	for _, model := range []string{"model-a", "model-b"} {
		sub := large.subs[model]
		require.Len(t, sub.ValidationWeights, 30, model)
		require.Len(t, sub.MemberSeedSignatures, 30, model)
		for i, vw := range sub.ValidationWeights {
			require.Equal(t, large.members[i], vw.MemberAddress)
			require.Len(t, vw.MlNodes, 1)
			require.Equal(t, large.members[i]+"-"+model, vw.MlNodes[0].NodeId)
		}
		require.Equal(t, int64(30*5), sub.TotalWeight, model)
	}
}

type addEpochMembersResult struct {
	members       []string
	reads, writes int
	gas           storetypes.Gas
	root          types.EpochGroupData
	subs          map[string]types.EpochGroupData
}

func addEpochMembersRun(t *testing.T, n int) addEpochMembersResult {
	t.Helper()
	fixture := newFormationRecoveryFixture(t, noopCollateralKeeper{}, 1, 2)
	ctx := fixture.ctx
	fixture.keeper.SetModel(ctx, &types.Model{Id: "model-b", ProposedBy: "genesis"})
	eg, err := fixture.keeper.GetEpochGroupForEpoch(ctx, fixture.upcomingEpoch)
	require.NoError(t, err)

	res := addEpochMembersResult{subs: map[string]types.EpochGroupData{}}
	participants := make([]*types.ActiveParticipant, n)
	for i := range participants {
		addr := sdk.AccAddress(fmt.Sprintf("epoch-member-%07d", i)).String()
		res.members = append(res.members, addr)
		participants[i] = &types.ActiveParticipant{
			Index:        addr,
			ValidatorKey: "pubkey-" + addr,
			Weight:       10,
			Models:       []string{"model-a", "model-b"},
			Seed:         &types.RandomSeed{Participant: addr, Signature: "seed-" + addr},
			MlNodes: []*types.ModelMLNodes{
				{MlNodes: []*types.MLNodeInfo{{NodeId: addr + "-model-a", PocWeight: 5, Throughput: 1}}},
				{MlNodes: []*types.MLNodeInfo{{NodeId: addr + "-model-b", PocWeight: 5, Throughput: 1}}},
			},
			VotingPowers: []*types.ModelVotingPower{{ModelId: "model-a", VotingPower: 5}, {ModelId: "model-b", VotingPower: 5}},
		}
	}

	prefix := fixture.keeper.EpochGroupDataMap.GetPrefix()
	var trace bytes.Buffer
	ctx = ctx.WithGasMeter(storetypes.NewInfiniteGasMeter())
	ctx.MultiStore().SetTracer(&trace)
	fixture.module.addEpochMembers(ctx, eg, participants)
	ctx.MultiStore().SetTracer(nil)
	res.gas = ctx.GasMeter().GasConsumed()

	for _, line := range strings.Split(trace.String(), "\n") {
		var op struct {
			Operation string `json:"operation"`
			Key       string `json:"key"`
		}
		if line == "" || json.Unmarshal([]byte(line), &op) != nil {
			continue
		}
		key, err := base64.StdEncoding.DecodeString(op.Key)
		require.NoError(t, err)
		if !bytes.HasPrefix(key, prefix) {
			continue
		}
		switch op.Operation {
		case "read":
			res.reads++
		case "write":
			res.writes++
		}
	}
	t.Logf("members=%d egd reads=%d writes=%d gas=%d", n, res.reads, res.writes, res.gas)

	root, found := fixture.keeper.GetEpochGroupData(ctx, fixture.upcomingEpoch.Index, "")
	require.True(t, found)
	res.root = root
	for _, model := range []string{"model-a", "model-b"} {
		sub, found := fixture.keeper.GetEpochGroupData(ctx, fixture.upcomingEpoch.Index, model)
		require.True(t, found, model)
		res.subs[model] = sub
	}
	return res
}
