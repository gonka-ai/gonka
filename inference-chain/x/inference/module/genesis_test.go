package inference_test

import (
	"testing"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/testutil"
	keepertest "github.com/productscience/inference/testutil/keeper"
	"github.com/productscience/inference/testutil/nullify"
	inference "github.com/productscience/inference/x/inference/module"
	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
)

func TestGenesis(t *testing.T) {
	genesisState := types.GenesisState{
		Params:            types.DefaultParams(),
		GenesisOnlyParams: types.DefaultGenesisOnlyParams(),
		ModelList: []types.Model{
			{
				ProposedBy:             "genesis",
				Id:                     "model-1",
				UnitsOfComputePerToken: 10,
				HfRepo:                 "repo1",
				HfCommit:               "commit1",
				ModelArgs:              []string{"--arg1"},
				VRam:                   16,
				ThroughputPerNonce:     100,
				ValidationThreshold:    &types.Decimal{Value: 99, Exponent: -2},
			},
		},
		// this line is used by starport scaffolding # genesis/test/state
	}

	k, ctx, mocks := keepertest.InferenceKeeperReturningMocks(t)

	mocks.StubForInitGenesis(ctx)

	inference.InitGenesis(ctx, k, genesisState)
	got := inference.ExportGenesis(ctx, k)
	require.NotNil(t, got)
	currentSigningEpochID, found := k.BlsKeeper.GetCurrentSigningEpochID(ctx)
	require.True(t, found)
	require.Equal(t, uint64(0), currentSigningEpochID)

	nullify.Fill(&genesisState)
	nullify.Fill(got)

	require.ElementsMatch(t, genesisState.ModelList, got.ModelList)
	// this line is used by starport scaffolding # genesis/test/assert
}

func TestGenesis_BridgePendingRefundsRoundTrip(t *testing.T) {
	k, ctx, mocks := keepertest.InferenceKeeperReturningMocks(t)
	mocks.StubForInitGenesis(ctx)

	genesisState := mocks.StubGenesisState()
	genesisState.Bridge = &types.Bridge{
		PendingMintRefunds: []*types.BridgePendingMintRefund{
			{
				RequestId:          "mint-refund-1",
				Creator:            testutil.Creator,
				Amount:             "1000",
				DestinationAddress: "0xabc",
				ChainId:            "ethereum",
			},
		},
		PendingWithdrawalRefunds: []*types.BridgePendingWithdrawalRefund{
			{
				RequestId:          "withdrawal-refund-1",
				Creator:            testutil.Creator,
				UserAddress:        testutil.Requester,
				Amount:             "2000",
				DestinationAddress: "0xdef",
				ChainId:            "ethereum",
				ContractAddress:    "0xtoken",
			},
		},
	}

	inference.InitGenesis(ctx, k, genesisState)
	exportedGenesis := inference.ExportGenesis(ctx, k)

	require.NotNil(t, exportedGenesis.Bridge)
	require.Len(t, exportedGenesis.Bridge.PendingMintRefunds, 1)
	require.Len(t, exportedGenesis.Bridge.PendingWithdrawalRefunds, 1)

	pendingMint := exportedGenesis.Bridge.PendingMintRefunds[0]
	require.Equal(t, "mint-refund-1", pendingMint.RequestId)
	require.Equal(t, testutil.Creator, pendingMint.Creator)
	require.Equal(t, "1000", pendingMint.Amount)
	require.Equal(t, "0xabc", pendingMint.DestinationAddress)
	require.Equal(t, "ethereum", pendingMint.ChainId)

	pendingWithdrawal := exportedGenesis.Bridge.PendingWithdrawalRefunds[0]
	require.Equal(t, "withdrawal-refund-1", pendingWithdrawal.RequestId)
	require.Equal(t, testutil.Creator, pendingWithdrawal.Creator)
	require.Equal(t, testutil.Requester, pendingWithdrawal.UserAddress)
	require.Equal(t, "2000", pendingWithdrawal.Amount)
	require.Equal(t, "0xdef", pendingWithdrawal.DestinationAddress)
	require.Equal(t, "ethereum", pendingWithdrawal.ChainId)
	require.Equal(t, "0xtoken", pendingWithdrawal.ContractAddress)
}

