// Package nmrpc is the NodeManager gRPC twin of GET /block/:height and
// GET /block/:height/prove. It talks to the same BlockOracle the HTTP
// mount uses; live tip stays Comet NewBlock, not a gRPC stream.
package nmrpc

import (
	"context"
	"errors"

	"common/chainoracle/blocks"
	"common/nodemanager/gen"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// GetBlockHeader serves NodeManager.GetBlockHeader. Height 0 is Latest().
func GetBlockHeader(ctx context.Context, oracle blocks.BlockOracle, req *gen.GetBlockHeaderRequest) (*gen.GetBlockHeaderResponse, error) {
	if oracle == nil {
		return nil, status.Error(codes.FailedPrecondition, "block header: oracle not configured")
	}
	height := int64(0)
	if req != nil {
		height = req.GetHeight()
	}
	if height < 0 {
		return nil, status.Error(codes.InvalidArgument, "block header: negative height")
	}
	var (
		h   *blocks.Header
		err error
	)
	if height == 0 {
		h, err = oracle.Latest(ctx)
	} else {
		h, err = oracle.At(ctx, height)
	}
	if err != nil {
		return nil, statusFromOracleErr(err)
	}
	if h == nil {
		return nil, status.Error(codes.NotFound, blocks.ErrHeaderNotFound.Error())
	}
	return &gen.GetBlockHeaderResponse{Header: HeaderToProto(h)}, nil
}

// ProveBlockPath serves NodeManager.ProveBlockPath.
func ProveBlockPath(ctx context.Context, oracle blocks.BlockOracle, req *gen.ProveBlockPathRequest) (*gen.ProveBlockPathResponse, error) {
	if oracle == nil {
		return nil, status.Error(codes.FailedPrecondition, "block proof: oracle not configured")
	}
	if req == nil || req.GetPath() == "" {
		return nil, status.Error(codes.InvalidArgument, "block proof: path is required")
	}
	if req.GetHeight() < 0 {
		return nil, status.Error(codes.InvalidArgument, "block proof: negative height")
	}
	p, err := oracle.Prove(ctx, req.GetPath(), req.GetHeight())
	if err != nil {
		return nil, statusFromOracleErr(err)
	}
	if p == nil {
		return nil, status.Error(codes.NotFound, blocks.ErrHeaderNotFound.Error())
	}
	return &gen.ProveBlockPathResponse{Proof: ProofToProto(p)}, nil
}

func statusFromOracleErr(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, blocks.ErrHeaderNotFound) {
		return status.Error(codes.NotFound, err.Error())
	}
	if errors.Is(err, blocks.ErrProveNotImplemented) {
		return status.Error(codes.Unimplemented, err.Error())
	}
	if st, ok := status.FromError(err); ok {
		return st.Err()
	}
	return status.Error(codes.Unavailable, err.Error())
}
