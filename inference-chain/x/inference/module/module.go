package inference

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math/rand"

	"cosmossdk.io/core/appmodule"
	"cosmossdk.io/core/store"
	"cosmossdk.io/depinject"
	"cosmossdk.io/log"
	wasmkeeper "github.com/CosmWasm/wasmd/x/wasm/keeper"
	"github.com/cosmos/cosmos-sdk/client"
	"github.com/cosmos/cosmos-sdk/codec"
	cdctypes "github.com/cosmos/cosmos-sdk/codec/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/types/module"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	govtypes "github.com/cosmos/cosmos-sdk/x/gov/types"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"
	"github.com/grpc-ecosystem/grpc-gateway/runtime"
	"github.com/productscience/inference/x/inference/calculations"
	"github.com/productscience/inference/x/inference/epochgroup"
	"github.com/shopspring/decimal"

	// this line is used by starport scaffolding # 1

	modulev1 "github.com/productscience/inference/api/inference/inference/module"
	"github.com/productscience/inference/x/inference/keeper"
	"github.com/productscience/inference/x/inference/types"
)

var (
	_ module.AppModuleBasic      = (*AppModule)(nil)
	_ module.AppModuleSimulation = (*AppModule)(nil)
	_ module.HasGenesis          = (*AppModule)(nil)
	_ module.HasInvariants       = (*AppModule)(nil)
	_ module.HasConsensusVersion = (*AppModule)(nil)

	_ appmodule.AppModule       = (*AppModule)(nil)
	_ appmodule.HasBeginBlocker = (*AppModule)(nil)
	_ appmodule.HasEndBlocker   = (*AppModule)(nil)
)

// ----------------------------------------------------------------------------
// AppModuleBasic
// ----------------------------------------------------------------------------

// AppModuleBasic implements the AppModuleBasic interface that defines the
// independent methods a Cosmos SDK module needs to implement.
type AppModuleBasic struct {
	cdc codec.BinaryCodec
}

func NewAppModuleBasic(cdc codec.BinaryCodec) AppModuleBasic {
	return AppModuleBasic{cdc: cdc}
}

// Name returns the name of the module as a string.
func (AppModuleBasic) Name() string {
	return types.ModuleName
}

// RegisterLegacyAminoCodec registers the amino codec for the module, which is used
// to marshal and unmarshal structs to/from []byte in order to persist them in the module's KVStore.
func (AppModuleBasic) RegisterLegacyAminoCodec(cdc *codec.LegacyAmino) {}

// RegisterInterfaces registers a module's interface types and their concrete implementations as proto.Message.
func (a AppModuleBasic) RegisterInterfaces(reg cdctypes.InterfaceRegistry) {
	types.RegisterInterfaces(reg)
}

// DefaultGenesis returns a default GenesisState for the module, marshalled to json.RawMessage.
// The default GenesisState need to be defined by the module developer and is primarily used for testing.
func (AppModuleBasic) DefaultGenesis(cdc codec.JSONCodec) json.RawMessage {
	return cdc.MustMarshalJSON(types.DefaultGenesis())
}

// ValidateGenesis used to validate the GenesisState, given in its json.RawMessage form.
func (AppModuleBasic) ValidateGenesis(cdc codec.JSONCodec, config client.TxEncodingConfig, bz json.RawMessage) error {
	var genState types.GenesisState
	if err := cdc.UnmarshalJSON(bz, &genState); err != nil {
		return fmt.Errorf("failed to unmarshal %s genesis state: %w", types.ModuleName, err)
	}
	return genState.Validate()
}

// RegisterGRPCGatewayRoutes registers the gRPC Gateway routes for the module.
func (AppModuleBasic) RegisterGRPCGatewayRoutes(clientCtx client.Context, mux *runtime.ServeMux) {
	if err := types.RegisterQueryHandlerClient(context.Background(), mux, types.NewQueryClient(clientCtx)); err != nil {
		panic(err)
	}
}

// ----------------------------------------------------------------------------
// AppModule
// ----------------------------------------------------------------------------

// AppModule implements the AppModule interface that defines the inter-dependent methods that modules need to implement
type AppModule struct {
	AppModuleBasic

	keeper         keeper.Keeper
	accountKeeper  types.AccountKeeper
	bankKeeper     types.BankKeeper
	groupMsgServer types.GroupMessageKeeper
}

func NewAppModule(
	cdc codec.Codec,
	keeper keeper.Keeper,
	accountKeeper types.AccountKeeper,
	bankKeeper types.BankKeeper,
	groupMsgServer types.GroupMessageKeeper,
) AppModule {
	return AppModule{
		AppModuleBasic: NewAppModuleBasic(cdc),
		keeper:         keeper,
		accountKeeper:  accountKeeper,
		bankKeeper:     bankKeeper,
		groupMsgServer: groupMsgServer,
	}
}

// RegisterServices registers a gRPC query service to respond to the module-specific gRPC queries
func (am AppModule) RegisterServices(cfg module.Configurator) {
	types.RegisterMsgServer(cfg.MsgServer(), keeper.NewMsgServerImpl(am.keeper))
	types.RegisterQueryServer(cfg.QueryServer(), am.keeper)
}

// RegisterInvariants registers the invariants of the module. If an invariant deviates from its predicted value, the InvariantRegistry triggers appropriate logic (most often the chain will be halted)
func (am AppModule) RegisterInvariants(_ sdk.InvariantRegistry) {}

