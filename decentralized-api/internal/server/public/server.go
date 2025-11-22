package public

import (
	"decentralized-api/apiconfig"
	"decentralized-api/broker"
	"decentralized-api/chainphase"
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
	blockQueue *BridgeQueue,
	phaseTracker *chainphase.ChainPhaseTracker) *Server {
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

	s.bandwidthLimiter = internal.NewBandwidthLimiterFromConfig(configManager, recorder, phaseTracker)

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

	g.GET("bridge/status", s.getBridgeStatus)
	g.GET("bridge/addresses", s.getBridgeAddresses)

	g.GET("epochs/:epoch", s.getEpochById)
	g.GET("epochs/:epoch/participants", s.getParticipantsByEpoch)

	// BLS Query Endpoints
	blsGroup := g.Group("bls/")
	blsGroup.GET("epoch/:id", s.getBLSEpochByID)
	blsGroup.GET("epochs/:id", s.getBLSEpochByID)
	blsGroup.GET("signatures/:request_id", s.getBLSSignatureByRequestID)

	// Restrictions public API (query-only)
	g.GET("restrictions/status", s.getRestrictionsStatus)
	g.GET("restrictions/exemptions", s.getRestrictionsExemptions)
	g.GET("restrictions/exemptions/:id/usage/:account", s.getRestrictionsExemptionUsage)

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
