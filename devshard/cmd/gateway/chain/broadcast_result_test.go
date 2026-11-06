package chain

import (
	"errors"
	"testing"

	sdk "github.com/cosmos/cosmos-sdk/types"
)

// Test flow:
//  1. For each table case's broadcast response (accepted, refused at CheckTx in the sdk or another codespace, already in the mempool, empty, or missing its hash), call `broadcastTxHash`.
//  2. Assert it returns the hash for an accepted transaction and ErrTxRejected exactly for a refusal that means the transaction entered no mempool.
func TestABroadcastRefusedAtCheckTxIsTypedAsRejected(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		response *sdk.TxResponse
		hash     string
		rejected bool
		failed   bool
	}{
		{name: "accepted", response: &sdk.TxResponse{TxHash: " ABC "}, hash: "ABC"},
		{name: "mempool_full", response: &sdk.TxResponse{Code: 20, Codespace: "sdk", RawLog: "mempool is full"}, rejected: true, failed: true},
		{name: "insufficient_fee", response: &sdk.TxResponse{Code: 13, Codespace: "sdk"}, rejected: true, failed: true},
		{name: "module_codespace", response: &sdk.TxResponse{Code: 19, Codespace: "inference"}, rejected: true, failed: true},
		{name: "already_in_mempool", response: &sdk.TxResponse{Code: 19, Codespace: "sdk", TxHash: "ABC"}, failed: true},
		{name: "no_result", failed: true},
		{name: "no_hash", response: &sdk.TxResponse{}, failed: true},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			hash, err := broadcastTxHash(testCase.response)
			if (err != nil) != testCase.failed || hash != testCase.hash {
				t.Fatalf("broadcastTxHash() = %q, %v; want %q, failed %v", hash, err, testCase.hash, testCase.failed)
			}
			if errors.Is(err, ErrTxRejected) != testCase.rejected {
				t.Fatalf("broadcastTxHash() error %v: rejected %v, want %v", err, !testCase.rejected, testCase.rejected)
			}
		})
	}
}
