package admin

import (
	"decentralized-api/apiconfig"
	"decentralized-api/broker"
	cosmos_client "decentralized-api/cosmosclient"
	"decentralized-api/internal/server/middleware"

	upgradetypes "cosmossdk.io/x/upgrade/types"
	blstypes "github.com/productscience/inference/x/bls/types"

	"github.com/cosmos/cosmos-sdk/codec"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
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
	cdc           *codec.ProtoCodec
}

func NewServer(
	recorder cosmos_client.CosmosMessageClient,
	nodeBroker *broker.Broker,
	configManager *apiconfig.ConfigManager) *Server {
	cdc := getCodec()

	e := echo.New()
	e.HTTPErrorHandler = middleware.TransparentErrorHandler
	s := &Server{
		e:             e,
		nodeBroker:    nodeBroker,
		configManager: configManager,
		recorder:      recorder,
		cdc:           cdc,
	}

	e.Use(middleware.LoggingMiddleware)
	g := e.Group("/admin/v1/")

	g.POST("nodes", s.createNewNode)
	g.POST("nodes/batch", s.createNewNodes)
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

	return s
}

func getCodec() *codec.ProtoCodec {
	interfaceRegistry := codectypes.NewInterfaceRegistry()
	app.RegisterLegacyModules(interfaceRegistry)
	types.RegisterInterfaces(interfaceRegistry)
	banktypes.RegisterInterfaces(interfaceRegistry)
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
