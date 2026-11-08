package main

import (
	"context"
	"testing"
)

func TestChainOracleFromEnvUnset(t *testing.T) {
	t.Setenv("NODE_RPC_URL", "")
	t.Setenv("DEVSHARD_CHAIN_RPC", "")
	t.Setenv("NODE_GRPC_URL", "")
	oracle, headers := chainOracleFromEnv(context.Background())
	if oracle != nil || headers != nil {
		t.Fatal("empty chain env should leave the host without an oracle")
	}
}

func TestGRPCURLFromRPC(t *testing.T) {
	if got := grpcURLFromRPC("http://mock-chain:26657"); got != "mock-chain:9090" {
		t.Fatalf("grpcURLFromRPC=%q", got)
	}
}
