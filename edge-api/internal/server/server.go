package server

import (
	"net/http"

	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"

	"common/chain"
	"common/queryapi"
	"common/queryapi/gen"
	"edge-api/observability"
)

// Server is the Echo instance plus the readiness state shutdown needs to reach.
type Server struct {
	*echo.Echo

	readiness *readiness
}

// New creates the Echo instance with Tier A read-only routes mounted:
//   - GET /healthz — liveness, 200 while the process is up
//   - GET /readyz  — readiness, probed by edge-api-router
//   - /v1/... (queryapi: status, participants, models, epochs, BLS, etc.)
func New(chainClient *chain.Client) *Server {
	e := echo.New()
	e.HideBanner = true
	e.HidePort = true
	e.Use(middleware.Recover())
	e.Use(observability.EchoMiddleware())

	ready := newReadiness(chainClient)

	e.GET("/healthz", func(c echo.Context) error { return c.String(http.StatusOK, "ok") })
	e.GET("/readyz", ready.handler)

	gen.RegisterHandlers(e, queryapi.NewHandlers(chainClient))

	return &Server{Echo: e, readiness: ready}
}

// BeginDrain makes /readyz answer 503 while the server keeps accepting and
// serving. Call it before waiting out the announce window, so the balancer stops
// routing here before anything stops being served.
func (s *Server) BeginDrain() { s.readiness.beginDrain() }
