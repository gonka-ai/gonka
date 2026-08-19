package observer

import (
	"fmt"

	"devshard/chainoracle/blocks"

	ctypes "github.com/cometbft/cometbft/rpc/core/types"
)

// HeaderFromResultBlock maps a Tendermint ResultBlock to a hash-only Header.
// Commit.Signatures and validator hashes are left empty (Phase D; Strong is F).
func HeaderFromResultBlock(res *ctypes.ResultBlock) (*blocks.Header, error) {
	if res == nil || res.Block == nil {
		return nil, fmt.Errorf("observer: nil result block")
	}
	h := res.Block.Header
	hash := res.Block.Hash().Bytes()
	if len(res.BlockID.Hash) > 0 {
		hash = res.BlockID.Hash.Bytes()
	}
	return blocks.HashOnlyHeader(h.Height, h.Time, h.ChainID, hash), nil
}
