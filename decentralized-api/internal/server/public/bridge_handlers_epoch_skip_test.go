package public

import (
	"errors"
	"fmt"
	"testing"
)

func TestClassifyBridgeExchangeSubmit(t *testing.T) {
	cases := []struct {
		name       string
		err        error
		wantAction bridgeSubmitAction
		wantReason string
	}{
		{
			name:       "already validated",
			err:        fmt.Errorf("bridge exchange failed: code=5 rawLog=validator has already validated this transaction"),
			wantAction: bridgeSubmitSkip,
			wantReason: bridgeSkipAlreadyValidated,
		},
		{
			name:       "not in tx epoch group",
			err:        fmt.Errorf("bridge exchange failed: code=5 rawLog=validator not in transaction's epoch group"),
			wantAction: bridgeSubmitSkip,
			wantReason: bridgeSkipNotInTxEpoch,
		},
		{
			name:       "not in active participants",
			err:        errors.New("validator not in active participants"),
			wantAction: bridgeSubmitSkip,
			wantReason: bridgeSkipNotActive,
		},
		{
			name:       "permission participant is not active",
			err:        fmt.Errorf("bridge exchange failed: code=1109 rawLog=participant is not active"),
			wantAction: bridgeSubmitSkip,
			wantReason: bridgeSkipNotActive,
		},
		{
			name:       "content mismatch",
			err:        errors.New("transaction content mismatch - potential attack detected"),
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
