package main

import (
	"errors"
	"testing"

	"devshard/bridge"
	devshardstorage "devshard/storage"

	"github.com/stretchr/testify/require"
)

type sinkFakeBridge struct {
	bridge.MainnetBridge
	escrow *bridge.EscrowInfo
	err    error
}

func (f *sinkFakeBridge) GetEscrow(string) (*bridge.EscrowInfo, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.escrow, nil
}

func TestEscrowWarmSink_WarmEscrowPopulatesCache(t *testing.T) {
	store := devshardstorage.NewMemory()
	br := &sinkFakeBridge{escrow: &bridge.EscrowInfo{
		EscrowID: "1", CreatorAddress: "gonka1owner", EpochID: 3, Amount: 100, VoteThresholdFactor: 2,
	}}
	sink := newEscrowWarmSink(br, store, nil)

	require.NoError(t, sink.WarmEscrow("1"))

	got, err := store.GetEscrowCache("1")
	require.NoError(t, err)
	require.Equal(t, "gonka1owner", got.CreatorAddress)
	require.Equal(t, uint64(3), got.EpochID)
	require.Equal(t, uint32(2), got.VoteThresholdFactor)
}

func TestEscrowWarmSink_WarmEscrowChainErrorDoesNotCache(t *testing.T) {
	store := devshardstorage.NewMemory()
	br := &sinkFakeBridge{err: errors.New("chain down")}
	sink := newEscrowWarmSink(br, store, nil)

	require.Error(t, sink.WarmEscrow("1"))

	_, err := store.GetEscrowCache("1")
	require.ErrorIs(t, err, devshardstorage.ErrEscrowCacheNotFound)
}

func TestEscrowWarmSink_OnEscrowSettledDropsCache(t *testing.T) {
	store := devshardstorage.NewMemory()
	require.NoError(t, store.PutEscrowCache(devshardstorage.EscrowCacheInfo{EscrowID: "1", EpochID: 1}))
	sink := newEscrowWarmSink(&sinkFakeBridge{}, store, nil)

	require.NoError(t, sink.OnEscrowSettled("1"))

	_, err := store.GetEscrowCache("1")
	require.ErrorIs(t, err, devshardstorage.ErrEscrowCacheNotFound)

	require.NotPanics(t, sink.RehydrateOpenEscrows)
}
