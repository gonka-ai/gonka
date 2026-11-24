package heights

import (
	"strings"

	"common/chain"
	"common/chainoracle/blocks"
	"common/chainoracle/blocks/observer"

	"devshard/chainoracle/blocks/direct"
	"devshard/chainoracle/blocks/headertime"
	"devshard/cmd/gateway/internal/logkey"
	"devshard/logging"
	"devshard/state"
)

// NewHeaderTimes is the block-time source every session measures timeouts on. See README.md, "What it owns".
func NewHeaderTimes(cometRPC string, follower *Oracle, chainClient *chain.Client) state.HeaderTimeSource {
	if rpc := strings.TrimSpace(cometRPC); rpc != "" {
		verified, err := observer.NewTendermint(observer.TendermintConfig{RPCURL: rpc, VerifyCommits: true})
		if err == nil {
			return headertime.New(verified)
		}
		logging.Warn("header times: the verifying oracle is unavailable", logkey.Subsystem, "heightsync", logkey.Error, err)
	}
	var fallback blocks.BlockOracle
	switch {
	case follower != nil:
		fallback = follower
	case chainClient != nil:
		fallback = direct.NewFromChain(chainClient)
	default:
		return nil
	}
	logging.Warn("header times are not signature-verified", logkey.Subsystem, "heightsync")
	return headertime.New(fallback)
}
