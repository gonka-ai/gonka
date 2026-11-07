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

// headerFollowInterval is how often the e2e host polls Latest and backfills
// missing heights into the header-time map. Mock-chain advances about once a
// second; this keeps StampHeaderTime(startH) findable before AnswerWait.
const headerFollowInterval = 500 * time.Millisecond

// chainOracleFromEnv attaches the chain the e2e stack points at. Without it
// a heartbeat ack has no block, and the gateway chat gate stays unseeded.
// An empty env keeps the standalone host unchanged.
//
// Production devshardd fills the same map from Comet NewBlock + dapi poll.
// This binary only seeded Latest once, so after mock-chain's ticker moved
// past that tip VerifyTimeout waited AnswerWait on a permanent miss. The
// follow goroutine Observes each new tip (and backfills gaps) for the life
// of ctx.
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
	var last int64
	if hdr, err := oracle.Latest(seedCtx); err != nil || hdr == nil || hdr.Height <= 0 || len(hdr.BlockHash) == 0 {
		log.Printf("chain oracle: latest header unavailable: %v", err)
	} else {
		src.Observe(hdr, headertime.OriginChain, false)
		last = hdr.Height
		log.Printf("chain oracle: seeded header height=%d", hdr.Height)
	}
	go followChainHeaders(ctx, oracle, src, last)
	return oracle, src
}

// followChainHeaders keeps the header-time map aligned with the advancing
// chain tip. last is the highest height already Observed (0 if seed missed).
func followChainHeaders(ctx context.Context, oracle blocks.BlockOracle, src *headertime.Source, last int64) {
	if oracle == nil || src == nil {
		return
	}
	ticker := time.NewTicker(headerFollowInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			last = observeChainProgress(ctx, oracle, src, last)
		}
	}
}

// observeChainProgress stores tip and any skipped heights between last and tip.
// Returns the new last Observed height.
func observeChainProgress(ctx context.Context, oracle blocks.BlockOracle, src *headertime.Source, last int64) int64 {
	if oracle == nil || src == nil {
		return last
	}
	hdr, err := oracle.Latest(ctx)
	if err != nil || hdr == nil || hdr.Height <= 0 || len(hdr.BlockHash) == 0 {
		return last
	}
	if hdr.Height <= last {
		return last
	}
	for h := last + 1; h < hdr.Height; h++ {
		mid, midErr := oracle.At(ctx, h)
		if midErr != nil || mid == nil || mid.Height <= 0 || len(mid.BlockHash) == 0 {
			continue
		}
		src.Observe(mid, headertime.OriginChain, false)
	}
	src.Observe(hdr, headertime.OriginChain, false)
	return hdr.Height
}

func grpcURLFromRPC(rpc string) string {
	u := strings.TrimPrefix(strings.TrimPrefix(strings.TrimSpace(rpc), "https://"), "http://")
	return strings.Replace(u, ":26657", ":9090", 1)
}
