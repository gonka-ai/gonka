package storage

import (
	"bytes"
	"errors"
	"slices"
	"testing"

	"github.com/stretchr/testify/require"

	"devshard/types"
)

// pageCallStore records every GetDiffs window and every DiffSizes read. A
// GetDiffs that returns more than one page fails the test: 64 nonces, or
// 8 MiB of txs_proto, and a single oversized diff is the only exception to
// the byte cap.
type pageCallStore struct {
	diffs     map[uint64]types.DiffRecord
	calls     [][2]uint64
	sizeCalls [][2]uint64
}

func (s *pageCallStore) GetDiffs(_ string, from, to uint64) ([]types.DiffRecord, error) {
	s.calls = append(s.calls, [2]uint64{from, to})
	var out []types.DiffRecord
	var nbytes int
	for n := from; n <= to; n++ {
		rec, ok := s.diffs[n]
		if !ok {
			continue
		}
		out = append(out, rec)
		nbytes += diffTxsProtoSize(rec)
	}
	span := int(to - from + 1)
	if span > DiffPageMaxNonces || (nbytes > DiffPageMaxBytes && len(out) > 1) {
		return nil, errors.New("GetDiffs spans more than one page")
	}
	return out, nil
}

func (s *pageCallStore) DiffSizes(_ string, from, to uint64, limit int) ([]DiffSize, error) {
	s.sizeCalls = append(s.sizeCalls, [2]uint64{from, to})
	nonces := make([]uint64, 0, len(s.diffs))
	for n := range s.diffs {
		if n >= from && n <= to {
			nonces = append(nonces, n)
		}
	}
	slices.Sort(nonces)
	if len(nonces) > limit {
		nonces = nonces[:limit]
	}
	out := make([]DiffSize, len(nonces))
	for i, n := range nonces {
		out[i] = DiffSize{Nonce: n, Bytes: diffTxsProtoSize(s.diffs[n])}
	}
	return out, nil
}

func (s *pageCallStore) put(n uint64, txs []*types.DevshardTx) {
	if s.diffs == nil {
		s.diffs = make(map[uint64]types.DiffRecord)
	}
	s.diffs[n] = types.DiffRecord{Diff: types.Diff{Nonce: n, Txs: txs}}
}

func txWithPayload(n int) []*types.DevshardTx {
	return []*types.DevshardTx{{Tx: &types.DevshardTx_StartInference{StartInference: &types.MsgStartInference{
		PromptHash: bytes.Repeat([]byte{0xab}, n),
	}}}}
}

func TestReadDiffPages_VisitsJournalInOrder(t *testing.T) {
	store := &pageCallStore{}
	const n = 200
	for i := uint64(1); i <= n; i++ {
		store.put(i, nil)
	}

	var got []uint64
	var pageLens []int
	err := ReadDiffPages(store, "escrow", 1, n, func(page []types.DiffRecord) error {
		pageLens = append(pageLens, len(page))
		for _, rec := range page {
			got = append(got, rec.Nonce)
		}
		return nil
	})
	require.NoError(t, err)
	require.Len(t, got, n)
	for i := uint64(1); i <= n; i++ {
		require.Equal(t, i, got[i-1])
	}
	require.Equal(t, []int{64, 64, 64, 8}, pageLens)
	require.Equal(t, [][2]uint64{{1, 64}, {65, 128}, {129, 192}, {193, 200}}, store.calls,
		"one GetDiffs per page, and no nonce is read twice")
	require.Len(t, store.sizeCalls, 4, "one size read per page")
}

func TestReadDiffPages_HoleStopsOnFirstMissingNonce(t *testing.T) {
	store := &pageCallStore{}
	for i := uint64(1); i <= 200; i++ {
		if i == 50 {
			continue
		}
		store.put(i, nil)
	}

	var got []uint64
	err := ReadDiffPages(store, "escrow", 1, 200, func(page []types.DiffRecord) error {
		for _, rec := range page {
			got = append(got, rec.Nonce)
		}
		return nil
	})
	var gap *DiffGapError
	require.ErrorAs(t, err, &gap)
	require.Equal(t, uint64(50), gap.Expected)
	require.Equal(t, uint64(51), gap.Next)
	require.Contains(t, err.Error(), "missing nonce 50, next stored nonce is 51")
	require.Len(t, got, 49)
	for i := uint64(1); i <= 49; i++ {
		require.Equal(t, i, got[i-1])
	}
	require.Equal(t, [][2]uint64{{1, 49}}, store.calls)
	require.Len(t, store.sizeCalls, 1, "the size read that packed the page also names the hole")
}

func TestReadDiffPages_HoleAtStartIsOneRead(t *testing.T) {
	store := &pageCallStore{}
	const to = 10_000
	store.put(to, nil)

	err := ReadDiffPages(store, "escrow", 1, to, func([]types.DiffRecord) error {
		t.Fatal("a hole at the start must not visit a page")
		return nil
	})
	var gap *DiffGapError
	require.ErrorAs(t, err, &gap)
	require.Equal(t, uint64(1), gap.Expected)
	require.Equal(t, uint64(to), gap.Next)
	require.Empty(t, store.calls, "no diff is decoded")
	require.Len(t, store.sizeCalls, 1, "the next stored nonce comes from one read, not a probe per nonce")
}

