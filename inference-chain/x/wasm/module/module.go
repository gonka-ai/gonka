package wasm

import (
	"context"
	"encoding/json"

	"cosmossdk.io/core/appmodule"
	"cosmossdk.io/core/store"
	"cosmossdk.io/depinject"
	"cosmossdk.io/log"

	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/cosmos/cosmos-sdk/codec"
	"github.com/cosmos/cosmos-sdk/types/module"

	wasmkeeper "github.com/CosmWasm/wasmd/x/wasm/keeper"

	wasmtypes "github.com/CosmWasm/wasmd/x/wasm/types"
	wasmmodulev1 "github.com/productscience/inference/api/cosmwasm/wasm/module/v1"

	"github.com/cosmos/cosmos-sdk/client"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	"github.com/grpc-ecosystem/grpc-gateway/runtime"

	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	govtypes "github.com/cosmos/cosmos-sdk/x/gov/types"

	wasmvmtypes "github.com/CosmWasm/wasmvm/v2/types"

	"log/slog"

	"github.com/cosmos/cosmos-sdk/baseapp"

	distrkeeper "github.com/cosmos/cosmos-sdk/x/distribution/keeper"
	ibcfeekeeper "github.com/cosmos/ibc-go/v8/modules/apps/29-fee/keeper"
	localkeeper "github.com/productscience/inference/x/wasm/keeper"
)

var (
	_ module.AppModuleBasic      = (*AppModule)(nil)
	_ module.HasGenesis          = (*AppModule)(nil)
	_ module.HasInvariants       = (*AppModule)(nil)
	_ module.HasConsensusVersion = (*AppModule)(nil)
	_ appmodule.AppModule        = (*AppModule)(nil)
)

// ModuleInputs defines the inputs needed for the WASM module.
type ModuleInputs struct {
	depinject.In

	Config       *wasmmodulev1.Module
	StoreService store.KVStoreService
	Cdc          codec.Codec
	Logger       log.Logger

	AccountKeeper    wasmtypes.AccountKeeper
	BankKeeper       wasmtypes.BankKeeper
	StakingKeeper    wasmtypes.StakingKeeper
	DistrKeeper      distrkeeper.Keeper
	ChannelKeeper    wasmtypes.ChannelKeeper
	PortKeeper       wasmtypes.PortKeeper
	CapabilityKeeper wasmtypes.CapabilityKeeper
	Router           *baseapp.MsgServiceRouter
	IBCFeeKeeper     ibcfeekeeper.Keeper
}

// ModuleOutputs defines the outputs of the WASM module.
type ModuleOutputs struct {
	depinject.Out

	WasmKeeper *wasmkeeper.Keeper
	Module     appmodule.AppModule
}

func init() {
	appmodule.Register(&wasmmodulev1.Module{},
		appmodule.Provide(
			ProvideModule,
		),
	)
}

func InvokeWasm(keeper *wasmkeeper.Keeper) error {
	return nil
}

type AppModule struct {
	cdc    codec.Codec
	keeper *wasmkeeper.Keeper
	logger log.Logger
}

func ProvideModule(in ModuleInputs) (ModuleOutputs, error) {
	distrAdapter := localkeeper.NewDistributionKeeperAdapter(&in.DistrKeeper)
	slog.Info("WASM Providing module")

	// Use IBCFeeKeeper as ICS4Wrapper since it's used in the middleware stack
	var ics4Wrapper wasmtypes.ICS4Wrapper = in.IBCFeeKeeper

	keeper := wasmkeeper.NewKeeper(
		in.Cdc,
		in.StoreService,
		in.AccountKeeper,
		in.BankKeeper,
		in.StakingKeeper,
		distrAdapter,
		ics4Wrapper,
		in.ChannelKeeper,
		in.PortKeeper,
		in.CapabilityKeeper,
		nil, // portSource
		in.Router,
		nil, // _
		"",  // homeDir
		wasmtypes.DefaultNodeConfig(),
		wasmtypes.VMConfig{
			WasmLimits: wasmvmtypes.WasmLimits{
				InitialMemoryLimitPages: uint32Ptr(32),
				TableSizeLimitElements:  uint32Ptr(64),
				MaxImports:              uint32Ptr(100),
				MaxFunctions:            uint32Ptr(1000),
				MaxFunctionParams:       uint32Ptr(128),
				MaxTotalFunctionParams:  uint32Ptr(512),
				MaxFunctionResults:      uint32Ptr(128),
			},
		}, // vmConfig
		in.Config.AllowedCapabilities,
		authtypes.NewModuleAddress(govtypes.ModuleName).String(), // authority
	)
	slog.Info("WASM keeper created")
	module := NewAppModule(in.Cdc, &keeper, in.Logger)
	slog.Info("WASM module created")

	return ModuleOutputs{
		WasmKeeper: &keeper,
		Module:     module,
	}, nil
}

