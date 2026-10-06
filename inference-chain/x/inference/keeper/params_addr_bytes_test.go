package keeper_test

import (
	"crypto/sha256"
	"strings"
	"testing"

	storetypes "cosmossdk.io/store/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/stretchr/testify/require"

	keepertest "github.com/productscience/inference/testutil/keeper"
	"github.com/productscience/inference/x/inference/keeper"
	"github.com/productscience/inference/x/inference/types"
)

func testAddrs(n int, salt byte) []string {
	out := make([]string, n)
	for i := range out {
		h := sha256.Sum256([]byte{salt, byte(i)})
		out[i] = sdk.AccAddress(h[:20]).String()
	}
	return out
}

// allowlistedParams has mainnet's allowlist shape (h=6428892): 18 escrow
// creators, 18 developers, 7 transfer agents, 3 guardian validators.
func allowlistedParams() types.Params {
	p := types.DefaultParams()
	p.DevshardEscrowParams.AllowedCreatorAddresses = testAddrs(18, 1)
	p.DeveloperAccessParams = &types.DeveloperAccessParams{UntilBlockHeight: 3_000_000, AllowedDeveloperAddresses: testAddrs(18, 1)}
	p.TransferAgentAccessParams = &types.TransferAgentAccessParams{AllowedTransferAddresses: testAddrs(7, 2)}
	guardians := make([]string, 3)
	for i := range guardians {
		h := sha256.Sum256([]byte{3, byte(i)})
		guardians[i] = sdk.ValAddress(h[:20]).String()
	}
	p.GenesisGuardianParams.GuardianAddresses = guardians
	return p
}

func TestParams_AllowlistsStoredAsBytes(t *testing.T) {
	k, ctx := keepertest.InferenceKeeper(t)
	want := allowlistedParams()
	legacy, err := want.Marshal()
	require.NoError(t, err)

	given := allowlistedParams()
	require.NoError(t, k.SetParams(ctx, given))
	require.Equal(t, want, given, "caller's params must not change")

	raw := keeper.RawParamsForTesting(k, ctx)
	var stored types.Params
	require.NoError(t, stored.Unmarshal(raw))
	require.Empty(t, stored.DevshardEscrowParams.AllowedCreatorAddresses)
	require.Len(t, stored.DevshardEscrowParams.AllowedCreatorAddrs, 18)
	require.Empty(t, stored.DeveloperAccessParams.AllowedDeveloperAddresses)
	require.Len(t, stored.DeveloperAccessParams.AllowedDeveloperAddrs, 18)
	require.Empty(t, stored.TransferAgentAccessParams.AllowedTransferAddresses)
	require.Len(t, stored.TransferAgentAccessParams.AllowedTransferAddrs, 7)
	require.Empty(t, stored.GenesisGuardianParams.GuardianAddresses)
	require.Len(t, stored.GenesisGuardianParams.GuardianAddrs, 3)

	readCtx := ctx.WithGasMeter(storetypes.NewInfiniteGasMeter())
	got, err := k.GetParams(readCtx)
	require.NoError(t, err)
	require.Equal(t, want, got)

	keeper.SetRawParamsForTesting(k, ctx, legacy)
	legacyCtx := ctx.WithGasMeter(storetypes.NewInfiniteGasMeter())
	gotLegacy, err := k.GetParams(legacyCtx)
	require.NoError(t, err)
	require.Equal(t, want, gotLegacy, "params written by an older binary read the same")

	t.Logf("params %d -> %d B, GetParams %d -> %d gas", len(legacy), len(raw),
		legacyCtx.GasMeter().GasConsumed(), readCtx.GasMeter().GasConsumed())
	require.Less(t, readCtx.GasMeter().GasConsumed(), legacyCtx.GasMeter().GasConsumed())
}

func TestParams_AllowlistWithNonCanonicalEntryStaysStrings(t *testing.T) {
	k, ctx := keepertest.InferenceKeeper(t)
	p := allowlistedParams()
	p.TransferAgentAccessParams.AllowedTransferAddresses[3] = strings.ToUpper(p.TransferAgentAccessParams.AllowedTransferAddresses[3])
	want := allowlistedParams()
	want.TransferAgentAccessParams.AllowedTransferAddresses[3] = p.TransferAgentAccessParams.AllowedTransferAddresses[3]
	require.NoError(t, k.SetParams(ctx, p))

	var stored types.Params
	require.NoError(t, stored.Unmarshal(keeper.RawParamsForTesting(k, ctx)))
	require.Len(t, stored.TransferAgentAccessParams.AllowedTransferAddresses, 7)
	require.Empty(t, stored.TransferAgentAccessParams.AllowedTransferAddrs)
	require.Len(t, stored.DevshardEscrowParams.AllowedCreatorAddrs, 18)

	got, err := k.GetParams(ctx)
	require.NoError(t, err)
	require.Equal(t, want, got)
}

func TestParams_StorageOnlyAllowlistFieldsRejected(t *testing.T) {
	k, ctx := keepertest.InferenceKeeper(t)
	for name, set := range map[string]func(*types.Params){
		"creator":   func(p *types.Params) { p.DevshardEscrowParams.AllowedCreatorAddrs = [][]byte{make([]byte, 20)} },
		"developer": func(p *types.Params) { p.DeveloperAccessParams.AllowedDeveloperAddrs = [][]byte{make([]byte, 20)} },
		"transfer":  func(p *types.Params) { p.TransferAgentAccessParams.AllowedTransferAddrs = [][]byte{make([]byte, 20)} },
		"guardian":  func(p *types.Params) { p.GenesisGuardianParams.GuardianAddrs = [][]byte{make([]byte, 20)} },
	} {
		p := allowlistedParams()
		set(&p)
		require.Error(t, k.SetParams(ctx, p), name)
	}
}
