package inference

import (
	"math/rand"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/types/module"
	simtypes "github.com/cosmos/cosmos-sdk/types/simulation"
	"github.com/cosmos/cosmos-sdk/x/simulation"

	"github.com/productscience/inference/testutil/sample"
	inferencesimulation "github.com/productscience/inference/x/inference/simulation"
	"github.com/productscience/inference/x/inference/types"
)

// avoid unused import issue
var (
	_ = inferencesimulation.FindAccount
	_ = rand.Rand{}
	_ = sample.AccAddress
	_ = sdk.AccAddress{}
	_ = simulation.MsgEntryKind
)

const (
	opWeightMsgStartInference = "op_weight_msg_start_inference"
	// TODO: Determine the simulation weight value
	defaultWeightMsgStartInference int = 100

	opWeightMsgFinishInference = "op_weight_msg_finish_inference"
	// TODO: Determine the simulation weight value
	defaultWeightMsgFinishInference int = 100

	// this line is used by starport scaffolding # simapp/module/const
)

// GenerateGenesisState creates a randomized GenState of the module.
func (AppModule) GenerateGenesisState(simState *module.SimulationState) {
	accs := make([]string, len(simState.Accounts))
	for i, acc := range simState.Accounts {
		accs[i] = acc.Address.String()
	}
	inferenceGenesis := types.GenesisState{
		Params: types.DefaultParams(),
		// this line is used by starport scaffolding # simapp/module/genesisState
	}
	simState.GenState[types.ModuleName] = simState.Cdc.MustMarshalJSON(&inferenceGenesis)
}

// RegisterStoreDecoder registers a decoder.
func (am AppModule) RegisterStoreDecoder(_ simtypes.StoreDecoderRegistry) {}

// WeightedOperations returns the all the gov module operations with their respective weights.
func (am AppModule) WeightedOperations(simState module.SimulationState) []simtypes.WeightedOperation {
	operations := make([]simtypes.WeightedOperation, 0)

	var weightMsgStartInference int
	simState.AppParams.GetOrGenerate(opWeightMsgStartInference, &weightMsgStartInference, nil,
		func(_ *rand.Rand) {
			weightMsgStartInference = defaultWeightMsgStartInference
		},
	)
	operations = append(operations, simulation.NewWeightedOperation(
		weightMsgStartInference,
		inferencesimulation.SimulateMsgStartInference(am.accountKeeper, am.bankKeeper, am.keeper),
	))

	var weightMsgFinishInference int
	simState.AppParams.GetOrGenerate(opWeightMsgFinishInference, &weightMsgFinishInference, nil,
		func(_ *rand.Rand) {
			weightMsgFinishInference = defaultWeightMsgFinishInference
		},
	)
	operations = append(operations, simulation.NewWeightedOperation(
		weightMsgFinishInference,
		inferencesimulation.SimulateMsgFinishInference(am.accountKeeper, am.bankKeeper, am.keeper),
	))

	// this line is used by starport scaffolding # simapp/module/operation

	return operations
}

// ProposalMsgs returns msgs used for governance proposals for simulations.
func (am AppModule) ProposalMsgs(simState module.SimulationState) []simtypes.WeightedProposalMsg {
	return []simtypes.WeightedProposalMsg{
		simulation.NewWeightedProposalMsg(
			opWeightMsgStartInference,
			defaultWeightMsgStartInference,
			func(r *rand.Rand, ctx sdk.Context, accs []simtypes.Account) sdk.Msg {
				inferencesimulation.SimulateMsgStartInference(am.accountKeeper, am.bankKeeper, am.keeper)
				return nil
			},
		),
		simulation.NewWeightedProposalMsg(
			opWeightMsgFinishInference,
			defaultWeightMsgFinishInference,
			func(r *rand.Rand, ctx sdk.Context, accs []simtypes.Account) sdk.Msg {
				inferencesimulation.SimulateMsgFinishInference(am.accountKeeper, am.bankKeeper, am.keeper)
				return nil
			},
		),
		// this line is used by starport scaffolding # simapp/module/OpMsg
	}
}
