package headertime

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestCacheRestoresCaughtUpRunAndPrunesWithFloor(t *testing.T) {
	t.Setenv("DEVSHARD_STORAGE_MODE", "sqlite")
	t.Setenv("PGHOST", "")
	path := filepath.Join(t.TempDir(), SQLiteFileName)
	ctx := context.Background()
	at := time.Unix(1_800_000_000, 0)

	c1, err := OpenCache(ctx, path)
	require.NoError(t, err)
	s := New()
	s.SetCache(c1)
	for h := int64(10000); h <= 10005; h++ {
		s.Observe(hdr(h, at.Add(time.Duration(h)*time.Millisecond), []byte{byte(h)}), OriginComet, true)
	}
	require.Equal(t, uint64(10005), s.HighWater())
	require.NoError(t, s.CloseCache())

	c2, err := OpenCache(ctx, path)
	require.NoError(t, err)
	restored := New()
	restored.SetCache(c2)
	require.Equal(t, uint64(10005), restored.HighWater())
	for h := uint64(10000); h <= 10005; h++ {
		_, ok := restored.StoredAt(h)
		require.Truef(t, ok, "height %d", h)
	}

	restored.SetFloor(func() uint64 { return 10003 })
	_, ok := restored.StoredAt(10000)
	require.False(t, ok)
	_, ok = restored.StoredAt(10003)
	require.True(t, ok)
	require.NoError(t, restored.CloseCache())

	c3, err := OpenCache(ctx, path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = c3.Close() })
	pruned := New()
	pruned.SetCache(c3)
	_, ok = pruned.StoredAt(10000)
	require.False(t, ok)
	_, ok = pruned.StoredAt(10002)
	require.False(t, ok)
	_, ok = pruned.StoredAt(10003)
	require.True(t, ok)
	require.Equal(t, uint64(10005), pruned.HighWater())
}

func TestCacheDoesNotStorePassThroughBelowFloor(t *testing.T) {
	t.Setenv("DEVSHARD_STORAGE_MODE", "sqlite")
	t.Setenv("PGHOST", "")
	path := filepath.Join(t.TempDir(), SQLiteFileName)
	ctx := context.Background()
	at := time.Unix(1_800_000_000, 0)
	hash := []byte{0x05}

	c, err := OpenCache(ctx, path)
	require.NoError(t, err)
	s := New(
		WithFloor(func() uint64 { return 1000 }),
		WithFiller(fillerFunc(func(_ context.Context, height int64) (Fill, error) {
			return Fill{Header: hdr(height, at, hash), Origin: OriginChain}, nil
		})),
	)
	s.SetCache(c)
	ms, err := s.HeaderTimeMs(500, hash)
	require.NoError(t, err)
	require.Equal(t, at.UnixMilli(), ms)
	_, ok := s.StoredAt(500)
	require.False(t, ok)
	require.NoError(t, s.CloseCache())

	c2, err := OpenCache(ctx, path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = c2.Close() })
	rows, err := c2.Load(ctx)
	require.NoError(t, err)
	for _, r := range rows {
		require.NotEqual(t, uint64(500), r.Height)
	}
}
