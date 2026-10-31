package escrow

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"devshard/cmd/gateway/chain"
	"devshard/cmd/gateway/store"
	"devshard/signing"
)

const checkTxRejectedSettleHash = "SETTLE-REJECTED"

var (
	errCheckTxRejected = fmt.Errorf("%w: code=20 codespace=sdk raw_log=mempool is full", chain.ErrTxRejected)
	errBroadcastLost   = errors.New("broadcast tx: connection reset")
)

func broadcastFailingManager(t *testing.T, record store.DevshardRecord, broadcastErr error) (*Manager, *fakeStore, *int) {
	t.Helper()
	testStore := newFakeStore()
	testStore.devshards[record.EscrowID] = record
	broadcasts := 0
	txClient := &fakeTxClient{
		settleTxHash: checkTxRejectedSettleHash,
		settleEscrowFn: func(context.Context, *signing.Secp256k1Signer, chain.SettlementInput) (chain.SettleEscrowResult, error) {
			broadcasts++
			return chain.SettleEscrowResult{}, broadcastErr
		},
	}
	return &Manager{
		tx:               txClient,
		store:            testStore,
		signer:           &fakeSignerSource{signer: testSigner(t)},
		settlementSource: &fakeSettlementSource{buildInput: chain.SettlementInput{EscrowID: 7}},
		now:              time.Now,
	}, testStore, &broadcasts
}

// Test flow:
//  1. Build a manager whose settle broadcast the node rejects at CheckTx, after the hash was recorded.
//  2. Settle the parked escrow once and assert the error carries ErrTxRejected.
//  3. Assert the row no longer carries the settle hash.
//  4. Settle again at once and assert it broadcasts a second time instead of answering ErrSettlementInFlight.
func TestASettleRejectedAtCheckTxIsRetriedOnTheNextTick(t *testing.T) {
	record := parkedRecord("7")
	manager, testStore, broadcasts := broadcastFailingManager(t, record, errCheckTxRejected)

	_, err := manager.settle(context.Background(), record, false)

	if !errors.Is(err, chain.ErrTxRejected) {
		t.Fatalf("settle() = %v, want ErrTxRejected", err)
	}
	if hash := testStore.devshards[record.EscrowID].SettleTxHash; hash != "" {
		t.Errorf("settle hash after a CheckTx rejection = %q, want it cleared", hash)
	}
	_, err = manager.settle(context.Background(), record, false)
	if errors.Is(err, ErrSettlementInFlight) {
		t.Fatalf("second settle() = %v, want a fresh broadcast, not ErrSettlementInFlight", err)
	}
	if *broadcasts != 2 {
		t.Errorf("broadcasts = %d, want 2", *broadcasts)
	}
}

// Test flow:
//  1. Build a manager whose settle broadcast fails in transport, after the hash was recorded.
//  2. Settle the parked escrow once.
//  3. Assert the row keeps the settle hash.
//  4. Settle again at once and assert it answers ErrSettlementInFlight without a second broadcast.
func TestASettleLostInTransportStaysInFlight(t *testing.T) {
	record := parkedRecord("7")
	manager, testStore, broadcasts := broadcastFailingManager(t, record, errBroadcastLost)

	if _, err := manager.settle(context.Background(), record, false); err == nil {
		t.Fatal("settle() = nil, want the transport error")
	}

	if hash := testStore.devshards[record.EscrowID].SettleTxHash; hash != checkTxRejectedSettleHash {
		t.Errorf("settle hash after a transport error = %q, want %q kept", hash, checkTxRejectedSettleHash)
	}
	if _, err := manager.settle(context.Background(), record, false); !errors.Is(err, ErrSettlementInFlight) {
		t.Fatalf("second settle() = %v, want ErrSettlementInFlight", err)
	}
	if *broadcasts != 1 {
		t.Errorf("broadcasts = %d, want 1", *broadcasts)
	}
}

// Test flow:
//  1. For each case, build a manager whose create broadcast fails after the commitment was saved: rejected at CheckTx, or lost in transport.
//  2. Call createFor and assert it fails.
//  3. Assert a CheckTx rejection leaves no commitment, and a transport error keeps it.
func TestACreateRejectedAtCheckTxDropsItsCommitment(t *testing.T) {
	cases := []struct {
		name            string
		broadcastErr    error
		keepsCommitment bool
	}{
		{name: "rejected_at_check_tx", broadcastErr: errCheckTxRejected},
		{name: "lost_in_transport", broadcastErr: errBroadcastLost, keepsCommitment: true},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			testStore := newFakeStore()
			m := &Manager{
				tx: &fakeTxClient{
					createEscrowFn: func(_ context.Context, _ *signing.Secp256k1Signer, _ uint64, _ string, onPrepared func(string) error) (chain.CreateEscrowResult, error) {
						if err := onPrepared("TX-CREATE"); err != nil {
							return chain.CreateEscrowResult{}, err
						}
						return chain.CreateEscrowResult{}, testCase.broadcastErr
					},
				},
				store:   testStore,
				signer:  &fakeSignerSource{signer: testSigner(t)},
				breaker: newCreateBreaker(),
				now:     time.Now,
			}
			model := ModelConfig{ModelID: "model-a", Amount: 1000, PrivateKeyEnv: "MODEL_A_KEY"}

			if _, err := m.createFor(context.Background(), model, roleRegular, 5, string(createdByOperator), chain.PhaseSnapshot{EpochIndex: 5, BlockHeight: 100}); err == nil {
				t.Fatal("createFor() = nil, want the broadcast error")
			}

			commitments, _ := testStore.LoadCommitments(context.Background())
			if kept := len(commitments) == 1; kept != testCase.keepsCommitment {
				t.Errorf("commitments = %+v, want kept %v", commitments, testCase.keepsCommitment)
			}
		})
	}
}
