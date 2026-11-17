package escrow

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"devshard/cmd/gateway/scheduler"
	"devshard/cmd/gateway/store"
	"devshard/types"
)

// Test flow:
//  1. Build an on-mode manager and report escrow 5 at its balance floor and escrow 6 at its nonce cap.
//  2. Run one tick.
//  3. Assert escrow 6 was parked, escrow 5 still serves, the balance mark is counted as ignored, and only the nonce cap was narrated as a mark.
func TestANonceCapMarkRetiresAndABalanceMarkIsCounted(t *testing.T) {
	manager, testStore, _, narrator, _ := onManager(t, onModels(1, 0))
	manager.OnBalanceExhausted("5", scheduler.ExhaustionBalanceFloor)
	manager.OnBalanceExhausted("6", scheduler.ExhaustionNonceCap)

	require.NoError(t, manager.tick(context.Background()))

	if row := testStore.devshards["6"]; row.Active || !row.SettlementPending {
		t.Fatalf("row 6 = %+v, want parked at its nonce cap", row)
	}
	require.True(t, testStore.devshards["5"].Active, "the escrow marked at its balance floor still serves")
	reports := manager.FundingReports()
	require.Len(t, reports, 1)
	require.Equal(t, uint64(1), reports[0].IgnoredMarks[string(scheduler.ExhaustionBalanceFloor)], "ignored balance-floor marks")
	marks := slices.DeleteFunc(narrator.recorded(), func(line string) bool { return !strings.HasPrefix(line, "marked for replacement") })
	require.Equal(t, []string{"marked for replacement 6: nonce_cap"}, marks)
}

// Test flow:
//  1. Table-driven: a temp labelled with the current epoch beside two current regulars; the same temp beside two regulars of the previous epoch; a temp labelled one epoch ahead beside two current regulars.
//  2. Plan once.
//  3. Assert the current temp is retired only once the spread is met, and narrated as the bridge finished, and a temp labelled ahead is never retired by the finish rule.
func TestThePlannerRetiresOldTempsOnlyOnceTheSpreadIsMet(t *testing.T) {
	for _, testCase := range []struct {
		name        string
		temp        int64
		regulars    int64
		wantRetired bool
		wantCreates int
	}{
		{name: "current temp, spread met", temp: 7, regulars: 7, wantRetired: true},
		{name: "current temp, spread short", temp: 7, regulars: 6, wantCreates: 1},
		{name: "temp labelled ahead, spread met", temp: 8, regulars: 7},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			manager, testStore, txClient, narrator, _ := onManager(t, onModels(1, 0))
			for _, escrowID := range []string{"5", "6"} {
				row := testStore.devshards[escrowID]
				row.RotationEpoch = testCase.regulars
				testStore.devshards[escrowID] = row
			}
			testStore.devshards["9"] = store.DevshardRecord{EscrowID: "9", Model: "qwen", Active: true, RotationRole: roleTemp, RotationEpoch: testCase.temp, PrivateKeyEnv: "K"}
			manager.funds.(fakeFunds)["9"] = EscrowMoney{Config: types.SessionConfig{RefusalTimeout: 60, ExecutionTimeout: 1800}, Balance: 1_000_000, TokenPrice: 1, FeePerNonce: 10}

			require.NoError(t, manager.planFunding(context.Background(), true))

			require.Equal(t, testCase.wantRetired, !testStore.devshards["9"].Active, "temp 9 retired")
			require.Equal(t, testCase.wantCreates, txClient.createCalls, "CreateEscrow calls")
			if testCase.wantRetired {
				require.Contains(t, narrator.recorded(), "bridge finished qwen epoch 7: created 0 retired 1")
			}
		})
	}
}

// Test flow:
//  1. Build an on-mode manager and report its model money-short a thousand times.
//  2. Assert one wakeup is queued and the model is marked.
//  3. Take the wakeup, report a thousand more times with the mark still undrained, and assert nothing more is queued.
//  4. Drain the mark, report once, and assert one wakeup is queued again.
func TestAThousandMoneyShortSignalsWakeTheTickOnce(t *testing.T) {
	manager, _, _, _, _ := onManager(t, onModels(1, 0))

	for range 1000 {
		manager.OnMoneyShort("qwen")
	}
	require.Len(t, manager.wakeup, 1, "wakeups after a thousand signals")
	<-manager.wakeup
	for range 1000 {
		manager.OnMoneyShort("qwen")
	}
	require.Empty(t, manager.wakeup, "wakeups while the mark is undrained")
	require.True(t, manager.moneyShort.drain()["qwen"], "the model is marked")
	manager.OnMoneyShort("qwen")

	require.Len(t, manager.wakeup, 1, "wakeups after the mark was drained")
}
