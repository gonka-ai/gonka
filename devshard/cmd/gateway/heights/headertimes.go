package heights

import (
	"context"
	"errors"
	"strings"
	"time"

	"common/chain"
	"common/chainoracle/blocks"
	"common/chainoracle/blocks/observer"

	"devshard/chainoracle/blocks/direct"
	"devshard/chainoracle/blocks/headertime"
	"devshard/cmd/gateway/internal/logkey"
	"devshard/logging"
)

const rpcProbeTimeout = 3 * time.Second

// NewHeaderTimes is where a session reads the block time of a floor height. See README.md, "What it owns".
func NewHeaderTimes(follower *Oracle, chainClient *chain.Client, verified blocks.BlockOracle) *headertime.Source {
	if verified != nil {
		return headertime.New(verified)
	}
	logging.Warn("header times are not signature-verified", logkey.Subsystem, "heightsync")
	if follower != nil {
		return headertime.New(follower.BlockOracle)
	}
	return headertime.New(direct.NewFromChain(chainClient))
}

// CommitVerifiedOracle reads block headers from the CometBFT RPC endpoint the chain client resolves and checks their commit signatures; it is nil when there is no endpoint, it cannot be used, or an endpoint derived from the gRPC host does not answer.
func CommitVerifiedOracle(grpcURL, rpcURL string) blocks.BlockOracle {
	return commitVerifiedOracle(grpcURL, rpcURL, newCommitVerifier)
}

func commitVerifiedOracle(grpcURL, rpcURL string, build func(cometRPC string) (blocks.BlockOracle, error)) blocks.BlockOracle {
	cometRPC := chain.ResolveRPCURL(grpcURL, rpcURL)
	if cometRPC == "" {
		return nil
	}
	verified, err := build(cometRPC)
	if err != nil {
		logging.Warn("the commit-verifying header source is unavailable", logkey.Subsystem, "heightsync")
		return nil
	}
	return probedVerifiedOracle(verified, strings.TrimSpace(rpcURL) == "")
}

// newCommitVerifier drops the client's error: it quotes the endpoint, which can carry credentials.
func newCommitVerifier(cometRPC string) (blocks.BlockOracle, error) {
	verifier, err := observer.NewTendermint(observer.TendermintConfig{RPCURL: cometRPC, VerifyCommits: true})
	if err != nil {
		return nil, errors.New("the RPC client cannot be built")
	}
	return verifier, nil
}

// probedVerifiedOracle keeps an explicitly set endpoint even when it does not answer, and drops a derived one, which a deployment may not serve.
func probedVerifiedOracle(verified blocks.BlockOracle, derived bool) blocks.BlockOracle {
	ctx, cancel := context.WithTimeout(context.Background(), rpcProbeTimeout)
	defer cancel()
	if _, err := verified.Latest(ctx); err == nil {
		return verified
	}
	if derived {
		logging.Warn("the CometBFT RPC endpoint derived from the gRPC host does not answer, header times fall back to the unverified read; set DEVSHARD_CHAIN_RPC to verify them",
			logkey.Subsystem, "heightsync")
		return nil
	}
	logging.Warn("the CometBFT RPC endpoint in DEVSHARD_CHAIN_RPC does not answer, header lookups stay pending until it does",
		logkey.Subsystem, "heightsync")
	return verified
}
