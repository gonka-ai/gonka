package admin

import (
	"decentralized-api/apiconfig"
	"decentralized-api/broker"
	cosmos_client "decentralized-api/cosmosclient"
	"decentralized-api/internal/server/middleware"
	"github.com/cosmos/cosmos-sdk/codec"
	"github.com/labstack/echo/v4"
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
	configManager *apiconfig.ConfigManager,
	cdc *codec.ProtoCodec) *Server {
	e := echo.New()
	s := &Server{
		e:             e,
		nodeBroker:    nodeBroker,
		configManager: configManager,
		recorder:      recorder,
		cdc:           cdc,
	}

	// TODO test
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

func (s *Server) Start(addr string) {
	go s.e.Start(addr)
}
