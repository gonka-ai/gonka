package public

import (
	"decentralized-api/apiconfig"
	"decentralized-api/broker"
	"decentralized-api/cosmosclient"
	"decentralized-api/internal/server/middleware"
	"decentralized-api/training"
	"github.com/labstack/echo/v4"
	"net/http"
)

type Server struct {
	e                *echo.Echo
	nodeBroker       *broker.Broker
	configManager    *apiconfig.ConfigManager
	recorder         cosmosclient.CosmosMessageClient
	trainingExecutor *training.Executor
}

// TODO: think about rate limits
func NewServer(
	nodeBroker *broker.Broker,
	configManager *apiconfig.ConfigManager,
	recorder cosmosclient.CosmosMessageClient,
	trainingExecutor *training.Executor) *Server {
	e := echo.New()
	s := &Server{
		e:                e,
		nodeBroker:       nodeBroker,
		configManager:    configManager,
		recorder:         recorder,
		trainingExecutor: trainingExecutor,
	}

	e.Use(middleware.LoggingMiddleware)
	g := e.Group("/v1/")

	g.GET("status", s.getStatus)

	g.POST("chat/completions", s.postChat)
	g.GET("chat/completions/:id", s.getChatById)

	g.GET("participants/:address", s.getInferenceParticipantByAddress)
	g.GET("participants", s.getAllParticipants)
	g.POST("participants", s.submitNewParticipantHandler)

	g.POST("training/tasks", s.postTrainingTask)
	g.GET("training/tasks", s.getTrainingTasks)
	g.GET("training/tasks/:id", s.getTrainingTask)
	g.POST("training/lock-nodes", s.lockTrainingNodes)

	g.POST("verify-proof", s.postVerifyProof)
	g.POST("verify-block", s.postVerifyBlock)

	g.GET("pricing", s.getPricing)
	g.GET("models", s.getModels)
	g.GET("epochs/:epoch/participants", s.getParticipantsByEpoch)
	g.GET("poc-batches/:epoch", s.getPoCBatches)

	g.GET("debug/pubkey-to-addr/:pubkey", s.debugPubKeyToAddr)
	g.GET("debug/verify/:height", s.debugVerify)

	g.GET("versions", s.getVersions)

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
