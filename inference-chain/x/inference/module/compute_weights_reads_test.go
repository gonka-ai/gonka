package inference_test

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	storetypes "cosmossdk.io/store/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/stretchr/testify/require"

	"github.com/productscience/inference/testutil"
	keepertest "github.com/productscience/inference/testutil/keeper"
	inference "github.com/productscience/inference/x/inference/module"
	"github.com/productscience/inference/x/inference/types"
)

// A participant with commits for two models has its participant record, seed and
// allowlist entry read once per ComputeNewWeights, not once per model
// (tracekv does not trace Has, so the allowlist shows only in gas).
func TestComputeNewWeightsReadsParticipantStateOncePerAddress(t *testing.T) {
	sdk.GetConfig().SetBech32PrefixForAccount("gonka", "gonkapub")
	k, ctx, mocks := keepertest.InferenceKeeperReturningMocks(t)
	mocks.StubForInitGenesis(ctx)
	inference.InitGenesis(ctx, k, mocks.StubGenesisState())
	am := inference.NewAppModule(nil, k, nil, nil, nil, nil)

	addrs := []string{testutil.Executor, testutil.Executor2}
	for _, addr := range addrs {
		for _, model := range []string{"model-a", "model-b"} {
			require.NoError(t, k.SetPoCV2StoreCommit(ctx, types.PoCV2StoreCommit{
				ParticipantAddress:       addr,
				PocStageStartBlockHeight: 100,
				Count:                    1,
				RootHash:                 make([]byte, 32),
				CommitBlockHeight:        100,
				ModelId:                  model,
			}))
			require.NoError(t, k.SetMLNodeWeightDistribution(ctx, types.MLNodeWeightDistribution{
				ParticipantAddress:       addr,
				PocStageStartBlockHeight: 100,
				Weights:                  []*types.MLNodeWeight{{NodeId: addr + "-" + model, Weight: 1}},
				ModelId:                  model,
			}))
		}
		require.NoError(t, k.SetParticipant(ctx, types.Participant{Index: addr, Address: addr, ValidatorKey: "vk-" + addr}))
		k.SetRandomSeed(ctx, types.RandomSeed{Participant: addr, EpochIndex: 1, Signature: "sig-" + addr})
		acc, err := sdk.AccAddressFromBech32(addr)
		require.NoError(t, err)
		require.NoError(t, k.ParticipantAllowListSet.Set(ctx, acc))
	}
	params, err := k.GetParams(ctx)
	require.NoError(t, err)
	params.ParticipantAccessParams.UseParticipantAllowlist = true
	require.NoError(t, k.SetParams(ctx, params))

	prefixes := map[string][]byte{
		"participant": k.Participants.GetPrefix(),
		"seed":        k.RandomSeeds.GetPrefix(),
	}
	var trace bytes.Buffer
	ctx = ctx.WithGasMeter(storetypes.NewInfiniteGasMeter())
	ctx.MultiStore().SetTracer(&trace)
	am.ComputeNewWeights(ctx, types.Epoch{Index: 1, PocStartBlockHeight: 100})
	ctx.MultiStore().SetTracer(nil)

	reads := map[string]int{}
	for _, line := range strings.Split(trace.String(), "\n") {
		var op struct {
			Operation string `json:"operation"`
			Key       string `json:"key"`
		}
		if line == "" || json.Unmarshal([]byte(line), &op) != nil || op.Operation != "read" {
			continue
		}
		key, err := base64.StdEncoding.DecodeString(op.Key)
		require.NoError(t, err)
		for name, p := range prefixes {
			if bytes.HasPrefix(key, p) {
				reads[name]++
			}
		}
	}
	t.Logf("reads=%v gas=%d", reads, ctx.GasMeter().GasConsumed())
	require.Equal(t, len(addrs), reads["participant"])
	require.Equal(t, len(addrs), reads["seed"])
}
