package grpcface

import (
	"context"
	"strings"

	"github.com/cosmos/btcutil/bech32"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	cryptocodec "github.com/cosmos/cosmos-sdk/crypto/codec"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"devshard/testenv/mockchain/store"
)

// AuthServer implements cosmos.auth.v1beta1 Query for mock-chain accounts.
type AuthServer struct {
	authtypes.UnimplementedQueryServer
	store    *store.Store
	registry codectypes.InterfaceRegistry
}

func newAuthServer(st *store.Store) *AuthServer {
	registry := codectypes.NewInterfaceRegistry()
	cryptocodec.RegisterInterfaces(registry)
	authtypes.RegisterInterfaces(registry)
	return &AuthServer{store: st, registry: registry}
}

func (s *AuthServer) Account(ctx context.Context, req *authtypes.QueryAccountRequest) (*authtypes.QueryAccountResponse, error) {
	_ = ctx
	address := strings.TrimSpace(req.Address)
	if address == "" {
		return nil, status.Error(codes.InvalidArgument, "address is required")
	}
	acc := s.store.GetOrCreateAccount(address)
	_, addrBytes, err := bech32.Decode(address, 1023)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid address: %v", err)
	}
	base := authtypes.NewBaseAccount(addrBytes, nil, acc.AccountNumber, acc.Sequence)
	anyAcc, err := codectypes.NewAnyWithValue(base)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "pack account: %v", err)
	}
	return &authtypes.QueryAccountResponse{Account: anyAcc}, nil
}
