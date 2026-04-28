package bls

import (
	"context"
	"path/filepath"
	"testing"

	"decentralized-api/apiconfig"

	"github.com/stretchr/testify/require"
)

func TestDealerOpeningsPersistedAcrossManagerRestart(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "bls-openings.sqlite")
	db, err := apiconfig.OpenSQLite(apiconfig.SqliteConfig{Path: dbPath})
	require.NoError(t, err)
	defer db.Close()
	require.NoError(t, apiconfig.EnsureSchema(context.Background(), db))

	m1 := NewBlsManager(createMockCosmosClient())
	require.NoError(t, m1.SetDealerOpeningsDB(db))

	share := []byte{1, 2, 3, 4}
	seed := []byte{5, 6, 7, 8}
	require.NoError(t, m1.storeDealerOpeningRecord(11, 2, 3, 44, share, seed))

	m2 := NewBlsManager(createMockCosmosClient())
	require.NoError(t, m2.SetDealerOpeningsDB(db))

	record, ok := m2.getDealerOpeningRecord(11, 2, 3)
	require.True(t, ok)
	require.Equal(t, uint32(44), record.slotIndex)
	require.Equal(t, share, record.shareBytes)
	require.Equal(t, seed, record.seed)
}

func TestDeleteDealerOpeningsPersists(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "bls-openings.sqlite")
	db, err := apiconfig.OpenSQLite(apiconfig.SqliteConfig{Path: dbPath})
	require.NoError(t, err)
	defer db.Close()
	require.NoError(t, apiconfig.EnsureSchema(context.Background(), db))

	m1 := NewBlsManager(createMockCosmosClient())
	require.NoError(t, m1.SetDealerOpeningsDB(db))

	require.NoError(t, m1.storeDealerOpeningRecord(20, 1, 1, 10, []byte{1}, []byte{2}))
	require.NoError(t, m1.storeDealerOpeningRecord(21, 1, 1, 11, []byte{3}, []byte{4}))
	require.NoError(t, m1.deleteDealerOpeningsForEpoch(20))

	m2 := NewBlsManager(createMockCosmosClient())
	require.NoError(t, m2.SetDealerOpeningsDB(db))

	_, ok := m2.getDealerOpeningRecord(20, 1, 1)
	require.False(t, ok)
	record, ok := m2.getDealerOpeningRecord(21, 1, 1)
	require.True(t, ok)
	require.Equal(t, uint32(11), record.slotIndex)
}
