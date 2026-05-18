package simulation

import (
	"github.com/cosmos/cosmos-sdk/types/module"

	"github.com/productscience/inference/x/inference/types"
)

// SimModelIDs is the canonical ordered list of model IDs registered at
// sim genesis. Tests assert exact membership; factories pick from it.
var SimModelIDs = []string{
	"Qwen/Qwen2.5-7B-Instruct",
	"moonshotai/Kimi-K2.6",
}

// BuildSimGenesisModels returns the Phase-2 sim model registry. Values
// mirror real mainnet governance entries:
//
//   - Qwen/Qwen2.5-7B-Instruct: from x/inference/keeper/model_test.go
//     (the canonical genesis-style example in the keeper's own tests).
//   - moonshotai/Kimi-K2.6: from app/upgrades/v0_2_12/upgrades.go
//     (kimiGovernanceModel) — installed on mainnet via the v0.2.12 upgrade
//     handler.
//
// ProposedBy="genesis" is required by the production InitGenesis check at
// module/genesis.go; InitGenesis then rewrites it to k.GetAuthority().
// Using realistic mainnet parameters (Phase-2 issue text §«helper logic
// for selecting valid accounts and valid preconditions … realistic
// messages rather than mostly generating invalid transactions»).
//
// Sim-only — does not run on production chain init.
func BuildSimGenesisModels() []types.Model {
	return []types.Model{
		{
			ProposedBy:             "genesis",
			Id:                     "Qwen/Qwen2.5-7B-Instruct",
			UnitsOfComputePerToken: 100,
			HfRepo:                 "Qwen/Qwen2.5-7B-Instruct",
			HfCommit:               "a09a35458c702b33eeacc393d103063234e8bc28",
			ModelArgs:              []string{"--quantization", "fp8"},
			VRam:                   24,
			ThroughputPerNonce:     10000,
			ValidationThreshold:    &types.Decimal{Value: 85, Exponent: -2},
		},
		{
			ProposedBy:             "genesis",
			Id:                     "moonshotai/Kimi-K2.6",
			UnitsOfComputePerToken: 10000,
			HfRepo:                 "moonshotai/Kimi-K2.6",
			HfCommit:               "5a49d036ab7472b7d5912ded487150ec1358c11d",
			ModelArgs: []string{
				"--max-model-len", "240000",
				"--tool-call-parser", "kimi_k2",
				"--reasoning-parser", "kimi_k2",
			},
			VRam:                720,
			ThroughputPerNonce:  1500,
			ValidationThreshold: &types.Decimal{Value: 920, Exponent: -3},
		},
	}
}

// NumSimGenesisParticipants is the count of sim accounts pre-registered as
// Participants at sim genesis. The bootstrap helper (bootstrap.go) promotes
// them into ActiveParticipantsSet[currentEpoch] on first call from any
// active-participant-gated factory.
const NumSimGenesisParticipants = 5

// BuildSimGenesisParticipants picks N sim accounts and returns them as
// Participant entries for GenesisState.ParticipantList. Status is left
// zero — production InitGenesis runs the participant loop BEFORE SetParams,
// so UpdateParticipantStatus → ComputeStatus hits its genesis fast path
// (calculations/status.go) and assigns ACTIVE.
//
// Sim-only — no production code change. ActiveParticipantsSet promotion is
// done lazily by EnsureActiveParticipantsSeeded (bootstrap.go).
//
// Determinism: simState.Accounts is generated from r *rand.Rand by simsx,
// so the same seed yields the same prefix selection.
func BuildSimGenesisParticipants(simState *module.SimulationState) []types.Participant {
	n := NumSimGenesisParticipants
	if len(simState.Accounts) < n {
		n = len(simState.Accounts)
	}
	out := make([]types.Participant, 0, n)
	for i := 0; i < n; i++ {
		addr := simState.Accounts[i].Address.String()
		out = append(out, types.Participant{
			Index:   addr,
			Address: addr,
		})
	}
	return out
}