// InitGenesis performs the module's genesis initialization. It returns no validator updates.
func (am AppModule) InitGenesis(ctx sdk.Context, cdc codec.JSONCodec, gs json.RawMessage) {
	var genState types.GenesisState
	// Initialize global index to index in genesis state
	cdc.MustUnmarshalJSON(gs, &genState)

	InitGenesis(ctx, am.keeper, genState)
}

// ExportGenesis returns the module's exported genesis state as raw JSON bytes.
func (am AppModule) ExportGenesis(ctx sdk.Context, cdc codec.JSONCodec) json.RawMessage {
	genState := ExportGenesis(ctx, am.keeper)
	return cdc.MustMarshalJSON(genState)
}

// ConsensusVersion is a sequence number for state-breaking change of the module.
// It should be incremented on each consensus-breaking change introduced by the module.
// To avoid wrong/empty versions, the initial version should be set to 1.
func (AppModule) ConsensusVersion() uint64 { return 3 }

// BeginBlock contains the logic that is automatically triggered at the beginning of each block.
// The begin block implementation is optional.
func (am AppModule) BeginBlock(_ context.Context) error {
	return nil
}

func (am AppModule) expireInferences(ctx context.Context, timeouts []types.InferenceTimeout) error {
	for _, i := range timeouts {
		inference, found := am.keeper.GetInference(ctx, i.InferenceId)
		if !found {
			continue
		}
		if inference.Status == types.InferenceStatus_STARTED {
			am.handleExpiredInference(ctx, inference)
		}
	}
	return nil
}

func (am AppModule) handleExpiredInference(ctx context.Context, inference types.Inference) {
	executor, found := am.keeper.GetParticipant(ctx, inference.AssignedTo)
	if !found {
		am.LogWarn("Unable to find participant for expired inference", types.Inferences, "inferenceId", inference.InferenceId, "executedBy", inference.ExecutedBy)
		return
	}
	am.LogInfo("Inference expired, not finished. Issuing refund", types.Inferences, "inferenceId", inference.InferenceId, "executor", inference.AssignedTo)
	inference.Status = types.InferenceStatus_EXPIRED
	inference.ActualCost = 0
	err := am.keeper.IssueRefund(ctx, uint64(inference.EscrowAmount), inference.RequestedBy, "expired_inference:"+inference.InferenceId)
	if err != nil {
		am.LogError("Error issuing refund", types.Inferences, "error", err)
	}
	am.keeper.SetInference(ctx, inference)
	am.keeper.SetParticipant(ctx, executor)
}

// EndBlock contains the logic that is automatically triggered at the end of each block.
// The end block implementation is optional.
func (am AppModule) EndBlock(ctx context.Context) error {
	sdkCtx := sdk.UnwrapSDKContext(ctx)
	blockHeight := sdkCtx.BlockHeight()
	blockTime := sdkCtx.BlockTime().Unix()
	epochParams := am.keeper.GetParams(ctx).EpochParams
	currentEpoch, found := am.keeper.GetEffectiveEpoch(ctx)
	if !found || currentEpoch == nil {
		am.LogError("Unable to get effective epoch", types.EpochGroup, "blockHeight", blockHeight)
		return nil
	}
	epochContext := types.NewEpochContextFromEffectiveEpoch(*currentEpoch, *epochParams, blockHeight)

	currentEpochGroup, err := am.keeper.GetEpochGroupForEpoch(ctx, *currentEpoch)
	// TODO: Why error here?
	if err != nil {
		am.LogError("Unable to get current epoch group", types.EpochGroup, "error", err.Error())
		return nil
	}

	timeouts := am.keeper.GetAllInferenceTimeoutForHeight(ctx, uint64(blockHeight))
	err = am.expireInferences(ctx, timeouts)
	if err != nil {
		am.LogError("Error expiring inferences", types.Inferences)
	}
	for _, t := range timeouts {
		am.keeper.RemoveInferenceTimeout(ctx, t.ExpirationHeight, t.InferenceId)
	}

	partialUpgrades := am.keeper.GetAllPartialUpgrade(ctx)
	for _, pu := range partialUpgrades {
		if pu.Height < uint64(blockHeight) {
			am.LogInfo("PartialUpgradeExpired", types.Upgrades, "partialUpgradeHeight", pu.Height, "blockHeight", blockHeight)
			am.keeper.RemovePartialUpgrade(ctx, pu.Height)
		}
	}

	// Stage execution order for epoch transitions:
	// 1. IsEndOfPoCValidationStage: Complete all epoch formation (onEndOfPoCValidationStage)
	// 2. IsSetNewValidatorsStage: Switch validators and activate epoch (onSetNewValidatorsStage)
	// This separation ensures clean boundaries between epoch preparation and validator switching
	// and allow time for api nodes to load models on ml nodes.

	if epochContext.IsEndOfPoCValidationStage(blockHeight) {
		am.LogInfo("onEndOfPoCValidationStage start", types.Stages, "blockHeight", blockHeight)
		am.onEndOfPoCValidationStage(ctx, blockHeight, blockTime)
	}

	if epochContext.IsSetNewValidatorsStage(blockHeight) {
		am.LogInfo("onSetNewValidatorsStage start", types.Stages, "blockHeight", blockHeight)
		am.onSetNewValidatorsStage(ctx, blockHeight, blockTime)
		am.keeper.SetEffectiveEpochIndex(ctx, getNextEpochIndex(*currentEpoch))
	}

	if epochContext.IsStartOfPocStage(blockHeight) {
		upcomingEpoch := createNewEpoch(*currentEpoch, blockHeight)
		am.keeper.SetEpoch(ctx, upcomingEpoch)

		am.LogInfo("NewPocStart", types.Stages, "blockHeight", blockHeight)
		newGroup, err := am.keeper.CreateEpochGroup(ctx, uint64(blockHeight), upcomingEpoch.Index)
		if err != nil {
			am.LogError("Unable to create epoch group", types.EpochGroup, "error", err.Error())
			return err
		}
		err = newGroup.CreateGroup(ctx)
		if err != nil {
			am.LogError("Unable to create epoch group", types.EpochGroup, "error", err.Error())
			return err
		}
	}

	if currentEpochGroup.IsChanged(ctx) {
		am.LogInfo("EpochGroupChanged", types.EpochGroup, "blockHeight", blockHeight)
		computeResult, err := currentEpochGroup.GetComputeResults(ctx)
		if err != nil {
			am.LogError("Unable to get compute results", types.EpochGroup, "error", err.Error())
			return nil
		}
		am.LogInfo("EpochGroupChanged", types.EpochGroup, "computeResult", computeResult, "error", err)

		_, err = am.keeper.Staking.SetComputeValidators(ctx, computeResult)
		if err != nil {
			am.LogError("Unable to update epoch group", types.EpochGroup, "error", err.Error())
		}
		currentEpochGroup.MarkUnchanged(ctx)
	}

	return nil
}

