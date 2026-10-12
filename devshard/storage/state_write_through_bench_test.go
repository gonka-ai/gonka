package storage

import (
	"crypto/rand"
	"fmt"
	"slices"
	"testing"
	"time"

	"devshard/types"
)

const (
	writeThroughLiveRecords = 60_000
	writeThroughRecordBytes = 200
	writeThroughDiffBytes   = 450
	writeThroughHeaderBytes = 1_500
)

func randomBytes(b *testing.B, size int) []byte {
	b.Helper()
	data := make([]byte, size)
	if _, err := rand.Read(data); err != nil {
		b.Fatal(err)
	}
	return data
}

// BenchmarkAppendDiffWithSessionState measures the per-diff transaction alone against the same
// transaction carrying the state the diff leaves behind, on a store already holding the live set.
func BenchmarkAppendDiffWithSessionState(b *testing.B) {
	for _, touched := range []int{-1, 3, 6} {
		name := fmt.Sprintf("touched=%d", touched)
		if touched < 0 {
			name = "no-state"
		}
		b.Run(name, func(b *testing.B) {
			store := benchSQLite(b)
			escrowID := defaultParams().EscrowID
			record := randomBytes(b, writeThroughRecordBytes)
			header := randomBytes(b, writeThroughHeaderBytes)
			txs := []*types.DevshardTx{{Tx: &types.DevshardTx_RevealSeed{RevealSeed: &types.MsgRevealSeed{Signature: randomBytes(b, writeThroughDiffBytes)}}}}
			liveSet := make(map[uint64][]byte, writeThroughLiveRecords)
			for id := range writeThroughLiveRecords {
				liveSet[uint64(id+1)] = record
			}
			baseline := types.DiffRecord{Diff: types.Diff{Nonce: 1, Txs: txs, UserSig: header[:65]}, StateHash: header[:32],
				SessionState: &types.SessionStateDelta{Header: header, Upserts: liveSet, ReplaceAll: true}}
			if err := store.AppendDiff(escrowID, baseline); err != nil {
				b.Fatal(err)
			}
			durations := make([]time.Duration, 0, b.N)
			b.ResetTimer()
			for iteration := range b.N {
				nonce := uint64(iteration + 2)
				rec := types.DiffRecord{Diff: types.Diff{Nonce: nonce, Txs: txs, UserSig: header[:65]}, StateHash: header[:32]}
				if touched >= 0 {
					upserts := make(map[uint64][]byte, touched)
					for offset := range touched {
						upserts[writeThroughLiveRecords+nonce-uint64(offset*97)] = record
					}
					rec.SessionState = &types.SessionStateDelta{Header: header, Upserts: upserts}
				}
				started := time.Now()
				if err := store.AppendDiff(escrowID, rec); err != nil {
					b.Fatal(err)
				}
				durations = append(durations, time.Since(started))
			}
			b.StopTimer()
			slices.Sort(durations)
			b.ReportMetric(float64(durations[len(durations)*99/100].Microseconds()), "p99-µs")
			b.ReportMetric(float64(durations[len(durations)-1].Microseconds()), "max-µs")
		})
	}
}
