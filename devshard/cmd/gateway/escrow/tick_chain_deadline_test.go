package escrow

import (
	"context"
	"errors"
	"testing"
	"time"

	"devshard/cmd/gateway/chain"
	"devshard/cmd/gateway/config"
	"devshard/cmd/gateway/store"
)

type unansweredCall struct {
	budget      time.Duration
	hasDeadline bool
}

type unansweredTransport struct {
	entered  chan unansweredCall
	returned chan error
}

func newUnansweredTransport() *unansweredTransport {
	return &unansweredTransport{entered: make(chan unansweredCall, 16), returned: make(chan error, 16)}
}

func (u *unansweredTransport) neverAnswer(ctx context.Context) error {
	deadline, hasDeadline := ctx.Deadline()
	select {
	case u.entered <- unansweredCall{budget: time.Until(deadline), hasDeadline: hasDeadline}:
	default:
	}
	<-ctx.Done()
	select {
	case u.returned <- ctx.Err():
	default:
	}
	return ctx.Err()
}

func (u *unansweredTransport) ChainID(ctx context.Context) (string, error) {
	return "", u.neverAnswer(ctx)
}

func (u *unansweredTransport) Account(ctx context.Context, _ string) (chain.Account, error) {
	return chain.Account{}, u.neverAnswer(ctx)
}

func (u *unansweredTransport) SpendableBalance(ctx context.Context, _, _ string) (uint64, error) {
	return 0, u.neverAnswer(ctx)
}

func (u *unansweredTransport) Broadcast(ctx context.Context, _ []byte) (string, error) {
	return "", u.neverAnswer(ctx)
}

func (u *unansweredTransport) Tx(ctx context.Context, _ string) (chain.TxResult, bool, error) {
	return chain.TxResult{}, false, u.neverAnswer(ctx)
}

func (u *unansweredTransport) Escrow(ctx context.Context, _ uint64) (chain.EscrowInfo, bool, error) {
	return chain.EscrowInfo{}, false, u.neverAnswer(ctx)
}

type signallingSweeper struct {
	swept chan struct{}
}

func (s *signallingSweeper) SweepExecutionTimeouts(context.Context, time.Duration, int) (int, int, int) {
	select {
	case s.swept <- struct{}{}:
	default:
	}
	return 0, 0, 0
}

func (s *signallingSweeper) SweepBeforeFinalize(context.Context, string, int) (int, int, int, error) {
	return 0, 0, 0, nil
}

// Test flow:
//  1. Store one commitment and build a real `chain.TxClient` with a short call timeout over a transport that records each call's deadline and then never answers.
//  2. Start the manager on a context with no deadline, as the gateway's boot does, and wait for the first tick to reach the transport through `GetTxEscrowID`.
//  3. Assert the chain call's context carries a deadline no further away than the call timeout.
//  4. Assert the call ended with `context.DeadlineExceeded` while the manager was still running.
//  5. Assert the execution-timeout sweep then runs, although the chain still never answers.
func TestATickHeldByAChainCallThatNeverAnswersMovesOnAtTheCallDeadline(t *testing.T) {
	const (
		waitBound   = 5 * time.Second
		callTimeout = 50 * time.Millisecond
	)
	testStore := newFakeStore()
	testStore.commitments["TX-HUNG"] = store.Commitment{TxHash: "TX-HUNG", Model: "model-a", Role: store.RoleRegular, CreatedAt: time.Now()}
	transport := newUnansweredTransport()
	txClient, err := chain.NewTxClient(chain.Config{Transport: transport, CallTimeout: callTimeout})
	if err != nil {
		t.Fatalf("NewTxClient: %v", err)
	}
	configuration := config.Defaults()
	sweeper := &signallingSweeper{swept: make(chan struct{}, 1)}
	deps := testManagerDeps(t, testStore, &fakeTxClient{}, &fakeSnapshotSource{}, &configuration)
	deps.Tx = txClient
	deps.Timeouts = sweeper
	manager := mustManager(t, deps)

	manager.Start(context.Background())
	defer manager.Stop()
	var call unansweredCall
	select {
	case call = <-transport.entered:
	case <-time.After(waitBound):
		t.Fatal("the first tick never reached the chain transport for the stored commitment")
	}
	if !call.hasDeadline {
		t.Fatal("the chain call got a context with no deadline, so only shutdown could end it")
	}
	if call.budget > callTimeout {
		t.Fatalf("the chain call's deadline is %s away, want at most the call timeout %s", call.budget, callTimeout)
	}

	select {
	case err := <-transport.returned:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("the chain call ended with %v, want context.DeadlineExceeded from its own deadline", err)
		}
	case <-time.After(waitBound):
		t.Fatal("the chain call did not end at its deadline")
	}
	select {
	case <-sweeper.swept:
	case <-time.After(waitBound):
		t.Fatal("the execution-timeout sweep never ran after the chain call hit its deadline")
	}
}
