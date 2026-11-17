package heightsync

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"devshard/chainoracle/blocks/headertime"
)

type mapStub struct {
	rows map[uint64]headertime.Record
}

func (m *mapStub) StoredAt(height uint64) (headertime.Record, bool) {
	if m == nil || m.rows == nil {
		return headertime.Record{}, false
	}
	row, ok := m.rows[height]
	return row, ok
}

func TestResolveObservedTimeMs_MapHitUsesMap(t *testing.T) {
	hash := []byte{0xaa}
	src := &mapStub{rows: map[uint64]headertime.Record{
		100: {Hash: hash, TimeMs: 5_000},
	}}
	ms, err := ResolveObservedTimeMs(src, 100, hash, 0)
	require.NoError(t, err)
	require.Equal(t, int64(5_000), ms)
}

func TestResolveObservedTimeMs_MapMissReturnsCandidate(t *testing.T) {
	ms, err := ResolveObservedTimeMs(&mapStub{}, 100, []byte{0xaa}, 7_000)
	require.NoError(t, err)
	require.Equal(t, int64(7_000), ms)

	ms, err = ResolveObservedTimeMs(nil, 100, []byte{0xaa}, 7_000)
	require.NoError(t, err)
	require.Equal(t, int64(7_000), ms)
}

func TestResolveObservedTimeMs_MapHitDisagreeRejects(t *testing.T) {
	hash := []byte{0xaa}
	src := &mapStub{rows: map[uint64]headertime.Record{
		100: {Hash: hash, TimeMs: 5_000},
	}}
	_, err := ResolveObservedTimeMs(src, 100, hash, 9_000)
	require.ErrorIs(t, err, ErrStampTimeMismatch)
}

func TestResolveObservedTimeMs_HashMismatchTreatedAsMiss(t *testing.T) {
	src := &mapStub{rows: map[uint64]headertime.Record{
		100: {Hash: []byte{0xaa}, TimeMs: 5_000},
	}}
	ms, err := ResolveObservedTimeMs(src, 100, []byte{0xbb}, 7_000)
	require.NoError(t, err)
	require.Equal(t, int64(7_000), ms, "different hash is not a time reject; candidate wins")
}

func TestHeaderTimeMs(t *testing.T) {
	require.Zero(t, HeaderTimeMs(time.Time{}))
	at := time.UnixMilli(12_345)
	require.Equal(t, int64(12_345), HeaderTimeMs(at))
}
