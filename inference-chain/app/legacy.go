package app

import (
	"path/filepath"

	"cosmossdk.io/core/appmodule"
	"github.com/CosmWasm/wasmd/x/wasm"
	wasmkeeper "github.com/CosmWasm/wasmd/x/wasm/keeper"
	wasmtypes "github.com/CosmWasm/wasmd/x/wasm/types"
	wasmvmtypes "github.com/CosmWasm/wasmvm/v3/types"
	"github.com/cosmos/cosmos-sdk/client/flags"
	cdctypes "github.com/cosmos/cosmos-sdk/codec/types"
	"github.com/cosmos/cosmos-sdk/runtime"
	servertypes "github.com/cosmos/cosmos-sdk/server/types"
	storetypes "github.com/cosmos/cosmos-sdk/store/v2/types"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	distrkeeper "github.com/cosmos/cosmos-sdk/x/distribution/keeper"
	govtypes "github.com/cosmos/cosmos-sdk/x/gov/types"
	govv1beta1 "github.com/cosmos/cosmos-sdk/x/gov/types/v1beta1"
	proto "github.com/cosmos/gogoproto/proto"
	icamodule "github.com/cosmos/ibc-go/v11/modules/apps/27-interchain-accounts"
	icacontroller "github.com/cosmos/ibc-go/v11/modules/apps/27-interchain-accounts/controller"
	icacontrollerkeeper "github.com/cosmos/ibc-go/v11/modules/apps/27-interchain-accounts/controller/keeper"
	icacontrollertypes "github.com/cosmos/ibc-go/v11/modules/apps/27-interchain-accounts/controller/types"
	icahost "github.com/cosmos/ibc-go/v11/modules/apps/27-interchain-accounts/host"
	icahostkeeper "github.com/cosmos/ibc-go/v11/modules/apps/27-interchain-accounts/host/keeper"
	icahosttypes "github.com/cosmos/ibc-go/v11/modules/apps/27-interchain-accounts/host/types"
	icatypes "github.com/cosmos/ibc-go/v11/modules/apps/27-interchain-accounts/types"
	ibccallbacks "github.com/cosmos/ibc-go/v11/modules/apps/callbacks"
	ibctransfer "github.com/cosmos/ibc-go/v11/modules/apps/transfer"
	ibctransferkeeper "github.com/cosmos/ibc-go/v11/modules/apps/transfer/keeper"
	ibctransfertypes "github.com/cosmos/ibc-go/v11/modules/apps/transfer/types"
	transferv2 "github.com/cosmos/ibc-go/v11/modules/apps/transfer/v2"
	ibc "github.com/cosmos/ibc-go/v11/modules/core"
	porttypes "github.com/cosmos/ibc-go/v11/modules/core/05-port/types"
	ibcapi "github.com/cosmos/ibc-go/v11/modules/core/api"
	ibcexported "github.com/cosmos/ibc-go/v11/modules/core/exported"
	ibckeeper "github.com/cosmos/ibc-go/v11/modules/core/keeper"
	solomachine "github.com/cosmos/ibc-go/v11/modules/light-clients/06-solomachine"
	ibctm "github.com/cosmos/ibc-go/v11/modules/light-clients/07-tendermint"
	inferencetypes "github.com/productscience/inference/x/inference/types"
	streamvestingtypes "github.com/productscience/inference/x/streamvesting/types"
	"github.com/spf13/cast"
)

