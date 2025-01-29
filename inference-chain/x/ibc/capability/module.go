package capability

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"cosmossdk.io/core/appmodule"
	"cosmossdk.io/depinject"
	storetypes "cosmossdk.io/store/types"
	"cosmossdk.io/x/nft/simulation"
	gwruntime "github.com/grpc-ecosystem/grpc-gateway/runtime"

	"github.com/cosmos/cosmos-sdk/client"
	"github.com/cosmos/cosmos-sdk/codec"
	cdctypes "github.com/cosmos/cosmos-sdk/codec/types"
	"github.com/cosmos/cosmos-sdk/telemetry"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/types/module"
	simtypes "github.com/cosmos/cosmos-sdk/types/simulation"

	"github.com/cosmos/ibc-go/modules/capability/keeper"
	"github.com/cosmos/ibc-go/modules/capability/types"

	capabilitymodulev1 "github.com/productscience/inference/api/ibc/capability/module/v1"
)

var _ depinject.OnePerModuleType = AppModule{}

// IsOnePerModuleType implements the depinject.OnePerModuleType interface.
func (AppModule) IsOnePerModuleType() {}

func init() {
	appmodule.Register(
		&capabilitymodulev1.Module{},
		appmodule.Provide(ProvideModule),
	)
}

// ModuleInputs defines the capability module inputs for depinject.
type ModuleInputs struct {
	depinject.In

	Config      *capabilitymodulev1.Module
	Cdc         codec.Codec
	Key         *storetypes.KVStoreKey
	MemStoreKey *storetypes.MemoryStoreKey
}

// ModuleOutputs defines the capability module outputs for depinject.
type ModuleOutputs struct {
	depinject.Out

	CapabilityKeeper *keeper.Keeper
	Module           appmodule.AppModule
}

// ProvideModule returns the capability module outputs for dependency injection
func ProvideModule(in ModuleInputs) ModuleOutputs {
	capabilityKeeper := keeper.NewKeeper(
		in.Cdc,
		in.Key,
		in.MemStoreKey,
	)
	m := NewAppModule(in.Cdc, *capabilityKeeper, in.Config.SealKeeper)

	return ModuleOutputs{CapabilityKeeper: capabilityKeeper, Module: m}
}

var (
	_ module.AppModule           = (*AppModule)(nil)
	_ module.AppModuleSimulation = (*AppModule)(nil)
	_ module.HasName             = (*AppModule)(nil)
	_ module.HasConsensusVersion = (*AppModule)(nil)
	_ module.HasGenesis          = (*AppModule)(nil)
	_ appmodule.AppModule        = (*AppModule)(nil)
	_ appmodule.HasBeginBlocker  = (*AppModule)(nil)
)

// ----------------------------------------------------------------------------
// AppModuleBasic
// ----------------------------------------------------------------------------

// AppModuleBasic implements the AppModuleBasic interface for the capability module.
type AppModuleBasic struct {
	cdc codec.Codec
}

func NewAppModuleBasic(cdc codec.Codec) AppModuleBasic {
	return AppModuleBasic{cdc: cdc}
}

// Name returns the capability module's name.
func (AppModuleBasic) Name() string {
	return types.ModuleName
}

// RegisterLegacyAminoCodec does nothing. Capability does not support amino.
func (AppModuleBasic) RegisterLegacyAminoCodec(cdc *codec.LegacyAmino) {}

// RegisterInterfaces registers the module's interface types
func (AppModuleBasic) RegisterInterfaces(_ cdctypes.InterfaceRegistry) {}

// DefaultGenesis returns the capability module's default genesis state.
func (AppModuleBasic) DefaultGenesis(cdc codec.JSONCodec) json.RawMessage {
	return cdc.MustMarshalJSON(types.DefaultGenesis())
}

// ValidateGenesis performs genesis state validation for the capability module.
func (AppModuleBasic) ValidateGenesis(cdc codec.JSONCodec, config client.TxEncodingConfig, bz json.RawMessage) error {
	var genState types.GenesisState
	if err := cdc.UnmarshalJSON(bz, &genState); err != nil {
		return fmt.Errorf("failed to unmarshal %s genesis state: %w", types.ModuleName, err)
	}
	return genState.Validate()
}

