package app

import (
	"github.com/cosmos/cosmos-sdk/types/module"
	simtypes "github.com/cosmos/cosmos-sdk/types/simulation"
)

// disabledOpsSimModule wraps an AppModuleSimulation and returns nil from
// WeightedOperations, so the wrapped module's sim ops are excluded from
// the simulator while GenerateGenesisState and store decoders continue
// to work through embedding. HasProposalMsgs and HasProposalContents are
// not on AppModuleSimulation, so they are never promoted regardless of
// the wrapped module.
//
// Used for:
//   - staking/distribution: the PoC chain has no token bonding or
//     traditional rewards (docs/cosmos_changes.md); the stock sim msgs
//     target state machinery this fork has neutralised.
//   - wasmd v0.54.2: BuildOperationInput uses a failingAddressCodec
//     rather than the app codec, so all wasm sim ops fail. Fixed in
//     v0.60.0+ (CosmWasm/wasmd#2250).
//   - group: x/group's sim ops (SimulateMsgUpdateGroupMembers etc.) pick a
//     random group and remove/replace members. They cannot tell gonka's
//     epoch-group x/group groups apart from their own, so they corrupt the
//     validation groups gonka's epoch flow owns — desyncing them from
//     EpochGroupData.ValidationWeights. gonka exercises x/group through its
//     own ops (MsgValidation / revalidation votes), not x/group's stock ops.
//
// Originally proposed by hleb-albau in gonka-ai/gonka#995.
type disabledOpsSimModule struct {
	module.AppModuleSimulation
}

// WeightedOperations always returns nil. See type doc.
func (disabledOpsSimModule) WeightedOperations(_ module.SimulationState) []simtypes.WeightedOperation {
	return nil
}
