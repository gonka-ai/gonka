package mlnode

import (
	"decentralized-api/apiconfig"
	"decentralized-api/broker"
	cosmos_client "decentralized-api/cosmosclient"
	"decentralized-api/internal/server/middleware"
	"decentralized-api/observability"
	"decentralized-api/poc/artifacts"
	"net/http"
	"sort"

	devshardobservability "devshard/observability"

	"github.com/labstack/echo/v4"
)

type Server struct {
	e             *echo.Echo
	recorder      cosmos_client.CosmosMessageClient
	broker        *broker.Broker
	artifactStore *artifacts.ManagedArtifactStore
	configManager *apiconfig.ConfigManager
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

func NewServer(recorder cosmos_client.CosmosMessageClient, broker *broker.Broker, opts ...ServerOption) *Server {
	e := echo.New()

	e.HTTPErrorHandler = middleware.TransparentErrorHandler

	e.Use(middleware.LoggingMiddleware)

	s := &Server{
		e:        e,
		recorder: recorder,
		broker:   broker,
	}

	for _, opt := range opts {
		opt(s)
	}

	// V2 callback routes (per-model).
	e.POST("/v2/poc-batches/:model_id/generated", s.postGeneratedArtifactsV2)
	e.POST("/v2/poc-batches/:model_id/validated", s.postValidatedArtifactsV2)

	// Devshard version list from chain params
	e.GET("/versions", s.getVersions)

	// Prometheus scrape endpoint. Merges api's default registry with the
	// devshard package's private registry so devshard_* lifecycle metrics
	// are also exposed from this api process.
	e.GET("/metrics", echo.WrapHandler(observability.MergedMetricsHandler(devshardobservability.Registry())))
	e.GET("/sd/devshardd", s.getDevshardSDTargets)

	return s
}

func (s *Server) getVersions(c echo.Context) error {
	if s.configManager == nil {
		return c.JSON(http.StatusOK, apiconfig.DevshardVersionsCache{Versions: []apiconfig.DevshardVersion{}})
	}
	return c.JSON(http.StatusOK, s.configManager.GetDevshardVersions())
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
	s.e.Server.ConnState = devshardobservability.ConnState("ml")
	go s.e.Start(addr)
}