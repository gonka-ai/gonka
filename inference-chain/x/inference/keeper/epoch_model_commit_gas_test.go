package keeper_test

import (
	"fmt"
	"testing"

	"cosmossdk.io/collections"
	storetypes "cosmossdk.io/store/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
)

// checkGas runs f on a fresh gas meter and returns the gas it used.
func checkGas(t *testing.T, ctx sdk.Context, f func(sdk.Context) error) (uint64, error) {
	t.Helper()
	c := ctx.WithGasMeter(storetypes.NewInfiniteGasMeter())
	err := f(c)
	return c.GasMeter().GasConsumed(), err
}

func TestCheckEpochModelCommit_ReadsOneParticipantEntry(t *testing.T) {
	k, _, ctx, _ := setupKeeperWithMocks(t)
	const epoch = uint64(439)
	models := []string{"MiniMaxAI/MiniMax-M2.7", "deepseek-ai/DeepSeek-V4-Flash-0731", "zai-org/GLM-5.3-Flash"}

	// Sized like mainnet epoch 439: 28 participants, ~16 KB blob.
	ap := types.ActiveParticipants{EpochId: epoch}
	var addrs []sdk.AccAddress
	for i := 0; i < 28; i++ {
		addr := sdk.AccAddress(fmt.Appendf(nil, "participant-%08d", i))
		addrs = append(addrs, addr)
		p := &types.ActiveParticipant{
			Index: addr.String(), ValidatorKey: "Ab1/validator-key-padding-padding-padding=", Weight: 123456,
			InferenceUrl: fmt.Sprintf("http://host-%d.example.org:8000", i), Models: models[i%3 : i%3+1],
		}
		for j := 0; j < 22; j++ {
			p.MlNodes = append(p.MlNodes, &types.ModelMLNodes{MlNodes: []*types.MLNodeInfo{{NodeId: fmt.Sprintf("node-%d-%d", i, j), PocWeight: 123456}}})
		}
		ap.Participants = append(ap.Participants, p)
	}
	require.NoError(t, k.SetActiveParticipants(ctx, ap))
	raw, err := ap.Marshal()
	require.NoError(t, err)

	ok := []*types.PoCV2CommitEntry{{ModelId: models[0]}}
	bad := []*types.PoCV2CommitEntry{{ModelId: models[1]}}
	stranger := sdk.AccAddress("not-a-participant---")

	// Same results as the full scan: member with its model, member with a foreign model, non-member, unknown epoch.
	require.NoError(t, k.CheckEpochModelCommit(ctx, epoch, addrs[0], ok))
	require.ErrorIs(t, k.CheckEpochModelCommit(ctx, epoch, addrs[0], bad), types.ErrInvalidModel)
	require.ErrorIs(t, k.CheckEpochModelCommit(ctx, epoch, stranger, ok), types.ErrParticipantNotFound)
	require.ErrorIs(t, k.CheckEpochModelCommit(ctx, epoch+1, addrs[0], ok), types.ErrIllegalState)

	after, err := checkGas(t, ctx, func(c sdk.Context) error { return k.CheckEpochModelCommit(c, epoch, addrs[0], ok) })
	require.NoError(t, err)

	// An epoch written before the map existed falls back to the full blob.
	require.NoError(t, k.ActiveParticipantModels.Remove(ctx, collections.Join(epoch, addrs[0])))
	require.NoError(t, k.CheckEpochModelCommit(ctx, epoch, addrs[0], ok))
	require.ErrorIs(t, k.CheckEpochModelCommit(ctx, epoch, addrs[0], bad), types.ErrInvalidModel)
	before, err := checkGas(t, ctx, func(c sdk.Context) error { return k.CheckEpochModelCommit(c, epoch, addrs[0], ok) })
	require.NoError(t, err)

	t.Logf("ActiveParticipants %d bytes; gas before %d, after %d", len(raw), before, after)
	require.Less(t, after, before)

	// Rewriting the epoch replaces the entries; pruning by epoch removes them.
	ap.Participants = ap.Participants[1:]
	require.NoError(t, k.SetActiveParticipantsCache(ctx, ap))
	has, err := k.ActiveParticipantModels.Has(ctx, collections.Join(epoch, addrs[0]))
	require.NoError(t, err)
	require.False(t, has)
}
