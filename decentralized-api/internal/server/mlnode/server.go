package mlnode

import (
	cosmos_client "decentralized-api/cosmosclient"
	"decentralized-api/internal/server/middleware"
	"github.com/labstack/echo/v4"
)

type Server struct {
	e        *echo.Echo
	recorder cosmos_client.CosmosMessageClient
}

// TODO breacking changes: url path, support on ml node side
func NewServer(recorder cosmos_client.CosmosMessageClient) *Server {
	e := echo.New()

	e.Use(middleware.LoggingMiddleware)
	g := e.Group("/mlnode/v1/")

	s := &Server{
		e:        e,
		recorder: recorder,
	}

	// TODO test it
	g.POST("poc-batches/generated", s.postGeneratedBatches)
	g.POST("poc-batches/validated", s.postValidatedBatches)
	return s
}

func (s *Server) Start(addr string) {
	go s.e.Start(addr)
}
