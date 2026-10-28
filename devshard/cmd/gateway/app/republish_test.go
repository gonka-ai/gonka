package app

import (
	"context"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"devshard/cmd/gateway/registry"
	"devshard/cmd/gateway/store"
)

// Test flow:
//  1. Start the republish loop with one escrow left unopened at boot and no devshard write to come.
//  2. Let the retry delay pass twice: the first retry still fails, the second opens the escrow.
//  3. Assert the loop retried on its own until the escrow opened, and stopped retrying after it did.
func TestRepublishRetriesAnUnopenedEscrowUntilItOpens(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		var publishes atomic.Int32
		publish := func(context.Context) (int, error) {
			if publishes.Add(1) == 1 {
				return 1, nil
			}
			return 0, nil
		}

		done := republishEscrows(ctx, make(chan struct{}), 1, publish, nil)
		time.Sleep(unopenedEscrowRetryDelay)
		synctest.Wait()
		afterFirstRetry := publishes.Load()
		time.Sleep(unopenedEscrowRetryDelay)
		synctest.Wait()
		afterSecondRetry := publishes.Load()
		time.Sleep(10 * unopenedEscrowRetryDelay)
		synctest.Wait()
		cancel()
		<-done

		if afterFirstRetry != 1 || afterSecondRetry != 2 {
			t.Fatalf("publishes after each retry delay = %d, %d; want 1, 2", afterFirstRetry, afterSecondRetry)
		}
		if got := publishes.Load(); got != 2 {
			t.Fatalf("publishes once every escrow opened = %d, want 2", got)
		}
	})
}

// Test flow:
//  1. Start the republish loop with every escrow open at boot.
//  2. Let time pass with no write, then signal one devshard write.
//  3. Assert nothing republished on a timer and the write republished once.
func TestRepublishWaitsForAWriteWhenEveryEscrowOpened(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		var publishes atomic.Int32
		publish := func(context.Context) (int, error) {
			publishes.Add(1)
			return 0, nil
		}
		writes := make(chan struct{}, 1)

		done := republishEscrows(ctx, writes, 0, publish, nil)
		time.Sleep(10 * unopenedEscrowRetryDelay)
		synctest.Wait()
		withoutWrite := publishes.Load()
		writes <- struct{}{}
		synctest.Wait()
		cancel()
		<-done

		if withoutWrite != 0 {
			t.Fatalf("publishes with no write and nothing unopened = %d, want 0", withoutWrite)
		}
		if got := publishes.Load(); got != 1 {
			t.Fatalf("publishes after one write = %d, want 1", got)
		}
	})
}

// Test flow:
//  1. Store two active rows and build a gateway whose session factory parks both escrows while the first one opens, as the manager does: row first, then retire.
//  2. Run one publish pass, which lists both rows active before either park.
//  3. Assert neither parked escrow routes and both rows stay parked for settlement.
func TestAPublishPassNeverReopensAnEscrowParkedAfterItListedTheRows(t *testing.T) {
	ctx := context.Background()
	records := openedStore(t)
	for _, escrowID := range []string{"escrow-1", "escrow-2"} {
		if err := records.UpsertDevshard(ctx, store.DevshardRecord{EscrowID: escrowID, Model: "model-a", PrivateKeyEnv: "DEVSHARD_PARK_KEY", Active: true}); err != nil {
			t.Fatalf("UpsertDevshard(%s): %v", escrowID, err)
		}
	}
	var escrows *registry.Registry
	park := func(escrowID string) {
		if err := records.ParkForSettlement(ctx, escrowID); err != nil {
			t.Errorf("ParkForSettlement(%s): %v", escrowID, err)
		}
		if err := escrows.Retire(escrowID); err != nil {
			t.Errorf("Retire(%s): %v", escrowID, err)
		}
	}
	escrows = registry.New(registry.Deps{
		ServingSessions: func(_ context.Context, escrowID string) (registry.EscrowSession, error) {
			if escrowID == "escrow-1" {
				park("escrow-2")
				park("escrow-1")
			}
			return weightlessSession{participants: []string{"validator-a"}}, nil
		},
		Now: time.Now,
	})
	t.Cleanup(func() { escrows.Close() })
	composed := &gateway{store: records, escrows: escrows, builders: 1}

	if _, err := composed.publishEscrows(ctx); err != nil {
		t.Fatalf("publishEscrows() = %v, want nil", err)
	}

	for _, escrowID := range []string{"escrow-1", "escrow-2"} {
		if _, routable := escrows.Routable(escrowID); routable {
			t.Errorf("escrow %s routes after it was parked for settlement", escrowID)
		}
		if record := storedDevshard(t, records, escrowID); record.Active || !record.SettlementPending {
			t.Errorf("row %s = active %v, pending %v, want parked", escrowID, record.Active, record.SettlementPending)
		}
	}
}

// Test flow:
//  1. Wrap a store holding one active row in devshardWrites counting the writes it reports.
//  2. Park the row only if active, again only if active, then unconditionally.
//  3. Assert the first and the last park report a write and the park that matched no active row does not.
func TestAParkReportsADevshardWrite(t *testing.T) {
	ctx := context.Background()
	records := openedStore(t)
	if err := records.UpsertDevshard(ctx, store.DevshardRecord{EscrowID: "escrow-1", Model: "model-a", PrivateKeyEnv: "DEVSHARD_PARK_KEY", Active: true}); err != nil {
		t.Fatalf("UpsertDevshard(): %v", err)
	}
	reported := 0
	writes := devshardWrites{Store: records, changed: func() { reported++ }}

	for attempt, want := range []bool{true, false} {
		parked, err := writes.ParkForSettlementIfActive(ctx, "escrow-1")
		if err != nil || parked != want {
			t.Fatalf("ParkForSettlementIfActive() #%d = %v, %v, want %v, nil", attempt+1, parked, err, want)
		}
	}
	if reported != 1 {
		t.Fatalf("writes reported after two conditional parks = %d, want 1", reported)
	}
	if err := writes.ParkForSettlement(ctx, "escrow-1"); err != nil {
		t.Fatalf("ParkForSettlement() = %v", err)
	}
	if reported != 2 {
		t.Fatalf("writes reported after the unconditional park = %d, want 2", reported)
	}
}
