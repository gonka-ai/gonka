package storage

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"devshard/types"
)

func validationJournal(length int) []types.DiffRecord {
	records := make([]types.DiffRecord, 0, length)
	for nonce := 1; nonce <= length; nonce++ {
		records = append(records, types.DiffRecord{Diff: types.Diff{
			Nonce: uint64(nonce),
			Txs:   []*types.DevshardTx{validationTx(uint64(nonce), uint32(nonce%5)), validationTx(uint64(nonce), uint32((nonce+1)%5))},
		}})
	}
	return records
}

func feedInPages(records []types.DiffRecord, pageSize int) func(add func([]types.DiffRecord) error) error {
	return func(add func([]types.DiffRecord) error) error {
		for start := 0; start < len(records); start += pageSize {
			if err := add(records[start:min(start+pageSize, len(records))]); err != nil {
				return err
			}
		}
		return nil
	}
}

// Test flow:
//  1. Rebuild a 1499-record journal with validations whole, through RebuildValidationObsFromDiffs.
//  2. Rebuild the same journal into a second store fed in pages of 7, 500 and 1024.
//  3. Every paged rebuild leaves the same observability rows as the whole one.
func TestRebuildValidationObs_PagedMatchesWholeJournal(t *testing.T) {
	records := validationJournal(1499)
	sealed := []uint64{3, 50, 11, 1400}
	wholeStore := setupObsTestStore(t)
	require.NoError(t, RebuildValidationObsFromDiffs(wholeStore, "escrow-1", records, sealed))
	want, err := wholeStore.GetValidationObservability("escrow-1")
	require.NoError(t, err)
	require.NotEmpty(t, want)

	for _, pageSize := range []int{7, 500, 1024} {
		pagedStore := setupObsTestStore(t)
		require.NoError(t, RebuildValidationObs(pagedStore, "escrow-1", feedInPages(records, pageSize), sealed))
		got, err := pagedStore.GetValidationObservability("escrow-1")
		require.NoError(t, err)
		require.Equal(t, want, got, "RebuildValidationObs(page %d) rows = %v, want %v", pageSize, got, want)
	}
}

// Test flow:
//  1. Rebuild through a page feed that fails on its second page.
//  2. The feed's error is returned unchanged.
func TestRebuildValidationObs_ReturnsThePageError(t *testing.T) {
	store := setupObsTestStore(t)
	pageFailure := errors.New("page read failed")
	pages := 0
	eachPage := func(add func([]types.DiffRecord) error) error {
		for _, page := range [][]types.DiffRecord{validationJournal(3), validationJournal(3)} {
			pages++
			if pages == 2 {
				return pageFailure
			}
			if err := add(page); err != nil {
				return err
			}
		}
		return nil
	}

	err := RebuildValidationObs(store, "escrow-1", eachPage, nil)

	require.ErrorIs(t, err, pageFailure, "RebuildValidationObs(failing feed) = %v, want %v", err, pageFailure)
}