func (app *App) registerLegacyModules(appOpts servertypes.AppOptions, wasmOpts []wasmkeeper.Option) wasmtypes.NodeConfig {
	if err := app.RegisterStores(
		storetypes.NewKVStoreKey(ibcexported.StoreKey),
		storetypes.NewKVStoreKey(ibctransfertypes.StoreKey),
		storetypes.NewKVStoreKey(icahosttypes.StoreKey),
		storetypes.NewKVStoreKey(icacontrollertypes.StoreKey),
		storetypes.NewKVStoreKey(wasmtypes.StoreKey),
	); err != nil {
		//nolint:forbidigo // init code
		panic(err)
	}

	authAddr := authtypes.NewModuleAddress(govtypes.ModuleName).String()

	app.IBCKeeper = ibckeeper.NewKeeper(
		app.appCodec,
		runtime.NewKVStoreService(app.GetKey(ibcexported.StoreKey)),
		app.UpgradeKeeper,
		authAddr,
	)

	govRouter := govv1beta1.NewRouter()
	govRouter.AddRoute(govtypes.RouterKey, govv1beta1.ProposalHandler)
	app.GovKeeper.SetLegacyRouter(govRouter)

	app.TransferKeeper = ibctransferkeeper.NewKeeper(
		app.appCodec,
		app.AccountKeeper.AddressCodec(),
		runtime.NewKVStoreService(app.GetKey(ibctransfertypes.StoreKey)),
		app.IBCKeeper.ChannelKeeper,
		app.MsgServiceRouter(),
		app.AccountKeeper,
		app.BankKeeper,
		authAddr,
	)

	app.ICAHostKeeper = icahostkeeper.NewKeeper(
		app.appCodec,
		runtime.NewKVStoreService(app.GetKey(icahosttypes.StoreKey)),
		app.IBCKeeper.ChannelKeeper,
		app.AccountKeeper,
		app.MsgServiceRouter(),
		app.GRPCQueryRouter(),
		authAddr,
	)

	app.ICAControllerKeeper = icacontrollerkeeper.NewKeeper(
		app.appCodec,
		runtime.NewKVStoreService(app.GetKey(icacontrollertypes.StoreKey)),
		app.IBCKeeper.ChannelKeeper,
		app.MsgServiceRouter(),
		authAddr,
	)

	homePath := cast.ToString(appOpts.Get(flags.FlagHome))
	wasmDir := filepath.Join(homePath, "wasm")

	nodeConfig, err := wasm.ReadNodeConfig(appOpts)
	if err != nil {
		//nolint:forbidigo // init code
		panic(err)
	}
	vmConfig := wasmtypes.VMConfig{
		WasmLimits: wasmvmtypes.WasmLimits{},
	}

	querierOpts := wasmkeeper.WithQueryPlugins(
		&wasmkeeper.QueryPlugins{
			Grpc:     wasmkeeper.AcceptListGrpcQuerier(AcceptedGrpcQueries(), app.GRPCQueryRouter(), app.AppCodec()),
			Stargate: wasmkeeper.AcceptListStargateQuerier(AcceptedStargateQueries(), app.GRPCQueryRouter(), app.AppCodec()),
		})
	wasmOpts = append(wasmOpts, querierOpts)

	app.WasmKeeper = wasmkeeper.NewKeeper(
		app.AppCodec(),
		runtime.NewKVStoreService(app.GetKey(wasmtypes.StoreKey)),
		app.AccountKeeper,
		app.BankKeeper,
		app.StakingKeeper,
		distrkeeper.NewQuerier(app.DistrKeeper),
		app.IBCKeeper.ChannelKeeper,
		app.IBCKeeper.ChannelKeeper,
		app.IBCKeeper.ChannelKeeperV2,
		app.TransferKeeper,
		app.MsgServiceRouter(),
		app.GRPCQueryRouter(),
		wasmDir,
		nodeConfig,
		vmConfig,
		AllCapabilities(),
		authAddr,
		wasmOpts...,
	)

	wasmStackIBCHandler := wasm.NewIBCHandler(app.WasmKeeper, app.IBCKeeper.ChannelKeeper, app.TransferKeeper, app.IBCKeeper.ChannelKeeper)

	var (
		noAuthzModule      porttypes.IBCModule
		icaControllerStack porttypes.IBCModule
	)
	icaStackBuilder := porttypes.NewIBCStackBuilder(app.IBCKeeper.ChannelKeeper)
	icaStackBuilder.Base(
		icacontroller.NewIBCMiddlewareWithAuth(noAuthzModule, app.ICAControllerKeeper)).Next(
		ibccallbacks.NewIBCMiddleware(wasmStackIBCHandler, wasm.DefaultMaxIBCCallbackGas),
	)
	icaControllerStack = icaStackBuilder.Build()
	app.ICAControllerKeeper.WithICS4Wrapper(icaControllerStack.(porttypes.ICS4Wrapper))

	transferStackBuilder := porttypes.NewIBCStackBuilder(app.IBCKeeper.ChannelKeeper)
	transferStackBuilder.Base(
		ibctransfer.NewIBCModule(app.TransferKeeper)).Next(
		ibccallbacks.NewIBCMiddleware(wasmStackIBCHandler, wasm.DefaultMaxIBCCallbackGas),
	)
	transferStack := transferStackBuilder.Build()
	app.TransferKeeper.WithICS4Wrapper(transferStack.(porttypes.ICS4Wrapper))

	icaHostStack := icahost.NewIBCModule(app.ICAHostKeeper)

	ibcRouter := porttypes.NewRouter().
		AddRoute(ibctransfertypes.ModuleName, transferStack).
		AddRoute(wasmtypes.ModuleName, wasmStackIBCHandler).
		AddRoute(icacontrollertypes.SubModuleName, icaControllerStack).
		AddRoute(icahosttypes.SubModuleName, icaHostStack)
	app.IBCKeeper.SetRouter(ibcRouter)

	ibcRouterV2 := ibcapi.NewRouter().
		AddRoute(ibctransfertypes.PortID, transferv2.NewIBCModule(app.TransferKeeper)).
		AddPrefixRoute(wasmkeeper.PortIDPrefixV2, wasmkeeper.NewIBC2Handler(app.WasmKeeper))
	app.IBCKeeper.SetRouterV2(ibcRouterV2)

	// CRITICAL IN IBC v11: Register light client modules on ClientKeeper!
	clientKeeper := app.IBCKeeper.ClientKeeper
	storeProvider := clientKeeper.GetStoreProvider()
	tmLightClientModule := ibctm.NewLightClientModule(app.appCodec, storeProvider)
	clientKeeper.AddRoute(ibctm.ModuleName, &tmLightClientModule)
	smLightClientModule := solomachine.NewLightClientModule(app.appCodec, storeProvider)
	clientKeeper.AddRoute(solomachine.ModuleName, &smLightClientModule)

	if err := app.RegisterModules(
		ibc.NewAppModule(app.IBCKeeper),
		ibctransfer.NewAppModule(app.TransferKeeper),
		icamodule.NewAppModule(app.ICAControllerKeeper, app.ICAHostKeeper),
		ibctm.NewAppModule(tmLightClientModule),
		solomachine.NewAppModule(smLightClientModule),
		wasm.NewAppModule(app.AppCodec(), &app.WasmKeeper, app.StakingKeeper, app.AccountKeeper, app.BankKeeper, app.MsgServiceRouter()),
	); err != nil {
		//nolint:forbidigo // init code
		panic(err)
	}

	return nodeConfig
}

