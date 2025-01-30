package app

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	_ "cosmossdk.io/api/cosmos/tx/config/v1" // import for side-effects
	"cosmossdk.io/depinject"
	"cosmossdk.io/log"
	storetypes "cosmossdk.io/store/types"
	_ "cosmossdk.io/x/circuit" // import for side-effects
	circuitkeeper "cosmossdk.io/x/circuit/keeper"
	_ "cosmossdk.io/x/evidence" // import for side-effects
	evidencekeeper "cosmossdk.io/x/evidence/keeper"
	feegrantkeeper "cosmossdk.io/x/feegrant/keeper"
	_ "cosmossdk.io/x/feegrant/module" // import for side-effects
	nftkeeper "cosmossdk.io/x/nft/keeper"
	_ "cosmossdk.io/x/nft/module" // import for side-effects
	_ "cosmossdk.io/x/upgrade"    // import for side-effects
	upgradekeeper "cosmossdk.io/x/upgrade/keeper"

	dbm "github.com/cosmos/cosmos-db"
	"github.com/cosmos/cosmos-sdk/baseapp"
	"github.com/cosmos/cosmos-sdk/client"
	"github.com/cosmos/cosmos-sdk/codec"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	"github.com/cosmos/cosmos-sdk/runtime"
	"github.com/cosmos/cosmos-sdk/server"
	"github.com/cosmos/cosmos-sdk/server/api"
	"github.com/cosmos/cosmos-sdk/server/config"
	servertypes "github.com/cosmos/cosmos-sdk/server/types"
	"github.com/cosmos/cosmos-sdk/types/module"
	"github.com/cosmos/cosmos-sdk/x/auth"
	_ "github.com/cosmos/cosmos-sdk/x/auth" // import for side-effects
	authkeeper "github.com/cosmos/cosmos-sdk/x/auth/keeper"
	authsims "github.com/cosmos/cosmos-sdk/x/auth/simulation"
	_ "github.com/cosmos/cosmos-sdk/x/auth/tx/config" // import for side-effects
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	_ "github.com/cosmos/cosmos-sdk/x/auth/vesting" // import for side-effects
	authzkeeper "github.com/cosmos/cosmos-sdk/x/authz/keeper"
	_ "github.com/cosmos/cosmos-sdk/x/authz/module" // import for side-effects
	_ "github.com/cosmos/cosmos-sdk/x/bank"         // import for side-effects
	bankkeeper "github.com/cosmos/cosmos-sdk/x/bank/keeper"
	_ "github.com/cosmos/cosmos-sdk/x/consensus" // import for side-effects
	consensuskeeper "github.com/cosmos/cosmos-sdk/x/consensus/keeper"
	_ "github.com/cosmos/cosmos-sdk/x/crisis" // import for side-effects
	crisiskeeper "github.com/cosmos/cosmos-sdk/x/crisis/keeper"
	_ "github.com/cosmos/cosmos-sdk/x/distribution" // import for side-effects
	distrkeeper "github.com/cosmos/cosmos-sdk/x/distribution/keeper"
	"github.com/cosmos/cosmos-sdk/x/genutil"
	genutiltypes "github.com/cosmos/cosmos-sdk/x/genutil/types"
	"github.com/cosmos/cosmos-sdk/x/gov"
	govclient "github.com/cosmos/cosmos-sdk/x/gov/client"
	govkeeper "github.com/cosmos/cosmos-sdk/x/gov/keeper"
	govtypes "github.com/cosmos/cosmos-sdk/x/gov/types"
	groupkeeper "github.com/cosmos/cosmos-sdk/x/group/keeper"
	_ "github.com/cosmos/cosmos-sdk/x/group/module" // import for side-effects
	_ "github.com/cosmos/cosmos-sdk/x/mint"         // import for side-effects
	mintkeeper "github.com/cosmos/cosmos-sdk/x/mint/keeper"
	_ "github.com/cosmos/cosmos-sdk/x/params" // import for side-effects
	paramsclient "github.com/cosmos/cosmos-sdk/x/params/client"
	paramskeeper "github.com/cosmos/cosmos-sdk/x/params/keeper"
	paramstypes "github.com/cosmos/cosmos-sdk/x/params/types"
	_ "github.com/cosmos/cosmos-sdk/x/slashing" // import for side-effects
	slashingkeeper "github.com/cosmos/cosmos-sdk/x/slashing/keeper"
	_ "github.com/cosmos/cosmos-sdk/x/staking" // import for side-effects
	stakingkeeper "github.com/cosmos/cosmos-sdk/x/staking/keeper"
	_ "github.com/cosmos/ibc-go/modules/capability" // import for side-effects
	capabilitykeeper "github.com/cosmos/ibc-go/modules/capability/keeper"
	_ "github.com/cosmos/ibc-go/v8/modules/apps/27-interchain-accounts" // import for side-effects
	icacontrollerkeeper "github.com/cosmos/ibc-go/v8/modules/apps/27-interchain-accounts/controller/keeper"
	icahostkeeper "github.com/cosmos/ibc-go/v8/modules/apps/27-interchain-accounts/host/keeper"
	_ "github.com/cosmos/ibc-go/v8/modules/apps/29-fee" // import for side-effects
	ibcfeekeeper "github.com/cosmos/ibc-go/v8/modules/apps/29-fee/keeper"
	ibctransferkeeper "github.com/cosmos/ibc-go/v8/modules/apps/transfer/keeper"
	ibckeeper "github.com/cosmos/ibc-go/v8/modules/core/keeper"

	inferencemodulekeeper "github.com/productscience/inference/x/inference/keeper"
	// starport scaffolding # stargate/app/moduleImport

	// WASM
	"github.com/CosmWasm/wasmd/x/wasm"
	wasmkeeper "github.com/CosmWasm/wasmd/x/wasm/keeper"
	wasmtypes "github.com/CosmWasm/wasmd/x/wasm/types"

	"github.com/productscience/inference/docs"
)

