package admin

import (
	"decentralized-api/apiconfig"
	"decentralized-api/broker"
	cosmos_client "decentralized-api/cosmosclient"
	"decentralized-api/internal/server/middleware"
	pserver "decentralized-api/internal/server/public"
	"decentralized-api/internal/validation"

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
	e             *echo.Echo
	nodeBroker    *broker.Broker
	configManager *apiconfig.ConfigManager
	recorder      cosmos_client.CosmosMessageClient
	validator     *validation.InferenceValidator
	cdc           *codec.ProtoCodec
	blockQueue    *pserver.BridgeQueue
}

func NewServer(
	recorder cosmos_client.CosmosMessageClient,
	nodeBroker *broker.Broker,
	configManager *apiconfig.ConfigManager,
	validator *validation.InferenceValidator,
	blockQueue *pserver.BridgeQueue) *Server {
	cdc := getCodec()

	e := echo.New()
	e.HTTPErrorHandler = middleware.TransparentErrorHandler
	s := &Server{
		e:             e,
		nodeBroker:    nodeBroker,
		configManager: configManager,
		recorder:      recorder,
		validator:     validator,
		cdc:           cdc,
		blockQueue:    blockQueue,
	}

	e.Use(middleware.LoggingMiddleware)
	g := e.Group("/admin/v1/")

	g.POST("nodes", s.createNewNode)
	g.POST("nodes/batch", s.createNewNodes)
	// For explicit updates, also allow PUT on a single node
	g.PUT("nodes/:id", s.createNewNode)
	g.GET("nodes/upgrade-status", s.getUpgradeStatus)
	g.POST("nodes/version-status", s.postVersionStatus)
	g.GET("nodes", s.getNodes)
	g.DELETE("nodes/:id", s.deleteNode)
	g.POST("nodes/:id/enable", s.enableNode)
	g.POST("nodes/:id/disable", s.disableNode)

	g.POST("unit-of-compute-price-proposal", s.postUnitOfComputePriceProposal)
	g.GET("unit-of-compute-price-proposal", s.getUnitOfComputePriceProposal)

	g.POST("models", s.registerModel)
	g.POST("tx/send", s.sendTransaction)

	g.POST("bls/request", s.postRequestThresholdSignature)

	g.POST("debug/create-dummy-training-task", s.postDummyTrainingTask)

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

	return s
}

func getCodec() *codec.ProtoCodec {
	interfaceRegistry := codectypes.NewInterfaceRegistry()
	app.RegisterLegacyModules(interfaceRegistry)
	types.RegisterInterfaces(interfaceRegistry)
	banktypes.RegisterInterfaces(interfaceRegistry)
	authztypes.RegisterInterfaces(interfaceRegistry)
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
