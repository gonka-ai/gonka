package inference

import (
	"math/rand"

	"github.com/cosmos/cosmos-sdk/testutil/simsx"
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
	// Inference-lifecycle baseline (highest-frequency op); other weights are
	// tuned relative to this 100.
	defaultWeightMsgStartInference int = 100

	opWeightMsgFinishInference = "op_weight_msg_finish_inference"
	// Pairs 1:1 with StartInference, so same baseline weight.
	defaultWeightMsgFinishInference int = 100

	opWeightMsgSubmitNewParticipant = "op_weight_msg_submit_new_participant"
	// Registration is infrequent relative to inference traffic; the genesis
	// sim participants already populate the active set, so new joins are rare.
	defaultWeightMsgSubmitNewParticipant int = 20

	opWeightMsgValidation = "op_weight_msg_validation"
	// Highest-frequency post-inference op and the highest bug-finding value
	// path (validation/invalidation/refund accounting), so skewed above the
	// inference-lifecycle baseline.
	defaultWeightMsgValidation int = 130

	opWeightMsgSubmitPoC = "op_weight_msg_submit_po_c"
	// TODO: Determine the simulation weight value
	defaultWeightMsgSubmitPoC int = 100

	opWeightMsgSubmitNewUnfundedParticipant = "op_weight_msg_submit_new_unfunded_participant"
	// Direct (unfunded) registration is rare relative to inference traffic.
	defaultWeightMsgSubmitNewUnfundedParticipant int = 15

	opWeightMsgInvalidateInference = "op_weight_msg_invalidate_inference"
	// TODO: Determine the simulation weight value
	defaultWeightMsgInvalidateInference int = 100

	opWeightMsgRevalidateInference = "op_weight_msg_revalidate_inference"
	// TODO: Determine the simulation weight value
	defaultWeightMsgRevalidateInference int = 100

	opWeightMsgRevalidationVote = "op_weight_msg_revalidation_vote"
	// Skewed above the uniform Phase 2 default of 100 so revalidation votes
	// reliably coincide, within a block, with the failing MsgValidation that
	// opened the proposals — the 4-minute group voting window closes before the
	// next block. Provisional; this plan's Phase 3 weight-tuning chunk
	// recalibrates from observed hit ratios.
	defaultWeightMsgRevalidationVote int = 200

	opWeightMsgClaimRewards = "op_weight_msg_claim_rewards"
	// Periodic (per settlement cycle), not per-block, so below the
	// inference-lifecycle baseline.
	defaultWeightMsgClaimRewards int = 40

	opWeightMsgSubmitPocBatch = "op_weight_msg_submit_poc_batch"
	// TODO: Determine the simulation weight value
	defaultWeightMsgSubmitPocBatch int = 100

	opWeightMsgPoCV2StoreCommit = "op_weight_msg_poc_v2_store_commit"
	// Per-PoC-window op (self-reschedules via future-ops); fires only inside
	// its exchange window, so a moderate weight suffices.
	defaultWeightMsgPoCV2StoreCommit int = 80

	opWeightMsgMLNodeWeightDistribution = "op_weight_msg_ml_node_weight_distribution"
	// Per-PoC-window op (paired with the commit above).
	defaultWeightMsgMLNodeWeightDistribution int = 80

	opWeightMsgSubmitPocValidationsV2 = "op_weight_msg_submit_poc_validations_v2"
	// Per-PoC-validation-window op.
	defaultWeightMsgSubmitPocValidationsV2 int = 80

	opWeightMsgSubmitSeed = "op_weight_msg_submit_seed"
	// Once per epoch per participant; low weight (extra attempts just skip on
	// "seed already submitted").
	defaultWeightMsgSubmitSeed int = 30

	opWeightMsgSubmitUnitOfComputePriceProposal = "op_weight_msg_submit_unit_of_compute_price_proposal"
	// Pricing proposals are occasional (per-participant governance input).
	defaultWeightMsgSubmitUnitOfComputePriceProposal int = 30

	opWeightMsgRegisterModel = "op_weight_msg_register_model"
	// TODO: Determine the simulation weight value
	defaultWeightMsgRegisterModel int = 100

	opWeightMsgSubmitHardwareDiff = "op_weight_msg_submit_hardware_diff"
	// Hardware topology changes are occasional, not per-block.
	defaultWeightMsgSubmitHardwareDiff int = 25

	opWeightMsgCreatePartialUpgrade = "op_weight_msg_create_partial_upgrade"
	// TODO: Determine the simulation weight value
	defaultWeightMsgCreatePartialUpgrade int = 100

	// this line is used by starport scaffolding # simapp/module/const
)

// GenerateGenesisState creates a randomized GenState of the module.
//
//   - ParticipantList is pre-seeded so the four ActiveParticipant-gated ops
//     (Start/Finish/Validation/ClaimRewards) have someone to promote via
//     BuildEpochSubstrate (x/inference/simulation/substrate.go).
//   - ModelList is pre-seeded with Qwen + Kimi (mainnet-realistic values)
//     so MsgStartInference's RecordInferencePrice has a
//     governance-registered model to look up. EnsureModelsInEpochGroup
//     (x/inference/simulation/bootstrap.go) promotes them into the genesis
//     EpochGroup's SubGroupModels on first op invocation, which is the
//     state BeginBlocker / UpdateDynamicPricing iterate.
func (AppModule) GenerateGenesisState(simState *module.SimulationState) {
	inferenceGenesis := types.GenesisState{
		Params:            inferencesimulation.BuildSimGenesisParams(simState),
		GenesisOnlyParams: types.DefaultGenesisOnlyParams(),
		ParticipantList:   inferencesimulation.BuildSimGenesisParticipants(simState),
		ModelList:         inferencesimulation.BuildSimGenesisModels(),
		// this line is used by starport scaffolding # simapp/module/genesisState
	}
	simState.GenState[types.ModuleName] = simState.Cdc.MustMarshalJSON(&inferenceGenesis) //nolint:forbidigo // Simulation code
}

