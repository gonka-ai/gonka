package rpcserver

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"devshard/transport"
)

func TestRPCTraffic_GetSignaturesCounted(t *testing.T) {
	now := time.Unix(1_710_000_000, 0)
	clock := now
	env := startLimitEnv(t, PeerAuthConfig{Now: func() time.Time { return clock }}, stubLookup{core: stubCore{}})
	require.NoError(t, env.getSigs())

	clock = now.Add(time.Minute)
	snap := env.auth.Traffic().Snapshot(clock)
	require.Equal(t, now.Unix(), snap.MinuteUnix)
	var sigs uint64
	for _, e := range snap.Host.Endpoints {
		if e.Endpoint == "GetSignatures" && e.Zone == transport.RPCZoneShared {
			sigs = e.Requests
		}
	}
	require.Equal(t, uint64(1), sigs)
	require.Equal(t, uint64(1), snap.Host.Attach.Attempts)
}
