package grpcface

import (
	"context"
	"fmt"
	"net"

	"github.com/cosmos/cosmos-sdk/client/grpc/cmtservice"
	inferencetypes "github.com/productscience/inference/x/inference/types"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	txtypes "github.com/cosmos/cosmos-sdk/types/tx"
	"google.golang.org/grpc"

	"devshard/testenv/mockchain/rpcface"
	"devshard/testenv/mockchain/store"
	"devshard/testenv/mockchain/txledger"
)

// Deps configures which gRPC services mock-chain exposes.
type Deps struct {
	Store  *store.Store
	RPC    *rpcface.Service // required for tx broadcast
	Ledger *txledger.Ledger // required for tx broadcast; created when nil and RPC is set
}

func (d Deps) ledger() *txledger.Ledger {
	if d.Ledger != nil {
		return d.Ledger
	}
	if d.RPC != nil {
		return txledger.New()
	}
	return nil
}

// Register mounts inference Query + cmtservice on srv. When deps.RPC is set, also
// registers auth Query and tx Service.
func Register(srv *grpc.Server, deps Deps) {
	if deps.Store == nil {
		panic("mockchain grpc: nil store")
	}
	inferencetypes.RegisterQueryServer(srv, NewInferenceServer(deps.Store))
	cmtservice.RegisterServiceServer(srv, NewCometServer(deps.Store))
	if deps.RPC != nil {
		ledger := deps.ledger()
		authtypes.RegisterQueryServer(srv, newAuthServer(deps.Store))
		txtypes.RegisterServiceServer(srv, newTxServer(deps.Store, deps.RPC, ledger))
	}
}

// Serve listens on addr until ctx is cancelled, then graceful-stops.
func Serve(ctx context.Context, addr string, deps Deps) error {
	if deps.Store == nil {
		return fmt.Errorf("mockchain grpc: nil store")
	}
	lis, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("mockchain grpc listen %s: %w", addr, err)
	}
	srv := grpc.NewServer()
	Register(srv, deps)

	errCh := make(chan error, 1)
	go func() {
		errCh <- srv.Serve(lis)
	}()

	select {
	case <-ctx.Done():
		srv.GracefulStop()
		return ctx.Err()
	case err := <-errCh:
		if err == grpc.ErrServerStopped {
			return nil
		}
		return err
	}
}

// NewInProcessServer starts a gRPC server on a random localhost port for tests.
// Pass deps.RPC to enable auth + tx services.
func NewInProcessServer(deps Deps) (*grpc.Server, net.Listener, error) {
	if deps.Store == nil {
		return nil, nil, fmt.Errorf("mockchain grpc: nil store")
	}
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, nil, err
	}
	srv := grpc.NewServer()
	Register(srv, deps)
	go func() { _ = srv.Serve(lis) }()
	return srv, lis, nil
}
