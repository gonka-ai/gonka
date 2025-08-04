package public

import (
	"decentralized-api/apiconfig"
	"decentralized-api/broker"
	"decentralized-api/cosmosclient"
	"decentralized-api/internal"
	"decentralized-api/internal/server/middleware"
	"decentralized-api/training"
	"net/http"

	"github.com/labstack/echo/v4"
)

type Server struct {
	e                *echo.Echo
	nodeBroker       *broker.Broker
	configManager    *apiconfig.ConfigManager
	recorder         cosmosclient.CosmosMessageClient
	trainingExecutor *training.Executor
	blockQueue       *BridgeQueue
	bandwidthLimiter *internal.BandwidthLimiter
}

// TODO: think about rate limits
func NewServer(
	nodeBroker *broker.Broker,
	configManager *apiconfig.ConfigManager,
	recorder cosmosclient.CosmosMessageClient,
	trainingExecutor *training.Executor,
	blockQueue *BridgeQueue) *Server {
	e := echo.New()
	e.HTTPErrorHandler = middleware.TransparentErrorHandler

	// Set the package-level configManagerRef
	configManagerRef = configManager

	s := &Server{
		e:                e,
		nodeBroker:       nodeBroker,
		configManager:    configManager,
		recorder:         recorder,
		trainingExecutor: trainingExecutor,
		blockQueue:       blockQueue,
	}

	validationParams := configManager.GetValidationParams()
	limitsPerBlockKB := validationParams.EstimatedLimitsPerBlockKb
	if limitsPerBlockKB == 0 {
		limitsPerBlockKB = 1024 // Default to 1MB if not set
	}
	requestLifespanBlocks := validationParams.ExpirationBlocks
	if requestLifespanBlocks == 0 {
		requestLifespanBlocks = 10 // Default to 10 blocks
	}
	kbPerInputToken := validationParams.KbPerInputToken
	if kbPerInputToken == 0 {
		kbPerInputToken = 0.0023 // Default from README.md analysis
	}
	kbPerOutputToken := validationParams.KbPerOutputToken
	if kbPerOutputToken == 0 {
		kbPerOutputToken = 0.64 // Default from README.md analysis
	}

	s.bandwidthLimiter = internal.NewBandwidthLimiter(limitsPerBlockKB, requestLifespanBlocks, kbPerInputToken, kbPerOutputToken)

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
	g.GET("governance/pricing", s.getGovernancePricing)
	g.GET("governance/models", s.getGovernanceModels)
	g.GET("poc-batches/:epoch", s.getPoCBatches)

	g.GET("debug/pubkey-to-addr/:pubkey", s.debugPubKeyToAddr)
	g.GET("debug/verify/:height", s.debugVerify)

	g.GET("versions", s.getVersions)

	g.POST("bridge/block", s.postBlock)
	g.GET("bridge/status", s.getBridgeStatus)

	g.GET("epochs/:epoch", s.getEpochById)
	g.GET("epochs/:epoch/participants", s.getParticipantsByEpoch)
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