const (
	AccountAddressPrefix = "cosmos"
	Name                 = "inference"
)

// DefaultNodeHome default home directory for the daemon
var DefaultNodeHome string

// These variables allow your code to satisfy runtime.AppI and servertypes.Application
var (
	_ runtime.AppI            = (*App)(nil)
	_ servertypes.Application = (*App)(nil)
)

// App is your ABCI application
type App struct {
	*runtime.App
	legacyAmino       *codec.LegacyAmino
	appCodec          codec.Codec
	txConfig          client.TxConfig
	interfaceRegistry codectypes.InterfaceRegistry

	// keepers
	AccountKeeper         authkeeper.AccountKeeper
	BankKeeper            bankkeeper.Keeper
	StakingKeeper         *stakingkeeper.Keeper
	DistrKeeper           distrkeeper.Keeper
	ConsensusParamsKeeper consensuskeeper.Keeper

	SlashingKeeper       slashingkeeper.Keeper
	MintKeeper           mintkeeper.Keeper
	GovKeeper            *govkeeper.Keeper
	CrisisKeeper         *crisiskeeper.Keeper
	UpgradeKeeper        *upgradekeeper.Keeper
	ParamsKeeper         paramskeeper.Keeper
	AuthzKeeper          authzkeeper.Keeper
	EvidenceKeeper       evidencekeeper.Keeper
	FeeGrantKeeper       feegrantkeeper.Keeper
	GroupKeeper          groupkeeper.Keeper
	NFTKeeper            nftkeeper.Keeper
	CircuitBreakerKeeper circuitkeeper.Keeper

	// IBC
	IBCKeeper           *ibckeeper.Keeper
	CapabilityKeeper    *capabilitykeeper.Keeper
	IBCFeeKeeper        ibcfeekeeper.Keeper
	ICAControllerKeeper icacontrollerkeeper.Keeper
	ICAHostKeeper       icahostkeeper.Keeper
	TransferKeeper      ibctransferkeeper.Keeper

	// Scoped IBC
	ScopedIBCKeeper           capabilitykeeper.ScopedKeeper
	ScopedIBCTransferKeeper   capabilitykeeper.ScopedKeeper
	ScopedICAControllerKeeper capabilitykeeper.ScopedKeeper
	ScopedICAHostKeeper       capabilitykeeper.ScopedKeeper

	// custom module keepers
	InferenceKeeper inferencemodulekeeper.Keeper
	// starport scaffolding # stargate/app/keeperDeclaration

	// Manually added WASM keeper
	WasmKeeper wasmkeeper.Keeper

	// Simulation manager
	sm *module.SimulationManager
}

func init() {
	userHomeDir, err := os.UserHomeDir()
	if err != nil {
		panic(err)
	}
	DefaultNodeHome = filepath.Join(userHomeDir, "."+Name)
}

// getGovProposalHandlers returns the chain's proposal handlers
func getGovProposalHandlers() []govclient.ProposalHandler {
	var govProposalHandlers []govclient.ProposalHandler
	govProposalHandlers = append(
		govProposalHandlers,
		paramsclient.ProposalHandler,
	)
	return govProposalHandlers
}

