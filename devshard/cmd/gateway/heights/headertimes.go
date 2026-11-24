package heights

import (
	"common/chain"
	"common/chainoracle/blocks"

	"devshard/chainoracle/blocks/direct"
	"devshard/chainoracle/blocks/headertime"
)

// NewHeaderTimes is where a session reads the block time of a floor height. See README.md, "What it owns".
func NewHeaderTimes(follower *Oracle, chainClient *chain.Client) *headertime.Source {
	var oracle blocks.BlockOracle = direct.NewFromChain(chainClient)
	if follower != nil {
		oracle = follower.BlockOracle
	}
	return headertime.New(oracle)
}