func createNewEpoch(prevEpoch types.Epoch, blockHeight int64) *types.Epoch {
	return &types.Epoch{
		Index:               getNextEpochIndex(prevEpoch),
		PocStartBlockHeight: int64(blockHeight),
	}
}

func getNextEpochIndex(prevEpoch types.Epoch) uint64 {
	return prevEpoch.Index + 1
}

// onEndOfPoCValidationStage handles all epoch formation logic at the end of PoC validation.
// This stage is responsible for:
// - Account settling from the previous epoch
// - Computing new weights based on PoC results
// - Setting models for participants (MLNode allocation)
// - Registering top miners
// - Setting active participants for the upcoming epoch
// - Adding epoch members to the upcoming epoch group
// This stage executes at IsEndOfPoCValidationStage(blockHeight) and must complete
// before validator switching occurs in onSetNewValidatorsStage.
func (am AppModule) onEndOfPoCValidationStage(ctx context.Context, blockHeight int64, blockTime int64) {
	effectiveEpoch, found := am.keeper.GetEffectiveEpoch(ctx)
	if !found {
		am.LogError("onEndOfPoCValidationStage: Unable to get effective epoch", types.EpochGroup, "blockHeight", blockHeight)
		return
	}

	err := am.keeper.SettleAccounts(ctx, uint64(effectiveEpoch.PocStartBlockHeight))
	if err != nil {
		am.LogError("onEndOfPoCValidationStage: Unable to settle accounts", types.Settle, "error", err.Error())
	}

	upcomingEpoch, found := am.keeper.GetUpcomingEpoch(ctx)
	if !found || upcomingEpoch == nil {
		am.LogError("onEndOfPoCValidationStage: Unable to get upcoming epoch group", types.EpochGroup)
		return
	}

	activeParticipants := am.ComputeNewWeights(ctx, *upcomingEpoch)
	if activeParticipants == nil {
		am.LogError("onEndOfPoCValidationStage: computeResult == nil && activeParticipants == nil", types.PoC)
		return
	}

	am.setModelsForParticipants(ctx, activeParticipants, *upcomingEpoch)

	err = am.RegisterTopMiners(ctx, activeParticipants, blockTime)
	if err != nil {
		am.LogError("onEndOfPoCValidationStage: Unable to register top miners", types.Tokenomics, "error", err.Error())
		return
	}

	am.LogInfo("onEndOfPoCValidationStage: computed new weights", types.Stages,
		"PocStartBlockHeight", upcomingEpoch.PocStartBlockHeight,
		"len(activeParticipants)", len(activeParticipants))

	am.keeper.SetActiveParticipants(ctx, types.ActiveParticipants{
		Participants:        activeParticipants,
		EpochGroupId:        upcomingEpoch.Index,
		EpochId:             upcomingEpoch.Index,
		PocStartBlockHeight: upcomingEpoch.PocStartBlockHeight,
		// TODO [PRTODO]: not sure EffectiveBlockHeight is set by now
		EffectiveBlockHeight: blockHeight + 2, // FIXME: verify it's +2, I'm not sure
		CreatedAtBlockHeight: blockHeight,
	})

	upcomingEg, err := am.keeper.GetEpochGroupForEpoch(ctx, *upcomingEpoch)
	if err != nil {
		am.LogError("onEndOfPoCValidationStage: Unable to get epoch group for upcoming epoch", types.EpochGroup,
			"upcomingEpoch.Index", upcomingEpoch.Index, "upcomingEpoch.PocStartBlockHeight", upcomingEpoch.PocStartBlockHeight, "error", err.Error())
		return
	}

	am.addEpochMembers(ctx, upcomingEg, activeParticipants)
}