// ProvideWasmKeeper manually constructs the WASM keeper with minimal IBC/distribution
// (passed as nil), so it compiles but won't enable advanced features in WASM.
func ProvideWasmKeeper(app *App) (*wasmkeeper.Keeper, error) {
	storeKey := storetypes.NewKVStoreKey(wasmtypes.StoreKey)

	if err := app.RegisterStores(
		storeKey,
	); err != nil {
		return nil, err
	}
	// The store key for WASM
	if storeKey == nil {
		return nil, fmt.Errorf("wasm store key not found")
	}

	// Build a store service from that key
	storeService := runtime.NewKVStoreService(storeKey)
	wasmConfig := wasmtypes.DefaultWasmConfig()

	// Typically the "authority" is the x/gov module address
	authority := authtypes.NewModuleAddress(govtypes.ModuleName).String()

	// Capabilities string: can be "iterator,staking", or "all", etc.
	availableCapabilities := "iterator,staking"

	// Construct the keeper with minimal stubs for advanced features
	k := wasmkeeper.NewKeeper(
		app.appCodec,
		storeService,
		app.AccountKeeper,
		app.BankKeeper,
		*app.StakingKeeper,
		nil, // DistributionKeeper
		nil, // ICS4Wrapper
		nil, // ChannelKeeper
		nil, // PortKeeper
		nil, // CapabilityKeeper
		nil, // ICS20TransferPortSource
		nil, // MessageRouter
		nil, // GRPCQueryRouter
		filepath.Join(DefaultNodeHome, "wasm"),
		wasmConfig,
		availableCapabilities,
		authority,
	)
	return &k, nil
}

// AppConfig provides the app config using depinject
func AppConfig() depinject.Config {
	return depinject.Configs(
		appConfig,
		// If you have a YAML config, uncomment and call: appconfig.LoadYAML(AppConfigYAML)
		depinject.Supply(
			map[string]module.AppModuleBasic{
				genutiltypes.ModuleName: genutil.NewAppModuleBasic(genutiltypes.DefaultMessageValidator),
				govtypes.ModuleName:     gov.NewAppModuleBasic(getGovProposalHandlers()),
			},
		),
	)
}

// New initializes and returns a reference to your App
func New(
	logger log.Logger,
	db dbm.DB,
	traceStore io.Writer,
	loadLatest bool,
	appOpts servertypes.AppOptions,
	baseAppOptions ...func(*baseapp.BaseApp),
) (*App, error) {
	var (
		app        = &App{}
		appBuilder *runtime.AppBuilder
		// merges config in one
		appConfig = depinject.Configs(
			AppConfig(),
			depinject.Supply(
				// supply the application options and a few custom callbacks
				appOpts,
				app.GetIBCKeeper,
				app.GetCapabilityScopedKeeper,
				logger,
			),
		)
	)

	// Depinject all keepers except the WasmKeeper
	if err := depinject.Inject(appConfig,
		&appBuilder,
		&app.appCodec,
		&app.legacyAmino,
		&app.txConfig,
		&app.interfaceRegistry,
		&app.AccountKeeper,
		&app.BankKeeper,
		&app.StakingKeeper,
		&app.DistrKeeper,
		&app.ConsensusParamsKeeper,
		&app.SlashingKeeper,
		&app.MintKeeper,
		&app.GovKeeper,
		&app.CrisisKeeper,
		&app.UpgradeKeeper,
		&app.ParamsKeeper,
		&app.AuthzKeeper,
		&app.EvidenceKeeper,
		&app.FeeGrantKeeper,
		&app.NFTKeeper,
		&app.GroupKeeper,
		&app.CircuitBreakerKeeper,
		&app.InferenceKeeper,
	); err != nil {
		panic(err)
	}

	// Build the base application
	app.App = appBuilder.Build(db, traceStore, baseAppOptions...)

	// Manually provide the WASM keeper
	wasmK, err := ProvideWasmKeeper(app)
	if err != nil {
		return nil, err
	}
	app.WasmKeeper = *wasmK

	wasmModule := wasm.NewAppModule(
		app.appCodec,
		&app.WasmKeeper,
		app.StakingKeeper,
		app.AccountKeeper,
		app.BankKeeper,
		nil,
		app.GetSubspace(wasmtypes.ModuleName),
	)
	if err := app.RegisterModules(
		wasmModule,
	); err != nil {
		return nil, err
	}

	// If you have custom IBC modules to register, do so
	if err := app.registerIBCModules(appOpts); err != nil {
		return nil, err
	}

	// Register streaming if needed
	if err := app.RegisterStreamingServices(appOpts, app.kvStoreKeys()); err != nil {
		return nil, err
	}

	// Register invariants
	app.ModuleManager.RegisterInvariants(app.CrisisKeeper)

	// Create a simulation manager
	overrideModules := map[string]module.AppModuleSimulation{
		authtypes.ModuleName: auth.NewAppModule(
			app.appCodec,
			app.AccountKeeper,
			authsims.RandomGenesisAccounts,
			app.GetSubspace(authtypes.ModuleName),
		),
	}
	app.sm = module.NewSimulationManagerFromAppModules(app.ModuleManager.Modules, overrideModules)
	app.sm.RegisterStoreDecoders()

	// Setup upgrade handlers if needed
	app.setupUpgradeHandlers()

	// Optionally override InitChainer if needed
	// e.g.:
	// app.SetInitChainer(func(ctx sdk.Context, req *abci.RequestInitChain) (*abci.ResponseInitChain, error) {
	//     app.UpgradeKeeper.SetModuleVersionMap(ctx, app.ModuleManager.GetVersionMap())
	//     return app.App.InitChainer(ctx, req)
	// })

	// Finally, load the chain state
	if err := app.Load(loadLatest); err != nil {
		return nil, err
	}

	if err := checkWasmKeeperWorks(app); err != nil {
		return nil, fmt.Errorf("Wasm keeper check failed: %w", err)
	}

	return app, nil
}