// RegisterStoreDecoder wires the x/inference store decoder so that
// AppHash-divergence dumps (TestAppImportExport_Postrun and the
// TestAppStateDeterminism divergence dump added in this chunk) print
// readable proto state instead of raw hex.
func (am AppModule) RegisterStoreDecoder(sdr simtypes.StoreDecoderRegistry) {
	sdr[types.StoreKey] = inferencesimulation.NewDecodeStore(am.cdc)
}

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

	/*
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
	*/

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
		/*
			simulation.NewWeightedProposalMsg(
				opWeightMsgSubmitPocValidation,
				defaultWeightMsgSubmitPocValidation,
				func(r *rand.Rand, ctx sdk.Context, accs []simtypes.Account) sdk.Msg {
					inferencesimulation.SimulateMsgSubmitPocValidation(am.accountKeeper, am.bankKeeper, am.keeper)
					return nil
				},
			),
		*/
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
			opWeightMsgSubmitHardwareDiff,
			defaultWeightMsgSubmitHardwareDiff,
			func(r *rand.Rand, ctx sdk.Context, accs []simtypes.Account) sdk.Msg {
				inferencesimulation.SimulateMsgSubmitHardwareDiff(am.accountKeeper, am.bankKeeper, am.keeper)
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

// WeightedOperationsX registers the real x/inference message factories with
// the simsx runner. simsx.Run prefers this method over the legacy
// WeightedOperations() when an AppModule implements HasWeightedOperationsX
// (see fork testutil/simsx/runner.go:333). Covered messages: the inference
// lifecycle (SubmitNewParticipant, Start/Finish, Validation, RevalidationVote,
// ClaimRewards), the PoC-v2 chain (PoCV2StoreCommit, MLNodeWeightDistribution,
// SubmitPocValidationsV2, SubmitSeed), and the participant-self ops
// (SubmitUnitOfComputePriceProposal, SubmitHardwareDiff,
// SubmitNewUnfundedParticipant). Authority-gated ops (allow-list, UpdateParams)
// are not factory-driven — their signer is the gov module account, which a
// simsx factory cannot sign for; mutable params are instead varied via genesis
// fuzzing (see x/inference/simulation/genesis_fuzz.go).
func (am AppModule) WeightedOperationsX(weights simsx.WeightSource, reg simsx.Registry) {
	reg.Add(weights.Get(opWeightMsgSubmitNewParticipant, uint32(defaultWeightMsgSubmitNewParticipant)),
		inferencesimulation.MsgSubmitNewParticipantFactory(am.keeper))
	reg.Add(weights.Get(opWeightMsgStartInference, uint32(defaultWeightMsgStartInference)),
		inferencesimulation.MsgStartInferenceFactory(am.keeper))
	reg.Add(weights.Get(opWeightMsgFinishInference, uint32(defaultWeightMsgFinishInference)),
		inferencesimulation.MsgFinishInferenceFactory(am.keeper))
	reg.Add(weights.Get(opWeightMsgValidation, uint32(defaultWeightMsgValidation)),
		inferencesimulation.MsgValidationFactory(am.keeper))
	reg.Add(weights.Get(opWeightMsgRevalidationVote, uint32(defaultWeightMsgRevalidationVote)),
		inferencesimulation.MsgRevalidationVoteFactory(am.keeper, am.groupMsgServer))
	reg.Add(weights.Get(opWeightMsgClaimRewards, uint32(defaultWeightMsgClaimRewards)),
		inferencesimulation.MsgClaimRewardsFactory(am.keeper))
	reg.Add(weights.Get(opWeightMsgPoCV2StoreCommit, uint32(defaultWeightMsgPoCV2StoreCommit)),
		inferencesimulation.MsgPoCV2StoreCommitFactory(am.keeper))
	reg.Add(weights.Get(opWeightMsgMLNodeWeightDistribution, uint32(defaultWeightMsgMLNodeWeightDistribution)),
		inferencesimulation.MsgMLNodeWeightDistributionFactory(am.keeper))
	reg.Add(weights.Get(opWeightMsgSubmitPocValidationsV2, uint32(defaultWeightMsgSubmitPocValidationsV2)),
		inferencesimulation.MsgSubmitPocValidationsV2Factory(am.keeper))
	reg.Add(weights.Get(opWeightMsgSubmitSeed, uint32(defaultWeightMsgSubmitSeed)),
		inferencesimulation.MsgSubmitSeedFactory(am.keeper))
	reg.Add(weights.Get(opWeightMsgSubmitUnitOfComputePriceProposal, uint32(defaultWeightMsgSubmitUnitOfComputePriceProposal)),
		inferencesimulation.MsgSubmitUnitOfComputePriceProposalFactory(am.keeper))
	reg.Add(weights.Get(opWeightMsgSubmitHardwareDiff, uint32(defaultWeightMsgSubmitHardwareDiff)),
		inferencesimulation.MsgSubmitHardwareDiffFactory(am.keeper))
	reg.Add(weights.Get(opWeightMsgSubmitNewUnfundedParticipant, uint32(defaultWeightMsgSubmitNewUnfundedParticipant)),
		inferencesimulation.MsgSubmitNewUnfundedParticipantFactory(am.keeper))
}