func TestGenesis_BridgeKeyLinksRoundTrip(t *testing.T) {
	sdk.GetConfig().SetBech32PrefixForAccount("gonka", "gonkapub")

	k, ctx, mocks := keepertest.InferenceKeeperReturningMocks(t)
	mocks.StubForInitGenesis(ctx)

	genesisState := mocks.StubGenesisState()
	genesisState.Bridge = &types.Bridge{
		KeyLinks: []*types.BridgeKeyLink{
			{OwnerAddress: testutil.Requester, RecipientAddress: testutil.Executor},
			{OwnerAddress: testutil.Creator, RecipientAddress: testutil.Validator},
		},
	}

	inference.InitGenesis(ctx, k, genesisState)
	linked, err := k.ResolveBridgeRecipient(ctx, testutil.Requester)
	require.NoError(t, err)
	require.Equal(t, testutil.Executor, linked)

	exported := inference.ExportGenesis(ctx, k)
	require.Len(t, exported.Bridge.KeyLinks, 2)
	require.Equal(t, testutil.Creator, exported.Bridge.KeyLinks[0].OwnerAddress)
	require.Equal(t, testutil.Validator, exported.Bridge.KeyLinks[0].RecipientAddress)
	require.Equal(t, testutil.Requester, exported.Bridge.KeyLinks[1].OwnerAddress)
	require.Equal(t, testutil.Executor, exported.Bridge.KeyLinks[1].RecipientAddress)

	k2, ctx2, mocks2 := keepertest.InferenceKeeperReturningMocks(t)
	mocks2.StubForInitGenesis(ctx2)
	imported := mocks2.StubGenesisState()
	imported.Bridge = exported.Bridge
	inference.InitGenesis(ctx2, k2, imported)
	again := inference.ExportGenesis(ctx2, k2)
	require.Equal(t, exported.Bridge.KeyLinks[0].OwnerAddress, again.Bridge.KeyLinks[0].OwnerAddress)
	require.Equal(t, exported.Bridge.KeyLinks[0].RecipientAddress, again.Bridge.KeyLinks[0].RecipientAddress)
	require.Equal(t, exported.Bridge.KeyLinks[1].OwnerAddress, again.Bridge.KeyLinks[1].OwnerAddress)
	require.Equal(t, exported.Bridge.KeyLinks[1].RecipientAddress, again.Bridge.KeyLinks[1].RecipientAddress)
}

func TestGenesis_ApprovedVersionsRoundTrip(t *testing.T) {
	k, ctx, mocks := keepertest.InferenceKeeperReturningMocks(t)
	mocks.StubForInitGenesis(ctx)

	v1 := &types.DevshardApprovedVersion{
		Name:   "v1",
		Binary: "https://example.com/v1.zip",
		Sha256: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
	}
	v2 := &types.DevshardApprovedVersion{
		Name:   "v2",
		Binary: "https://example.com/v2.zip",
		Sha256: "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789",
	}
	genesisState := mocks.StubGenesisState()
	genesisState.DevshardApprovedVersions = []*types.DevshardApprovedVersion{v2, v1}

	inference.InitGenesis(ctx, k, genesisState)
	got := inference.ExportGenesis(ctx, k)
	require.Len(t, got.DevshardApprovedVersions, 2)
	require.Equal(t, "v1", got.DevshardApprovedVersions[0].Name)
	require.Equal(t, "v2", got.DevshardApprovedVersions[1].Name)
	require.Empty(t, got.Params.DevshardEscrowParams.ApprovedVersions)
}

func TestGenesis_LegacyParamsApprovedVersionsMigrated(t *testing.T) {
	k, ctx, mocks := keepertest.InferenceKeeperReturningMocks(t)
	mocks.StubForInitGenesis(ctx)

	legacy := &types.DevshardApprovedVersion{
		Name:   "v-legacy",
		Binary: "https://example.com/legacy.zip",
		Sha256: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
	}
	genesisState := mocks.StubGenesisState()
	genesisState.DevshardApprovedVersions = nil
	genesisState.Params.DevshardEscrowParams.ApprovedVersions = []*types.DevshardApprovedVersion{legacy}

	inference.InitGenesis(ctx, k, genesisState)
	got := inference.ExportGenesis(ctx, k)
	require.Len(t, got.DevshardApprovedVersions, 1)
	require.Equal(t, "v-legacy", got.DevshardApprovedVersions[0].Name)
	require.Empty(t, got.Params.DevshardEscrowParams.ApprovedVersions)
}

func TestGenesis_ExportPromotesLeftoverParamsApprovedVersions(t *testing.T) {
	k, ctx, mocks := keepertest.InferenceKeeperReturningMocks(t)
	mocks.StubForInitGenesis(ctx)
	inference.InitGenesis(ctx, k, mocks.StubGenesisState())

	legacy := &types.DevshardApprovedVersion{
		Name:   "v-legacy",
		Binary: "https://example.com/legacy.zip",
		Sha256: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
	}
	params, err := k.GetParams(ctx)
	require.NoError(t, err)
	params.DevshardEscrowParams.ApprovedVersions = []*types.DevshardApprovedVersion{legacy}
	require.NoError(t, k.SetParams(ctx, params))

	got := inference.ExportGenesis(ctx, k)
	require.Len(t, got.DevshardApprovedVersions, 1)
	require.Equal(t, "v-legacy", got.DevshardApprovedVersions[0].Name)
	require.Empty(t, got.Params.DevshardEscrowParams.ApprovedVersions)
}
