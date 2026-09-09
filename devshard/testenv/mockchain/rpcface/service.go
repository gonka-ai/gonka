package rpcface

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"

	cmtcfg "github.com/cometbft/cometbft/config"
	"github.com/cometbft/cometbft/libs/log"
	"github.com/cometbft/cometbft/rpc/core"
	ctypes "github.com/cometbft/cometbft/rpc/core/types"
	rpctypes "github.com/cometbft/cometbft/rpc/jsonrpc/types"
	rpcserver "github.com/cometbft/cometbft/rpc/jsonrpc/server"
	cmttypes "github.com/cometbft/cometbft/types"

	inferencetypes "github.com/productscience/inference/x/inference/types"

	"devshard/testenv/mockchain/store"
)

const defaultBlockInterval = time.Second

// Config tunes the CometBFT RPC face (Phase 3b).
type Config struct {
	BlockInterval      time.Duration
	BlockIntervalDelta time.Duration
	BlockSeed          int64
}

// Service owns the event bus, block ticker, and tx event publishing for mock-chain RPC.
type Service struct {
	store *store.Store
	bus   *cmttypes.EventBus
	env   *core.Environment

	blockInterval      time.Duration
	blockIntervalDelta time.Duration
	blockSeed          int64

	mu       sync.Mutex
	started  bool
	stopOnce sync.Once
}

// NewService wires a CometBFT RPC service over st. Caller must call Serve to listen.
func NewService(st *store.Store, config Config) (*Service, error) {
	if st == nil {
		return nil, fmt.Errorf("mockchain rpc: nil store")
	}
	interval := config.BlockInterval
	if interval <= 0 {
		interval = defaultBlockInterval
	}

	bus := cmttypes.NewEventBus()
	if err := bus.Start(); err != nil {
		return nil, fmt.Errorf("mockchain rpc event bus: %w", err)
	}

	cmtCfg := cmtcfg.DefaultConfig()
	env := &core.Environment{
		EventBus: bus,
		Logger:   log.NewNopLogger(),
		Config:   *cmtCfg.RPC,
	}

	return &Service{
		store:              st,
		bus:                bus,
		env:                env,
		blockInterval:      interval,
		blockIntervalDelta: config.BlockIntervalDelta,
		blockSeed:          config.BlockSeed,
	}, nil
}

// Store returns the backing chain store.
func (s *Service) Store() *store.Store {
	return s.store
}

// EventBus exposes the CometBFT event bus (tests).
func (s *Service) EventBus() *cmttypes.EventBus {
	return s.bus
}

// PutEscrowAndEmit stores an escrow and publishes a devshard_escrow_created Tx event.
func (s *Service) PutEscrowAndEmit(e *inferencetypes.DevshardEscrow) error {
	if e == nil || e.Id == 0 {
		return fmt.Errorf("mockchain rpc: invalid escrow")
	}
	s.store.PutEscrow(e)
	return s.publishEscrowCreated(e)
}

// PublishEscrowCreated emits devshard_escrow_created for an existing escrow record.
func (s *Service) PublishEscrowCreated(id uint64) error {
	e := s.store.GetEscrow(id)
	if e == nil {
		return fmt.Errorf("mockchain rpc: escrow %d not found", id)
	}
	return s.publishEscrowCreated(e)
}

func (s *Service) publishEscrowCreated(e *inferencetypes.DevshardEscrow) error {
	height := s.store.GetBlockHeight()
	if height == 0 {
		height = 1
	}
	return s.bus.PublishEventTx(txResult(height, escrowCreatedEvent(e)))
}

// PublishEscrowSettled emits devshard_escrow_settled for an escrow id.
func (s *Service) PublishEscrowSettled(id uint64, settler string, totalPayout, fees, remainder uint64) error {
	height := s.store.GetBlockHeight()
	if height == 0 {
		height = 1
	}
	return s.bus.PublishEventTx(txResult(height, escrowSettledEvent(id, settler, totalPayout, fees, remainder)))
}

func (s *Service) publishNewBlock(height int64) error {
	return s.bus.PublishEventNewBlock(newBlockEvent(s.store.GetChainID(), height))
}

