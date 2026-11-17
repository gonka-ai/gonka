package main

import (
	"context"
	"errors"
	"testing"

	"common/chain"

	chaintypes "github.com/productscience/inference/x/inference/types"
	"google.golang.org/grpc"
)

type epochQueryStub struct {
	chain.InferenceClient
	epoch uint64
	err   error
}

func (s epochQueryStub) GetCurrentEpoch(context.Context, *chaintypes.QueryGetCurrentEpochRequest, ...grpc.CallOption) (*chaintypes.QueryGetCurrentEpochResponse, error) {
	if s.err != nil {
		return nil, s.err
	}
	return &chaintypes.QueryGetCurrentEpochResponse{Epoch: s.epoch}, nil
}

func TestBootstrapPhase_SeedsEpoch(t *testing.T) {
	phase := new(chain.Phase)
	if err := bootstrapPhase(context.Background(), epochQueryStub{epoch: 42}, phase); err != nil {
		t.Fatalf("bootstrapPhase: %v", err)
	}
	if got := phase.EpochID(); got != 42 {
		t.Fatalf("phase epoch = %d, want 42", got)
	}
}

func TestBootstrapPhase_FailsInsteadOfStartingAtZero(t *testing.T) {
	phase := new(chain.Phase)
	queryErr := errors.New("unavailable")
	err := bootstrapPhase(context.Background(), epochQueryStub{err: queryErr}, phase)
	if !errors.Is(err, queryErr) {
		t.Fatalf("bootstrapPhase error = %v, want wrapped %v", err, queryErr)
	}
	if got := phase.EpochID(); got != 0 {
		t.Fatalf("phase epoch = %d after failed bootstrap, want untouched 0", got)
	}
}