// onSetNewValidatorsStage handles validator switching and epoch group activation.
// This stage is responsible for:
// - Computing unit of compute price for the upcoming epoch
// - Moving the upcoming epoch group to effective status
// - Switching the active validator set
// - Setting the effective epoch index
// This stage executes at IsSetNewValidatorsStage(blockHeight) and should run after
// all epoch formation logic has completed in onEndOfPoCValidationStage.
// The stage focuses solely on validator switching, with all epoch preparation
// handled by the previous stage for clean separation of concerns.
func (am AppModule) onSetNewValidatorsStage(ctx context.Context, blockHeight int64, blockTime int64) {
	am.LogInfo("onSetNewValidatorsStage start", types.Stages, "blockHeight", blockHeight)

	upcomingEpoch, found := am.keeper.GetUpcomingEpoch(ctx)
	if !found || upcomingEpoch == nil {
		am.LogError("onSetNewValidatorsStage: Unable to get upcoming epoch group", types.EpochGroup)
		return
	}

	upcomingEg, err := am.keeper.GetEpochGroupForEpoch(ctx, *upcomingEpoch)
	if err != nil {
		am.LogError("onSetNewValidatorsStage: Unable to get epoch group for upcoming epoch", types.EpochGroup,
			"upcomingEpoch.Index", upcomingEpoch.Index, "upcomingEpoch.PocStartBlockHeight", upcomingEpoch.PocStartBlockHeight, "error", err.Error())
		return
	}

	unitOfComputePrice, err := am.computePrice(ctx, *upcomingEpoch, upcomingEg)
	if err != nil {
		am.LogError("onSetNewValidatorsStage: Unable to compute price", types.Pricing, "error", err.Error())
		return
	}

	// TODO: Move this so active participants are set 1 block before new validators
	am.moveUpcomingToEffectiveGroup(ctx, blockHeight, unitOfComputePrice)
}

func (am AppModule) addEpochMembers(ctx context.Context, upcomingEg *epochgroup.EpochGroup, activeParticipants []*types.ActiveParticipant) {
	validationParams := am.keeper.GetParams(ctx).ValidationParams

	for _, p := range activeParticipants {
		// FIXME: add some centralized way that'd govern key enc/dec rules
		reputation, err := am.calculateParticipantReputation(ctx, p, validationParams)
		if err != nil {
			am.LogError("onSetNewValidatorsStage: Unable to calculate participant reputation", types.EpochGroup, "error", err.Error())
			reputation = 0
		}
		member := epochgroup.NewEpochMemberFromActiveParticipant(p, reputation)
		err = upcomingEg.AddMember(ctx, member)
		if err != nil {
			am.LogError("onSetNewValidatorsStage: Unable to add member", types.EpochGroup, "error", err.Error())
			continue
		}
	}
}

func (am AppModule) computePrice(ctx context.Context, upcomingEpoch types.Epoch, upcomingEg *epochgroup.EpochGroup) (uint64, error) {
	var defaultPrice int64
	if upcomingEpoch.Index > 1 {
		currentEg, err := am.keeper.GetCurrentEpochGroup(ctx)
		if err != nil {
			am.LogError("onSetNewValidatorsStage: Unable to get current epoch group", types.EpochGroup, "error", err.Error())
			return 0, err
		}
		defaultPrice = currentEg.GroupData.UnitOfComputePrice
	} else {
		defaultPrice = am.keeper.GetParams(ctx).EpochParams.DefaultUnitOfComputePrice
	}

	proposals, err := am.keeper.AllUnitOfComputePriceProposals(ctx)
	if err != nil {
		am.LogError("onSetNewValidatorsStage: Unable to get all unit of compute price proposals", types.Pricing, "error", err.Error())
		return 0, err
	}

	am.LogInfo("onSetNewValidatorsStage: unitOfCompute: retrieved proposals", types.Pricing, "len(proposals)", len(proposals))

	medianProposal, err := upcomingEg.ComputeUnitOfComputePrice(ctx, proposals, uint64(defaultPrice))
	am.LogInfo("onSetNewValidatorsStage: unitOfCompute: ", types.Pricing, "medianProposal", medianProposal)
	if err != nil {
		am.LogError("onSetNewValidatorsStage: unitOfCompute: onSetNewValidatorsStage: Unable to compute unit of compute price", types.Pricing, "error", err.Error())
		return 0, err
	}

	return medianProposal, nil
}

func (am AppModule) calculateParticipantReputation(ctx context.Context, p *types.ActiveParticipant, params *types.ValidationParams) (int64, error) {
	summaries := am.keeper.GetEpochPerformanceSummariesByParticipant(ctx, p.Index)

	reputationContext := calculations.ReputationContext{
		EpochCount:           int64(len(summaries)),
		EpochMissPercentages: make([]decimal.Decimal, len(summaries)),
		ValidationParams:     params,
	}

	for i, summary := range summaries {
		inferenceCount := decimal.NewFromInt(int64(summary.InferenceCount))
		if inferenceCount.IsZero() {
			reputationContext.EpochMissPercentages[i] = decimal.Zero
			continue
		}

		missed := decimal.NewFromInt(int64(summary.MissedRequests))
		reputationMetric := missed.Div(inferenceCount)
		reputationContext.EpochMissPercentages[i] = reputationMetric
	}

	reputation := calculations.CalculateReputation(&reputationContext)

	return reputation, nil
}

