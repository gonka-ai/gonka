package rpcface

import (
	"time"

	"github.com/cometbft/cometbft/p2p"
	ctypes "github.com/cometbft/cometbft/rpc/core/types"
	rpctypes "github.com/cometbft/cometbft/rpc/jsonrpc/types"

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
