package main

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestPerfStoreHostScoresRoundTrip(t *testing.T) {
	dir := t.TempDir()
	store, err := NewPerfStore(filepath.Join(dir, "perf.db"))
	require.NoError(t, err)
	defer store.Close()

	now := time.Now().UTC().Truncate(time.Microsecond)
	states := []HostScoreState{
		{
			Model: "moonshotai/Kimi-K2.6", Host: "p-a", Bucket: "5k_15k", Elo: 1612.5,
			Samples: []hostScoreSample{
				{Timestamp: now, TtftMs: 123.4, TotalMs: 5678.9},
				{Timestamp: now.Add(time.Second), TtftMs: 200, TotalMs: 6000},
			},
			UpdatedAt: now,
		},
		{
			Model: "Qwen/Qwen3-235B", Host: "p-b", Bucket: "lt_1k", Elo: 1500.0,
			Samples:   []hostScoreSample{{Timestamp: now, TtftMs: 50, TotalMs: 800}},
			UpdatedAt: now,
		},
	}
	require.NoError(t, store.SaveHostScores(states))

	loaded, err := store.LoadHostScores()
	require.NoError(t, err)
	require.Len(t, loaded, 2)

	byKey := map[string]HostScoreState{}
	for _, s := range loaded {
		byKey[s.Model+"|"+s.Host+"|"+s.Bucket] = s
	}
	k := "moonshotai/Kimi-K2.6|p-a|5k_15k"
	require.InDelta(t, 1612.5, byKey[k].Elo, 0.001)
	require.Len(t, byKey[k].Samples, 2)
	require.InDelta(t, 123.4, byKey[k].Samples[0].TtftMs, 0.001)
	require.InDelta(t, 5678.9, byKey[k].Samples[0].TotalMs, 0.001)
	require.True(t, byKey[k].Samples[0].Timestamp.Equal(now), "timestamps preserved across round-trip")
}

func TestPerfStoreHostScoresUpsert(t *testing.T) {
	dir := t.TempDir()
	store, err := NewPerfStore(filepath.Join(dir, "perf.db"))
	require.NoError(t, err)
	defer store.Close()

	now := time.Now().UTC()
	first := []HostScoreState{{
		Model: "m", Host: "h", Bucket: "lt_1k", Elo: 1500, UpdatedAt: now,
		Samples: []hostScoreSample{{TtftMs: 100, TotalMs: 1000}},
	}}
	require.NoError(t, store.SaveHostScores(first))

	second := []HostScoreState{{
		Model: "m", Host: "h", Bucket: "lt_1k", Elo: 1612, UpdatedAt: now.Add(time.Minute),
		Samples: []hostScoreSample{{TtftMs: 200, TotalMs: 2000}, {TtftMs: 250, TotalMs: 2500}},
	}}
	require.NoError(t, store.SaveHostScores(second))

	loaded, err := store.LoadHostScores()
	require.NoError(t, err)
	require.Len(t, loaded, 1, "upsert on (model, host, bucket) primary key, not duplicate insert")
	require.InDelta(t, 1612, loaded[0].Elo, 0.001)
	require.Len(t, loaded[0].Samples, 2)
}

func TestPerfStoreHostScoresEmpty(t *testing.T) {
	dir := t.TempDir()
	store, err := NewPerfStore(filepath.Join(dir, "perf.db"))
	require.NoError(t, err)
	defer store.Close()

	loaded, err := store.LoadHostScores()
	require.NoError(t, err)
	require.Empty(t, loaded, "fresh DB has no host-score rows")

	require.NoError(t, store.SaveHostScores(nil))
	require.NoError(t, store.SaveHostScores([]HostScoreState{}))
}

func TestPerfStoreHostScoresSchemaIdempotent(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "perf.db")

	store, err := NewPerfStore(dbPath)
	require.NoError(t, err)
	require.NoError(t, store.Close())

	store2, err := NewPerfStore(dbPath)
	require.NoError(t, err, "re-opening must not error on existing schema")
	defer store2.Close()
}
