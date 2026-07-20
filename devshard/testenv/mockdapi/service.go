package mockdapi

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"

	"common/chain"
	"common/nodemanager/gen"
	commonruntimeconfig "common/runtimeconfig"
	cosrv "devshard/chainoracle/server"
	"devshard/chainoracle/blocks"
	"devshard/chainoracle/blocks/observer"
	"devshard/chainoracle/params"
	"devshard/signing"
	"devshard/testenv/gatewayphase"
	"devshard/testenv/mockchain/adminface"
	"devshard/testenv/mockchain/fetcher"

	"github.com/labstack/echo/v4"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// Service runs mock-dapi gRPC + HTTP (chainoracle + /testenv fault proxy).
type Service struct {
	cfg            Config
	paramsSrc      *params.CachedSource
	paramsSrv      *params.Server
	runtimeFetcher fetcher.SnapshotFetcher
	blockMock      *observer.Mock
	admin          *adminface.Client
	grpcServer     *grpc.Server
	httpEcho       *echo.Echo
}

// New connects to mock-chain gRPC and prepares chainoracle surfaces.
func New(ctx context.Context, cfg Config) (*Service, error) {
	if cfg.ChainGRPCAddr == "" {
		return nil, errors.New("mockdapi: ChainGRPCAddr is required")
	}
	if cfg.MLEndpoint == "" {
		return nil, errors.New("mockdapi: MLEndpoint is required")
	}
	if cfg.ChainPollInterval <= 0 {
		cfg.ChainPollInterval = time.Second
	}
	if cfg.BlockInterval <= 0 {
		cfg.BlockInterval = time.Second
	}
	if cfg.ChainID == "" {
		cfg.ChainID = "gonka-test"
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
		MaxWaitCap: func() time.Duration { return commonruntimeconfig.DefaultMaxWaitCap },
		Log:        slog.Default(),
	})
	if err != nil {
		_ = conn.Close()
		return nil, err
	}

	blockMock, err := newBlockMock(cfg)
	if err != nil {
		_ = conn.Close()
		return nil, err
	}

	s := &Service{
		cfg:            cfg,
		paramsSrc:      src,
		paramsSrv:      paramsSrv,
		runtimeFetcher: runtimeFetcher,
		blockMock:      blockMock,
		admin:          adminClient,
	}
	return s, nil
}

func newBlockMock(cfg Config) (*observer.Mock, error) {
	signer, err := signing.GenerateKey()
	if err != nil {
		return nil, err
	}
	addrBytes, err := blocks.AddressBytes(signer.CompressedPublicKeyBytes())
	if err != nil {
		return nil, err
	}
	mock, err := observer.NewMock(observer.MockConfig{
		ChainID:       cfg.ChainID,
		Validators:    []observer.MockValidator{{Signer: signer, Address: addrBytes, Power: 1}},
		BlockInterval: cfg.BlockInterval,
		Seed:          cfg.BlockSeed,
		Start:         time.Unix(1_700_000_000, 0).UTC(),
		InitialHeight: 1,
	})
	if err != nil {
		return nil, err
	}
	if _, err := mock.AdvanceOne(); err != nil {
		return nil, err
	}
	return mock, nil
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
	go func() { errCh <- s.blockMock.Run(ctx) }()
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
	gs := grpc.NewServer()
	gen.RegisterNodeManagerServer(gs, s.paramsSrv)
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
		Blocks:   s.blockMock,
		Versions: s.cfg.Versions,
	})
	gatewayphase.Mount(e.Group(""), gatewayphase.Config{
		BlockHeight: s.cfg.GatewayBlockHeight,
		EpochIndex:  s.cfg.GatewayEpochIndex,
	})
	mountTestenvProxy(e.Group(""), s.admin, s.RefreshRuntimeConfig)
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

// ParamsSource exposes the cached params source for tests.
func (s *Service) ParamsSource() *params.CachedSource {
	if s == nil {
		return nil
	}
	return s.paramsSrc
}

// BlockOracle returns the mock block observer for tests.
func (s *Service) BlockOracle() *observer.Mock {
	if s == nil {
		return nil
	}
	return s.blockMock
}
