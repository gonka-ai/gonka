package transport

import (
	"context"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"devshard/transport/rpcpb/rpcpbconnect"
)

func TestRPCTraffic_MinuteBucketsAndBanned(t *testing.T) {
	now := time.Unix(1_710_000_000, 0)
	clock := now
	tr := NewRPCTraffic(func() time.Time { return clock })

	tr.Observe(context.Background(), RPCSample{
		Procedure: rpcpbconnect.SessionServiceGetDiffsProcedure,
		Peer:      "gonka1a",
		Escrow:    "42",
		IP:        "203.0.113.9",
	})
	tr.Observe(context.Background(), RPCSample{
		Procedure: rpcpbconnect.SessionServiceGetDiffsProcedure,
		Peer:      "gonka1a",
		Escrow:    "42",
		IP:        "203.0.113.9",
		Banned:    true,
	})
	tr.Observe(context.Background(), RPCSample{
		Procedure: rpcpbconnect.SessionServiceGetMempoolProcedure,
		Peer:      "gonka1a",
		Escrow:    "42",
		IP:        "203.0.113.9",
		Banned:    true,
	})

	clock = now.Add(time.Minute)
	snap := tr.Snapshot(clock)
	require.Equal(t, now.Unix(), snap.MinuteUnix)
	require.Equal(t, uint64(3), snap.Host.Requests)
	require.Equal(t, uint64(2), snap.Host.Banned)
	require.Equal(t, "GetDiffs", snap.Host.Endpoints[0].Endpoint)
	require.Equal(t, RPCZoneShared, snap.Host.Endpoints[0].Zone)
	require.Equal(t, uint64(2), snap.Host.Endpoints[0].Requests)
	require.Equal(t, uint64(1), snap.Host.Endpoints[0].Banned)
	require.Len(t, snap.Shards, 1)
	require.Equal(t, "42", snap.Shards[0].EscrowID)
	require.Equal(t, uint64(3), snap.Shards[0].Requests)
	require.Equal(t, uint64(2), snap.Shards[0].Banned)
}

func TestRPCTraffic_AttachFloor(t *testing.T) {
	now := time.Unix(1_710_000_000, 0)
	clock := now
	tr := NewRPCTraffic(func() time.Time { return clock })
	tr.Observe(context.Background(), RPCSample{
		Procedure:   rpcpbconnect.PeerAuthServiceAttachProcedure,
		Attach:      true,
		Banned:      true,
		AttachFloor: true,
	})
	clock = now.Add(time.Minute)
	snap := tr.Snapshot(clock)
	require.Equal(t, uint64(1), snap.Host.Attach.Attempts)
	require.Equal(t, uint64(1), snap.Host.Attach.Banned)
	require.Equal(t, uint64(1), snap.Host.Attach.BannedFloor)
	require.Equal(t, RPCZoneAttachFloor, snap.Host.Zones[0].Zone)
	require.Empty(t, snap.Host.IPs)
}

func TestRPCTraffic_PeerCapFoldsOther(t *testing.T) {
	now := time.Unix(1_710_000_000, 0)
	clock := now
	tr := NewRPCTraffic(func() time.Time { return clock })
	n := rpcTrafficShardFoldCap(rpcTrafficPeerCap)*rpcTrafficShards + 1
	for i := 0; i < n; i++ {
		tr.Observe(context.Background(), RPCSample{
			Procedure: rpcpbconnect.SessionServiceGetSignaturesProcedure,
			Peer:      "peer-" + strconv.Itoa(i),
			Escrow:    "1",
		})
	}
	clock = now.Add(time.Minute)
	snap := tr.Snapshot(clock)
	require.Equal(t, uint64(n), snap.Host.Requests)
	var other uint64
	for _, p := range snap.Host.Peers {
		if p.Peer == rpcTrafficOtherKey {
			other = p.Requests
		}
	}
	require.Greater(t, other, uint64(0), "overflow peers must fold into other")
	require.LessOrEqual(t, len(snap.Host.Peers), rpcTrafficPeerCap+rpcTrafficShards)
}