func NewAppModule(cdc codec.Codec, keeper *wasmkeeper.Keeper, logger log.Logger) AppModule {
	return AppModule{
		cdc:    cdc,
		keeper: keeper,
		logger: logger,
	}
}

// ConsensusVersion implements HasConsensusVersion
func (am AppModule) ConsensusVersion() uint64 {
	return 1
}

// Name implements AppModuleBasic
func (am AppModule) Name() string {
	return wasmtypes.ModuleName
}

// RegisterInterfaces registers the module's interface types
func (am AppModule) RegisterInterfaces(registry codectypes.InterfaceRegistry) {
	wasmtypes.RegisterInterfaces(registry)
}

// RegisterServices registers module services
func (am AppModule) RegisterServices(cfg module.Configurator) {
	wasmtypes.RegisterMsgServer(cfg.MsgServer(), wasmkeeper.NewMsgServerImpl(am.keeper))
}

// RegisterInvariants registers the wasm module invariants
func (am AppModule) RegisterInvariants(_ sdk.InvariantRegistry) {
	// No invariants to register
}

// InitGenesis performs genesis initialization
func (am AppModule) InitGenesis(ctx sdk.Context, cdc codec.JSONCodec, data json.RawMessage) {
	var genesisState wasmtypes.GenesisState
	cdc.MustUnmarshalJSON(data, &genesisState)
	InitGenesis(ctx, am.keeper, genesisState)
}

// ValidateGenesis validates the genesis state data
func (AppModule) ValidateGenesis(cdc codec.JSONCodec, _ client.TxEncodingConfig, bz json.RawMessage) error {
	var data wasmtypes.GenesisState
	if err := cdc.UnmarshalJSON(bz, &data); err != nil {
		return err
	}
	return nil // Add specific validation if needed
}

// ExportGenesis returns the exported genesis state.
func (am AppModule) ExportGenesis(ctx sdk.Context, cdc codec.JSONCodec) json.RawMessage {
	genState := ExportGenesis(ctx)
	return cdc.MustMarshalJSON(genState)
}

// DefaultGenesis returns default genesis state as raw bytes for the module
func (AppModule) DefaultGenesis(cdc codec.JSONCodec) json.RawMessage {
	return cdc.MustMarshalJSON(&wasmtypes.GenesisState{
		Params: wasmtypes.DefaultParams(),
	})
}

// IsOnePerModuleType implements AppModule/IsOnePerModuleType
func (am AppModule) IsOnePerModuleType() {}

// IsAppModule implements AppModule
func (am AppModule) IsAppModule() {}

// RegisterGRPCGatewayRoutes registers the gRPC Gateway routes for the module
func (AppModule) RegisterGRPCGatewayRoutes(clientCtx client.Context, mux *runtime.ServeMux) {
	if err := wasmtypes.RegisterQueryHandlerClient(context.Background(), mux, wasmtypes.NewQueryClient(clientCtx)); err != nil {
		panic(err)
	}
}

func InitGenesis(ctx context.Context, keeper *wasmkeeper.Keeper, data wasmtypes.GenesisState) {
	keeper.SetParams(ctx, data.Params)
}

// RegisterLegacyAminoCodec registers the module's types on the LegacyAmino codec
func (AppModule) RegisterLegacyAminoCodec(cdc *codec.LegacyAmino) {
	wasmtypes.RegisterLegacyAminoCodec(cdc)
}

func ExportGenesis(ctx context.Context) *wasmtypes.GenesisState {
	return &wasmtypes.GenesisState{
		Params: wasmtypes.DefaultParams(),
	}
}

func uint32Ptr(v uint32) *uint32 {
	return &v
}
