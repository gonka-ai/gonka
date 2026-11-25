package chain

import (
	"context"
	"errors"
	"testing"
	"time"
)

type hangingTransport struct {
	*fakeTransport
	hangs   string
	budgets chan time.Duration
}

func (h *hangingTransport) hang(ctx context.Context, method string) error {
	if method != h.hangs {
		return nil
	}
	deadline, hasDeadline := ctx.Deadline()
	if !hasDeadline {
		h.budgets <- 0
		return errors.New(method + " got a context with no deadline")
	}
	h.budgets <- time.Until(deadline)
	<-ctx.Done()
	return ctx.Err()
}

func (h *hangingTransport) ChainID(ctx context.Context) (string, error) {
	if err := h.hang(ctx, "ChainID"); err != nil {
		return "", err
	}
	return h.fakeTransport.ChainID(ctx)
}

func (h *hangingTransport) Account(ctx context.Context, address string) (Account, error) {
	if err := h.hang(ctx, "Account"); err != nil {
		return Account{}, err
	}
	return h.fakeTransport.Account(ctx, address)
}

func (h *hangingTransport) SpendableBalance(ctx context.Context, address, denom string) (uint64, error) {
	if err := h.hang(ctx, "SpendableBalance"); err != nil {
		return 0, err
	}
	return h.fakeTransport.SpendableBalance(ctx, address, denom)
}

func (h *hangingTransport) Broadcast(ctx context.Context, txBytes []byte) (string, error) {
	if err := h.hang(ctx, "Broadcast"); err != nil {
		return "", err
	}
	return h.fakeTransport.Broadcast(ctx, txBytes)
}

func (h *hangingTransport) Tx(ctx context.Context, txHash string) (TxResult, bool, error) {
	if err := h.hang(ctx, "Tx"); err != nil {
		return TxResult{}, false, err
	}
	return h.fakeTransport.Tx(ctx, txHash)
}

func (h *hangingTransport) Escrow(ctx context.Context, escrowID uint64) (EscrowInfo, bool, error) {
	if err := h.hang(ctx, "Escrow"); err != nil {
		return EscrowInfo{}, false, err
	}
	return h.fakeTransport.Escrow(ctx, escrowID)
}

// Test flow:
//  1. For each transport method, build a `TxClient` with a short call timeout over a transport that answers every method but that one, which records its context's deadline and then never answers.
//  2. Call the `TxClient` operation that reaches the hanging method on a context with no deadline of its own: `SettleEscrow` for ChainID, Account, Broadcast and the commit wait's Tx, `CreateEscrow` for SpendableBalance, `GetTxEscrowID` and `TxCommitted` for Tx, `GetEscrow` for Escrow.
//  3. Assert the hanging call's context carried a deadline no further away than the call timeout.
//  4. Assert the operation returns an error wrapping `context.DeadlineExceeded`.
func TestEveryTransportCallEndsAtTheCallTimeout(t *testing.T) {
	const callTimeout = 20 * time.Millisecond
	signer := fixedSigner(t)
	testCases := []struct {
		name  string
		hangs string
		call  func(ctx context.Context, client *TxClient) error
	}{
		{name: "settle reading the chain id", hangs: "ChainID", call: func(ctx context.Context, client *TxClient) error {
			_, err := client.SettleEscrow(ctx, signer, fixedSettlementFull(), nil)
			return err
		}},
		{name: "settle reading the account", hangs: "Account", call: func(ctx context.Context, client *TxClient) error {
			_, err := client.SettleEscrow(ctx, signer, fixedSettlementFull(), nil)
			return err
		}},
		{name: "settle broadcasting", hangs: "Broadcast", call: func(ctx context.Context, client *TxClient) error {
			_, err := client.SettleEscrow(ctx, signer, fixedSettlementFull(), nil)
			return err
		}},
		{name: "settle awaiting its commit", hangs: "Tx", call: func(ctx context.Context, client *TxClient) error {
			_, err := client.SettleEscrow(ctx, signer, fixedSettlementFull(), nil)
			return err
		}},
		{name: "create reading the spendable balance", hangs: "SpendableBalance", call: func(ctx context.Context, client *TxClient) error {
			_, err := client.CreateEscrow(ctx, signer, 1_000, "model-a", nil)
			return err
		}},
		{name: "recovering an escrow id", hangs: "Tx", call: func(ctx context.Context, client *TxClient) error {
			_, _, err := client.GetTxEscrowID(ctx, "TX-HUNG")
			return err
		}},
		{name: "checking a commit", hangs: "Tx", call: func(ctx context.Context, client *TxClient) error {
			_, err := client.TxCommitted(ctx, "TX-HUNG")
			return err
		}},
		{name: "reading an escrow", hangs: "Escrow", call: func(ctx context.Context, client *TxClient) error {
			_, _, err := client.GetEscrow(ctx, "7")
			return err
		}},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			transport := &hangingTransport{fakeTransport: newFakeTransport(), hangs: testCase.hangs, budgets: make(chan time.Duration, 64)}
			client, err := NewTxClient(Config{Transport: transport, PollInterval: time.Millisecond, PollTimeout: 2 * callTimeout, CallTimeout: callTimeout})
			if err != nil {
				t.Fatalf("NewTxClient: %v", err)
			}
			err = testCase.call(context.Background(), client)
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("error = %v, want one wrapping context.DeadlineExceeded from the call timeout", err)
			}
			select {
			case budget := <-transport.budgets:
				if budget > callTimeout {
					t.Fatalf("%s deadline was %s away, want at most the call timeout %s", testCase.hangs, budget, callTimeout)
				}
			default:
				t.Fatalf("%s was never called", testCase.hangs)
			}
		})
	}
}
