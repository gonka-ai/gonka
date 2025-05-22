package admin

import (
	upgradetypes "cosmossdk.io/x/upgrade/types"
	"decentralized-api/apiconfig"
	"decentralized-api/broker"
	cosmos_client "decentralized-api/cosmosclient"
	"decentralized-api/internal/server/middleware"
	"github.com/cosmos/cosmos-sdk/codec"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	banktypes "github.com/cosmos/cosmos-sdk/x/bank/types"
	v1 "github.com/cosmos/cosmos-sdk/x/gov/types/v1"
	"github.com/labstack/echo/v4"
	"github.com/productscience/inference/app"
	"github.com/productscience/inference/x/inference/types"
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

	g.POST("unit-of-compute-price-proposal", s.postUnitOfComputePriceProposal)
	g.GET("unit-of-compute-price-proposal", s.getUnitOfComputePriceProposal)

	g.POST("models", s.registerModel)
	g.POST("tx/send", s.sendTransaction)
	return s
}

func getCodec() *codec.ProtoCodec {
	interfaceRegistry := codectypes.NewInterfaceRegistry()
	app.RegisterIBC(interfaceRegistry)
	types.RegisterInterfaces(interfaceRegistry)
	banktypes.RegisterInterfaces(interfaceRegistry)
	v1.RegisterInterfaces(interfaceRegistry)
	upgradetypes.RegisterInterfaces(interfaceRegistry)
	cdc := codec.NewProtoCodec(interfaceRegistry)
	return cdc
}

func (s *Server) Start(addr string) {
	go s.e.Start(addr)
}
