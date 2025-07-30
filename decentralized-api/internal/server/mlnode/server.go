package mlnode

import (
	"decentralized-api/broker"
	cosmos_client "decentralized-api/cosmosclient"
	"decentralized-api/internal/server/middleware"

	"github.com/labstack/echo/v4"
)

type Server struct {
	e        *echo.Echo
	recorder cosmos_client.CosmosMessageClient
	broker   *broker.Broker
}

// TODO breacking changes: url path, support on mlnode side
func NewServer(recorder cosmos_client.CosmosMessageClient, broker *broker.Broker) *Server {
	e := echo.New()

	e.HTTPErrorHandler = middleware.TransparentErrorHandler

	e.Use(middleware.LoggingMiddleware)
	g := e.Group("/mlnode/v1/")

	s := &Server{
		e:        e,
		recorder: recorder,
		broker:   broker,
	}

	// keep old paths too for backward compatibility
	g.POST("poc-batches/generated", s.postGeneratedBatches)
	e.POST("/v1/poc-batches/generated", s.postGeneratedBatches)

	g.POST("poc-batches/validated", s.postValidatedBatches)
	e.POST("/v1/poc-batches/validated", s.postValidatedBatches)
	return s
}

func (s *Server) Start(addr string) {
	go s.e.Start(addr)
}
