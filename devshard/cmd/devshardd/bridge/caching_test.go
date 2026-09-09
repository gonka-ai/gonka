package bridge

import (
	"errors"
	"testing"

	"devshard/bridge"
	"devshard/storage"

	"github.com/stretchr/testify/require"
)

type fakeBridge struct {
	bridge.MainnetBridge
	escrow *bridge.EscrowInfo
	err    error
}

func (f *fakeBridge) GetEscrow(string) (*bridge.EscrowInfo, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.escrow, nil
}

type fakeCacheStore struct {
	info *storage.EscrowCacheInfo
	err  error
}

func (f *fakeCacheStore) GetEscrowCache(string) (*storage.EscrowCacheInfo, error) {
	return f.info, f.err
}

func TestCachingEscrowBridge_ChainFirstWhenUp(t *testing.T) {
	inner := &fakeBridge{escrow: &bridge.EscrowInfo{EscrowID: "1", CreatorAddress: "gonka1live", EpochID: 7}}
	cache := &fakeCacheStore{info: &storage.EscrowCacheInfo{EscrowID: "1", CreatorAddress: "STALE"}}
	b := NewCachingEscrowBridge(inner, cache, nil)

	got, err := b.GetEscrow("1")
	require.NoError(t, err)
	require.Equal(t, "gonka1live", got.CreatorAddress, "chain result must win when live query succeeds")
}

func TestCachingEscrowBridge_CacheFallbackOnLiveError(t *testing.T) {
	inner := &fakeBridge{err: errors.New("chain down")}
	cache := &fakeCacheStore{info: &storage.EscrowCacheInfo{
		EscrowID: "1", CreatorAddress: "gonka1owner", EpochID: 9, Amount: 500, VoteThresholdFactor: 3,
	}}
	b := NewCachingEscrowBridge(inner, cache, nil)

	got, err := b.GetEscrow("1")
	require.NoError(t, err)
	require.Equal(t, "gonka1owner", got.CreatorAddress)
	require.Equal(t, uint64(9), got.EpochID)
	require.Equal(t, uint64(500), got.Amount)
	require.Equal(t, uint32(3), got.VoteThresholdFactor)
}

func TestCachingEscrowBridge_ReturnsLiveErrOnCacheMiss(t *testing.T) {
	liveErr := errors.New("chain down")
	inner := &fakeBridge{err: liveErr}
	cache := &fakeCacheStore{err: storage.ErrEscrowCacheNotFound}
	b := NewCachingEscrowBridge(inner, cache, nil)

	_, err := b.GetEscrow("1")
	require.ErrorIs(t, err, liveErr)
}

func TestEscrowCacheRoundTrip(t *testing.T) {
	in := &bridge.EscrowInfo{
		EscrowID: "2", Amount: 1, CreatorAddress: "c", Slots: []string{"a", "b"},
		TokenPrice: 3, ValidationRate: 4, VoteThresholdFactor: 5, EpochID: 6,
	}
	cache := EscrowCacheFromInfo(in)
	require.Equal(t, uint32(5), cache.VoteThresholdFactor)

	out := EscrowInfoFromCache(&cache)
	require.Equal(t, in.CreatorAddress, out.CreatorAddress)
	require.Equal(t, in.Slots, out.Slots)
	require.Equal(t, in.VoteThresholdFactor, out.VoteThresholdFactor)
	require.Equal(t, in.EpochID, out.EpochID)
}
