package public

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"decentralized-api/cosmosclient"

	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func TestClassifyBridgeExchangeSubmit(t *testing.T) {
	cases := []struct {
		name       string
		err        error
		wantAction bridgeSubmitAction
		wantReason string
	}{
		{
			name: "already validated",
			err: fmt.Errorf("bridge exchange failed: code=5 rawLog=%s",
				types.ErrBridgeAlreadyValidated.Error()),
			wantAction: bridgeSubmitSkip,
			wantReason: bridgeSkipAlreadyValidated,
		},
		{
			name: "not in tx epoch group",
			err: fmt.Errorf("bridge exchange failed: code=5 rawLog=%s",
				types.ErrBridgeValidatorNotInTxEpochGroup.Error()),
			wantAction: bridgeSubmitSkip,
			wantReason: bridgeSkipNotInTxEpoch,
		},
		{
			name:       "not in active participants",
			err:        errors.New(types.ErrBridgeValidatorNotInActiveGroup.Error()),
			wantAction: bridgeSubmitSkip,
			wantReason: bridgeSkipNotActive,
		},
		{
			name: "permission participant is not active",
			err: fmt.Errorf("bridge exchange failed: code=1109 rawLog=%s",
				types.ErrActiveParticipantNotFound.Error()),
			wantAction: bridgeSubmitSkip,
			wantReason: bridgeSkipNotActive,
		},
		{
			name:       "content mismatch",
			err:        errors.New(types.ErrBridgeContentMismatch.Error()),
			wantAction: bridgeSubmitSkip,
			wantReason: bridgeSkipContentMismatch,
		},
		{
			name:       "out of gas retries",
			err:        fmt.Errorf("bridge exchange failed: code=11 rawLog=out of gas"),
			wantAction: bridgeSubmitRetry,
			wantReason: bridgeRetryUncertain,
		},
		{
			name:       "unknown message retries",
			err:        fmt.Errorf("bridge exchange failed: code=2 rawLog=no handler exists for message type"),
			wantAction: bridgeSubmitRetry,
			wantReason: bridgeRetryUncertain,
		},
		{
			name:       "mint failure retries",
			err:        errors.New("failed to mint tokens: boom"),
			wantAction: bridgeSubmitRetry,
			wantReason: bridgeRetryUncertain,
		},
		{
			name:       "invalid amount retries",
			err:        errors.New("invalid amount: xyz"),
			wantAction: bridgeSubmitRetry,
			wantReason: bridgeRetryUncertain,
		},
		{
			name:       "epoch group query failure retries",
			err:        errors.New("unable to get epoch group for existing transaction: missing"),
			wantAction: bridgeSubmitRetry,
			wantReason: bridgeRetryUncertain,
		},
		{
			name:       "nil retries",
			err:        nil,
			wantAction: bridgeSubmitRetry,
			wantReason: bridgeRetryUncertain,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			action, reason := classifyBridgeExchangeSubmit(tc.err)
			if action != tc.wantAction || reason != tc.wantReason {
				t.Fatalf("got action=%v reason=%q, want action=%v reason=%q",
					action, reason, tc.wantAction, tc.wantReason)
			}
		})
	}
}

// TestCommitReceipt_AnteShapedPermanentRejectAdvances is the DAPI half of the
// certainty path that #1478 assumed but never proved end-to-end:
//
//	ante CheckTx err → BroadcastTxSync Code!=0 → BridgeExchange wrap →
//	classifyBridgeExchangeSubmit skip → commitReceipt returns nil (no confirm wait).
//
// Without this, a Sync Code=0 + DeliverTx reject stalls on confirm timeout.
func TestCommitReceipt_AnteShapedPermanentRejectAdvances(t *testing.T) {
	mockClient := &cosmosclient.MockCosmosMessageClient{}
	mockClient.On("GetAccountAddress").Return("gonka1validator")
	mockClient.On("BridgeExchange", mock.AnythingOfType("*types.MsgBridgeExchange")).Return(
		fmt.Errorf("bridge exchange failed: code=5 rawLog=%s",
			types.ErrBridgeValidatorNotInTxEpochGroup.Error()),
	)

	q := NewBlockQueue(mockClient, nil)
	err := q.commitReceipt(context.Background(), BridgeReceipt{
		ContractAddress: "0xabc",
		OwnerAddress:    "ignored",
		OwnerPubKey:     "034daac7b5be60532b562b030824b2c9b7c450985b894dd5544f9c7dd807d8bb88",
		Amount:          "100",
		ReceiptIndex:    "0",
	}, BridgeBlock{
		BlockNumber:  "25586367",
		OriginChain:  "ethereum",
		ReceiptsRoot: "0xroot",
	})
	require.NoError(t, err, "permanent ante-shaped reject must skip (advance), not pause drain")
	mockClient.AssertExpectations(t)
	mockClient.AssertNotCalled(t, "BridgeTransactionsByReceipt", mock.Anything, mock.Anything, mock.Anything, mock.Anything)
}

func TestCommitReceipt_UncertainRejectPausesDrain(t *testing.T) {
	mockClient := &cosmosclient.MockCosmosMessageClient{}
	mockClient.On("GetAccountAddress").Return("gonka1validator")
	mockClient.On("BridgeExchange", mock.AnythingOfType("*types.MsgBridgeExchange")).Return(
		fmt.Errorf("bridge exchange failed: code=11 rawLog=out of gas"),
	)

	q := NewBlockQueue(mockClient, nil)
	err := q.commitReceipt(context.Background(), BridgeReceipt{
		ContractAddress: "0xabc",
		OwnerAddress:    "ignored",
		OwnerPubKey:     "034daac7b5be60532b562b030824b2c9b7c450985b894dd5544f9c7dd807d8bb88",
		Amount:          "100",
		ReceiptIndex:    "0",
	}, BridgeBlock{
		BlockNumber:  "25586367",
		OriginChain:  "ethereum",
		ReceiptsRoot: "0xroot",
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "not confirmable")
}
