package mlnode

import (
	"decentralized-api/apiconfig"
	"decentralized-api/broker"
	cosmos_client "decentralized-api/cosmosclient"
	"decentralized-api/internal/callbackauth"
	"decentralized-api/internal/server/middleware"
	"decentralized-api/observability"
	"decentralized-api/poc/artifacts"
	"net/http"
	"sort"

	"github.com/labstack/echo/v4"
)

type Server struct {
	e              *echo.Echo
	recorder       cosmos_client.CosmosMessageClient
	broker         *broker.Broker
	artifactStore  *artifacts.ManagedArtifactStore
	configManager  *apiconfig.ConfigManager
	callbackSigner *callbackauth.Signer
}

// ServerOption configures optional Server dependencies.
type ServerOption func(*Server)

// WithArtifactStore enables local artifact storage for off-chain PoC.
func WithArtifactStore(store *artifacts.ManagedArtifactStore) ServerOption {
	return func(s *Server) {
		s.artifactStore = store
	}
}

// WithConfigManager enables serving devshard versions from chain params.
func WithConfigManager(cm *apiconfig.ConfigManager) ServerOption {
	return func(s *Server) {
		s.configManager = cm
	}
}

func WithCallbackSigner(signer *callbackauth.Signer) ServerOption {
	return func(s *Server) {
		s.callbackSigner = signer
	}
}

func NewServer(recorder cosmos_client.CosmosMessageClient, broker *broker.Broker, opts ...ServerOption) *Server {
	e := echo.New()

	e.HTTPErrorHandler = middleware.TransparentErrorHandler

	e.Use(middleware.RedactingLoggingMiddleware)

	s := &Server{
		e:        e,
		recorder: recorder,
		broker:   broker,
	}

	for _, opt := range opts {
		opt(s)
	}

	// V2 callback routes (per-model).
	e.POST("/v2/poc-batches/:model_id/g/:token/generated", s.postGeneratedArtifactsV2)
	e.POST("/v2/poc-batches/:model_id/v/:n/:min/:token/validated", s.postValidatedArtifactsV2)

	// Devshard version list from chain params
	e.GET("/versions", s.getVersions)

	// Prometheus scrape endpoint (restored after accidental removal in
	// b53fd8fcd / #1482). Default registry only — dapi no longer hosts an
	// in-process devshard registry to merge.
	e.GET("/metrics", echo.WrapHandler(observability.MetricsHandler()))
	e.GET("/sd/devshardd", s.getDevshardSDTargets)

	e.Server.ConnState = observability.ConnState("ml")

	return s
}

func (s *Server) getVersions(c echo.Context) error {
	if s.configManager == nil {
		return c.JSON(http.StatusOK, apiconfig.DevshardVersionsCache{Versions: []apiconfig.DevshardVersion{}})
	}
	return c.JSON(http.StatusOK, s.configManager.GetEffectiveDevshardVersions())
}

// prometheusTargetGroup is the target-group format consumed by Prometheus's
// http_sd_config: a list of targets sharing a label set.
type prometheusTargetGroup struct {
	Targets []string          `json:"targets"`
	Labels  map[string]string `json:"labels"`
}

// getDevshardSDTargets serves Prometheus HTTP service discovery for the set of
// devshardd binaries currently approved by chain params. All instances run
// behind versiond on a single host:port; the Prometheus scrape path is
// rewritten per version so each binary lands on its own time series.
func (s *Server) getDevshardSDTargets(c echo.Context) error {
	if s.configManager == nil {
		return c.JSON(http.StatusOK, []prometheusTargetGroup{})
	}

	versions := s.configManager.GetDevshardVersions().Versions
	sort.Slice(versions, func(i, j int) bool { return versions[i].Name < versions[j].Name })

	targets := make([]prometheusTargetGroup, 0, len(versions))
	for _, version := range versions {
		if version.Name == "" {
			continue
		}
		targets = append(targets, prometheusTargetGroup{
			Targets: []string{"versiond:8080"},
			Labels: map[string]string{
				"__metrics_path__": "/" + version.Name + "/metrics",
				"version":          version.Name,
				"service":          "devshardd",
			},
		})
	}
	return c.JSON(http.StatusOK, targets)
}

func (s *Server) Start(addr string) {
	go s.e.Start(addr)
}
