package state

import (
	"fmt"
	"testing"

	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/storage"
)

func benchFillSQLite(b *testing.B, escrowID string) (*StateMachine, *storage.SQLite) {
	b.Helper()
	store, err := storage.NewSQLite(b.TempDir())
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = store.Close() })
	return benchFillSM(b, store, escrowID), store
}

func benchFillSM(b *testing.B, store storage.Storage, escrowID string) *StateMachine {
	b.Helper()
	user, err := signing.GenerateKey()
	if err != nil {
		b.Fatal(err)
	}
	hosts := make([]*signing.Secp256k1Signer, 3)
	for i := range hosts {
		hosts[i], err = signing.GenerateKey()
		if err != nil {
			b.Fatal(err)
		}
	}
	group := testutil.MakeGroup(hosts)
	config := testutil.DefaultConfig(len(hosts))
	if err := store.CreateSession(storage.CreateSessionParams{
		EscrowID:       escrowID,
		EpochID:        1,
		Version:        testutil.RuntimeTestVersion,
		CreatorAddr:    user.Address(),
		Config:         config,
		Group:          group,
		InitialBalance: 100000,
	}); err != nil {
		b.Fatal(err)
	}
	sm, err := NewStateMachine(escrowID, config, group, 100000, user.Address(),
		signing.NewSecp256k1Verifier(), store, WithVersion(testutil.RuntimeTestVersion))
	if err != nil {
		b.Fatal(err)
	}
	return sm
}

func benchSealedNonces(n int) (map[uint64]uint64, []storage.InferenceRow) {
	nonces := make(map[uint64]uint64, n)
	rows := make([]storage.InferenceRow, n)
	for i := range n {
		id := uint64(i + 1)
		nonces[id] = id
		rows[i] = storage.InferenceRow{
			InferenceID: id, SealedNonce: id, ObsPresent: true, SealedModel: "llama",
		}
	}
	return nonces, rows
}

// Snapshot restart: every sealed id already has a row, so Fill writes nothing.
func BenchmarkFillSealedInferenceIndexGaps_NoGaps(b *testing.B) {
	for _, n := range []int{2_000, 20_000} {
		b.Run(fmt.Sprintf("n=%d", n), func(b *testing.B) {
			sm, store := benchFillSQLite(b, "bench-fill")
			nonces, rows := benchSealedNonces(n)
			if err := store.InsertSealedInferences("bench-fill", rows); err != nil {
				b.Fatal(err)
			}
			sm.RestoreSealedNonces(nonces)
			b.ResetTimer()
			for range b.N {
				inserted, err := sm.FillSealedInferenceIndexGaps()
				if err != nil {
					b.Fatal(err)
				}
				if inserted != 0 {
					b.Fatalf("inserted %d, want 0", inserted)
				}
			}
		})
	}
}

// Worst-case gap fill: every sealed id is missing (still no delete).
func BenchmarkFillSealedInferenceIndexGaps_AllMissing(b *testing.B) {
	for _, n := range []int{2_000, 20_000} {
		b.Run(fmt.Sprintf("n=%d", n), func(b *testing.B) {
			sm, store := benchFillSQLite(b, "bench-fill-miss")
			nonces, _ := benchSealedNonces(n)
			sm.RestoreSealedNonces(nonces)
			b.ResetTimer()
			for range b.N {
				b.StopTimer()
				if err := store.DeleteSealedInferences("bench-fill-miss"); err != nil {
					b.Fatal(err)
				}
				b.StartTimer()
				inserted, err := sm.FillSealedInferenceIndexGaps()
				if err != nil {
					b.Fatal(err)
				}
				if inserted != n {
					b.Fatalf("inserted %d, want %d", inserted, n)
				}
			}
		})
	}
}
