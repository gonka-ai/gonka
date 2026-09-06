package public

import (
	"os"
	"strings"

	"common/chainoracle/blocks"
	"common/chainoracle/blocks/observer"
	blockserver "common/chainoracle/blocks/server"
	"common/logging"

	"github.com/productscience/inference/x/inference/types"
)

// NewChainOracle builds the hash-only production oracle, or (nil, nil) when
// the mount is disabled or chain_node.url is empty. Tip is Observe from the
// dapi NewBlock listener; At() is tipcache then Comet Header RPC.
func NewChainOracle(rpcURL string) (*observer.Oracle, error) {
	if chainOracleDisabled() {
		logging.Info("chainoracle skipped (DAPI_CHAINORACLE_DISABLED)", types.Server)
		return nil, nil
	}
	rpcURL = strings.TrimSpace(rpcURL)
	if rpcURL == "" {
		logging.Warn("chainoracle skipped: empty chain_node.url", types.Server)
		return nil, nil
	}
	return observer.NewTendermint(observer.TendermintConfig{RPCURL: rpcURL})
}

// WithChainOracle mounts GET /block/:height from the shared oracle (same
// instance the NewBlock listener Observes into).
func WithChainOracle(o blocks.BlockOracle) ServerOption {
	return func(s *Server) {
		s.chainOracle = o
	}
}

// mountChainOracle registers GET /block/:height and GET /block/:height/prove
// on the existing public Echo at the root (not under /v1/). Hash-only
// (height, hash, block time, chain id; empty Commit). Prove is 501.
// Live tip is Comet NewBlock, not /block/latest or /block/stream.
// Public /api/block/:height is intentional (proxy GNKAPI rate limit).
// Disabled with DAPI_CHAINORACLE_DISABLED.
func (s *Server) mountChainOracle() {
	if s == nil || s.e == nil {
		return
	}
	if chainOracleDisabled() {
		logging.Info("chainoracle mount skipped (DAPI_CHAINORACLE_DISABLED)", types.Server)
		return
	}
	o := s.chainOracle
	if o == nil {
		rpcURL := ""
		if s.configManager != nil {
			rpcURL = strings.TrimSpace(s.configManager.GetChainNodeConfig().Url)
		}
		created, err := NewChainOracle(rpcURL)
		if err != nil {
			logging.Error("chainoracle observer", types.Server, "error", err)
			return
		}
		if created == nil {
			return
		}
		o = created
		s.chainOracle = o
	}
	blockserver.Mount(s.e.Group(""), o)
	logging.Info("chainoracle mounted", types.Server)
}

func chainOracleDisabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("DAPI_CHAINORACLE_DISABLED"))) {
	case "1", "true", "yes":
		return true
	default:
		return false
	}
}
