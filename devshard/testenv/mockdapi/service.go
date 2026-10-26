package mockdapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"path/filepath"
	"time"

	"common/chain"
	"common/chainoracle/blocks"
	"common/chainoracle/blocks/observer"
	"common/nodemanager/gen"
	commonobs "common/observability"
	commonruntimeconfig "common/runtimeconfig"
	"devshard/chainoracle/params"
	cosrv "devshard/chainoracle/server"
	"devshard/testenv/gatewayphase"
	"devshard/testenv/mockchain/adminface"
	"devshard/testenv/mockchain/fetcher"

	"github.com/labstack/echo/v4"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// grpcTracerName is the instrumentation scope for mock-dapi server spans.
const grpcTracerName = "mock-dapi.nodemanager"

// Service runs mock-dapi gRPC + HTTP (chainoracle + /testenv fault proxy).
type Service struct {
	cfg            Config
	paramsSrc      *params.CachedSource
	paramsSrv      *params.Server
	nodeManager    *nodeManagerServer
	runtimeFetcher fetcher.SnapshotFetcher
	blocks         *observer.Oracle
	admin          *adminface.Client
	versions       *versionStore
	hostEvents     *hostEventRing
	grpcServer     *grpc.Server
	httpEcho       *echo.Echo
}

type mlNodeStats struct {
	Allocations         uint64 `json:"allocations"`
	RequestsReceived    uint64 `json:"requests_received"`
	SuccessfulResponses uint64 `json:"successful_responses"`
	FailedResponses     uint64 `json:"failed_responses"`
	Timeouts            uint64 `json:"timeouts"`
	ReplayHits          uint64 `json:"replay_hits"`
	ReplayMisses        uint64 `json:"replay_misses"`
	Error               string `json:"error,omitempty"`
}

// New connects to mock-chain gRPC and prepares chainoracle surfaces.
func New(ctx context.Context, cfg Config) (*Service, error) {
	if cfg.ChainGRPCAddr == "" {
		return nil, errors.New("mockdapi: ChainGRPCAddr is required")
	}
	if len(cfg.MLNodes) == 0 && cfg.MLEndpoint == "" {
		return nil, errors.New("mockdapi: MLNodes or MLEndpoint is required")
	}
	if cfg.ChainPollInterval <= 0 {
		cfg.ChainPollInterval = time.Second
	}
	if cfg.ChainRPCAddr == "" && !cfg.OmitBlockRoutes {
		return nil, errors.New("mockdapi: ChainRPCAddr is required for block routes")
	}
	if len(cfg.Versions) == 0 {
		cfg.Versions = DefaultConfig().Versions
	}

	conn, err := grpc.NewClient(cfg.ChainGRPCAddr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		return nil, fmt.Errorf("mockdapi: chain grpc dial: %w", err)
	}
	chainClient := chain.NewFromConn(conn)
	adminClient := adminface.NewClient(cfg.ChainTestenvURL)
	runtimeFetcher := fetcher.New(chainClient, adminClient)

	src, err := params.NewCachedSource(ctx, runtimeFetcher, commonruntimeconfig.Snapshot{})
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("mockdapi: cached source: %w", err)
	}

	paramsSrv, err := params.NewServer(params.Config{
		Source:     src,
		MLEndpoint: cfg.MLEndpoint,
		MLNodes:    cfg.MLNodes,
		MaxWaitCap: func() time.Duration { return commonruntimeconfig.DefaultMaxWaitCap },
		Log:        slog.Default(),
	})
	if err != nil {
		_ = conn.Close()
		return nil, err
	}

	var blockOracle *observer.Oracle
	if cfg.ChainRPCAddr != "" {
		// Same block source as dapi: mock-chain CometBFT RPC, commits verified.
		blockOracle, err = observer.NewTendermint(observer.TendermintConfig{RPCURL: cfg.ChainRPCAddr, VerifyCommits: true})
		if err != nil {
			_ = conn.Close()
			return nil, fmt.Errorf("mockdapi: chain block oracle: %w", err)
		}
	}
	hostEvents := newHostEventRing()
	nodeManager := newNodeManagerServer(paramsSrv, hostEvents, oracleOrNil(blockOracle), cfg.OmitBlockRoutes, cfg.MLNodes)

	s := &Service{
		cfg:            cfg,
		paramsSrc:      src,
		paramsSrv:      paramsSrv,
		nodeManager:    nodeManager,
		runtimeFetcher: runtimeFetcher,
		blocks:         blockOracle,
		admin:          adminClient,
		versions:       newVersionStore(cfg.Versions),
		hostEvents:     hostEvents,
	}
	return s, nil
}

// oracleOrNil keeps a nil *observer.Oracle a nil blocks.BlockOracle.
func oracleOrNil(o *observer.Oracle) blocks.BlockOracle {
	if o == nil {
		return nil
	}
	return o
}

// Run starts gRPC, HTTP, background poll loops until ctx ends.
func (s *Service) Run(ctx context.Context) error {
	grpcAddr := s.cfg.GRPCAddr
	if grpcAddr == "" {
		grpcAddr = ":9400"
	}
	httpAddr := s.cfg.HTTPAddr
	if httpAddr == "" {
		httpAddr = ":9100"
	}
	grpcL, err := net.Listen("tcp", grpcAddr)
	if err != nil {
		return fmt.Errorf("mockdapi grpc listen: %w", err)
	}
	httpL, err := net.Listen("tcp", httpAddr)
	if err != nil {
		return err
	}
	return s.RunOn(ctx, grpcL, httpL)
}

