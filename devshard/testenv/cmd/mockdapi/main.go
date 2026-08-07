package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"devshard/chainoracle/params"
	cosrv "devshard/chainoracle/server"
	"devshard/testenv/mockdapi"
)

func main() {
	cfg := mockdapi.DefaultConfig()
	cfg.GRPCAddr = envOr("MOCK_DAPI_GRPC_ADDR", ":9400")
	cfg.HTTPAddr = envOr("MOCK_DAPI_HTTP_ADDR", ":9100")
	cfg.ChainGRPCAddr = envOr("MOCK_CHAIN_GRPC_ADDR", "mock-chain:9090")
	cfg.ChainRPCAddr = envOr("MOCK_CHAIN_RPC_ADDR", "http://mock-chain:26657")
	cfg.ChainTestenvURL = os.Getenv("MOCK_CHAIN_TESTENV_URL")
	cfg.MLEndpoint = envOr("MOCK_ML_ENDPOINT", "http://mock-openai-0:8088")
	if raw := os.Getenv("MOCK_ML_NODES"); raw != "" {
		nodes, err := params.ParseMLNodesEnv(raw)
		if err != nil {
			log.Fatalf("mock-dapi: MOCK_ML_NODES: %v", err)
		}
		cfg.MLNodes = nodes
	}
	cfg.ChainID = envOr("CHAIN_ID", cfg.ChainID)
	cfg.BinaryDir = os.Getenv("MOCK_DAPI_BINARY_DIR")
	if v := versionFromEnv(); v.Name != "" {
		cfg.Versions = []cosrv.Version{v}
	}
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

	mlDesc := cfg.MLEndpoint
	if len(cfg.MLNodes) > 0 {
		mlDesc = os.Getenv("MOCK_ML_NODES")
	}
	log.Printf("mock-dapi gRPC on %s HTTP on %s chain=%s ml=%s",
		cfg.GRPCAddr, cfg.HTTPAddr, cfg.ChainGRPCAddr, mlDesc)
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

func versionFromEnv() cosrv.Version {
	name := os.Getenv("MOCK_DAPI_VERSION_NAME")
	binary := os.Getenv("MOCK_DAPI_VERSION_BINARY")
	sha := os.Getenv("MOCK_DAPI_VERSION_SHA256")
	if name == "" || binary == "" || sha == "" {
		return cosrv.Version{}
	}
	return cosrv.Version{Name: name, Binary: binary, SHA256: sha}
}