func checkWasmKeeperWorks(app *App) error {
	// your baseapp only allows a single bool param. So just do:
	ctx := app.App.NewContext(true)

	// Attempt enumerating pinned codes
	if err := app.WasmKeeper.InitializePinnedCodes(ctx); err != nil {
		return fmt.Errorf("InitializePinnedCodes returned error: %w", err)
	}
	// log success
	ctx.Logger().Info("WASM keeper check: pinned codes enumerated successfully. Keeper is functional.")
	return nil
}

// LegacyAmino returns the application's amino codec
func (app *App) LegacyAmino() *codec.LegacyAmino {
	return app.legacyAmino
}

// AppCodec returns the application's codec
func (app *App) AppCodec() codec.Codec {
	return app.appCodec
}

// GetKey returns a KVStoreKey from the runtime store
func (app *App) GetKey(storeKey string) *storetypes.KVStoreKey {
	kv, ok := app.UnsafeFindStoreKey(storeKey).(*storetypes.KVStoreKey)
	if !ok {
		return nil
	}
	return kv
}

// GetMemKey returns a MemoryStoreKey from the runtime store
func (app *App) GetMemKey(storeKey string) *storetypes.MemoryStoreKey {
	mk, ok := app.UnsafeFindStoreKey(storeKey).(*storetypes.MemoryStoreKey)
	if !ok {
		return nil
	}
	return mk
}

// kvStoreKeys returns all KVStoreKey references
func (app *App) kvStoreKeys() map[string]*storetypes.KVStoreKey {
	out := make(map[string]*storetypes.KVStoreKey)
	for _, k := range app.GetStoreKeys() {
		if kv, ok := k.(*storetypes.KVStoreKey); ok {
			out[kv.Name()] = kv
		}
	}
	return out
}

// GetSubspace returns a param subspace for a given module
func (app *App) GetSubspace(moduleName string) paramstypes.Subspace {
	subspace, _ := app.ParamsKeeper.GetSubspace(moduleName)
	return subspace
}

// GetIBCKeeper returns the IBC keeper
func (app *App) GetIBCKeeper() *ibckeeper.Keeper {
	return app.IBCKeeper
}

// GetCapabilityScopedKeeper returns the scoped capability keeper for a module
func (app *App) GetCapabilityScopedKeeper(moduleName string) capabilitykeeper.ScopedKeeper {
	return app.CapabilityKeeper.ScopeToModule(moduleName)
}

// SimulationManager returns the simulation manager
func (app *App) SimulationManager() *module.SimulationManager {
	return app.sm
}

// RegisterAPIRoutes adds all application module routes to the provided API server
func (app *App) RegisterAPIRoutes(apiSvr *api.Server, apiConfig config.APIConfig) {
	app.App.RegisterAPIRoutes(apiSvr, apiConfig)

	// register swagger
	if err := server.RegisterSwaggerAPI(apiSvr.ClientCtx, apiSvr.Router, apiConfig.Swagger); err != nil {
		panic(err)
	}

	// register openapi
	docs.RegisterOpenAPIService(Name, apiSvr.Router)
}

// GetMaccPerms returns a copy of the module account permissions
func GetMaccPerms() map[string][]string {
	dup := make(map[string][]string)
	for _, perms := range moduleAccPerms {
		dup[perms.Account] = perms.Permissions
	}
	return dup
}

// BlockedAddresses returns a map of all blocked account addresses
func BlockedAddresses() map[string]bool {
	out := make(map[string]bool)
	if len(blockAccAddrs) > 0 {
		for _, addr := range blockAccAddrs {
			out[addr] = true
		}
	} else {
		for addr := range GetMaccPerms() {
			out[addr] = true
		}
	}
	return out
}
