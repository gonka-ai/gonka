package storage

import (
	"testing"

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
}
