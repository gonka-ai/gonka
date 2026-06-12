package admin

import (
	"decentralized-api/apiconfig"
	"decentralized-api/broker"
	"decentralized-api/chainphase"
	cosmos_client "decentralized-api/cosmosclient"
	"decentralized-api/internal/server/middleware"
	pserver "decentralized-api/internal/server/public"
	"decentralized-api/mlnodeclient"
	"decentralized-api/participant"
	"decentralized-api/payloadstorage"
	"net/http"
	_ "net/http/pprof"

	"cosmossdk.io/x/feegrant"
	upgradetypes "cosmossdk.io/x/upgrade/types"
	blstypes "github.com/productscience/inference/x/bls/types"

	"github.com/cosmos/cosmos-sdk/codec"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	authztypes "github.com/cosmos/cosmos-sdk/x/authz"
	banktypes "github.com/cosmos/cosmos-sdk/x/bank/types"
	v1 "github.com/cosmos/cosmos-sdk/x/gov/types/v1"
	"github.com/labstack/echo/v4"
	"github.com/productscience/inference/app"
	collateraltypes "github.com/productscience/inference/x/collateral/types"
	"github.com/productscience/inference/x/inference/types"
	restrictionstypes "github.com/productscience/inference/x/restrictions/types"
)

type Server struct {
	e               *echo.Echo
	nodeBroker      *broker.Broker
	configManager   *apiconfig.ConfigManager
	recorder        cosmos_client.CosmosMessageClient
	cdc             *codec.ProtoCodec
	blockQueue      *pserver.BridgeQueue
	payloadStorage  payloadstorage.PayloadStorage
	phaseTracker    *chainphase.ChainPhaseTracker
	activityTracker *participant.ActivityTracker
	tester          *MLNodeTester
}

func NewServer(
	recorder cosmos_client.CosmosMessageClient,
	nodeBroker *broker.Broker,
	configManager *apiconfig.ConfigManager,
	blockQueue *pserver.BridgeQueue,
	payloadStorage payloadstorage.PayloadStorage,
	phaseTracker *chainphase.ChainPhaseTracker,
	activityTracker *participant.ActivityTracker,
	mlnodeFactory mlnodeclient.ClientFactory) *Server {
	cdc := getCodec()

	e := echo.New()
	e.HTTPErrorHandler = middleware.TransparentErrorHandler
	s := &Server{
		e:               e,
		nodeBroker:      nodeBroker,
		configManager:   configManager,
		recorder:        recorder,
		cdc:             cdc,
		blockQueue:      blockQueue,
		payloadStorage:  payloadStorage,
		phaseTracker:    phaseTracker,
		activityTracker: activityTracker,
		tester:          NewMLNodeTester(configManager, mlnodeFactory, nodeBroker.GetChainBridge()),
	}

	e.Use(middleware.LoggingMiddleware)
	e.Any("/debug/pprof/*", echo.WrapHandler(http.DefaultServeMux))
	g := e.Group("/admin/v1/")

	g.POST("nodes", s.createNewNode)
	g.POST("nodes/batch", s.createNewNodes)
	// For explicit updates, also allow PUT on a single node
	g.PUT("nodes/:id", s.createNewNode)
	g.GET("nodes/upgrade-status", s.getUpgradeStatus)
	g.POST("nodes/version-status", s.postVersionStatus)
	g.POST("nodes/:id/test", s.postNodeTest)
	// Read-only fact endpoints (see fact_handlers.go): primitives an
	// operator/script/AI skill can compose an onboarding flow from.
	g.GET("nodes/:id/test", s.getNodeTestResult)
	g.GET("nodes/:id/launch-plan", s.getNodeLaunchPlan)
	g.GET("poc/timing", s.getPoCTiming)
	g.GET("nodes", s.getNodes)
	g.DELETE("nodes/:id", s.deleteNode)
	g.POST("nodes/:id/enable", s.enableNode)
	g.POST("nodes/:id/disable", s.disableNode)

	g.POST("unit-of-compute-price-proposal", s.postUnitOfComputePriceProposal)
	g.GET("unit-of-compute-price-proposal", s.getUnitOfComputePriceProposal)

	g.POST("models", s.registerModel)
	g.POST("tx/send", s.sendTransaction)

	g.POST("bls/request", blsRequestDeprecated)

	// Export DB state (human-readable JSON) for admin purposes
	g.GET("export/db", s.exportDb)

	// Return current unsanitized config as JSON
	g.GET("config", s.getConfig)

	// Manual validation recovery and claim endpoint
	g.POST("claim-reward/recover", s.postClaimRewardRecover)

	// EXPERIMENTAL: Setup and health report endpoint for participant onboarding
	g.GET("setup/report", s.getSetupReport)

	// Bridge
	g.POST("bridge/block", s.postBridgeBlock)

	// Payload storage for testing (allows testermint to store payloads directly)
	g.POST("payloads", s.storePayload)

	return s
}

func getCodec() *codec.ProtoCodec {
	interfaceRegistry := codectypes.NewInterfaceRegistry()
	app.RegisterLegacyModules(interfaceRegistry)
	types.RegisterInterfaces(interfaceRegistry)
	banktypes.RegisterInterfaces(interfaceRegistry)
	authztypes.RegisterInterfaces(interfaceRegistry)
	feegrant.RegisterInterfaces(interfaceRegistry)
	v1.RegisterInterfaces(interfaceRegistry)
	upgradetypes.RegisterInterfaces(interfaceRegistry)
	collateraltypes.RegisterInterfaces(interfaceRegistry)
	restrictionstypes.RegisterInterfaces(interfaceRegistry)
	blstypes.RegisterInterfaces(interfaceRegistry)
	cdc := codec.NewProtoCodec(interfaceRegistry)
	return cdc
}

func (s *Server) Start(addr string) {
	go s.e.Start(addr)
}

// getConfig returns the current configuration as JSON (unsanitized)
func (s *Server) getConfig(c echo.Context) error {
	cfg := s.configManager.GetConfig()
	return c.JSONPretty(200, cfg, "  ")
}
