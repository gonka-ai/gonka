package chain

import (
	"context"
	"errors"
	"testing"
	"time"

	sdk "github.com/cosmos/cosmos-sdk/types"
	txtypes "github.com/cosmos/cosmos-sdk/types/tx"
	"github.com/productscience/inference/x/inference/types"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"trainshard/internal/domain/shared"
)

type senderStub struct {
	txtypes.ServiceClient
	answers []error
	asked   int
}

func (s *senderStub) GetTx(context.Context, *txtypes.GetTxRequest, ...grpc.CallOption) (*txtypes.GetTxResponse, error) {
	s.asked++
	if s.asked > len(s.answers) {
		return &txtypes.GetTxResponse{TxResponse: &sdk.TxResponse{Code: 0}}, nil
	}
	return nil, s.answers[s.asked-1]
}

func signerOver(sender *senderStub, landing time.Duration) *Signer {
	return &Signer{Client: &Client{poll: time.Millisecond}, sender: sender, landing: landing}
}

func TestLandedWaitsThroughNotFoundUntilTheBlockRuns(t *testing.T) {
	notFound := status.Error(codes.NotFound, "tx not found")
	sender := &senderStub{answers: []error{notFound, notFound, errors.New("blip")}}

	answer, err := signerOver(sender, time.Second).landed(context.Background(), &types.MsgSettleTrainshard{}, "ABCD")

	if err != nil || answer == nil {
		t.Fatalf("got %v, %v", answer, err)
	}
	if sender.asked != 4 {
		t.Fatalf("asked %d times", sender.asked)
	}
}

func TestLandedGivesUpAfterTheLandingWindow(t *testing.T) {
	notFound := status.Error(codes.NotFound, "tx not found")
	sender := &senderStub{answers: make([]error, 0)}
	for range 100000 {
		sender.answers = append(sender.answers, notFound)
	}

	_, err := signerOver(sender, 20*time.Millisecond).landed(context.Background(), &types.MsgSettleTrainshard{}, "ABCD")

	if shared.CodeOf(err) != "CHAIN_SLOW" {
		t.Fatalf("got %v, want CHAIN_SLOW", err)
	}
}

func TestBlocksToLandCoverTheLandingWindow(t *testing.T) {
	if got := signerOver(nil, 2*time.Minute).blocksToLand(); got != 25 {
		t.Fatalf("got %d blocks", got)
	}
	if got := signerOver(nil, time.Second).blocksToLand(); got != 2 {
		t.Fatalf("got %d blocks", got)
	}
}
