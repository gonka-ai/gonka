package simulation_test

import (
	"encoding/json"
	"math/rand"
	"testing"
	"time"

	"github.com/cosmos/cosmos-sdk/types/module"
	simtypes "github.com/cosmos/cosmos-sdk/types/simulation"
	"github.com/stretchr/testify/require"

	"github.com/productscience/inference/x/inference/simulation"
)

// TestBuildSimGenesisParticipants_PicksN verifies the helper extracts
// exactly NumSimGenesisParticipants entries from a longer Accounts slice,
// preserving the prefix order (deterministic given simState.Rand seed).
func TestBuildSimGenesisParticipants_PicksN(t *testing.T) {
	r := rand.New(rand.NewSource(1))
	accs := simtypes.RandomAccounts(r, simulation.NumSimGenesisParticipants+3)
	simState := &module.SimulationState{
		Accounts:     accs,
		Rand:         r,
		GenState:     map[string]json.RawMessage{},
		GenTimestamp: time.Unix(0, 0),
	}

	got := simulation.BuildSimGenesisParticipants(simState)
	require.Len(t, got, simulation.NumSimGenesisParticipants)
	for i, p := range got {
		require.Equal(t, accs[i].Address.String(), p.Index)
		require.Equal(t, accs[i].Address.String(), p.Address)
		require.NotEmpty(t, p.ValidatorKey)
	}
}

// TestBuildSimGenesisParticipants_FewerAccountsAvailable caps the output
// length at len(simState.Accounts) when that is below
// NumSimGenesisParticipants — the helper must not panic or pad.
func TestBuildSimGenesisParticipants_FewerAccountsAvailable(t *testing.T) {
	r := rand.New(rand.NewSource(1))
	want := simulation.NumSimGenesisParticipants - 2
	require.Greater(t, want, 0)
	accs := simtypes.RandomAccounts(r, want)
	simState := &module.SimulationState{
		Accounts:     accs,
		Rand:         r,
		GenState:     map[string]json.RawMessage{},
		GenTimestamp: time.Unix(0, 0),
	}

	got := simulation.BuildSimGenesisParticipants(simState)
	require.Len(t, got, want)
}

// TestBuildSimGenesisModels_KimiAndQwen — sim genesis registers Qwen +
// Kimi with ProposedBy="genesis" (required by production InitGenesis at
// module/genesis.go). Values must match the canonical mainnet
// entries so the simulation pressure-tests realistic chain config.
func TestBuildSimGenesisModels_KimiAndQwen(t *testing.T) {
	got := simulation.BuildSimGenesisModels()
	require.Len(t, got, 2)
	ids := []string{got[0].Id, got[1].Id}
	require.ElementsMatch(t, simulation.SimModelIDs, ids)
	for _, m := range got {
		require.Equal(t, "genesis", m.ProposedBy,
			"sim genesis model must set ProposedBy=genesis")
		require.NotEmpty(t, m.HfRepo)
		require.NotZero(t, m.UnitsOfComputePerToken)
		require.NotNil(t, m.ValidationThreshold)
	}
}