// RunOn serves on pre-bound listeners (tests).
func (s *Service) RunOn(ctx context.Context, grpcL, httpL net.Listener) error {
	if s == nil {
		return errors.New("mockdapi: nil service")
	}
	errCh := make(chan error, 4)
	go func() { errCh <- s.runBlockSync(ctx) }()
	go func() { errCh <- s.serveGRPCOn(ctx, grpcL) }()
	go func() { errCh <- s.serveHTTPOn(ctx, httpL) }()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-errCh:
			if err != nil && !errors.Is(err, context.Canceled) {
				return err
			}
		}
	}
}

func (s *Service) runChainPoll(ctx context.Context) error {
	ticker := time.NewTicker(s.cfg.ChainPollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if err := s.paramsSrc.Refresh(ctx); err != nil {
				slog.Warn("mockdapi: chain refresh failed", "err", err)
			}
		}
	}
}

func (s *Service) serveGRPCOn(ctx context.Context, lis net.Listener) error {
	// Continue the caller's trace so AcquireMLNode/ReleaseMLNode logs join the
	// gateway → host → dapi chain on one trace_id / request_id.
	gs := grpc.NewServer(
		grpc.ChainUnaryInterceptor(commonobs.UnaryServerTraceInterceptor(grpcTracerName)),
	)
	gen.RegisterNodeManagerServer(gs, s.nodeManager)
	s.grpcServer = gs
	go func() {
		<-ctx.Done()
		gs.GracefulStop()
	}()
	slog.Info("mock-dapi gRPC listening", "addr", lis.Addr().String())
	return gs.Serve(lis)
}

func (s *Service) serveHTTPOn(ctx context.Context, lis net.Listener) error {
	e := echo.New()
	e.HideBanner = true
	cosrv.Mount(e.Group(""), cosrv.Config{
		Blocks:          oracleOrNil(s.blocks),
		OmitBlocks:      s.cfg.OmitBlockRoutes,
		VersionProvider: s.versions,
	})
	e.GET("/testenv/ml-allocations", func(c echo.Context) error {
		return c.JSON(http.StatusOK, s.nodeManager.AllocationCounts())
	})
	e.GET("/testenv/ml-stats", func(c echo.Context) error {
		return c.JSON(http.StatusOK, s.mlNodeStats(c.Request().Context()))
	})
	if s.cfg.BinaryDir != "" {
		mountBinaryFiles(e.Group(""), s.cfg.BinaryDir)
	}
	gatewayphase.Mount(e.Group(""), gatewayphase.Config{
		BlockHeight: s.cfg.GatewayBlockHeight,
		EpochIndex:  s.cfg.GatewayEpochIndex,
	})
	mountTestenvProxy(e.Group(""), s.admin, s.RefreshRuntimeConfig, s.versions, s.hostEvents)
	s.httpEcho = e
	e.Server.BaseContext = func(net.Listener) context.Context { return ctx }
	go func() {
		<-ctx.Done()
		shCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = e.Shutdown(shCtx)
	}()
	slog.Info("mock-dapi HTTP listening", "addr", lis.Addr().String())
	if err := e.Server.Serve(lis); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return ctx.Err()
}

func (s *Service) mlNodeStats(ctx context.Context) map[string]mlNodeStats {
	allocations := s.nodeManager.AllocationCounts()
	result := make(map[string]mlNodeStats, len(s.nodeManager.mlNodes))
	client := &http.Client{Timeout: 2 * time.Second}
	for _, node := range s.nodeManager.mlNodes {
		stats := mlNodeStats{Allocations: allocations[node.ID]}
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, node.Endpoint+"/testenv/stats", nil)
		if err != nil {
			stats.Error = err.Error()
			result[node.ID] = stats
			continue
		}
		response, err := client.Do(request)
		if err != nil {
			stats.Error = err.Error()
			result[node.ID] = stats
			continue
		}
		if response.StatusCode != http.StatusOK {
			stats.Error = response.Status
			_ = response.Body.Close()
			result[node.ID] = stats
			continue
		}
		var nodeStats struct {
			RequestsReceived    uint64 `json:"requests_received"`
			SuccessfulResponses uint64 `json:"successful_responses"`
			FailedResponses     uint64 `json:"failed_responses"`
			Timeouts            uint64 `json:"timeouts"`
			ReplayHits          uint64 `json:"replay_hits"`
			ReplayMisses        uint64 `json:"replay_misses"`
		}
		if err := json.NewDecoder(response.Body).Decode(&nodeStats); err != nil {
			stats.Error = err.Error()
		} else {
			stats.RequestsReceived = nodeStats.RequestsReceived
			stats.SuccessfulResponses = nodeStats.SuccessfulResponses
			stats.FailedResponses = nodeStats.FailedResponses
			stats.Timeouts = nodeStats.Timeouts
			stats.ReplayHits = nodeStats.ReplayHits
			stats.ReplayMisses = nodeStats.ReplayMisses
		}
		_ = response.Body.Close()
		result[node.ID] = stats
	}
	return result
}

func mountBinaryFiles(g *echo.Group, dir string) {
	g.GET("/testenv/binaries/:name", func(c echo.Context) error {
		name := filepath.Base(c.Param("name"))
		if name == "." || name == ".." || name == string(filepath.Separator) || name == "" {
			return echo.NewHTTPError(http.StatusBadRequest, "invalid binary name")
		}
		return c.File(filepath.Join(dir, name))
	})
}

// ParamsSource exposes the cached params source for tests.
func (s *Service) ParamsSource() *params.CachedSource {
	if s == nil {
		return nil
	}
	return s.paramsSrc
}

// BlockOracle returns the chain block oracle (tests).
func (s *Service) BlockOracle() blocks.BlockOracle {
	if s == nil {
		return nil
	}
	return oracleOrNil(s.blocks)
}