func (am AppModule) moveUpcomingToEffectiveGroup(ctx context.Context, blockHeight int64, unitOfComputePrice uint64) {
	newEpochPocStartHeight, found := am.keeper.GetUpcomingEpochPocStartHeight(ctx)
	if !found {
		am.LogError("MoveUpcomingToEffectiveGroup: Unable to get upcoming epoch group id", types.EpochGroup, "blockHeight", blockHeight)
		return
	}

	previousEpochPocStartHeight, found := am.keeper.GetEffectiveEpochPocStartHeight(ctx)
	if !found {
		am.LogError("MoveUpcomingToEffectiveGroup: Unable to get upcoming epoch group id", types.EpochGroup, "blockHeight", blockHeight)
		return
	}

	am.LogInfo("NewEpochGroup", types.EpochGroup, "blockHeight", blockHeight, "newEpochPocStartHeight", newEpochPocStartHeight)
	newGroupData, found := am.keeper.GetEpochGroupData(ctx, newEpochPocStartHeight, "")
	if !found {
		am.LogWarn("NewEpochGroupDataNotFound", types.EpochGroup, "blockHeight", blockHeight, "newEpochPocStartHeight", newEpochPocStartHeight)
		return
	}
	previousGroupData, found := am.keeper.GetEpochGroupData(ctx, previousEpochPocStartHeight, "")
	if !found {
		am.LogWarn("PreviousEpochGroupDataNotFound", types.EpochGroup, "blockHeight", blockHeight, "previousEpochPocStartHeight", previousEpochPocStartHeight)
		return
	}
	params := am.keeper.GetParams(ctx)
	newGroupData.EffectiveBlockHeight = blockHeight
	newGroupData.UnitOfComputePrice = int64(unitOfComputePrice)
	newGroupData.PreviousEpochRequests = previousGroupData.NumberOfRequests
	newGroupData.ValidationParams = params.ValidationParams

	previousGroupData.LastBlockHeight = blockHeight - 1

	am.keeper.SetEpochGroupData(ctx, newGroupData)
	am.keeper.SetEpochGroupData(ctx, previousGroupData)
}

// IsOnePerModuleType implements the depinject.OnePerModuleType interface.
func (am AppModule) IsOnePerModuleType() {}

// IsAppModule implements the appmodule.AppModule interface.
func (am AppModule) IsAppModule() {}

// ----------------------------------------------------------------------------
// App Wiring Setup
// ----------------------------------------------------------------------------

func init() {
	appmodule.Register(
		&modulev1.Module{},
		appmodule.Provide(ProvideModule),
	)
}

type ModuleInputs struct {
	depinject.In

	StoreService store.KVStoreService
	Cdc          codec.Codec
	Config       *modulev1.Module
	Logger       log.Logger

	AccountKeeper    types.AccountKeeper
	BankKeeper       types.BankKeeper
	BankEscrowKeeper types.BankEscrowKeeper
	ValidatorSet     types.ValidatorSet
	StakingKeeper    types.StakingKeeper
	GroupServer      types.GroupMessageKeeper
	GetWasmKeeper    func() wasmkeeper.Keeper `optional:"true"`
}

type ModuleOutputs struct {
	depinject.Out

	InferenceKeeper keeper.Keeper
	Module          appmodule.AppModule
	Hooks           stakingtypes.StakingHooksWrapper
}

func ProvideModule(in ModuleInputs) ModuleOutputs {
	// default to governance authority if not provided
	authority := authtypes.NewModuleAddress(govtypes.ModuleName)
	if in.Config.Authority != "" {
		authority = authtypes.NewModuleAddressOrBech32Address(in.Config.Authority)
	}

	k := keeper.NewKeeper(
		in.Cdc,
		in.StoreService,
		in.Logger,
		authority.String(),
		in.BankEscrowKeeper,
		in.BankKeeper,
		in.GroupServer,
		in.ValidatorSet,
		in.StakingKeeper,
		in.AccountKeeper,
		in.GetWasmKeeper,
	)

	m := NewAppModule(
		in.Cdc,
		k,
		in.AccountKeeper,
		in.BankKeeper,
		in.GroupServer,
	)

	return ModuleOutputs{
		InferenceKeeper: k,
		Module:          m,
		Hooks:           stakingtypes.StakingHooksWrapper{StakingHooks: StakingHooksLogger{}},
	}
}

func (am AppModule) LogInfo(msg string, subSystem types.SubSystem, keyvals ...interface{}) {
	kvWithSubsystem := append([]interface{}{"subsystem", subSystem.String()}, keyvals...)
	am.keeper.Logger().Info(msg, kvWithSubsystem...)
}

func (am AppModule) LogError(msg string, subSystem types.SubSystem, keyvals ...interface{}) {
	kvWithSubsystem := append([]interface{}{"subsystem", subSystem.String()}, keyvals...)
	am.keeper.Logger().Error(msg, kvWithSubsystem...)
}

func (am AppModule) LogWarn(msg string, subSystem types.SubSystem, keyvals ...interface{}) {
	kvWithSubsystem := append([]interface{}{"subsystem", subSystem.String()}, keyvals...)
	am.keeper.Logger().Warn(msg, kvWithSubsystem...)
}

func (am AppModule) LogDebug(msg string, subSystem types.SubSystem, keyvals ...interface{}) {
	kvWithSubsystem := append([]interface{}{"subsystem", subSystem.String()}, keyvals...)
	am.keeper.Logger().Debug(msg, kvWithSubsystem...)
}

