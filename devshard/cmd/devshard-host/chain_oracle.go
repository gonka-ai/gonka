package main

import (
	"context"
	"log"
	"os"
	"strings"
	"time"

	"common/chain"
	"common/chainoracle/blocks"

	"devshard/chainoracle/blocks/direct"
	"devshard/chainoracle/blocks/headertime"
	"devshard/state"
)

// chainOracleFromEnv attaches the chain the e2e stack points at. Without it
// a heartbeat ack has no block, and the gateway chat gate stays unseeded.
// An empty env keeps the standalone host unchanged.
func chainOracleFromEnv(ctx context.Context) (blocks.BlockOracle, state.HeaderTimeSource) {
	rpc := strings.TrimSpace(os.Getenv("NODE_RPC_URL"))
	if rpc == "" {
		rpc = strings.TrimSpace(os.Getenv("DEVSHARD_CHAIN_RPC"))
	}
	grpcURL := strings.TrimSpace(os.Getenv("NODE_GRPC_URL"))
	if rpc == "" && grpcURL == "" {
		return nil, nil
	}
	if grpcURL == "" {
		grpcURL = grpcURLFromRPC(rpc)
	}
	client, err := chain.NewWithQueryFallback(grpcURL, rpc)
	if err != nil {
		log.Printf("chain oracle: %v", err)
		return nil, nil
	}
	oracle := direct.NewFromChain(client)
	src := headertime.New()
	seedCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	hdr, err := oracle.Latest(seedCtx)
	if err != nil || hdr == nil || hdr.Height <= 0 || len(hdr.BlockHash) == 0 {
		log.Printf("chain oracle: latest header unavailable: %v", err)
		return oracle, src
	}
	src.Observe(hdr, headertime.OriginChain, false)
	log.Printf("chain oracle: seeded header height=%d", hdr.Height)
	return oracle, src
}

func grpcURLFromRPC(rpc string) string {
	u := strings.TrimPrefix(strings.TrimPrefix(strings.TrimSpace(rpc), "https://"), "http://")
	return strings.Replace(u, ":26657", ":9090", 1)
}