func TestReadDiffPages_MissingTail(t *testing.T) {
	store := &pageCallStore{}
	store.put(1, nil)
	err := ReadDiffPages(store, "escrow", 1, 3, func([]types.DiffRecord) error { return nil })
	var gap *DiffGapError
	require.ErrorAs(t, err, &gap)
	require.Equal(t, uint64(0), gap.Next)
	require.Contains(t, err.Error(), "missing trailing nonces 2..3")
	require.Len(t, store.sizeCalls, 1, "a short size read already shows the tail is missing")
}

func TestReadDiffPages_OversizedDiffIsItsOwnPage(t *testing.T) {
	store := &pageCallStore{}
	store.put(1, nil)
	store.put(2, txWithPayload(DiffPageMaxBytes+1))
	store.put(3, nil)

	var pages [][]uint64
	err := ReadDiffPages(store, "escrow", 1, 3, func(page []types.DiffRecord) error {
		nonces := make([]uint64, len(page))
		for i, rec := range page {
			nonces[i] = rec.Nonce
		}
		pages = append(pages, nonces)
		return nil
	})
	require.NoError(t, err)
	require.Equal(t, [][]uint64{{1}, {2}, {3}}, pages)
	require.Equal(t, [][2]uint64{{1, 1}, {2, 2}, {3, 3}}, store.calls, "the oversized diff is fetched alone")
}

func TestReadDiffPages_ByteBudgetEndsThePage(t *testing.T) {
	store := &pageCallStore{}
	// Two of these fit in 8 MiB. The third does not.
	payload := 3 << 20
	for n := uint64(1); n <= 3; n++ {
		store.put(n, txWithPayload(payload))
	}
	require.LessOrEqual(t, 2*diffTxsProtoSize(store.diffs[1]), DiffPageMaxBytes)
	require.Greater(t, 3*diffTxsProtoSize(store.diffs[1]), DiffPageMaxBytes)

	var pageLens []int
	err := ReadDiffPages(store, "escrow", 1, 3, func(page []types.DiffRecord) error {
		pageLens = append(pageLens, len(page))
		return nil
	})
	require.NoError(t, err)
	require.Equal(t, []int{2, 1}, pageLens)
	require.Equal(t, [][2]uint64{{1, 2}, {3, 3}}, store.calls)
}

func TestLoadBoundedDiffs_RejectsASecondPage(t *testing.T) {
	store := &pageCallStore{}
	const n = DiffPageMaxNonces + 1
	for i := uint64(1); i <= n; i++ {
		store.put(i, nil)
	}
	_, err := LoadBoundedDiffs(store, "escrow", 1, n)
	require.ErrorIs(t, err, ErrDiffPageLimit)
	require.Empty(t, store.calls, "a nonce span over the cap is rejected before a read")
	require.Empty(t, store.sizeCalls)
	require.Contains(t, err.Error(), "64")
	require.Contains(t, err.Error(), "8388608")

	page, err := LoadBoundedDiffs(store, "escrow", 1, DiffPageMaxNonces)
	require.NoError(t, err)
	require.Len(t, page, DiffPageMaxNonces)
	require.Equal(t, [][2]uint64{{1, DiffPageMaxNonces}}, store.calls, "one page is one read")
}

func TestLoadBoundedDiffs_ByteBudgetIsItsOwnPage(t *testing.T) {
	store := &pageCallStore{}
	store.put(1, txWithPayload(DiffPageMaxBytes+1))
	store.put(2, nil)

	_, err := LoadBoundedDiffs(store, "escrow", 1, 2)
	require.ErrorIs(t, err, ErrDiffPageLimit)
	require.Empty(t, store.calls, "the byte budget is checked before any diff is decoded")

	page, err := LoadBoundedDiffs(store, "escrow", 1, 1)
	require.NoError(t, err)
	require.Len(t, page, 1)
	require.Equal(t, uint64(1), page[0].Nonce)
}

func TestReadDiffPages_EmptyRange(t *testing.T) {
	store := &pageCallStore{}
	err := ReadDiffPages(store, "escrow", 5, 4, func([]types.DiffRecord) error {
		t.Fatal("empty range must not visit a page")
		return nil
	})
	require.NoError(t, err)
	require.Empty(t, store.calls)
	require.Empty(t, store.sizeCalls)
}

func TestLoadBoundedDiffs_SkipsMissingNonces(t *testing.T) {
	store := &pageCallStore{}
	store.put(3, nil)
	store.put(5, nil)

	page, err := LoadBoundedDiffs(store, "escrow", 1, 9)
	require.NoError(t, err)
	require.Len(t, page, 2)
	require.Equal(t, uint64(3), page[0].Nonce)
	require.Equal(t, uint64(5), page[1].Nonce)
	require.Equal(t, [][2]uint64{{3, 5}}, store.calls, "the read is trimmed to the stored nonces")

	page, err = LoadBoundedDiffs(store, "escrow", 6, 9)
	require.NoError(t, err)
	require.Empty(t, page)
	require.Len(t, store.calls, 1, "an empty range does not decode")
}