func (s *Service) runBlockTicker(ctx context.Context) {
	height := s.store.GetBlockHeight()
	for {
		nextHeight := height + 1
		if height <= 0 {
			nextHeight = 1
		}
		wait := IntervalForHeight(s.blockInterval, s.blockIntervalDelta, s.blockSeed, nextHeight)
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
			height = s.store.AdvanceBlock()
			if err := s.publishNewBlock(height); err != nil {
				s.env.Logger.Error("mockchain rpc: publish NewBlock", "err", err, "height", height)
			}
		}
	}
}

func (s *Service) routes() map[string]*rpcserver.RPCFunc {
	st := s.store
	return map[string]*rpcserver.RPCFunc{
		"subscribe":       rpcserver.NewWSRPCFunc(s.env.Subscribe, "query"),
		"unsubscribe":     rpcserver.NewWSRPCFunc(s.env.Unsubscribe, "query"),
		"unsubscribe_all": rpcserver.NewWSRPCFunc(s.env.UnsubscribeAll, ""),
		"health":          rpcserver.NewRPCFunc(func(_ *rpctypes.Context) (*ctypes.ResultHealth, error) { return healthHandler(nil) }, ""),
		"status": rpcserver.NewRPCFunc(
			func(_ *rpctypes.Context) (*ctypes.ResultStatus, error) { return statusHandler(st) },
			"",
		),
		"block": rpcserver.NewRPCFunc(
			func(_ *rpctypes.Context, height *int64) (*ctypes.ResultBlock, error) { return blockHandler(st, height) },
			"height",
		),
	}
}

// Serve listens on addr until ctx is cancelled.
func (s *Service) Serve(ctx context.Context, addr string) error {
	s.mu.Lock()
	if s.started {
		s.mu.Unlock()
		return fmt.Errorf("mockchain rpc: already serving")
	}
	s.started = true
	s.mu.Unlock()

	lis, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("mockchain rpc listen %s: %w", addr, err)
	}

	serveCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	go s.runBlockTicker(serveCtx)

	routes := s.routes()
	logger := log.NewNopLogger()
	mux := http.NewServeMux()
	rpcserver.RegisterRPCFuncs(mux, routes, logger)
	wm := rpcserver.NewWebsocketManager(routes)
	wm.SetLogger(logger)
	mux.HandleFunc("/websocket", wm.WebsocketHandler)

	config := rpcserver.DefaultConfig()
	errCh := make(chan error, 1)
	go func() {
		errCh <- rpcserver.Serve(lis, mux, logger, config)
	}()

	select {
	case <-ctx.Done():
		cancel()
		_ = lis.Close()
		s.Stop()
		return ctx.Err()
	case err := <-errCh:
		cancel()
		s.Stop()
		if err != nil && err != http.ErrServerClosed {
			return err
		}
		return nil
	}
}

func (s *Service) Stop() {
	s.stopOnce.Do(func() {
		if s.bus != nil {
			_ = s.bus.Stop()
		}
	})
}

// AttachInProcess exposes RPC+websocket for an existing service (tests).
func (s *Service) AttachInProcess(ctx context.Context) (string, func(), error) {
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", nil, err
	}
	serveCtx, cancel := context.WithCancel(ctx)
	go s.runBlockTicker(serveCtx)

	routes := s.routes()
	logger := log.NewNopLogger()
	mux := http.NewServeMux()
	rpcserver.RegisterRPCFuncs(mux, routes, logger)
	wm := rpcserver.NewWebsocketManager(routes)
	wm.SetLogger(logger)
	mux.HandleFunc("/websocket", wm.WebsocketHandler)
	config := rpcserver.DefaultConfig()
	go func() { _ = rpcserver.Serve(lis, mux, logger, config) }()

	cleanup := func() {
		cancel()
		_ = lis.Close()
	}
	return "http://" + lis.Addr().String(), cleanup, nil
}

// NewInProcessServer starts RPC on a random localhost port for tests.
func NewInProcessServer(st *store.Store, cfg Config) (*Service, string, func(), error) {
	svc, err := NewService(st, cfg)
	if err != nil {
		return nil, "", nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	url, detach, err := svc.AttachInProcess(ctx)
	if err != nil {
		cancel()
		_ = svc.bus.Stop()
		return nil, "", nil, err
	}
	cleanup := func() {
		detach()
		cancel()
		svc.Stop()
	}
	return svc, url, cleanup, nil
}
