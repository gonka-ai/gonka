package grpcface

import (
	"common/chainoracle/blocks/nmrpc"
	"common/nodemanager/gen"
	"context"
	"devshard/testenv/mockchain/store"
)

type mainnetServer struct {
	gen.UnimplementedNodeManagerServer
	store *store.Store
}

func (s mainnetServer) GetBlockHeader(ctx context.Context, req *gen.GetBlockHeaderRequest) (*gen.GetBlockHeaderResponse, error) {
	return nmrpc.GetBlockHeader(ctx, s.store.MainnetOracle(), req)
}
