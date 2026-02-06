package public

import (
	"decentralized-api/apiconfig"
	"decentralized-api/broker"
	"decentralized-api/chainphase"
	"decentralized-api/cosmosclient"
	"decentralized-api/internal"
	"decentralized-api/internal/authzcache"
	"decentralized-api/internal/server/middleware"
	"decentralized-api/payloadstorage"
	"decentralized-api/poc/artifacts"
	"decentralized-api/training"
	"net/http"
	"time"

	"github.com/labstack/echo/v4"
	echomw "github.com/labstack/echo/v4/middleware"
)

const httpClientTimeout = 5 * time.Minute

type Server struct {
	e                   *echo.Echo
	nodeBroker          *broker.Broker
	configManager       *apiconfig.ConfigManager
	recorder            cosmosclient.CosmosMessageClient
	trainingExecutor    *training.Executor
	blockQueue          *BridgeQueue
	bandwidthLimiter    *internal.BandwidthLimiter
	identityCache       *identityCache
	payloadStorage      payloadstorage.PayloadStorage
	phaseTracker        *chainphase.ChainPhaseTracker
	epochGroupDataCache *internal.EpochGroupDataCache
	artifactStore       *artifacts.ManagedArtifactStore
	authzCache          *authzcache.AuthzCache
	httpClient          *http.Client
}

// ServerOption configures optional Server dependencies.
type ServerOption func(*Server)

// WithArtifactStore enables local artifact storage for off-chain PoC proofs.
func WithArtifactStore(store *artifacts.ManagedArtifactStore) ServerOption {
	return func(s *Server) {
		s.artifactStore = store
	}
}

func NewServer(
	nodeBroker *broker.Broker,
	configManager *apiconfig.ConfigManager,
	recorder cosmosclient.CosmosMessageClient,
	trainingExecutor *training.Executor,
	blockQueue *BridgeQueue,
	phaseTracker *chainphase.ChainPhaseTracker,
	payloadStorage payloadstorage.PayloadStorage,
	opts ...ServerOption) *Server {
	e := echo.New()
	e.HTTPErrorHandler = middleware.TransparentErrorHandler

	// Set the package-level configManagerRef
	configManagerRef = configManager

	s := &Server{
		e:                   e,
		nodeBroker:          nodeBroker,
		configManager:       configManager,
		recorder:            recorder,
		trainingExecutor:    trainingExecutor,
		blockQueue:          blockQueue,
		identityCache:       newIdentityCache(),
		payloadStorage:      payloadStorage,
		phaseTracker:        phaseTracker,
		epochGroupDataCache: internal.NewEpochGroupDataCache(recorder),
		authzCache:          authzcache.NewAuthzCache(recorder),
		httpClient:          NewNoRedirectClient(httpClientTimeout),
	}

	for _, opt := range opts {
		opt(s)
	}

	s.bandwidthLimiter = internal.NewBandwidthLimiterFromConfig(configManager, recorder, phaseTracker)

	e.Use(middleware.LoggingMiddleware)
	g := e.Group("/v1/")

	g.GET("status", s.getStatus)
	g.GET("identity", s.getIdentity)

	g.POST("chat/completions", s.postChat)
	g.GET("chat/completions", s.getChatById)
	g.GET("inference/payloads", s.getInferencePayloads)

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

	// PoC proofs endpoint with IP rate limiting (100 req/min per IP)
	pocProofsRateLimiter := echomw.RateLimiter(echomw.NewRateLimiterMemoryStoreWithConfig(
		echomw.RateLimiterMemoryStoreConfig{
			Rate:      300.0 / 60.0, // 100 requests per minute
			Burst:     30,
			ExpiresIn: 3 * time.Minute,
		},
	))
	g.POST("poc/proofs", s.postPocProofs, pocProofsRateLimiter)

	// PoC artifact state endpoint (for testermint/validators to get real count and root_hash)
	g.GET("poc/artifacts/state", s.getPocArtifactsState)

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
