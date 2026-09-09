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

func blockHandler(st *store.Store, heightPtr *int64) (*ctypes.ResultBlock, error) {
	latest := st.GetBlockHeight()
	height := latest
	if heightPtr != nil {
		height = *heightPtr
	}
	if height <= 0 {
		return nil, fmt.Errorf("height must be greater than 0, but got %d", height)
	}
	if height > latest {
		return nil, fmt.Errorf("height %d must be less than or equal to the current blockchain height %d",
			height, latest)
	}
	block := cmttypes.MakeBlock(height, nil, nil, nil)
	block.Header.ChainID = st.GetChainID()
	return &ctypes.ResultBlock{
		BlockID: cmttypes.BlockID{Hash: block.Hash()},
		Block:   block,
	}, nil
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
