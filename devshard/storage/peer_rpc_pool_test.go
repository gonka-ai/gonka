package storage

import (
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

func TestPeerRPCPoolUnwrap(t *testing.T) {
	require.Nil(t, PeerRPCPool(nil))
	require.Nil(t, PeerRPCPool(NewMemory()))
	require.Nil(t, PeerRPCPool(NewObsRepairGate(NewMemory())))
	require.Nil(t, PeerRPCPool(NewHybridStorage(NewMemory())))

	pg := &Postgres{}
	require.Nil(t, PeerRPCPool(pg))
	router := newHybridRouter(nil, pg, true, "")
	require.Nil(t, PeerRPCPool(router))
	require.Nil(t, PeerRPCPool(NewObsRepairGate(router)))
	require.Nil(t, PeerRPCPool(NewManagedStorage(NewMemory(), 3, nil)))

	pool := &pgxpool.Pool{}
	live := &Postgres{pool: pool}
	liveRouter := newHybridRouter(nil, live, true, "")
	require.Same(t, pool, PeerRPCPool(live))
	require.Same(t, pool, PeerRPCPool(liveRouter))
	require.Same(t, pool, PeerRPCPool(NewObsRepairGate(liveRouter)))
	require.Same(t, pool, PeerRPCPool(NewManagedStorage(live, 3, nil)))
	require.Same(t, pool, PeerRPCPool(NewManagedStorage(NewObsRepairGate(live), 3, nil)))
	require.Same(t, pool, PeerRPCPool(NewManagedStorage(liveRouter, 3, nil)))
}