func (am AppModule) setModelsForParticipants(ctx context.Context, participants []*types.ActiveParticipant, upcomingEpoch types.Epoch) {
	// TODO: We may need to populate throughput in MLNodeInfo using the model's ThroughputPerNonce
	// This would ensure consistent throughput calculations based on governance model parameters
	// rather than relying on hardware node declarations alone.
	const flowContext = "model_assignment"
	am.LogInfo("Starting model and slot assignment for participants", types.EpochGroup, "flow_context", flowContext, "step", "start", "num_participants", len(participants), "epoch_index", upcomingEpoch.Index)

	// Get governance models to iterate through
	governanceModels, err := am.keeper.GetGovernanceModels(ctx)
	if err != nil {
		am.LogError("setModelsForParticipants: Unable to get governance models", types.EpochGroup, "error", err.Error(), "flow_context", flowContext)
		return
	}
	am.LogInfo("Retrieved governance models", types.EpochGroup, "flow_context", flowContext, "step", "get_governance_models", "num_models", len(governanceModels))

	for _, p := range participants {
		am.LogInfo("Processing participant", types.EpochGroup, "flow_context", flowContext, "step", "participant_loop_start", "participant_index", p.Index)
		hardwareNodes, found := am.keeper.GetHardwareNodes(ctx, p.Index)
		if !found {
			// No hardware nodes - just set empty arrays
			am.LogInfo("No hardware nodes found for participant, skipping model assignment.", types.EpochGroup, "flow_context", flowContext, "step", "no_hardware_nodes", "participant_index", p.Index)
			p.Models = make([]string, 0)
			p.MlNodes = make([]*types.ModelMLNodes, 0)
			continue
		}

		// Get the original MLNodes from the first array (index 0) - populated by task 5.8
		var originalMLNodes []*types.MLNodeInfo
		if len(p.MlNodes) > 0 && p.MlNodes[0] != nil {
			originalMLNodes = p.MlNodes[0].MlNodes
		}
		am.LogInfo("Original ML nodes before legacy weight distribution", types.EpochGroup, "flow_context", flowContext, "step", "pre_legacy_distribution", "participant_index", p.Index, "ml_nodes", originalMLNodes)

		// Handle legacy PoC weight distribution for batches without NodeId
		originalMLNodes = am.distributeLegacyWeight(originalMLNodes, hardwareNodes)
		am.LogInfo("ML nodes after legacy weight distribution", types.EpochGroup, "flow_context", flowContext, "step", "post_legacy_distribution", "participant_index", p.Index, "ml_nodes", originalMLNodes)

		// Set PRE_POC_SLOT to true and POC_SLOT to false for all MLNodes (default to mining PoC)
		for _, mlNode := range originalMLNodes {
			// Initialize timeslot allocation vector: [PRE_POC_SLOT=true, POC_SLOT=false]
			mlNode.TimeslotAllocation = []bool{true, false} // index 0=PRE_POC_SLOT, index 1=POC_SLOT
		}
		am.LogInfo("Initialized all ML nodes to PRE_POC_SLOT=true, POC_SLOT=false", types.EpochGroup, "flow_context", flowContext, "step", "init_slots", "participant_index", p.Index)

		// Track which MLNodes have been assigned
		assignedMLNodes := make(map[string]bool)
		var supportedModels []string
		var newMLNodeArrays []*types.ModelMLNodes

		// For each governance model, pick the first available MLNode that supports it
		for _, model := range governanceModels {
			am.LogInfo("Attempting to assign ML node for model", types.EpochGroup, "flow_context", flowContext, "step", "model_assignment_loop", "participant_index", p.Index, "model_id", model.Id)
			var modelMLNodes []*types.MLNodeInfo

			for _, mlNode := range originalMLNodes {
				if assignedMLNodes[mlNode.NodeId] {
					am.LogInfo("Skipping already assigned ML node", types.EpochGroup, "flow_context", flowContext, "step", "node_already_assigned", "participant_index", p.Index, "model_id", model.Id, "node_id", mlNode.NodeId)
					continue // MLNode already assigned to another model
				}

				// Check if this MLNode supports the current governance model
				if nodeSupportsModel(hardwareNodes, mlNode.NodeId, model.Id) {
					am.LogInfo("Found supporting and unassigned ML node for model", types.EpochGroup, "flow_context", flowContext, "step", "assign_node_to_model", "participant_index", p.Index, "model_id", model.Id, "node_id", mlNode.NodeId)
					// Add this MLNode to the current model's array
					modelMLNodes = append(modelMLNodes, mlNode)
					assignedMLNodes[mlNode.NodeId] = true
					break // Move to next governance model (only one MLNode per model)
				}
			}

			// Only add the model and MLNode array if we found supporting MLNodes
			if len(modelMLNodes) > 0 {
				supportedModels = append(supportedModels, model.Id)
				newMLNodeArrays = append(newMLNodeArrays, &types.ModelMLNodes{MlNodes: modelMLNodes})
				am.LogInfo("Assigned ML nodes to model", types.EpochGroup, "flow_context", flowContext, "step", "model_assignment_complete", "participant_index", p.Index, "model_id", model.Id, "assigned_nodes", modelMLNodes)
			} else {
				am.LogInfo("No available ML nodes support this model", types.EpochGroup, "flow_context", flowContext, "step", "no_supporting_nodes", "participant_index", p.Index, "model_id", model.Id)
			}
		}

		// Add remaining unassigned MLNodes as overflow array (if any exist)
		var unassignedMLNodes []*types.MLNodeInfo
		for _, mlNode := range originalMLNodes {
			if !assignedMLNodes[mlNode.NodeId] {
				unassignedMLNodes = append(unassignedMLNodes, mlNode)
			}
		}
		if len(unassignedMLNodes) > 0 {
			newMLNodeArrays = append(newMLNodeArrays, &types.ModelMLNodes{MlNodes: unassignedMLNodes})
			am.LogInfo("Added unassigned ML nodes to overflow array", types.EpochGroup, "flow_context", flowContext, "step", "overflow_nodes", "participant_index", p.Index, "unassigned_nodes", unassignedMLNodes)
		}

		// Update participant with reorganized MLNode arrays and supported models
		p.MlNodes = newMLNodeArrays
		p.Models = supportedModels
		am.LogInfo("Participant models and ML nodes updated before 50% allocation", types.EpochGroup, "flow_context", flowContext, "step", "pre_50_percent_alloc", "participant_index", p.Index, "supported_models", p.Models, "ml_nodes", p.MlNodes)

		// Task 6.2.2: Apply 50% weight allocation logic
		am.apply50PercentWeightAllocation(upcomingEpoch, p, supportedModels)
		am.LogInfo("Finished 50% weight allocation", types.EpochGroup, "flow_context", flowContext, "step", "post_50_percent_alloc", "participant_index", p.Index, "final_ml_nodes", p.MlNodes)
	}
	am.LogInfo("Finished model and slot assignment for all participants", types.EpochGroup, "flow_context", flowContext, "step", "end")
}

