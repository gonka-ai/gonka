package public

import (
	"context"
	"path/filepath"
	"testing"

	"common/chainoracle/blocks/observer"
	"decentralized-api/apiconfig"

	"github.com/stretchr/testify/require"
)

func TestChainOracleHeaderStoreRoundTripsOnSQLite(t *testing.T) {
	db, err := apiconfig.OpenSQLite(apiconfig.SqliteConfig{Path: filepath.Join(t.TempDir(), "dapi.db")})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()
	store, err := observer.NewSQLiteHeaderStore(ctx, db)
	require.NoError(t, err)
	_, err = observer.NewSQLiteHeaderStore(ctx, db)
	require.NoError(t, err, "the schema is created once and reused")

	rows := []observer.StoredHeader{
		{Height: 10, Hash: []byte{0x0a}, PrevHash: []byte{0x09}, TimeMs: 1000, ChainID: "c"},
		{Height: 11, Hash: []byte{0x0b}, TimeMs: 1005, ChainID: "c"},
		{Height: 12, Hash: []byte{0x0c}, PrevHash: []byte{0x0b}, TimeMs: 1010, ChainID: "c"},
	}
	require.NoError(t, store.SaveHeaders(ctx, rows))
	require.NoError(t, store.SaveHeaders(ctx, []observer.StoredHeader{{Height: 11, Hash: []byte{0x0b}, PrevHash: []byte{0x0a}, TimeMs: 1005, ChainID: "c"}}))
	require.NoError(t, store.SaveHeaders(ctx, []observer.StoredHeader{{Height: 12, Hash: []byte{0x0c}, TimeMs: 1010, ChainID: "c"}}))

	top, err := store.MaxHeight(ctx)
	require.NoError(t, err)
	require.Equal(t, int64(12), top)
	got, err := store.LoadHeaders(ctx, 11)
	require.NoError(t, err)
	require.Len(t, got, 2)
	require.Equal(t, []byte{0x0a}, got[0].PrevHash, "a later link fills the missing one")
	require.Equal(t, []byte{0x0b}, got[1].PrevHash, "a row without a link keeps the stored one")

	require.NoError(t, store.PruneHeaders(ctx, 12))
	got, err = store.LoadHeaders(ctx, 0)
	require.NoError(t, err)
	require.Len(t, got, 1)

	_, ok, err := store.LoadTrust(ctx)
	require.NoError(t, err)
	require.False(t, ok)
	require.NoError(t, store.SaveTrust(ctx, observer.TrustPoint{Height: 12, Hash: []byte{0x0c}}))
	require.NoError(t, store.SaveTrust(ctx, observer.TrustPoint{Height: 13, Hash: []byte{0x0d}, Epoch: 2}))
	tp, ok, err := store.LoadTrust(ctx)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, int64(13), tp.Height)
	require.Equal(t, []byte{0x0d}, tp.Hash)
	require.Equal(t, int64(2), tp.Epoch)

	require.NoError(t, store.SaveTrust(ctx, observer.TrustPoint{Hash: []byte{}, Epoch: 3}))
	tp, ok, err = store.LoadTrust(ctx)
	require.NoError(t, err)
	require.True(t, ok)
	require.Zero(t, tp.Height, "a reset keeps only the new epoch")
	require.Equal(t, int64(3), tp.Epoch)

	require.NoError(t, store.SaveHeaders(ctx, []observer.StoredHeader{{Height: 20, Hash: []byte{0x14}, TimeMs: 2000, Verified: true, Epoch: 3}}))
	got, err = store.LoadHeaders(ctx, 20)
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, int64(3), got[0].Epoch)
}

func TestChainOracleHeaderStoreNeverDowngradesAVerifiedRow(t *testing.T) {
	db, err := apiconfig.OpenSQLite(apiconfig.SqliteConfig{Path: filepath.Join(t.TempDir(), "dapi.db")})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()
	store, err := observer.NewSQLiteHeaderStore(ctx, db)
	require.NoError(t, err)

	require.NoError(t, store.SaveHeaders(ctx, []observer.StoredHeader{{Height: 5, Hash: []byte{0x05}, TimeMs: 500, Verified: true}}))
	require.NoError(t, store.SaveHeaders(ctx, []observer.StoredHeader{{Height: 5, Hash: []byte{0x05}, TimeMs: 999}}))
	require.NoError(t, store.SaveHeaders(ctx, []observer.StoredHeader{{Height: 6, Hash: []byte{0x06}, TimeMs: 600}}))
	require.NoError(t, store.SaveHeaders(ctx, []observer.StoredHeader{{Height: 6, Hash: []byte{0x06}, TimeMs: 601, Verified: true}}))

	got, err := store.LoadHeaders(ctx, 0)
	require.NoError(t, err)
	require.Len(t, got, 2)
	require.True(t, got[0].Verified)
	require.Equal(t, int64(500), got[0].TimeMs, "an unverified write cannot replace a verified time")
	require.True(t, got[1].Verified)
	require.Equal(t, int64(601), got[1].TimeMs, "a verified write upgrades an unverified row")
}

func TestChainOracleStoreInitializationFailureBlocksBootstrap(t *testing.T) {
	t.Setenv("DAPI_CHAINORACLE_DISABLED", "false")
	t.Setenv("DAPI_CHAINORACLE_VERIFY_COMMITS", "true")
	db, err := apiconfig.OpenSQLite(apiconfig.SqliteConfig{Path: filepath.Join(t.TempDir(), "dapi.db")})
	require.NoError(t, err)
	store, err := observer.NewSQLiteHeaderStore(context.Background(), db)
	require.NoError(t, err)
	require.NoError(t, store.SaveTrust(context.Background(), observer.TrustPoint{Height: 7, Hash: []byte{7}}))
	require.NoError(t, db.Close())
	oracle, err := NewChainOracle("http://127.0.0.1:1", db)
	require.ErrorContains(t, err, "chainoracle header store unavailable")
	require.Nil(t, oracle)
}
