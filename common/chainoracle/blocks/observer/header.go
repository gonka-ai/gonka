package observer

import (
	"fmt"

	"common/chainoracle/blocks"

	ctypes "github.com/cometbft/cometbft/rpc/core/types"
	cmttypes "github.com/cometbft/cometbft/types"
)

// HeaderFromResultBlock maps a Tendermint ResultBlock to a hash-only Header.
// Commit.Signatures and validator hashes stay empty until Strong (spec §8 / §15).
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

// AsEventDataNewBlock unwraps amino JSON into EventDataNewBlock. The
// Comet decoder may store either the value or a pointer behind TMEventData.
func AsEventDataNewBlock(v any) (cmttypes.EventDataNewBlock, bool) {
	switch d := v.(type) {
	case cmttypes.EventDataNewBlock:
		return d, true
	case *cmttypes.EventDataNewBlock:
		if d == nil {
			return cmttypes.EventDataNewBlock{}, false
		}
		return *d, true
	default:
		return cmttypes.EventDataNewBlock{}, false
	}
}

// HeaderFromNewBlock maps a Comet EventDataNewBlock to a hash-only Header.
func HeaderFromNewBlock(data cmttypes.EventDataNewBlock) (*blocks.Header, bool) {
	if data.Block == nil {
		return nil, false
	}
	h := data.Block.Header
	hash := h.Hash().Bytes()
	if len(data.BlockID.Hash) > 0 {
		hash = data.BlockID.Hash.Bytes()
	} else if bh := data.Block.Hash(); len(bh) > 0 {
		hash = bh.Bytes()
	}
	return blocks.HashOnlyHeader(data.Block.Height, h.Time, h.ChainID, hash), true
}