// apply50PercentWeightAllocation implements the 50% node allocation logic for PoC slots
// For each model, at most 50% of nodes (with floor rounding) will serve inference
func (am AppModule) apply50PercentWeightAllocation(upcomingEpoch types.Epoch, participant *types.ActiveParticipant, supportedModels []string) {
	const flowContext = "model_assignment"
	const subFlowContext = "apply_50_percent_allocation"
	am.LogInfo("Starting 50% node allocation for PoC slots", types.EpochGroup, "flow_context", flowContext, "sub_flow_context", subFlowContext, "step", "start", "participant_index", participant.Index)
	// Process each model separately
	for modelIdx, modelId := range supportedModels {
		am.LogInfo("Processing model for 50% allocation", types.EpochGroup, "flow_context", flowContext, "sub_flow_context", subFlowContext, "step", "model_loop_start", "participant_index", participant.Index, "model_id", modelId)
		if modelIdx >= len(participant.MlNodes) {
			am.LogInfo("Model index is out of bounds, skipping", types.EpochGroup, "flow_context", flowContext, "sub_flow_context", subFlowContext, "step", "model_index_oob", "participant_index", participant.Index, "model_id", modelId, "model_idx", modelIdx)
			continue // Skip if model index is out of bounds
		}

		modelMLNodes := participant.MlNodes[modelIdx].MlNodes
		if len(modelMLNodes) == 0 {
			am.LogInfo("No ML nodes for this model, skipping allocation", types.EpochGroup, "flow_context", flowContext, "sub_flow_context", subFlowContext, "step", "no_ml_nodes", "participant_index", participant.Index, "model_id", modelId)
			continue
		}

		// Create deterministic random seed from epoch ID, participant address, and model ID
		seed := fmt.Sprintf("%d_%s_%s", upcomingEpoch.Index, participant.Index, modelId)
		hash := sha256.Sum256([]byte(seed))
		seedInt := int64(binary.BigEndian.Uint64(hash[:8]))
		am.LogInfo("Generated deterministic seed for random shuffling", types.EpochGroup, "flow_context", flowContext, "sub_flow_context", subFlowContext, "step", "generate_seed", "participant_index", participant.Index, "model_id", modelId, "seed_string", seed, "seed_int", seedInt)

		// Create random generator with deterministic seed for this model
		rng := rand.New(rand.NewSource(seedInt))

		// Create shuffled node indices for deterministic random order
		nodeIndices := make([]int, len(modelMLNodes))
		for i := range nodeIndices {
			nodeIndices[i] = i
		}
		rng.Shuffle(len(nodeIndices), func(i, j int) {
			nodeIndices[i], nodeIndices[j] = nodeIndices[j], nodeIndices[i]
		})
		am.LogInfo("Shuffled node indices for model", types.EpochGroup, "flow_context", flowContext, "sub_flow_context", subFlowContext, "step", "shuffle_nodes", "participant_index", participant.Index, "model_id", modelId, "shuffled_indices", nodeIndices)

		// Calculate how many nodes can serve inference (at most 50% with floor rounding)
		totalNodes := len(modelMLNodes)
		nodesToInference := totalNodes / 2 // This gives us floor(totalNodes / 2)
		am.LogInfo("Calculated node allocation for inference", types.EpochGroup, "flow_context", flowContext, "sub_flow_context", subFlowContext, "step", "calculate_allocation", "participant_index", participant.Index, "model_id", modelId, "total_nodes", totalNodes, "nodes_to_inference", nodesToInference)

		// Set POC_SLOT to true for the first nodesToInference shuffled nodes
		var inferenceNodeIds []string
		var pocOnlyNodeIds []string
		for i, nodeIdx := range nodeIndices {
			mlNode := modelMLNodes[nodeIdx]
			if i < nodesToInference {
				if len(mlNode.TimeslotAllocation) > 1 {
					mlNode.TimeslotAllocation[1] = true // Set POC_SLOT to true (serve inference)
					am.LogInfo("Setting POC_SLOT=true for node", types.EpochGroup, "flow_context", flowContext, "sub_flow_context", subFlowContext, "step", "set_poc_slot", "participant_index", participant.Index, "model_id", modelId, "node_id", mlNode.NodeId)
				}
				inferenceNodeIds = append(inferenceNodeIds, mlNode.NodeId)
			} else {
				pocOnlyNodeIds = append(pocOnlyNodeIds, mlNode.NodeId)
			}
		}

		// Log the allocation for debugging
		am.LogInfo("Applied 50% node allocation for model", types.EpochGroup,
			"flow_context", flowContext, "sub_flow_context", subFlowContext, "step", "allocation_summary",
			"participantIndex", participant.Index,
			"modelId", modelId,
			"totalNodes", totalNodes,
			"nodesToInference", nodesToInference,
			"inferenceNodeIds", inferenceNodeIds,
			"nodesToPoC", totalNodes-nodesToInference,
			"pocOnlyNodeIds", pocOnlyNodeIds)
	}
	am.LogInfo("Finished 50% node allocation for participant", types.EpochGroup, "flow_context", flowContext, "sub_flow_context", subFlowContext, "step", "end", "participant_index", participant.Index)
}

