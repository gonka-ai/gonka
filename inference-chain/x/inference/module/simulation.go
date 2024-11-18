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

	opWeightMsgSubmitNewParticipant = "op_weight_msg_submit_new_participant"
	// TODO: Determine the simulation weight value
	defaultWeightMsgSubmitNewParticipant int = 100

	opWeightMsgValidation = "op_weight_msg_validation"
	// TODO: Determine the simulation weight value
	defaultWeightMsgValidation int = 100

	opWeightMsgSubmitPoC = "op_weight_msg_submit_po_c"
	// TODO: Determine the simulation weight value
	defaultWeightMsgSubmitPoC int = 100

	opWeightMsgSubmitNewUnfundedParticipant = "op_weight_msg_submit_new_unfunded_participant"
	// TODO: Determine the simulation weight value
	defaultWeightMsgSubmitNewUnfundedParticipant int = 100

	opWeightMsgInvalidateInference = "op_weight_msg_invalidate_inference"
	// TODO: Determine the simulation weight value
	defaultWeightMsgInvalidateInference int = 100

	opWeightMsgRevalidateInference = "op_weight_msg_revalidate_inference"
	// TODO: Determine the simulation weight value
	defaultWeightMsgRevalidateInference int = 100

	opWeightMsgSubmitPocBatch = "op_weight_msg_submit_poc_batch"
	// TODO: Determine the simulation weight value
	defaultWeightMsgSubmitPocBatch int = 100

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

	var weightMsgSubmitNewParticipant int
	simState.AppParams.GetOrGenerate(opWeightMsgSubmitNewParticipant, &weightMsgSubmitNewParticipant, nil,
		func(_ *rand.Rand) {
			weightMsgSubmitNewParticipant = defaultWeightMsgSubmitNewParticipant
		},
	)
	operations = append(operations, simulation.NewWeightedOperation(
		weightMsgSubmitNewParticipant,
		inferencesimulation.SimulateMsgSubmitNewParticipant(am.accountKeeper, am.bankKeeper, am.keeper),
	))

	var weightMsgValidation int
	simState.AppParams.GetOrGenerate(opWeightMsgValidation, &weightMsgValidation, nil,
		func(_ *rand.Rand) {
			weightMsgValidation = defaultWeightMsgValidation
		},
	)
	operations = append(operations, simulation.NewWeightedOperation(
		weightMsgValidation,
		inferencesimulation.SimulateMsgValidation(am.accountKeeper, am.bankKeeper, am.keeper),
	))

	var weightMsgSubmitPoC int
	simState.AppParams.GetOrGenerate(opWeightMsgSubmitPoC, &weightMsgSubmitPoC, nil,
		func(_ *rand.Rand) {
			weightMsgSubmitPoC = defaultWeightMsgSubmitPoC
		},
	)
	operations = append(operations, simulation.NewWeightedOperation(
		weightMsgSubmitPoC,
		inferencesimulation.SimulateMsgSubmitPoC(am.accountKeeper, am.bankKeeper, am.keeper),
	))

	var weightMsgSubmitNewUnfundedParticipant int
	simState.AppParams.GetOrGenerate(opWeightMsgSubmitNewUnfundedParticipant, &weightMsgSubmitNewUnfundedParticipant, nil,
		func(_ *rand.Rand) {
			weightMsgSubmitNewUnfundedParticipant = defaultWeightMsgSubmitNewUnfundedParticipant
		},
	)
	operations = append(operations, simulation.NewWeightedOperation(
		weightMsgSubmitNewUnfundedParticipant,
		inferencesimulation.SimulateMsgSubmitNewUnfundedParticipant(am.accountKeeper, am.bankKeeper, am.keeper),
	))

	var weightMsgInvalidateInference int
	simState.AppParams.GetOrGenerate(opWeightMsgInvalidateInference, &weightMsgInvalidateInference, nil,
		func(_ *rand.Rand) {
			weightMsgInvalidateInference = defaultWeightMsgInvalidateInference
		},
	)
	operations = append(operations, simulation.NewWeightedOperation(
		weightMsgInvalidateInference,
		inferencesimulation.SimulateMsgInvalidateInference(am.accountKeeper, am.bankKeeper, am.keeper),
	))

	var weightMsgRevalidateInference int
	simState.AppParams.GetOrGenerate(opWeightMsgRevalidateInference, &weightMsgRevalidateInference, nil,
		func(_ *rand.Rand) {
			weightMsgRevalidateInference = defaultWeightMsgRevalidateInference
		},
	)
	operations = append(operations, simulation.NewWeightedOperation(
		weightMsgRevalidateInference,
		inferencesimulation.SimulateMsgRevalidateInference(am.accountKeeper, am.bankKeeper, am.keeper),
	))

	var weightMsgSubmitPocBatch int
	simState.AppParams.GetOrGenerate(opWeightMsgSubmitPocBatch, &weightMsgSubmitPocBatch, nil,
		func(_ *rand.Rand) {
			weightMsgSubmitPocBatch = defaultWeightMsgSubmitPocBatch
		},
	)
	operations = append(operations, simulation.NewWeightedOperation(
		weightMsgSubmitPocBatch,
		inferencesimulation.SimulateMsgSubmitPocBatch(am.accountKeeper, am.bankKeeper, am.keeper),
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
		simulation.NewWeightedProposalMsg(
			opWeightMsgSubmitNewParticipant,
			defaultWeightMsgSubmitNewParticipant,
			func(r *rand.Rand, ctx sdk.Context, accs []simtypes.Account) sdk.Msg {
				inferencesimulation.SimulateMsgSubmitNewParticipant(am.accountKeeper, am.bankKeeper, am.keeper)
				return nil
			},
		),
		simulation.NewWeightedProposalMsg(
			opWeightMsgValidation,
			defaultWeightMsgValidation,
			func(r *rand.Rand, ctx sdk.Context, accs []simtypes.Account) sdk.Msg {
				inferencesimulation.SimulateMsgValidation(am.accountKeeper, am.bankKeeper, am.keeper)
				return nil
			},
		),
		simulation.NewWeightedProposalMsg(
			opWeightMsgSubmitPoC,
			defaultWeightMsgSubmitPoC,
			func(r *rand.Rand, ctx sdk.Context, accs []simtypes.Account) sdk.Msg {
				inferencesimulation.SimulateMsgSubmitPoC(am.accountKeeper, am.bankKeeper, am.keeper)
				return nil
			},
		),
		simulation.NewWeightedProposalMsg(
			opWeightMsgSubmitNewUnfundedParticipant,
			defaultWeightMsgSubmitNewUnfundedParticipant,
			func(r *rand.Rand, ctx sdk.Context, accs []simtypes.Account) sdk.Msg {
				inferencesimulation.SimulateMsgSubmitNewUnfundedParticipant(am.accountKeeper, am.bankKeeper, am.keeper)
				return nil
			},
		),
		simulation.NewWeightedProposalMsg(
			opWeightMsgInvalidateInference,
			defaultWeightMsgInvalidateInference,
			func(r *rand.Rand, ctx sdk.Context, accs []simtypes.Account) sdk.Msg {
				inferencesimulation.SimulateMsgInvalidateInference(am.accountKeeper, am.bankKeeper, am.keeper)
				return nil
			},
		),
		simulation.NewWeightedProposalMsg(
			opWeightMsgRevalidateInference,
			defaultWeightMsgRevalidateInference,
			func(r *rand.Rand, ctx sdk.Context, accs []simtypes.Account) sdk.Msg {
				inferencesimulation.SimulateMsgRevalidateInference(am.accountKeeper, am.bankKeeper, am.keeper)
				return nil
			},
		),
		simulation.NewWeightedProposalMsg(
			opWeightMsgSubmitPocBatch,
			defaultWeightMsgSubmitPocBatch,
			func(r *rand.Rand, ctx sdk.Context, accs []simtypes.Account) sdk.Msg {
				inferencesimulation.SimulateMsgSubmitPocBatch(am.accountKeeper, am.bankKeeper, am.keeper)
				return nil
			},
		),
		// this line is used by starport scaffolding # simapp/module/OpMsg
	}
}
