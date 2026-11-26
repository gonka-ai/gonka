package rpcface

import (
	"context"
	"fmt"

	"devshard/testenv/mockchain/store"
	ctypes "github.com/cometbft/cometbft/rpc/core/types"
	rpcserver "github.com/cometbft/cometbft/rpc/jsonrpc/server"
	rpctypes "github.com/cometbft/cometbft/rpc/jsonrpc/types"
	tmtypes "github.com/cometbft/cometbft/types"
)

func mainnetRoutes(st *store.Store) map[string]*rpcserver.RPCFunc {
	height := func(p *int64) int64 {
		if p == nil {
			return 0
		}
		return *p
	}
	return map[string]*rpcserver.RPCFunc{
		"header": rpcserver.NewRPCFunc(func(_ *rpctypes.Context, h *int64) (*ctypes.ResultHeader, error) {
			block, err := blockHandler(st, h)
			if err != nil {
				return nil, err
			}
			return &ctypes.ResultHeader{Header: &block.Block.Header}, nil
		}, "height"),
		"commit": rpcserver.NewRPCFunc(func(_ *rpctypes.Context, h *int64) (*ctypes.ResultCommit, error) {
			lb, err := st.MainnetLightBlock(context.Background(), height(h))
			if err != nil {
				return nil, err
			}
			return &ctypes.ResultCommit{SignedHeader: *lb.SignedHeader, CanonicalCommit: true}, nil
		}, "height"),
		"validators": rpcserver.NewRPCFunc(func(_ *rpctypes.Context, h *int64, page, perPage *int) (*ctypes.ResultValidators, error) {
			lb, err := st.MainnetLightBlock(context.Background(), height(h))
			if err != nil {
				return nil, err
			}
			p, n := 1, 30
			if page != nil {
				p = *page
			}
			if perPage != nil {
				n = *perPage
			}
			if p <= 0 || n <= 0 || n > 100 {
				return nil, fmt.Errorf("invalid validators page")
			}
			total := len(lb.ValidatorSet.Validators)
			from := min((p-1)*n, total)
			to := min(from+n, total)
			return &ctypes.ResultValidators{BlockHeight: lb.Height, Validators: lb.ValidatorSet.Validators[from:to], Count: to - from, Total: total}, nil
		}, "height,page,per_page"),
		"blockchain": rpcserver.NewRPCFunc(func(_ *rpctypes.Context, minHeight, maxHeight int64) (*ctypes.ResultBlockchainInfo, error) {
			latest := st.GetBlockHeight()
			if maxHeight == 0 || maxHeight > latest {
				maxHeight = latest
			}
			if minHeight == 0 {
				minHeight = max(int64(1), maxHeight-19)
			}
			if minHeight <= 0 || maxHeight < minHeight || maxHeight-minHeight >= 20 {
				return nil, fmt.Errorf("invalid blockchain range")
			}
			result := &ctypes.ResultBlockchainInfo{LastHeight: latest}
			for h := maxHeight; h >= minHeight; h-- {
				lb, err := st.MainnetLightBlock(context.Background(), h)
				if err != nil {
					return nil, err
				}
				result.BlockMetas = append(result.BlockMetas, &tmtypes.BlockMeta{BlockID: lb.Commit.BlockID, Header: *lb.Header})
			}
			return result, nil
		}, "minHeight,maxHeight"),
	}
}
