package inference

import (
	"context"
	"cosmossdk.io/core/appmodule"
	"cosmossdk.io/core/store"
	"cosmossdk.io/depinject"
	"cosmossdk.io/log"
	"encoding/base64"
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
	"github.com/productscience/inference/x/inference/proofofcompute"

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
func (AppModule) ConsensusVersion() uint64 { return 1 }

// BeginBlock contains the logic that is automatically triggered at the beginning of each block.
// The begin block implementation is optional.
func (am AppModule) BeginBlock(_ context.Context) error {
	return nil
}

// EndBlock contains the logic that is automatically triggered at the end of each block.
// The end block implementation is optional.
func (am AppModule) EndBlock(ctx context.Context) error {
	sdkCtx := sdk.UnwrapSDKContext(ctx)
	blockHeight := sdkCtx.BlockHeight()

	if proofofcompute.IsSetNewValidatorsStage(blockHeight) {
		am.onSetNewValidatorsStage(ctx, blockHeight)
	}

	if proofofcompute.IsStartOfPoCStage(blockHeight) {
		am.LogInfo("NewPocStart", "blockHeight", blockHeight)
		newGroup, err := am.keeper.GetEpochGroup(ctx, uint64(blockHeight))
		if err != nil {
			am.LogError("Unable to create epoch group", "error", err.Error())
			return err
		}
		err = newGroup.CreateGroup(ctx)
		if err != nil {
			am.LogError("Unable to create epoch group", "error", err.Error())
			return err
		}
		am.keeper.SetUpcomingEpochGroupId(ctx, uint64(blockHeight))
	}
	currentEpochGroup, err := am.keeper.GetCurrentEpochGroup(ctx)
	if err != nil {
		am.LogError("Unable to get current epoch group", "error", err.Error())
		return nil
	}

	if currentEpochGroup.IsChanged(ctx) {
		am.LogInfo("EpochGroupChanged", "blockHeight", blockHeight)
		computeResult, err := currentEpochGroup.GetComputeResults(ctx)
		if err != nil {
			am.LogError("Unable to get compute results", "error", err.Error())
			return nil
		}
		_, err = am.keeper.Staking.SetComputeValidators(ctx, computeResult)
		if err != nil {
			am.LogError("Unable to update epoch group", "error", err.Error())
		}
		currentEpochGroup.MarkUnchanged(ctx)
	}

	return nil
}

func (am AppModule) onSetNewValidatorsStage(ctx context.Context, blockHeight int64) {
	am.LogInfo("onSetNewValidatorsStage start", "blockHeight", blockHeight)
	pocHeight := am.keeper.GetEffectiveEpochGroupId(ctx)
	err := am.keeper.SettleAccounts(ctx, pocHeight)
	if err != nil {
		am.LogError("onSetNewValidatorsStage: Unable to settle accounts", "error", err.Error())
	}

	upcomingEg, err := am.keeper.GetUpcomingEpochGroup(ctx)
	if err != nil {
		am.LogError("onSetNewValidatorsStage: Unable to get upcoming epoch group", "error", err.Error())
		return
	}

	computeResult, activeParticipants := am.ComputeNewWeights(ctx, upcomingEg.GroupData)
	if computeResult == nil && activeParticipants == nil {
		am.LogError("onSetNewValidatorsStage: computeResult == nil && activeParticipants == nil")
		return
	}

	am.LogInfo("onSetNewValidatorsStage: computed new weights", "PocStartBlockHeight", upcomingEg.GroupData.PocStartBlockHeight, "len(computeResult)", len(computeResult), "len(activeParticipants)", len(activeParticipants))

	am.keeper.SetActiveParticipants(ctx, types.ActiveParticipants{
		Participants:         activeParticipants,
		EpochGroupId:         upcomingEg.GroupData.EpochGroupId,
		PocStartBlockHeight:  int64(upcomingEg.GroupData.PocStartBlockHeight),
		EffectiveBlockHeight: int64(upcomingEg.GroupData.EffectiveBlockHeight),
		CreatedAtBlockHeight: blockHeight,
	})

	for _, result := range computeResult {
		// FIXME: add some centralized way that'd govern key enc/dec rules
		validatorPubKey := base64.StdEncoding.EncodeToString(result.ValidatorPubKey.Bytes())
		err := upcomingEg.AddMember(ctx, result.OperatorAddress, uint64(result.Power), validatorPubKey, "seedSignature")
		if err != nil {
			am.LogError("onSetNewValidatorsStage: Unable to add member", "error", err.Error())
			continue
		}
	}

	am.moveUpcomingToEffectiveGroup(ctx, blockHeight)
}
func (am AppModule) moveUpcomingToEffectiveGroup(ctx context.Context, blockHeight int64) {
	newGroupId := am.keeper.GetUpcomingEpochGroupId(ctx)
	previousGroupId := am.keeper.GetEffectiveEpochGroupId(ctx)

	am.LogInfo("NewEpochGroup", "blockHeight", blockHeight, "newGroupId", newGroupId)
	am.keeper.SetEffectiveEpochGroupId(ctx, newGroupId)
	am.keeper.SetPreviousEpochGroupId(ctx, previousGroupId)
	am.keeper.SetUpcomingEpochGroupId(ctx, 0)
	newGroupData, found := am.keeper.GetEpochGroupData(ctx, newGroupId)
	if !found {
		am.LogWarn("NewEpochGroupDataNotFound", "blockHeight", blockHeight, "newGroupId", newGroupId)
		return
	}
	previousGroupData, found := am.keeper.GetEpochGroupData(ctx, previousGroupId)
	if !found {
		am.LogWarn("PreviousEpochGroupDataNotFound", "blockHeight", blockHeight, "previousGroupId", previousGroupId)
		return
	}
	newGroupData.EffectiveBlockHeight = uint64(blockHeight)
	previousGroupData.LastBlockHeight = uint64(blockHeight - 1)
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

func (am AppModule) LogInfo(msg string, keyvals ...interface{}) {
	am.keeper.Logger().Info("INFO+ "+msg, keyvals...)
}

func (am AppModule) LogError(msg string, keyvals ...interface{}) {
	am.keeper.Logger().Error(msg, keyvals...)
}

func (am AppModule) LogWarn(msg string, keyvals ...interface{}) {
	am.keeper.Logger().Warn(msg, keyvals...)
}

func (am AppModule) LogDebug(msg string, keyvals ...interface{}) {
	am.keeper.Logger().Debug(msg, keyvals...)
}
