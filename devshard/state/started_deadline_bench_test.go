package state

import (
	"fmt"
	"testing"
	"time"

	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/types"

	"github.com/stretchr/testify/require"
)

func machineWithLiveRecords(b *testing.B, live, due int) (*StateMachine, time.Time) {
	b.Helper()
	hosts := []*signing.Secp256k1Signer{testutil.MustGenerateKey(b), testutil.MustGenerateKey(b)}
	user := testutil.MustGenerateKey(b)
	group := testutil.MakeGroup(hosts)
	configuration := testutil.DefaultConfig(len(hosts))
	verifier := signing.NewSecp256k1Verifier()
	store := testutil.MustMemoryStore(b, "escrow-1", user.Address(), configuration, group, 1_000_000)
	machine, err := NewStateMachine("escrow-1", configuration, group, 1_000_000, user.Address(), verifier, store)
	require.NoError(b, err)

	now := time.Now()
	machine.mu.Lock()
	for id := range uint64(live) {
		record := &types.InferenceRecord{Status: types.StatusStarted, ConfirmedAt: now.Unix()}
		if int(id) < due {
			record.ConfirmedAt = now.Add(-2 * time.Hour).Unix()
		}
		machine.state.Inferences[id+1] = record
	}
	machine.mu.Unlock()
	return machine, now
}

// The sweep scans on a fixed 15-second tick, so its cost must follow the live record count and not the
// request rate. Nothing is due in the ordinary case, and the scan must allocate nothing there.
func BenchmarkStartedInferencesPastDeadline(b *testing.B) {
	for _, live := range []int{1_000, 10_000, 100_000} {
		b.Run(fmt.Sprintf("live=%d/due=0", live), func(b *testing.B) {
			machine, now := machineWithLiveRecords(b, live, 0)
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				_ = machine.StartedInferencesPastDeadline(now, 0, 0, 0, 8)
			}
		})
		b.Run(fmt.Sprintf("live=%d/due=all", live), func(b *testing.B) {
			machine, now := machineWithLiveRecords(b, live, live)
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				_ = machine.StartedInferencesPastDeadline(now, 0, 0, 0, 8)
			}
		})
	}
}
