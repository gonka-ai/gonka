package simulation_test

import (
	"context"
	"math/rand"
	"testing"

	"cosmossdk.io/collections"
	"cosmossdk.io/core/address"
	sdkaddress "github.com/cosmos/cosmos-sdk/codec/address"
	sdk "github.com/cosmos/cosmos-sdk/types"
	simtypes "github.com/cosmos/cosmos-sdk/types/simulation"
	"github.com/stretchr/testify/require"

	"github.com/productscience/inference/x/inference/keeper"
	"github.com/productscience/inference/x/inference/simulation"
	"github.com/productscience/inference/x/inference/types"
)

// newSimAccounts returns n deterministic sim accounts using seed.
// Equivalent to simtypes.RandomAccounts with an explicit math/rand source.
func newSimAccounts(t *testing.T, seed int64, n int) []simtypes.Account {
	t.Helper()
	return simtypes.RandomAccounts(rand.New(rand.NewSource(seed)), n)
}

// registerAsParticipants registers each sim account as a Participant in the
// keeper. Status pre-set to ACTIVE and CurrentEpochStats initialized so
// UpdateParticipantStatus → ComputeStatus does not flip status under
// DefaultParams (matches the keeper_test.createNParticipant pattern at
// inference-chain/x/inference/keeper/participant_test.go).
func registerAsParticipants(t *testing.T, k keeper.Keeper, ctx context.Context, accs []simtypes.Account) []string {
	t.Helper()
	out := make([]string, 0, len(accs))
	for _, a := range accs {
		addr := a.Address.String()
		require.NoError(t, k.SetParticipant(ctx, types.Participant{
			Index:             addr,
			Address:           addr,
			Status:            types.ParticipantStatus_ACTIVE,
			CurrentEpochStats: types.NewCurrentEpochStats(),
		}))
		out = append(out, addr)
	}
	return out
}

// registerSimGenesisModels writes the Phase-2 sim models into the keeper.
// Unit-test analogue of what InitGenesis does (module/genesis.go)
// when it consumes GenesisState.ModelList. Needed so factory tests can
// successfully invoke PickRandomGovernanceModelID without running a full
// app-level sim genesis.
func registerSimGenesisModels(t *testing.T, k keeper.Keeper, ctx context.Context) {
	t.Helper()
	for _, m := range simulation.BuildSimGenesisModels() {
		mm := m
		mm.ProposedBy = k.GetAuthority()
		k.SetModel(ctx, &mm)
	}
}

// putFinishedInference writes a FINISHED-status Inference into the
// keeper. Used by MsgValidation factory tests: PickRandomFinishedInference
// filters by Status==FINISHED to avoid the hard ErrInferenceNotFinished
// path at msg_server_validation.go.
// ExecutedBy is required to satisfy validator!=executor at
// msg_server_validation.go — pass any registered participant
// address whose role you do NOT want drawn as the validator.
func putFinishedInference(t *testing.T, k keeper.Keeper, ctx context.Context, id, executedBy string) {
	t.Helper()
	require.NoError(t, k.SetInference(ctx, types.Inference{
		Index:       id,
		InferenceId: id,
		ExecutedBy:  executedBy,
		Status:      types.InferenceStatus_FINISHED,
	}))
}

// putStartedInference writes a STARTED-status Inference with the dev/TA
// components the MsgFinishInference start-first pairing path compares
// against (compareDevComponents / compareFinishTAComponents). assignedTo
// is both the executor and the account the Finish factory signs as.
func putStartedInference(t *testing.T, k keeper.Keeper, ctx context.Context, id, assignedTo, model string) {
	t.Helper()
	require.NoError(t, k.SetInference(ctx, types.Inference{
		Index:              id,
		InferenceId:        id,
		AssignedTo:         assignedTo,
		ExecutedBy:         assignedTo,
		TransferredBy:      assignedTo,
		RequestedBy:        assignedTo,
		PromptHash:         "sim-prompt-hash",
		OriginalPromptHash: "sim-original-prompt-hash",
		Model:              model,
		RequestTimestamp:   1_000_000,
		Status:             types.InferenceStatus_STARTED,
	}))
}

// collectActiveAddrs returns the bech32 addresses in ActiveParticipantsSet
// for the given epoch, in iteration (collections-sorted) order.
func collectActiveAddrs(t *testing.T, ctx context.Context, k keeper.Keeper, epoch uint64) []string {
	t.Helper()
	iter, err := k.ActiveParticipantsSet.Iterate(ctx,
		collections.NewPrefixedPairRange[uint64, sdk.AccAddress](epoch))
	require.NoError(t, err)
	defer iter.Close()
	out := make([]string, 0, 8)
	for ; iter.Valid(); iter.Next() {
		key, err := iter.Key()
		require.NoError(t, err)
		out = append(out, key.K2().String())
	}
	return out
}

// gonkaBech32Codec returns the address codec wired to the gonka account
// prefix that keepertest.InferenceKeeper installs via
// sdk.GetConfig().SetBech32PrefixForAccount("gonka", "gonka").
func gonkaBech32Codec() address.Codec {
	return sdkaddress.NewBech32Codec("gonka")
}
