package rpcface

import (
	"fmt"
	"time"

	"github.com/cometbft/cometbft/p2p"
	ctypes "github.com/cometbft/cometbft/rpc/core/types"
	rpctypes "github.com/cometbft/cometbft/rpc/jsonrpc/types"
	cmttypes "github.com/cometbft/cometbft/types"

	"devshard/testenv/mockchain/store"
)

func statusHandler(st *store.Store) (*ctypes.ResultStatus, error) {
	height := st.GetBlockHeight()
	chainID := st.GetChainID()
	return &ctypes.ResultStatus{
		NodeInfo: mockNodeInfo(chainID),
		SyncInfo: ctypes.SyncInfo{
			LatestBlockHeight: height,
			LatestBlockTime:   time.Now().UTC(),
			CatchingUp:        false,
		},
	}, nil
}

func blockAt(st *store.Store, chain *headerChain, heightPtr *int64) (mockBlock, error) {
	latest := st.GetBlockHeight()
	height := latest
	if heightPtr != nil {
		height = *heightPtr
	}
	if height <= 0 {
		return mockBlock{}, fmt.Errorf("height must be greater than 0, but got %d", height)
	}
	if height > latest {
		return mockBlock{}, fmt.Errorf("height %d must be less than or equal to the current blockchain height %d",
			height, latest)
	}
	return chain.at(height)
}

func blockHandler(st *store.Store, chain *headerChain, heightPtr *int64) (*ctypes.ResultBlock, error) {
	mb, err := blockAt(st, chain, heightPtr)
	if err != nil {
		return nil, err
	}
	return &ctypes.ResultBlock{BlockID: mb.id, Block: mb.block}, nil
}

func headerHandler(st *store.Store, chain *headerChain, heightPtr *int64) (*ctypes.ResultHeader, error) {
	mb, err := blockAt(st, chain, heightPtr)
	if err != nil {
		return nil, err
	}
	hdr := mb.block.Header
	return &ctypes.ResultHeader{Header: &hdr}, nil
}

func commitHandler(st *store.Store, chain *headerChain, heightPtr *int64) (*ctypes.ResultCommit, error) {
	mb, err := blockAt(st, chain, heightPtr)
	if err != nil {
		return nil, err
	}
	hdr := mb.block.Header
	return ctypes.NewResultCommit(&hdr, mb.commit, true), nil
}

func validatorsHandler(st *store.Store, chain *headerChain, heightPtr *int64, pagePtr *int) (*ctypes.ResultValidators, error) {
	mb, err := blockAt(st, chain, heightPtr)
	if err != nil {
		return nil, err
	}
	vals := chain.validators()
	if pagePtr != nil && *pagePtr != 1 {
		return nil, fmt.Errorf("page should be within [1, 1] range, given %d", *pagePtr)
	}
	return &ctypes.ResultValidators{
		BlockHeight: mb.block.Height,
		Validators:  vals.Validators,
		Count:       vals.Size(),
		Total:       vals.Size(),
	}, nil
}

// blockchainHandler mirrors CometBFT BlockchainInfo: at most 20 metas,
// highest first, bounded by the lowest served height and the tip.
func blockchainHandler(st *store.Store, chain *headerChain, minHeight, maxHeight int64) (*ctypes.ResultBlockchainInfo, error) {
	const limit int64 = 20
	latest := st.GetBlockHeight()
	if _, err := blockAt(st, chain, nil); err != nil {
		return nil, err
	}
	if minHeight < 0 || maxHeight < 0 {
		return nil, fmt.Errorf("heights must be non-negative")
	}
	if minHeight == 0 {
		minHeight = 1
	}
	if maxHeight == 0 {
		maxHeight = latest
	}
	maxHeight = min(latest, maxHeight)
	minHeight = max(chain.lowest(), minHeight, maxHeight-limit+1)
	if minHeight > maxHeight {
		return nil, fmt.Errorf("min height %d can't be greater than max height %d", minHeight, maxHeight)
	}
	res := &ctypes.ResultBlockchainInfo{LastHeight: latest}
	for h := maxHeight; h >= minHeight; h-- {
		mb, err := chain.at(h)
		if err != nil {
			return nil, err
		}
		res.BlockMetas = append(res.BlockMetas, &cmttypes.BlockMeta{BlockID: mb.id, Header: mb.block.Header})
	}
	return res, nil
}

func healthHandler(*rpctypes.Context) (*ctypes.ResultHealth, error) {
	return &ctypes.ResultHealth{}, nil
}

func mockNodeInfo(chainID string) p2p.DefaultNodeInfo {
	return p2p.DefaultNodeInfo{
		ProtocolVersion: p2p.ProtocolVersion{},
		DefaultNodeID:   p2p.ID("mockchain000000000000000000000000000000"),
		ListenAddr:      "tcp://0.0.0.0:26657",
		Network:         chainID,
		Version:         "0.38.21",
		Moniker:         "mock-chain",
		Other: p2p.DefaultNodeInfoOther{
			TxIndex:    "on",
			RPCAddress: "tcp://0.0.0.0:26657",
		},
	}
}
