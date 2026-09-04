package grpcface

import (
	"context"
	"strings"

	abcitypes "github.com/cometbft/cometbft/abci/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	txtypes "github.com/cosmos/cosmos-sdk/types/tx"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"devshard/testenv/mockchain/rpcface"
	"devshard/testenv/mockchain/store"
	"devshard/testenv/mockchain/txexec"
	"devshard/testenv/mockchain/txledger"
)

// TxServer implements cosmos.tx.v1beta1 Service for mock-chain broadcasts.
type TxServer struct {
	txtypes.UnimplementedServiceServer
	store  *store.Store
	rpc    *rpcface.Service
	ledger *txledger.Ledger
}

func newTxServer(st *store.Store, rpc *rpcface.Service, ledger *txledger.Ledger) *TxServer {
	return &TxServer{store: st, rpc: rpc, ledger: ledger}
}

func (s *TxServer) GetTx(_ context.Context, req *txtypes.GetTxRequest) (*txtypes.GetTxResponse, error) {
	hash := strings.TrimSpace(req.GetHash())
	if hash == "" {
		return nil, status.Error(codes.InvalidArgument, "hash is required")
	}
	stored, ok := s.ledger.Get(hash)
	if !ok {
		return nil, status.Error(codes.NotFound, "tx not found")
	}
	return &txtypes.GetTxResponse{
		Tx: nil,
		TxResponse: &sdk.TxResponse{
			Code:   stored.Code,
			TxHash: stored.Hash,
			Events: toABCIEvents(stored.Events),
		},
	}, nil
}

func (s *TxServer) BroadcastTx(_ context.Context, req *txtypes.BroadcastTxRequest) (*txtypes.BroadcastTxResponse, error) {
	if s.store == nil || s.rpc == nil || s.ledger == nil {
		return nil, status.Error(codes.FailedPrecondition, "mock-chain tx service is not configured")
	}
	txBytes := req.GetTxBytes()
	if len(txBytes) == 0 {
		return nil, status.Error(codes.InvalidArgument, "tx_bytes is required")
	}
	msgs, err := txexec.DecodeTxMessages(txBytes)
	if err != nil {
		return &txtypes.BroadcastTxResponse{
			TxResponse: &sdk.TxResponse{
				Code:      1,
				Codespace: "mockchain",
				RawLog:    err.Error(),
			},
		}, nil
	}
	result, err := txexec.ExecMessages(s.store, s.rpc, msgs)
	if err != nil {
		return &txtypes.BroadcastTxResponse{
			TxResponse: &sdk.TxResponse{
				Code:      1,
				Codespace: "mockchain",
				RawLog:    err.Error(),
			},
		}, nil
	}
	hash := s.ledger.Put(txBytes, result.Events)
	return &txtypes.BroadcastTxResponse{
		TxResponse: &sdk.TxResponse{
			Code:   0,
			TxHash: hash,
			Events: toABCIEvents(result.Events),
		},
	}, nil
}

func toABCIEvents(events []txexec.Event) []abcitypes.Event {
	out := make([]abcitypes.Event, len(events))
	for i, ev := range events {
		attrs := make([]abcitypes.EventAttribute, len(ev.Attributes))
		for j, a := range ev.Attributes {
			attrs[j] = abcitypes.EventAttribute{Key: a.Key, Value: a.Value}
		}
		out[i] = abcitypes.Event{Type: ev.Type, Attributes: attrs}
	}
	return out
}
