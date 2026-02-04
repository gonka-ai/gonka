package mlnode

import (
	"decentralized-api/broker"
	cosmos_client "decentralized-api/cosmosclient"
	"decentralized-api/internal/server/middleware"
	"decentralized-api/poc/artifacts"

	"github.com/labstack/echo/v4"
)

type Server struct {
	e             *echo.Echo
	recorder      cosmos_client.CosmosMessageClient
	broker        *broker.Broker
	artifactStore *artifacts.ManagedArtifactStore
}

// ServerOption configures optional Server dependencies.
type ServerOption func(*Server)

// WithArtifactStore enables local artifact storage for off-chain PoC.
func WithArtifactStore(store *artifacts.ManagedArtifactStore) ServerOption {
	return func(s *Server) {
		s.artifactStore = store
	}
}

func NewServer(recorder cosmos_client.CosmosMessageClient, broker *broker.Broker, opts ...ServerOption) *Server {
	e := echo.New()

	e.HTTPErrorHandler = middleware.TransparentErrorHandler

	e.Use(middleware.LoggingMiddleware)
	g := e.Group("/mlnode/v1/")

	s := &Server{
		e:        e,
		recorder: recorder,
		broker:   broker,
	}

	for _, opt := range opts {
		opt(s)
	}

	// V1 callback routes (on-chain batches, used when poc_v2_enabled=false)
	g.POST("poc-batches/generated", s.postGeneratedBatchesV1)
	e.POST("/v1/poc-batches/generated", s.postGeneratedBatchesV1)
	g.POST("poc-batches/validated", s.postValidatedBatchesV1)
	e.POST("/v1/poc-batches/validated", s.postValidatedBatchesV1)

	// V2 callback routes (off-chain artifacts, used when poc_v2_enabled=true)
	e.POST("/v2/poc-batches/generated", s.postGeneratedArtifactsV2)
	e.POST("/v2/poc-batches/validated", s.postValidatedArtifactsV2)
	return s
}

func (s *Server) Start(addr string) {
	go s.e.Start(addr)
}