// RegisterGRPCGatewayRoutes registers the gRPC Gateway routes for the capability module.
func (AppModuleBasic) RegisterGRPCGatewayRoutes(_ client.Context, _ *gwruntime.ServeMux) {
}

// ----------------------------------------------------------------------------
// AppModule
// ----------------------------------------------------------------------------

// AppModule implements the AppModule interface for the capability module.
type AppModule struct {
	AppModuleBasic

	keeper keeper.Keeper

	sealKeeper bool
}

func NewAppModule(cdc codec.Codec, capabilityKeeper keeper.Keeper, sealKeeper bool) AppModule {
	return AppModule{
		AppModuleBasic: NewAppModuleBasic(cdc),
		keeper:         capabilityKeeper,
		sealKeeper:     sealKeeper,
	}
}

// IsAppModule implements the appmodule.AppModule interface.
func (AppModule) IsAppModule() {}

// InitGenesis performs the capability module's genesis initialization It returns
// no validator updates.
func (am AppModule) InitGenesis(ctx sdk.Context, cdc codec.JSONCodec, gs json.RawMessage) {
	var genState types.GenesisState
	// Initialize global index to index in genesis state
	cdc.MustUnmarshalJSON(gs, &genState)

	InitGenesis(ctx, am.keeper, genState)
}

// ExportGenesis returns the capability module's exported genesis state as raw JSON bytes.
func (am AppModule) ExportGenesis(ctx sdk.Context, cdc codec.JSONCodec) json.RawMessage {
	genState := ExportGenesis(ctx, am.keeper)
	return cdc.MustMarshalJSON(genState)
}

// ConsensusVersion implements AppModule/ConsensusVersion.
func (AppModule) ConsensusVersion() uint64 { return 1 }

// BeginBlock executes all ABCI BeginBlock logic respective to the capability module.
// BeginBlocker calls InitMemStore to assert that the memory store is initialized.
// It's safe to run multiple times.
func (am AppModule) BeginBlock(ctx context.Context) error {
	defer telemetry.ModuleMeasureSince(types.ModuleName, time.Now(), telemetry.MetricKeyBeginBlocker)

	am.keeper.InitMemStore(sdk.UnwrapSDKContext(ctx))

	if am.sealKeeper && !am.keeper.IsSealed() {
		am.keeper.Seal()
	}

	return nil
}

// GenerateGenesisState creates a randomized GenState of the capability module.
func (AppModule) GenerateGenesisState(simState *module.SimulationState) {
	simulation.RandomizedGenState(simState, nil)
}

// RegisterStoreDecoder registers a decoder for capability module's types
func (am AppModule) RegisterStoreDecoder(sdr simtypes.StoreDecoderRegistry) {
	sdr[types.StoreKey] = simulation.NewDecodeStore(am.cdc)
}

// WeightedOperations returns the all the gov module operations with their respective weights.
func (AppModule) WeightedOperations(simState module.SimulationState) []simtypes.WeightedOperation {
	return nil
}

// InitGenesis initializes the capability module's state from a provided genesis
// state.
func InitGenesis(ctx sdk.Context, k keeper.Keeper, genState types.GenesisState) {
	if err := k.InitializeIndex(ctx, genState.Index); err != nil {
		panic(err)
	}

	// set owners for each index
	for _, genOwner := range genState.Owners {
		k.SetOwners(ctx, genOwner.Index, genOwner.IndexOwners)
	}

	// initialize in-memory capabilities
	k.InitMemStore(ctx)
}

// ExportGenesis returns the capability module's exported genesis.
func ExportGenesis(ctx sdk.Context, k keeper.Keeper) *types.GenesisState {
	index := k.GetLatestIndex(ctx)
	owners := []types.GenesisOwners{}

	for i := uint64(1); i < index; i++ {
		capabilityOwners, ok := k.GetOwners(ctx, i)
		if !ok || len(capabilityOwners.Owners) == 0 {
			continue
		}

		genOwner := types.GenesisOwners{
			Index:       i,
			IndexOwners: capabilityOwners,
		}
		owners = append(owners, genOwner)
	}

	return &types.GenesisState{
		Index:  index,
		Owners: owners,
	}
}