func RegisterLegacyModules(registry cdctypes.InterfaceRegistry) map[string]appmodule.AppModule {
	modules := map[string]appmodule.AppModule{
		ibcexported.ModuleName:      ibc.AppModule{},
		ibctransfertypes.ModuleName: ibctransfer.AppModule{},
		icatypes.ModuleName:         icamodule.AppModule{},
		ibctm.ModuleName:            ibctm.AppModule{},
		solomachine.ModuleName:      solomachine.AppModule{},
		wasmtypes.ModuleName:        wasm.AppModule{},
	}
	for _, m := range modules {
		if mod, ok := m.(interface {
			RegisterInterfaces(registry cdctypes.InterfaceRegistry)
		}); ok {
			mod.RegisterInterfaces(registry)
		}
	}
	return modules
}

// AcceptedGrpcQueries enumerates gRPC requests contracts are allowed to issue
func AcceptedGrpcQueries() wasmkeeper.AcceptedQueries {
	return wasmkeeper.AcceptedQueries{
		"/inference.inference.Query/ApprovedTokensForTrade": func() proto.Message {
			return &inferencetypes.QueryApprovedTokensForTradeResponse{}
		},
		"/inference.inference.Query/ValidateWrappedTokenForTrade": func() proto.Message {
			return &inferencetypes.QueryValidateWrappedTokenForTradeResponse{}
		},
		"/inference.inference.Query/ValidateIbcTokenForTrade": func() proto.Message {
			return &inferencetypes.QueryValidateIbcTokenForTradeResponse{}
		},
		"/inference.inference.Query/GetCurrentEpoch": func() proto.Message {
			return &inferencetypes.QueryGetCurrentEpochResponse{}
		},
		"/inference.inference.Query/ListClaimRecipients": func() proto.Message {
			return &inferencetypes.QueryListClaimRecipientsResponse{}
		},
		"/inference.inference.Query/EpochPerformanceSummaryByParticipant": func() proto.Message {
			return &inferencetypes.QueryEpochPerformanceSummaryByParticipantResponse{}
		},
		"/inference.streamvesting.Query/TotalVestingAmount": func() proto.Message {
			return &streamvestingtypes.QueryTotalVestingAmountResponse{}
		},
	}
}

// AcceptedStargateQueries enumerates Stargate responses contracts are allowed to decode
func AcceptedStargateQueries() wasmkeeper.AcceptedQueries {
	return wasmkeeper.AcceptedQueries{
		"/inference.inference.Query/ApprovedTokensForTrade": func() proto.Message {
			return &inferencetypes.QueryApprovedTokensForTradeResponse{}
		},
		"/inference.inference.Query/ValidateWrappedTokenForTrade": func() proto.Message {
			return &inferencetypes.QueryValidateWrappedTokenForTradeResponse{}
		},
		"/inference.inference.Query/ValidateIbcTokenForTrade": func() proto.Message {
			return &inferencetypes.QueryValidateIbcTokenForTradeResponse{}
		},
	}
}

// AllCapabilities returns all capabilities available with the current wasmvm
// See https://github.com/CosmWasm/cosmwasm/blob/main/docs/CAPABILITIES-BUILT-IN.md
// This functionality is going to be moved upstream: https://github.com/CosmWasm/wasmvm/issues/425
func AllCapabilities() []string {
	return []string{
		"iterator",
		"staking",
		"stargate",
		"cosmwasm_1_1",
		"cosmwasm_1_2",
		"cosmwasm_1_3",
		"cosmwasm_1_4",
		"cosmwasm_2_0",
	}
}
