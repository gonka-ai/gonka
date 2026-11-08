package storage

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	"devshard/types"
)

func TestSQLite_GetDiffs_SignaturesStayWithNonce(t *testing.T) {
	runGetDiffsSignaturesStayWithNonce(t, newTestSQLite(t))
}

func TestPostgres_GetDiffs_SignaturesStayWithNonce(t *testing.T) {
	runGetDiffsSignaturesStayWithNonce(t, newTestPostgres(t))
}

func runGetDiffsSignaturesStayWithNonce(t *testing.T, store Storage) {
	t.Helper()
	require.NoError(t, store.CreateSession(defaultParams()))

	full := make(map[uint32][]byte, types.MaxGroupSize)
	for slot := uint32(0); slot < types.MaxGroupSize; slot++ {
		full[slot] = []byte{byte(slot)}
	}
	payload := txWithPayload(32)
	require.NoError(t, store.AppendDiff("escrow-1", types.DiffRecord{
		Diff:       types.Diff{Nonce: 1, Txs: payload, UserSig: []byte("sig-1")},
		StateHash:  []byte{0x01},
		Signatures: full,
		CreatedAt:  11,
	}))
	require.NoError(t, store.AppendDiff("escrow-1", types.DiffRecord{
		Diff:      types.Diff{Nonce: 2, Txs: txWithPayload(4), UserSig: []byte("sig-2")},
		StateHash: []byte{0x02},
		CreatedAt: 12,
	}))
	require.NoError(t, store.AppendDiff("escrow-1", types.DiffRecord{
		Diff:       types.Diff{Nonce: 3, UserSig: []byte("sig-3")},
		StateHash:  []byte{0x03},
		Signatures: map[uint32][]byte{0: {0x30}},
		CreatedAt:  13,
	}))

	diffs, err := store.GetDiffs("escrow-1", 1, 3)
	require.NoError(t, err)
	require.Len(t, diffs, 3)

	require.Equal(t, payload[0].GetStartInference().GetPromptHash(), diffs[0].Txs[0].GetStartInference().GetPromptHash())
	require.Equal(t, full, diffs[0].Signatures)
	require.Equal(t, int64(11), diffs[0].CreatedAt)

	require.Nil(t, diffs[1].Signatures)
	require.Equal(t, []byte("sig-2"), diffs[1].UserSig)

	require.Equal(t, map[uint32][]byte{0: {0x30}}, diffs[2].Signatures)

	one, err := store.GetDiffs("escrow-1", 2, 2)
	require.NoError(t, err)
	require.Len(t, one, 1)
	require.Nil(t, one[0].Signatures)

	none, err := store.GetDiffs("escrow-1", 9, 9)
	require.NoError(t, err)
	require.Empty(t, none)
}

// BenchmarkSQLiteGetDiffs measures one GetDiffs page: a single nonce whose
// stored txs are about 1 MiB, with either no signatures or a full group.
// Allocations must stay near the no-signature case. The joined query this
// replaced repeated txs_proto once per signature row.
func BenchmarkSQLiteGetDiffs(b *testing.B) {
	for _, nSig := range []int{0, types.MaxGroupSize} {
		b.Run(fmt.Sprintf("sigs_%d", nSig), func(b *testing.B) {
			store, nbytes := sqliteBenchStore(b, nSig, 1<<20)
			b.SetBytes(int64(nbytes))
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				recs, err := store.GetDiffs("escrow-1", 1, 1)
				if err != nil {
					b.Fatal(err)
				}
				if len(recs) != 1 || len(recs[0].Signatures) != nSig {
					b.Fatalf("got %d diffs and %d sigs", len(recs), len(recs[0].Signatures))
				}
			}
		})
	}
}

func BenchmarkSQLiteGetDiffs8MiB(b *testing.B) {
	for _, nSig := range []int{0, types.MaxGroupSize} {
		b.Run(fmt.Sprintf("sigs_%d", nSig), func(b *testing.B) {
			store, nbytes := sqliteBenchStore(b, nSig, DiffPageMaxBytes)
			b.SetBytes(int64(nbytes))
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				recs, err := store.GetDiffs("escrow-1", 1, 1)
				if err != nil {
					b.Fatal(err)
				}
				if len(recs) != 1 || len(recs[0].Signatures) != nSig {
					b.Fatalf("got %d diffs and %d sigs", len(recs), len(recs[0].Signatures))
				}
			}
		})
	}
}

func sqliteBenchStore(b *testing.B, nSig, payload int) (*SQLite, int) {
	b.Helper()
	store, err := NewSQLite(b.TempDir())
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { store.Close() })
	if err := store.CreateSession(defaultParams()); err != nil {
		b.Fatal(err)
	}
	sigs := make(map[uint32][]byte, nSig)
	for slot := 0; slot < nSig; slot++ {
		sigs[uint32(slot)] = []byte("0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef")
	}
	rec := types.DiffRecord{
		Diff: types.Diff{
			Nonce:   1,
			Txs:     txWithPayload(payload),
			UserSig: []byte("user-sig"),
		},
		StateHash:  []byte{0x01},
		Signatures: sigs,
		CreatedAt:  1,
	}
	if err := store.AppendDiff("escrow-1", rec); err != nil {
		b.Fatal(err)
	}
	sizes, err := store.DiffSizes("escrow-1", 1, 1, 1)
	if err != nil {
		b.Fatal(err)
	}
	if len(sizes) != 1 {
		b.Fatalf("diff sizes: %+v", sizes)
	}
	return store, sizes[0].Bytes
}
