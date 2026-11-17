package nonces

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"devshard/cmd/gateway/accounting"
)

func newEpochlessLedger(t *testing.T) *Recorder {
	t.Helper()
	service, err := accounting.NewService(accounting.Settings{
		Now: func() time.Time { return time.Unix(0, 0).UTC() },
	})
	if err != nil {
		t.Fatalf("NewService(): %v", err)
	}
	ledger := recorderFor(service)
	t.Cleanup(func() { _ = ledger.service.Close() })
	return ledger
}

// Test flow:
//  1. Wire a creation-epoch resolver that answers epoch 100 for any escrow.
//  2. Look up the epoch of an unstamped escrow.
//  3. Assert it resolves to epoch 100, known.
func TestAnEscrowIsStampedWithTheEpochItWasCreatedIn(t *testing.T) {
	t.Parallel()
	ledger := newEpochlessLedger(t)
	ledger.SetCreationEpoch(func(context.Context, string) (uint64, bool) { return 100, true })

	epoch, known := ledger.epochOf(context.Background(), "escrow-1")

	if !known || epoch != 100 {
		t.Fatalf("epochOf = %d (known %v), want the epoch the chain stamped", epoch, known)
	}
}

// Test flow:
//  1. Wire a creation-epoch resolver that counts its own calls and answers epoch 100.
//  2. Look up the same escrow's epoch 5 times.
//  3. Assert every lookup reports known.
//  4. Assert the resolver was called exactly once, so a resolved epoch is cached rather than re-asked.
func TestTheCreationEpochIsResolvedOncePerEscrow(t *testing.T) {
	t.Parallel()
	ledger := newEpochlessLedger(t)
	var calls atomic.Int64
	ledger.SetCreationEpoch(func(context.Context, string) (uint64, bool) {
		calls.Add(1)
		return 100, true
	})

	for range 5 {
		if _, known := ledger.epochOf(context.Background(), "escrow-1"); !known {
			t.Fatal("the resolver stopped answering for an escrow it had already resolved")
		}
	}

	if got := calls.Load(); got != 1 {
		t.Fatalf("resolver calls = %d, want 1", got)
	}
}

// Test flow:
//  1. Define a table of resolver behaviors, varying across no resolver wired at all, a resolver that refuses, and a resolver that answers a zero epoch.
//  2. For each case, wire the resolver where one is given and look up the escrow's epoch.
//  3. Assert every case reports unknown with a zero epoch, never a guessed stamp.
func TestAnEscrowWhoseEpochIsUnknownIsNotStampedAtAll(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name    string
		resolve CreationEpochFunc
	}{
		{name: "no resolver is wired yet"},
		{
			name:    "the chain and the local record both refuse",
			resolve: func(context.Context, string) (uint64, bool) { return 0, false },
		},
		{
			name:    "the resolver answers a zero epoch",
			resolve: func(context.Context, string) (uint64, bool) { return 0, true },
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			ledger := newEpochlessLedger(t)
			if testCase.resolve != nil {
				ledger.SetCreationEpoch(testCase.resolve)
			}

			epoch, known := ledger.epochOf(context.Background(), "escrow-1")

			if known || epoch != 0 {
				t.Fatalf("epochOf = %d (known %v), want no stamp at all", epoch, known)
			}
		})
	}
}

// Test flow:
//  1. Wire a resolver that refuses on its first call and answers epoch 100 on its second.
//  2. Look up the escrow's epoch once and assert it comes back unknown.
//  3. Look up the same escrow's epoch again.
//  4. Assert it now resolves to epoch 100, known, so a refusal is never cached as an answer.
func TestARefusedEpochIsAskedAgain(t *testing.T) {
	t.Parallel()
	ledger := newEpochlessLedger(t)
	var answered atomic.Bool
	ledger.SetCreationEpoch(func(context.Context, string) (uint64, bool) {
		if !answered.Swap(true) {
			return 0, false
		}
		return 100, true
	})

	if _, known := ledger.epochOf(context.Background(), "escrow-1"); known {
		t.Fatal("a refusal was answered as a known epoch")
	}

	epoch, known := ledger.epochOf(context.Background(), "escrow-1")

	if !known || epoch != 100 {
		t.Fatalf("epochOf = %d (known %v), want the epoch the chain answered on the second ask", epoch, known)
	}
}