func TestRPCTraffic_TwoEscrowsOneSnapshot(t *testing.T) {
	now := time.Unix(1_710_000_000, 0)
	clock := now
	tr := NewRPCTraffic(func() time.Time { return clock })
	tr.Observe(context.Background(), RPCSample{
		Procedure: rpcpbconnect.SessionServiceGetDiffsProcedure,
		Peer:      "p",
		Escrow:    "a",
	})
	tr.Observe(context.Background(), RPCSample{
		Procedure: rpcpbconnect.SessionServiceGetSignaturesProcedure,
		Peer:      "p",
		Escrow:    "b",
	})
	clock = now.Add(time.Minute)
	snap := tr.Snapshot(clock)
	EnsureShard(&snap, "a", "v5")
	EnsureShard(&snap, "b", "v5")
	require.Len(t, snap.Shards, 2)
	require.Equal(t, "a", snap.Shards[0].EscrowID)
	require.Equal(t, "b", snap.Shards[1].EscrowID)
	require.Equal(t, "v5", snap.Shards[0].ProtocolVersion)
}

func TestRPCTraffic_HostWarnOncePerMinute(t *testing.T) {
	now := time.Unix(1_710_000_000, 0)
	clock := now
	tr := NewRPCTraffic(func() time.Time { return clock })
	var warns atomic.Int64
	tr.SetWarn(func(context.Context, int64, RPCStatsHost) { warns.Add(1) })
	for i := 0; i < 50; i++ {
		tr.Observe(context.Background(), RPCSample{
			Procedure: rpcpbconnect.SessionServiceGetDiffsProcedure,
			Peer:      "p",
			Escrow:    "1",
			Banned:    true,
		})
	}
	require.Equal(t, int64(0), warns.Load())
	clock = now.Add(time.Minute)
	tr.Observe(context.Background(), RPCSample{
		Procedure: rpcpbconnect.SessionServiceGetSignaturesProcedure,
		Peer:      "p",
		Escrow:    "1",
	})
	require.Equal(t, int64(1), warns.Load())
	clock = now.Add(2 * time.Minute)
	tr.Observe(context.Background(), RPCSample{
		Procedure: rpcpbconnect.SessionServiceGetSignaturesProcedure,
		Peer:      "p",
		Escrow:    "1",
		Banned:    true,
	})
	require.Equal(t, int64(1), warns.Load(), "clean second minute does not warn until it closes")
	clock = now.Add(3 * time.Minute)
	_ = tr.Snapshot(clock)
	require.Equal(t, int64(2), warns.Load())
}

func TestRPCTraffic_ClosedWarnDoesNotHoldMutex(t *testing.T) {
	run := func(t *testing.T, closeViaSnapshot bool) {
		t.Helper()
		now := time.Unix(1_710_000_000, 0)
		clock := now
		tr := NewRPCTraffic(func() time.Time { return clock })
		var warns atomic.Int64
		tr.SetWarn(func(context.Context, int64, RPCStatsHost) {
			for i := range tr.shards {
				if !tr.shards[i].mu.TryLock() {
					t.Error("closed-minute warn held a traffic shard mutex")
					return
				}
				tr.shards[i].mu.Unlock()
			}
			tr.Observe(context.Background(), RPCSample{
				Procedure: rpcpbconnect.SessionServiceGetSignaturesProcedure,
				Peer:      "q",
				Escrow:    "1",
			})
			warns.Add(1)
		})
		tr.Observe(context.Background(), RPCSample{
			Procedure: rpcpbconnect.SessionServiceGetDiffsProcedure,
			Peer:      "p",
			Escrow:    "1",
			Banned:    true,
		})
		clock = now.Add(time.Minute)
		if closeViaSnapshot {
			_ = tr.Snapshot(clock)
		} else {
			tr.Observe(context.Background(), RPCSample{
				Procedure: rpcpbconnect.SessionServiceGetSignaturesProcedure,
				Peer:      "p",
				Escrow:    "1",
			})
		}
		require.Equal(t, int64(1), warns.Load())
	}
	t.Run("observe", func(t *testing.T) { run(t, false) })
	t.Run("snapshot", func(t *testing.T) { run(t, true) })
}

func TestRPCTraffic_NextMinuteIsNewBucket(t *testing.T) {
	now := time.Unix(1_710_000_000, 0)
	clock := now
	tr := NewRPCTraffic(func() time.Time { return clock })
	tr.Observe(context.Background(), RPCSample{
		Procedure: rpcpbconnect.SessionServiceGetSignaturesProcedure,
		Peer:      "p",
		Escrow:    "1",
	})
	clock = now.Add(time.Minute)
	tr.Observe(context.Background(), RPCSample{
		Procedure: rpcpbconnect.SessionServiceGetSignaturesProcedure,
		Peer:      "p",
		Escrow:    "1",
	})
	snap := tr.Snapshot(clock)
	require.Equal(t, uint64(1), snap.Host.Requests)
	clock = now.Add(2 * time.Minute)
	snap = tr.Snapshot(clock)
	require.Equal(t, uint64(1), snap.Host.Requests)
}

