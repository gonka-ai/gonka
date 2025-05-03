package inference

import (
	"context"
	"cosmossdk.io/core/appmodule"
	"cosmossdk.io/core/store"
	"cosmossdk.io/depinject"
	"cosmossdk.io/log"
	"encoding/json"
	"fmt"
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

// ValidateGenesis used to validateo the GenesisState, given in its json.RawMessage form.
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
func (AppModule) ConsensusVersion() uint64 { return 2 }

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

	timeouts := am.keeper.GetAllInferenceTimeoutForHeight(ctx, uint64(blockHeight))
	err := am.expireInferences(ctx, timeouts)
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

	if epochParams.IsSetNewValidatorsStage(blockHeight) {
		am.LogInfo("onSetNewValidatorsStage start", types.Stages, "blockHeight", blockHeight)
		am.onSetNewValidatorsStage(ctx, blockHeight, blockTime)
	}

	if epochParams.IsStartOfPoCStage(blockHeight) {
		am.LogInfo("NewPocStart", types.Stages, "blockHeight", blockHeight)
		newGroup, err := am.keeper.GetEpochGroup(ctx, uint64(blockHeight))
		if err != nil {
			am.LogError("Unable to create epoch group", types.EpochGroup, "error", err.Error())
			return err
		}
		err = newGroup.CreateGroup(ctx)
		if err != nil {
			am.LogError("Unable to create epoch group", types.EpochGroup, "error", err.Error())
			return err
		}

		// Create nested EpochGroups for each model
		models, err := am.keeper.GetAllModels(ctx)
		if err != nil {
			am.LogError("Unable to get all models", types.EpochGroup, "error", err.Error())
		} else {
			err = newGroup.CreateModelEpochGroups(ctx, models)
			if err != nil {
				am.LogError("Unable to create model epoch groups", types.EpochGroup, "error", err.Error())
			}
		}

		am.keeper.SetUpcomingEpochGroupId(ctx, uint64(blockHeight))
	}
	currentEpochGroup, err := am.keeper.GetCurrentEpochGroup(ctx)
	if err != nil {
		am.LogError("Unable to get current epoch group", types.EpochGroup, "error", err.Error())
		return nil
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

func (am AppModule) onSetNewValidatorsStage(ctx context.Context, blockHeight int64, blockTime int64) {
	pocHeight := am.keeper.GetEffectiveEpochGroupId(ctx)
	err := am.keeper.SettleAccounts(ctx, pocHeight)
	if err != nil {
		am.LogError("onSetNewValidatorsStage: Unable to settle accounts", types.Settle, "error", err.Error())
	}

	upcomingEg, err := am.keeper.GetUpcomingEpochGroup(ctx)
	if err != nil {
		am.LogError("onSetNewValidatorsStage: Unable to get upcoming epoch group", types.EpochGroup, "error", err.Error())
		return
	}

	activeParticipants := am.ComputeNewWeights(ctx, upcomingEg.GroupData)
	if activeParticipants == nil {
		am.LogError("onSetNewValidatorsStage: computeResult == nil && activeParticipants == nil", types.PoC)
		return
	}

	err = am.RegisterTopMiners(ctx, activeParticipants, blockTime)
	if err != nil {
		am.LogError("onSetNewValidatorsStage: Unable to register top miners", types.Tokenomics, "error", err.Error())
		return
	}

	am.LogInfo("onSetNewValidatorsStage: computed new weights", types.Stages, "PocStartBlockHeight", upcomingEg.GroupData.PocStartBlockHeight, "len(activeParticipants)", len(activeParticipants))

	am.keeper.SetActiveParticipants(ctx, types.ActiveParticipants{
		Participants:         activeParticipants,
		EpochGroupId:         upcomingEg.GroupData.EpochGroupId,
		PocStartBlockHeight:  int64(upcomingEg.GroupData.PocStartBlockHeight),
		EffectiveBlockHeight: int64(upcomingEg.GroupData.EffectiveBlockHeight),
		CreatedAtBlockHeight: blockHeight,
	})
	validationParams := am.keeper.GetParams(ctx).ValidationParams

	for _, p := range activeParticipants {
		// FIXME: add some centralized way that'd govern key enc/dec rules
		reputation, err := am.calculateParticipantReputation(ctx, p, validationParams)
		if err != nil {
			am.LogError("onSetNewValidatorsStage: Unable to calculate participant reputation", types.EpochGroup, "error", err.Error())
			reputation = 0
		}
		err = upcomingEg.AddMember(ctx, p.Index, p.Weight, p.ValidatorKey, p.Seed.Signature, reputation)
		if err != nil {
			am.LogError("onSetNewValidatorsStage: Unable to add member", types.EpochGroup, "error", err.Error())
			continue
		}

		// Get the participant's models
		participant, found := am.keeper.GetParticipant(ctx, p.Index)
		if !found {
			am.LogError("onSetNewValidatorsStage: Unable to get participant", types.EpochGroup, "address", p.Index)
			continue
		}

		// Add member to model-specific EpochGroups
		err = upcomingEg.AddMemberToModelGroups(ctx, p.Index, p.Weight, p.ValidatorKey, participant.Models)
		if err != nil {
			am.LogError("onSetNewValidatorsStage: Unable to add member to model groups", types.EpochGroup, "error", err.Error())
		}
	}

	var defaultPrice int64
	if upcomingEg.GroupData.EpochGroupId != 1 {
		currentEg, err := am.keeper.GetCurrentEpochGroup(ctx)
		if err != nil {
			am.LogError("onSetNewValidatorsStage: Unable to get current epoch group", types.EpochGroup, "error", err.Error())
			return
		}
		defaultPrice = currentEg.GroupData.UnitOfComputePrice
	} else {
		defaultPrice = am.keeper.GetParams(ctx).EpochParams.DefaultUnitOfComputePrice
	}

	proposals, err := am.keeper.AllUnitOfComputePriceProposals(ctx)
	if err != nil {
		am.LogError("onSetNewValidatorsStage: Unable to get all unit of compute price proposals", types.Pricing, "error", err.Error())
		return
	}

	am.LogInfo("onSetNewValidatorsStage: unitOfCompute: retrieved proposals", types.Pricing, "len(proposals)", len(proposals))

	medianProposal, err := upcomingEg.ComputeUnitOfComputePrice(ctx, proposals, uint64(defaultPrice))
	am.LogInfo("onSetNewValidatorsStage: unitOfCompute: ", types.Pricing, "medianProposal", medianProposal)
	if err != nil {
		am.LogError("onSetNewValidatorsStage: unitOfCompute: onSetNewValidatorsStage: Unable to compute unit of compute price", types.Pricing, "error", err.Error())
		return
	}

	// TODO: Move this so active participants are set 1 block before new validators
	am.moveUpcomingToEffectiveGroup(ctx, blockHeight, medianProposal)
}

func (am AppModule) computePrice(ctx context.Context) {

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
	newGroupId := am.keeper.GetUpcomingEpochGroupId(ctx)
	previousGroupId := am.keeper.GetEffectiveEpochGroupId(ctx)

	am.LogInfo("NewEpochGroup", types.EpochGroup, "blockHeight", blockHeight, "newGroupId", newGroupId)
	am.keeper.SetEffectiveEpochGroupId(ctx, newGroupId)
	am.keeper.SetPreviousEpochGroupId(ctx, previousGroupId)
	am.keeper.SetUpcomingEpochGroupId(ctx, 0)
	newGroupData, found := am.keeper.GetEpochGroupData(ctx, newGroupId)
	if !found {
		am.LogWarn("NewEpochGroupDataNotFound", types.EpochGroup, "blockHeight", blockHeight, "newGroupId", newGroupId)
		return
	}
	previousGroupData, found := am.keeper.GetEpochGroupData(ctx, previousGroupId)
	if !found {
		am.LogWarn("PreviousEpochGroupDataNotFound", types.EpochGroup, "blockHeight", blockHeight, "previousGroupId", previousGroupId)
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
