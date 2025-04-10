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

	opWeightMsgClaimRewards = "op_weight_msg_claim_rewards"
	// TODO: Determine the simulation weight value
	defaultWeightMsgClaimRewards int = 100

	opWeightMsgSubmitPocBatch = "op_weight_msg_submit_poc_batch"
	// TODO: Determine the simulation weight value
	defaultWeightMsgSubmitPocBatch int = 100

	opWeightMsgSubmitPocValidation = "op_weight_msg_submit_poc_validation"
	// TODO: Determine the simulation weight value
	defaultWeightMsgSubmitPocValidation int = 100

	opWeightMsgSubmitSeed = "op_weight_msg_submit_seed"
	// TODO: Determine the simulation weight value
	defaultWeightMsgSubmitSeed int = 100

	opWeightMsgSubmitUnitOfComputePriceProposal = "op_weight_msg_submit_unit_of_compute_price_proposal"
	// TODO: Determine the simulation weight value
	defaultWeightMsgSubmitUnitOfComputePriceProposal int = 100

	opWeightMsgRegisterModel = "op_weight_msg_register_model"
	// TODO: Determine the simulation weight value
	defaultWeightMsgRegisterModel int = 100

	opWeightMsgCreateTrainingTask = "op_weight_msg_create_training_task"
	// TODO: Determine the simulation weight value
	defaultWeightMsgCreateTrainingTask int = 100

	opWeightMsgSubmitHardwareDiff = "op_weight_msg_submit_hardware_diff"
	// TODO: Determine the simulation weight value
	defaultWeightMsgSubmitHardwareDiff int = 100

	opWeightMsgClaimTrainingTaskForAssignment = "op_weight_msg_claim_training_task_for_assignment"
	// TODO: Determine the simulation weight value
	defaultWeightMsgClaimTrainingTaskForAssignment int = 100

	opWeightMsgAssignTrainingTask = "op_weight_msg_assign_training_task"
	// TODO: Determine the simulation weight value
	defaultWeightMsgAssignTrainingTask int = 100

	opWeightMsgCreatePartialUpgrade = "op_weight_msg_create_partial_upgrade"
	// TODO: Determine the simulation weight value
	defaultWeightMsgCreatePartialUpgrade int = 100

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

	var weightMsgClaimRewards int
	simState.AppParams.GetOrGenerate(opWeightMsgClaimRewards, &weightMsgClaimRewards, nil,
		func(_ *rand.Rand) {
			weightMsgClaimRewards = defaultWeightMsgClaimRewards
		},
	)
	operations = append(operations, simulation.NewWeightedOperation(
		weightMsgClaimRewards,
		inferencesimulation.SimulateMsgClaimRewards(am.accountKeeper, am.bankKeeper, am.keeper),
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

	var weightMsgSubmitPocValidation int
	simState.AppParams.GetOrGenerate(opWeightMsgSubmitPocValidation, &weightMsgSubmitPocValidation, nil,
		func(_ *rand.Rand) {
			weightMsgSubmitPocValidation = defaultWeightMsgSubmitPocValidation
		},
	)
	operations = append(operations, simulation.NewWeightedOperation(
		weightMsgSubmitPocValidation,
		inferencesimulation.SimulateMsgSubmitPocValidation(am.accountKeeper, am.bankKeeper, am.keeper),
	))

	var weightMsgSubmitSeed int
	simState.AppParams.GetOrGenerate(opWeightMsgSubmitSeed, &weightMsgSubmitSeed, nil,
		func(_ *rand.Rand) {
			weightMsgSubmitSeed = defaultWeightMsgSubmitSeed
		},
	)
	operations = append(operations, simulation.NewWeightedOperation(
		weightMsgSubmitSeed,
		inferencesimulation.SimulateMsgSubmitSeed(am.accountKeeper, am.bankKeeper, am.keeper),
	))

	var weightMsgSubmitUnitOfComputePriceProposal int
	simState.AppParams.GetOrGenerate(opWeightMsgSubmitUnitOfComputePriceProposal, &weightMsgSubmitUnitOfComputePriceProposal, nil,
		func(_ *rand.Rand) {
			weightMsgSubmitUnitOfComputePriceProposal = defaultWeightMsgSubmitUnitOfComputePriceProposal
		},
	)
	operations = append(operations, simulation.NewWeightedOperation(
		weightMsgSubmitUnitOfComputePriceProposal,
		inferencesimulation.SimulateMsgSubmitUnitOfComputePriceProposal(am.accountKeeper, am.bankKeeper, am.keeper),
	))

	var weightMsgRegisterModel int
	simState.AppParams.GetOrGenerate(opWeightMsgRegisterModel, &weightMsgRegisterModel, nil,
		func(_ *rand.Rand) {
			weightMsgRegisterModel = defaultWeightMsgRegisterModel
		},
	)
	operations = append(operations, simulation.NewWeightedOperation(
		weightMsgRegisterModel,
		inferencesimulation.SimulateMsgRegisterModel(am.accountKeeper, am.bankKeeper, am.keeper),
	))

	var weightMsgCreateTrainingTask int
	simState.AppParams.GetOrGenerate(opWeightMsgCreateTrainingTask, &weightMsgCreateTrainingTask, nil,
		func(_ *rand.Rand) {
			weightMsgCreateTrainingTask = defaultWeightMsgCreateTrainingTask
		},
	)
	operations = append(operations, simulation.NewWeightedOperation(
		weightMsgCreateTrainingTask,
		inferencesimulation.SimulateMsgCreateTrainingTask(am.accountKeeper, am.bankKeeper, am.keeper),
	))

	var weightMsgSubmitHardwareDiff int
	simState.AppParams.GetOrGenerate(opWeightMsgSubmitHardwareDiff, &weightMsgSubmitHardwareDiff, nil,
		func(_ *rand.Rand) {
			weightMsgSubmitHardwareDiff = defaultWeightMsgSubmitHardwareDiff
		},
	)
	operations = append(operations, simulation.NewWeightedOperation(
		weightMsgSubmitHardwareDiff,
		inferencesimulation.SimulateMsgSubmitHardwareDiff(am.accountKeeper, am.bankKeeper, am.keeper),
	))

	var weightMsgClaimTrainingTaskForAssignment int
	simState.AppParams.GetOrGenerate(opWeightMsgClaimTrainingTaskForAssignment, &weightMsgClaimTrainingTaskForAssignment, nil,
		func(_ *rand.Rand) {
			weightMsgClaimTrainingTaskForAssignment = defaultWeightMsgClaimTrainingTaskForAssignment
		},
	)
	operations = append(operations, simulation.NewWeightedOperation(
		weightMsgClaimTrainingTaskForAssignment,
		inferencesimulation.SimulateMsgClaimTrainingTaskForAssignment(am.accountKeeper, am.bankKeeper, am.keeper),
	))

	var weightMsgAssignTrainingTask int
	simState.AppParams.GetOrGenerate(opWeightMsgAssignTrainingTask, &weightMsgAssignTrainingTask, nil,
		func(_ *rand.Rand) {
			weightMsgAssignTrainingTask = defaultWeightMsgAssignTrainingTask
		},
	)
	operations = append(operations, simulation.NewWeightedOperation(
		weightMsgAssignTrainingTask,
		inferencesimulation.SimulateMsgAssignTrainingTask(am.accountKeeper, am.bankKeeper, am.keeper),
	))

	var weightMsgCreatePartialUpgrade int
	simState.AppParams.GetOrGenerate(opWeightMsgCreatePartialUpgrade, &weightMsgCreatePartialUpgrade, nil,
		func(_ *rand.Rand) {
			weightMsgCreatePartialUpgrade = defaultWeightMsgCreatePartialUpgrade
		},
	)
	operations = append(operations, simulation.NewWeightedOperation(
		weightMsgCreatePartialUpgrade,
		inferencesimulation.SimulateMsgCreatePartialUpgrade(am.accountKeeper, am.bankKeeper, am.keeper),
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
			opWeightMsgClaimRewards,
			defaultWeightMsgClaimRewards,
			func(r *rand.Rand, ctx sdk.Context, accs []simtypes.Account) sdk.Msg {
				inferencesimulation.SimulateMsgClaimRewards(am.accountKeeper, am.bankKeeper, am.keeper)
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
		simulation.NewWeightedProposalMsg(
			opWeightMsgSubmitPocValidation,
			defaultWeightMsgSubmitPocValidation,
			func(r *rand.Rand, ctx sdk.Context, accs []simtypes.Account) sdk.Msg {
				inferencesimulation.SimulateMsgSubmitPocValidation(am.accountKeeper, am.bankKeeper, am.keeper)
				return nil
			},
		),
		simulation.NewWeightedProposalMsg(
			opWeightMsgSubmitSeed,
			defaultWeightMsgSubmitSeed,
			func(r *rand.Rand, ctx sdk.Context, accs []simtypes.Account) sdk.Msg {
				inferencesimulation.SimulateMsgSubmitSeed(am.accountKeeper, am.bankKeeper, am.keeper)
				return nil
			},
		),
		simulation.NewWeightedProposalMsg(
			opWeightMsgSubmitUnitOfComputePriceProposal,
			defaultWeightMsgSubmitUnitOfComputePriceProposal,
			func(r *rand.Rand, ctx sdk.Context, accs []simtypes.Account) sdk.Msg {
				inferencesimulation.SimulateMsgSubmitUnitOfComputePriceProposal(am.accountKeeper, am.bankKeeper, am.keeper)
				return nil
			},
		),
		simulation.NewWeightedProposalMsg(
			opWeightMsgRegisterModel,
			defaultWeightMsgRegisterModel,
			func(r *rand.Rand, ctx sdk.Context, accs []simtypes.Account) sdk.Msg {
				inferencesimulation.SimulateMsgRegisterModel(am.accountKeeper, am.bankKeeper, am.keeper)
				return nil
			},
		),
		simulation.NewWeightedProposalMsg(
			opWeightMsgCreateTrainingTask,
			defaultWeightMsgCreateTrainingTask,
			func(r *rand.Rand, ctx sdk.Context, accs []simtypes.Account) sdk.Msg {
				inferencesimulation.SimulateMsgCreateTrainingTask(am.accountKeeper, am.bankKeeper, am.keeper)
				return nil
			},
		),
		simulation.NewWeightedProposalMsg(
			opWeightMsgSubmitHardwareDiff,
			defaultWeightMsgSubmitHardwareDiff,
			func(r *rand.Rand, ctx sdk.Context, accs []simtypes.Account) sdk.Msg {
				inferencesimulation.SimulateMsgSubmitHardwareDiff(am.accountKeeper, am.bankKeeper, am.keeper)
				return nil
			},
		),
		simulation.NewWeightedProposalMsg(
			opWeightMsgClaimTrainingTaskForAssignment,
			defaultWeightMsgClaimTrainingTaskForAssignment,
			func(r *rand.Rand, ctx sdk.Context, accs []simtypes.Account) sdk.Msg {
				inferencesimulation.SimulateMsgClaimTrainingTaskForAssignment(am.accountKeeper, am.bankKeeper, am.keeper)
				return nil
			},
		),
		simulation.NewWeightedProposalMsg(
			opWeightMsgAssignTrainingTask,
			defaultWeightMsgAssignTrainingTask,
			func(r *rand.Rand, ctx sdk.Context, accs []simtypes.Account) sdk.Msg {
				inferencesimulation.SimulateMsgAssignTrainingTask(am.accountKeeper, am.bankKeeper, am.keeper)
				return nil
			},
		),
		simulation.NewWeightedProposalMsg(
			opWeightMsgCreatePartialUpgrade,
			defaultWeightMsgCreatePartialUpgrade,
			func(r *rand.Rand, ctx sdk.Context, accs []simtypes.Account) sdk.Msg {
				inferencesimulation.SimulateMsgCreatePartialUpgrade(am.accountKeeper, am.bankKeeper, am.keeper)
				return nil
			},
		),
		// this line is used by starport scaffolding # simapp/module/OpMsg
	}
}
