package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"devshard/testenv/mockdapi"
)

func main() {
	cfg := mockdapi.DefaultConfig()
	cfg.GRPCAddr = envOr("MOCK_DAPI_GRPC_ADDR", ":9400")
	cfg.HTTPAddr = envOr("MOCK_DAPI_HTTP_ADDR", ":9100")
	cfg.ChainGRPCAddr = envOr("MOCK_CHAIN_GRPC_ADDR", "mock-chain:9090")
	cfg.ChainRPCAddr = envOr("MOCK_CHAIN_RPC_ADDR", "http://mock-chain:26657")
	cfg.ChainTestenvURL = os.Getenv("MOCK_CHAIN_TESTENV_URL")
	cfg.MLEndpoint = envOr("MOCK_ML_ENDPOINT", "http://mock-openai:8088")
	cfg.ChainID = envOr("CHAIN_ID", cfg.ChainID)
	if v := os.Getenv("MOCK_DAPI_CHAIN_POLL_INTERVAL"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			cfg.ChainPollInterval = d
		}
	}
	if v := os.Getenv("MOCK_DAPI_BLOCK_INTERVAL"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			cfg.BlockInterval = d
		}
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	svc, err := mockdapi.New(ctx, cfg)
	if err != nil {
		log.Fatalf("mock-dapi: %v", err)
	}

	log.Printf("mock-dapi gRPC on %s HTTP on %s chain=%s ml=%s",
		cfg.GRPCAddr, cfg.HTTPAddr, cfg.ChainGRPCAddr, cfg.MLEndpoint)
	if err := svc.Run(ctx); err != nil && err != context.Canceled {
		log.Fatalf("mock-dapi: %v", err)
	}
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
