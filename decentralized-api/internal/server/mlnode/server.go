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

func NewServer(recorder cosmos_client.CosmosMessageClient) *Server {
	e := echo.New()

	e.Use(middleware.LoggingMiddleware)
	g := e.Group("/mlnode/v1/")

	return &Server{
		e:        e,
		recorder: recorder,
	}
}