// distributeLegacyWeight handles legacy PoC batches by distributing weight from
// MLNodes with empty NodeId among actual hardware nodes
func (am AppModule) distributeLegacyWeight(originalMLNodes []*types.MLNodeInfo, hardwareNodes *types.HardwareNodes) []*types.MLNodeInfo {
	const flowContext = "model_assignment"
	const subFlowContext = "distribute_legacy_weight"
	am.LogInfo("Starting legacy weight distribution", types.PoC, "flow_context", flowContext, "sub_flow_context", subFlowContext, "step", "start")

	if len(originalMLNodes) == 0 || hardwareNodes == nil || len(hardwareNodes.HardwareNodes) == 0 {
		am.LogInfo("Empty inputs, returning original list.", types.PoC, "flow_context", flowContext, "sub_flow_context", subFlowContext, "step", "empty_inputs")
		return originalMLNodes
	}

	// Find MLNode with empty NodeId (legacy batches)
	var legacyMLNode *types.MLNodeInfo
	var legacyIndex int = -1

	for i, mlNode := range originalMLNodes {
		if mlNode.NodeId == "" {
			legacyMLNode = mlNode
			legacyIndex = i
			break
		}
	}

	// If no legacy MLNode found, return original list unchanged
	if legacyMLNode == nil {
		am.LogInfo("No legacy ML Node with empty NodeId found, returning original list.", types.PoC, "flow_context", flowContext, "sub_flow_context", subFlowContext, "step", "no_legacy_node")
		return originalMLNodes
	}
	am.LogInfo("Found legacy ML node to distribute weight from", types.PoC, "flow_context", flowContext, "sub_flow_context", subFlowContext, "step", "found_legacy_node", "legacy_node", legacyMLNode)

	// Remove the legacy MLNode from the list
	newMLNodes := make([]*types.MLNodeInfo, 0, len(originalMLNodes)-1)
	newMLNodes = append(newMLNodes, originalMLNodes[:legacyIndex]...)
	newMLNodes = append(newMLNodes, originalMLNodes[legacyIndex+1:]...)

	// Calculate weight per hardware node
	totalLegacyWeight := legacyMLNode.PocWeight
	numHardwareNodes := int64(len(hardwareNodes.HardwareNodes))
	weightPerNode := totalLegacyWeight / numHardwareNodes
	remainderWeight := totalLegacyWeight % numHardwareNodes
	am.LogInfo("Calculated weight distribution", types.PoC, "flow_context", flowContext, "sub_flow_context", subFlowContext, "step", "calculate_distribution", "total_legacy_weight", totalLegacyWeight, "num_hardware_nodes", numHardwareNodes, "weight_per_node", weightPerNode, "remainder_weight", remainderWeight)

	// Distribute weight among hardware nodes
	// Give weightPerNode to each, then distribute remainder by giving +1 to first nodes until remainder is over
	for i, hwNode := range hardwareNodes.HardwareNodes {
		nodeId := hwNode.LocalId
		distributedWeight := weightPerNode
		if int64(i) < remainderWeight {
			distributedWeight++ // Give +1 to first remainderWeight nodes
		}

		if distributedWeight <= 0 {
			continue
		}
		am.LogInfo("Distributing weight to hardware node", types.PoC, "flow_context", flowContext, "sub_flow_context", subFlowContext, "step", "distribute_to_node", "node_id", nodeId, "distributed_weight", distributedWeight)

		// Find existing MLNode for this hardware node
		found := false
		for _, existingMLNode := range newMLNodes {
			if existingMLNode.NodeId == nodeId {
				// Add distributed weight to existing MLNode
				existingMLNode.PocWeight += distributedWeight
				found = true
				am.LogInfo("Added weight to existing ML node", types.PoC, "flow_context", flowContext, "sub_flow_context", subFlowContext, "step", "add_to_existing_node", "node_id", existingMLNode.NodeId, "added_weight", distributedWeight, "new_total_weight", existingMLNode.PocWeight)
				break
			}
		}

		// If no existing MLNode found, create new one
		if !found {
			newMLNode := &types.MLNodeInfo{
				NodeId:     nodeId,
				PocWeight:  distributedWeight,
				Throughput: 0, // Will be populated later if needed
			}
			newMLNodes = append(newMLNodes, newMLNode)
			am.LogInfo("Created new ML node for hardware node", types.PoC, "flow_context", flowContext, "sub_flow_context", subFlowContext, "step", "create_new_ml_node", "node_id", newMLNode.NodeId, "weight", newMLNode.PocWeight)
		}
	}

	am.LogInfo("Finished distributing legacy PoC weight", types.PoC,
		"flow_context", flowContext, "sub_flow_context", subFlowContext, "step", "end",
		"legacyWeight", totalLegacyWeight,
		"numHardwareNodes", numHardwareNodes,
		"final_ml_nodes", newMLNodes)

	return newMLNodes
}

// Helper function to check if a specific MLNode supports a given model
func nodeSupportsModel(hardwareNodes *types.HardwareNodes, nodeId string, modelId string) bool {
	for _, node := range hardwareNodes.HardwareNodes {
		if node.LocalId == nodeId {
			for _, supportedModel := range node.Models {
				if supportedModel == modelId {
					return true
				}
			}
			break
		}
	}
	return false
}
