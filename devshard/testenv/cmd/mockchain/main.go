package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"devshard/testenv/mockchain/adminface"
	"devshard/testenv/mockchain/grpcface"
	"devshard/testenv/mockchain/rpcface"
	"devshard/testenv/mockchain/seed"
	"devshard/testenv/mockchain/txledger"
)

func main() {
	grpcAddr := envOr("MOCK_CHAIN_GRPC_ADDR", ":9090")
	rpcAddr := envOr("MOCK_CHAIN_RPC_ADDR", ":26657")
	configPath := os.Getenv("MOCK_CHAIN_CONFIG")
	if configPath == "" {
		configPath = os.Getenv("CONFIG_PATH")
	}
	blockInterval := envDuration("MOCK_CHAIN_BLOCK_INTERVAL", time.Second)
	blockIntervalDelta := envDuration("MOCK_CHAIN_BLOCK_INTERVAL_DELTA", 0)
	blockSeed := envInt64("MOCK_CHAIN_BLOCK_SEED", 42)

	st, err := seed.Load(configPath)
	if err != nil {
		log.Fatalf("mock-chain seed: %v", err)
	}

	rpcSvc, err := rpcface.NewService(st, rpcface.Config{
		BlockInterval:      blockInterval,
		BlockIntervalDelta: blockIntervalDelta,
		BlockSeed:          blockSeed,
	})
	if err != nil {
		log.Fatalf("mock-chain rpc: %v", err)
	}

	ledger := txledger.New()
	grpcDeps := grpcface.Deps{Store: st, RPC: rpcSvc, Ledger: ledger}

	adminAddr := envOr("MOCK_CHAIN_TESTENV_ADDR", ":9191")
	adminSrv := adminface.NewServer(st, rpcSvc, rpcSvc)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	log.Printf("mock-chain gRPC listening on %s chain_id=%s height=%d epoch=%d",
		grpcAddr, st.GetChainID(), st.GetBlockHeight(), st.GetEpoch().Index)
	log.Printf("mock-chain RPC listening on %s block_interval=%s delta=%s seed=%d",
		rpcAddr, blockInterval, blockIntervalDelta, blockSeed)
	log.Printf("mock-chain testenv admin on %s", adminAddr)

	errCh := make(chan error, 3)
	go func() {
		errCh <- grpcface.Serve(ctx, grpcAddr, grpcDeps)
	}()
	go func() {
		errCh <- rpcSvc.Serve(ctx, rpcAddr)
	}()
	go func() {
		errCh <- adminSrv.Serve(ctx, adminAddr)
	}()

	if err := waitServe(ctx, errCh); err != nil {
		log.Fatalf("mock-chain: %v", err)
	}
}

func waitServe(ctx context.Context, errCh <-chan error) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-errCh:
			if err == nil || errors.Is(err, context.Canceled) {
				continue
			}
			return err
		}
	}
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envDuration(key string, def time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			log.Printf("mock-chain: invalid %s=%q, using default %s", key, v, def)
			return def
		}
		return d
	}
	return def
}

func envInt64(key string, def int64) int64 {
	if v := os.Getenv(key); v != "" {
		var n int64
		if _, err := fmt.Sscanf(v, "%d", &n); err != nil {
			log.Printf("mock-chain: invalid %s=%q, using default %d", key, v, def)
			return def
		}
		return n
	}
	return def
}
