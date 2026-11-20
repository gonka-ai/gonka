package storage

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSQLiteGatewayRequiresDurableCommits(t *testing.T) {
	db := newTestSQLite(t)
	params := defaultParams()
	require.NoError(t, db.CreateSession(params))
	p, _, err := db.poolFor(params.EscrowID)
	require.NoError(t, err)
	var level int
	require.NoError(t, p.writeDB.QueryRow("PRAGMA synchronous").Scan(&level))
	require.Equal(t, 1, level, "ordinary host storage keeps its existing setting")
	require.NoError(t, db.RequireDurableCommits(params.EscrowID))
	require.NoError(t, p.writeDB.QueryRow("PRAGMA synchronous").Scan(&level))
	require.Equal(t, 2, level)
}
