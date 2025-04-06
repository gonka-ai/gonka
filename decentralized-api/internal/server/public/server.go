package public

import (
	"decentralized-api/apiconfig"
	"decentralized-api/broker"
	"decentralized-api/cosmosclient"
	"decentralized-api/internal/server/middleware"
	"github.com/labstack/echo/v4"
	"net/http"
)

type Server struct {
	e             *echo.Echo
	nodeBroker    *broker.Broker
	configManager *apiconfig.ConfigManager
	recorder      cosmosclient.CosmosMessageClient
}

// TODO: think about rate limits
func NewServer(
	nodeBroker *broker.Broker,
	configManager *apiconfig.ConfigManager,
	recorder cosmosclient.CosmosMessageClient) *Server {
	e := echo.New()
	s := &Server{
		e:             e,
		nodeBroker:    nodeBroker,
		configManager: configManager,
		recorder:      recorder,
	}

	e.Use(middleware.LoggingMiddleware)
	g := e.Group("/public/v1/")

	g.GET("status", s.getStatus)

	g.POST("chat/completions", s.postChat)
	g.GET("chat/completions/:id", s.getChatById)

	g.GET("participants/:address", s.getInferenceParticipantByAddress)
	g.GET("participants", s.getAllParticipants)
	g.POST("participants", s.submitNewParticipantHandler)

	// TODO test it
	g.POST("training-jobs", s.postTrainingJob)
	g.GET("training-jobs/:id", s.getTrainingJob)

	// TODO test it
	g.POST("verify-proof", s.postVerifyProof)
	g.POST("verify-block", s.postVerifyBlock)

	g.GET("pricing", s.getPricing)
	g.GET("models", s.getModels)
	g.GET("epochs/:epoch/participants", s.getParticipantsByEpoch)
	g.GET("poc-batches/:epoch", s.getPoCBatches)
	return s
}

func (s *Server) Start(addr string) {
	go s.e.Start(addr)
}

func (s *Server) getStatus(ctx echo.Context) error {
	return ctx.JSON(http.StatusOK, struct {
		Status string `json:"status"`
	}{Status: "ok"})
}
