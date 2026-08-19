package public

import (
	"context"
	"os"
	"strings"

	"devshard/chainoracle/blocks/observer"
	blockserver "devshard/chainoracle/blocks/server"

	"common/logging"
	"github.com/productscience/inference/x/inference/types"
)

// mountChainOracle registers GET /block/latest and GET /block/:height on the
// existing public Echo at the root (not under /v1/). Hash-only; Prove is 501.
// Disabled with DAPI_CHAINORACLE_DISABLED. Isolated for a separate dapi PR.
func (s *Server) mountChainOracle() {
	if s == nil || s.e == nil {
		return
	}
	if chainOracleDisabled() {
		logging.Info("chainoracle mount skipped (DAPI_CHAINORACLE_DISABLED)", types.Server)
		return
	}
	rpcURL := strings.TrimSpace(s.configManager.GetChainNodeConfig().Url)
	if rpcURL == "" {
		logging.Warn("chainoracle mount skipped: empty chain_node.url", types.Server)
		return
	}
	obs, err := observer.NewTendermint(context.Background(), observer.TendermintConfig{
		RPCURL:     rpcURL,
		PollPeriod: "1s",
	})
	if err != nil {
		logging.Error("chainoracle observer", types.Server, "error", err)
		return
	}
	blockserver.Mount(s.e.Group(""), obs)
	logging.Info("chainoracle mounted", types.Server, "rpc_url", rpcURL)
}

func chainOracleDisabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("DAPI_CHAINORACLE_DISABLED"))) {
	case "1", "true", "yes":
		return true
	default:
		return false
	}
}