func TestRPCStatsJoin(t *testing.T) {
	require.Equal(t, "gonka1a/1710000000", RPCStatsJoin("gonka1a", 1_710_000_000))
	require.Equal(t, "unknown/60", RPCStatsJoin("  ", 60))
	kv := AppendRPCStatsLogTag(nil, "gonka1a", 1_710_000_000)
	require.Equal(t, []any{"tag", RPCStatsLogTag, "rpc_stats_join", "gonka1a/1710000000"}, kv)
}

func TestAppendBannedIdentityLog(t *testing.T) {
	host := RPCStatsHost{
		Peers: []RPCStatsPeer{
			{Peer: "quiet", Requests: 9, Banned: 0},
			{Peer: "gonka1b", Requests: 4, Banned: 1},
			{Peer: "gonka1a", Requests: 8, Banned: 5},
		},
		IPs: []RPCStatsIP{
			{IP: "203.0.113.9", Requests: 8, Banned: 5},
			{IP: "198.51.100.1", Requests: 3, Banned: 0},
		},
	}
	kv := AppendBannedIdentityLog(nil, host)
	got := map[string]any{}
	for i := 0; i+1 < len(kv); i += 2 {
		got[kv[i].(string)] = kv[i+1]
	}
	require.Equal(t, "gonka1a=5,gonka1b=1", got["banned_peers"])
	require.Equal(t, 2, got["banned_peer_rows"])
	require.Equal(t, 0, got["banned_peer_omitted"])
	require.Equal(t, "203.0.113.9=5", got["banned_ips"])
	require.Equal(t, 1, got["banned_ip_rows"])
	require.Equal(t, 0, got["banned_ip_omitted"])
}

func TestTopBannedPeersOmitsPastCap(t *testing.T) {
	rows := make([]RPCStatsPeer, 0, RPCStatsBannedIdentityLogCap+3)
	for i := 0; i < RPCStatsBannedIdentityLogCap+3; i++ {
		rows = append(rows, RPCStatsPeer{Peer: "p" + strconv.Itoa(i), Banned: 1})
	}
	got, n, omit := topBannedPeers(rows, RPCStatsBannedIdentityLogCap)
	require.Equal(t, RPCStatsBannedIdentityLogCap+3, n)
	require.Equal(t, 3, omit)
	require.Len(t, got, RPCStatsBannedIdentityLogCap)
}

func TestSnapshotPeerReconnects(t *testing.T) {
	t.Cleanup(ResetPeerReconnectsForTest)
	ResetPeerReconnectsForTest()
	now := time.Unix(1_710_000_000, 0)
	processReconnects.add(now, "hostB@v5", ReconnectWatch)
	processReconnects.add(now, "hostB@v5", ReconnectWatch)
	got := processReconnects.snapshot(now.Add(time.Minute))
	require.Equal(t, []RPCStatsReconnect{{Peer: "hostB@v5", Reason: ReconnectWatch, Attempts: 2}}, got)
}

func TestRPCTraffic_ObserveShardsConcurrent(t *testing.T) {
	now := time.Unix(1_710_000_000, 0)
	var mu sync.Mutex
	clock := now
	tr := NewRPCTraffic(func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		return clock
	})
	const peers, perPeer = 32, 50
	var wg sync.WaitGroup
	for i := 0; i < peers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			peer := "peer-" + strconv.Itoa(i)
			for j := 0; j < perPeer; j++ {
				tr.Observe(context.Background(), RPCSample{
					Procedure: rpcpbconnect.SessionServiceGetSignaturesProcedure,
					Peer:      peer,
					Escrow:    "1",
					IP:        "203.0.113.9",
				})
			}
		}(i)
	}
	wg.Wait()
	mu.Lock()
	clock = now.Add(time.Minute)
	mu.Unlock()
	snap := tr.Snapshot(now.Add(time.Minute))
	require.Equal(t, uint64(peers*perPeer), snap.Host.Requests)
	require.Len(t, snap.Shards, 1)
}
