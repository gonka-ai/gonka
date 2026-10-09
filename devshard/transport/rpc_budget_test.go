package transport

import (
	"context"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"

	"devshard/observability"
	"devshard/transport/rpcpb"
	"devshard/transport/rpcpb/rpcpbconnect"
)

func TestPeerRPCBudget_GetDiffsWaitsForBurst(t *testing.T) {
	clock := time.Unix(1_700_000_000, 0)
	var slept time.Duration
	now := func() time.Time { return clock }
	sleep := func(ctx context.Context, d time.Duration) error {
		_ = ctx
		slept += d
		clock = clock.Add(d)
		return nil
	}
	var b peerRPCBudget
	b.apply(&rpcpb.RateLimits{MessagesPerMin: 6000, MessagesBurst: 60}, now())
	ctx := context.Background()
	beforeWaits := testutil.ToFloat64(observability.PeerRPCBudgetWaitCounter("GetDiffs"))
	beforeSec := testutil.ToFloat64(observability.PeerRPCBudgetWaitSecondsCounter("GetDiffs"))
	require.NoError(t, b.take(ctx, rpcpbconnect.SessionServiceGetDiffsProcedure, now, sleep))
	require.Zero(t, slept, "first GetDiffs must fit in the advertised burst")
	require.NoError(t, b.take(ctx, rpcpbconnect.SessionServiceGetDiffsProcedure, now, sleep))
	require.Equal(t, 60*time.Minute/6000, slept, "second GetDiffs waits one refill of weight 60 at 6000/min")
	require.Equal(t, 1.0, testutil.ToFloat64(observability.PeerRPCBudgetWaitCounter("GetDiffs"))-beforeWaits)
	require.InDelta(t, slept.Seconds(), testutil.ToFloat64(observability.PeerRPCBudgetWaitSecondsCounter("GetDiffs"))-beforeSec, 1e-9)
}

func TestPeerRPCBudget_CostAboveBurstOverdrafts(t *testing.T) {
	clock := time.Unix(1_700_000_000, 0)
	var slept time.Duration
	now := func() time.Time { return clock }
	sleep := func(ctx context.Context, d time.Duration) error {
		_ = ctx
		slept += d
		clock = clock.Add(d)
		return nil
	}
	var b peerRPCBudget
	b.apply(&rpcpb.RateLimits{MessagesPerMin: 6000, MessagesBurst: 10}, now())
	ctx := context.Background()
	require.NoError(t, b.take(ctx, rpcpbconnect.SessionServiceGetDiffsProcedure, now, sleep))
	require.Zero(t, slept, "first GetDiffs overdrafts from a full burst")
	require.NoError(t, b.take(ctx, rpcpbconnect.SessionServiceGetSignaturesProcedure, now, sleep))
	require.Equal(t, 51*time.Minute/6000, slept, "GetSignatures waits until tokens reach 1 from -50 at 6000/min")
}

func TestPeerRPCBudget_OverdraftSecondGetDiffsWaitsForRate(t *testing.T) {
	clock := time.Unix(1_700_000_000, 0)
	var slept time.Duration
	now := func() time.Time { return clock }
	sleep := func(ctx context.Context, d time.Duration) error {
		_ = ctx
		slept += d
		clock = clock.Add(d)
		return nil
	}
	var b peerRPCBudget
	b.apply(&rpcpb.RateLimits{MessagesPerMin: 6000, MessagesBurst: 10}, now())
	ctx := context.Background()
	require.NoError(t, b.take(ctx, rpcpbconnect.SessionServiceGetDiffsProcedure, now, sleep))
	slept = 0
	require.NoError(t, b.take(ctx, rpcpbconnect.SessionServiceGetDiffsProcedure, now, sleep))
	require.Equal(t, 60*time.Minute/6000, slept, "second GetDiffs waits to refill from -50 to burst 10 at 6000/min")
}

func TestPeerRPCBudget_OverdraftRefundRestoresBurst(t *testing.T) {
	sleep := func(context.Context, time.Duration) error {
		t.Fatal("refunded overdraft must be available immediately")
		return nil
	}
	now := func() time.Time { return time.Unix(1_700_000_000, 0) }
	var b peerRPCBudget
	b.apply(&rpcpb.RateLimits{MessagesPerMin: 6000, MessagesBurst: 10}, now())
	ctx := context.Background()
	require.NoError(t, b.take(ctx, rpcpbconnect.SessionServiceGetDiffsProcedure, now, sleep))
	b.refund(now(), rpcpbconnect.SessionServiceGetDiffsProcedure)
	require.NoError(t, b.take(ctx, rpcpbconnect.SessionServiceGetDiffsProcedure, now, sleep))
}

func TestPeerRPCBudget_NilAndUnlimitedSkip(t *testing.T) {
	sleep := func(context.Context, time.Duration) error {
		t.Fatal("unlimited budget must not wait")
		return nil
	}
	now := func() time.Time { return time.Unix(1_700_000_000, 0) }
	ctx := context.Background()
	var b peerRPCBudget
	require.NoError(t, b.take(ctx, rpcpbconnect.SessionServiceGetDiffsProcedure, now, sleep))
	b.apply(nil, now())
	require.NoError(t, b.take(ctx, rpcpbconnect.SessionServiceGetDiffsProcedure, now, sleep))
	b.apply(&rpcpb.RateLimits{}, now())
	require.NoError(t, b.take(ctx, rpcpbconnect.SessionServiceGetDiffsProcedure, now, sleep))
	b.apply(&rpcpb.RateLimits{MessagesPerMin: UnlimitedRPCLimit, MessagesBurst: 60}, now())
	require.NoError(t, b.take(ctx, rpcpbconnect.SessionServiceGetDiffsProcedure, now, sleep))
	b.apply(&rpcpb.RateLimits{MessagesPerMin: 1, MessagesBurst: 1}, now())
	require.NoError(t, b.take(ctx, rpcpbconnect.SessionServiceGetDiffsProcedure, now, sleep), "rate below minAdvertisedPeerMessagesPerMin is not advertised")
	b.apply(&rpcpb.RateLimits{MessagesPerMin: minAdvertisedPeerMessagesPerMin - 1, MessagesBurst: 10}, now())
	require.NoError(t, b.take(ctx, rpcpbconnect.SessionServiceGetDiffsProcedure, now, sleep))
	b.apply(&rpcpb.RateLimits{MessagesPerMin: maxAdvertisedPeerMessagesPerMin + 1, MessagesBurst: 60}, now())
	require.NoError(t, b.take(ctx, rpcpbconnect.SessionServiceGetDiffsProcedure, now, sleep))
	b.apply(&rpcpb.RateLimits{MessagesPerMin: 6000, MessagesBurst: maxAdvertisedPeerMessagesPerMin + 1}, now())
	require.NoError(t, b.take(ctx, rpcpbconnect.SessionServiceGetDiffsProcedure, now, sleep), "burst above max is not advertised")
}

func TestPeerRPCBudget_ApplyDoesNotResetTokens(t *testing.T) {
	clock := time.Unix(1_700_000_000, 0)
	var slept time.Duration
	now := func() time.Time { return clock }
	sleep := func(ctx context.Context, d time.Duration) error {
		_ = ctx
		slept += d
		clock = clock.Add(d)
		return nil
	}
	limits := &rpcpb.RateLimits{MessagesPerMin: 6000, MessagesBurst: 60}
	var b peerRPCBudget
	b.apply(limits, now())
	ctx := context.Background()
	require.NoError(t, b.take(ctx, rpcpbconnect.SessionServiceGetDiffsProcedure, now, sleep))
	b.apply(limits, now())
	slept = 0
	require.NoError(t, b.take(ctx, rpcpbconnect.SessionServiceGetDiffsProcedure, now, sleep))
	require.Equal(t, 60*time.Minute/6000, slept, "TTL refresh must not restore a full burst")
}

func TestPeerRPCBudget_RefundRestoresTokens(t *testing.T) {
	sleep := func(context.Context, time.Duration) error {
		t.Fatal("refunded tokens must be available immediately")
		return nil
	}
	now := func() time.Time { return time.Unix(1_700_000_000, 0) }
	var b peerRPCBudget
	b.apply(&rpcpb.RateLimits{MessagesPerMin: 6000, MessagesBurst: 60}, now())
	ctx := context.Background()
	require.NoError(t, b.take(ctx, rpcpbconnect.SessionServiceGetDiffsProcedure, now, sleep))
	b.refund(now(), rpcpbconnect.SessionServiceGetDiffsProcedure)
	require.NoError(t, b.take(ctx, rpcpbconnect.SessionServiceGetDiffsProcedure, now, sleep))
}

func TestPeerRPCBudget_ZeroWeightIsFree(t *testing.T) {
	sleep := func(context.Context, time.Duration) error {
		t.Fatal("Watch/Attach weight 0 must not wait")
		return nil
	}
	now := func() time.Time { return time.Unix(1_700_000_000, 0) }
	var b peerRPCBudget
	b.apply(&rpcpb.RateLimits{MessagesPerMin: 6000, MessagesBurst: 60}, now())
	require.NoError(t, b.take(context.Background(), rpcpbconnect.PeerAuthServiceWatchProcedure, now, sleep))
	require.NoError(t, b.take(context.Background(), rpcpbconnect.PeerAuthServiceAttachProcedure, now, sleep))
}

func TestPeerRPCBudget_WaitExceedingRetryBudgetSkips(t *testing.T) {
	sleep := func(context.Context, time.Duration) error {
		t.Fatal("wait >= retry budget must not park the RPC")
		return nil
	}
	now := func() time.Time { return time.Unix(1_700_000_000, 0) }
	var b peerRPCBudget
	b.apply(&rpcpb.RateLimits{MessagesPerMin: minAdvertisedPeerMessagesPerMin, MessagesBurst: 10}, now())
	ctx := context.Background()
	beforeSkip := testutil.ToFloat64(observability.PeerRPCBudgetWaitSkippedCounter("GetSignatures"))
	require.NoError(t, b.take(ctx, rpcpbconnect.SessionServiceGetDiffsProcedure, now, sleep))
	require.NoError(t, b.take(ctx, rpcpbconnect.SessionServiceGetSignaturesProcedure, now, sleep),
		"overdraft wait at 10/min exceeds 5s; ignore pacing")
	require.Equal(t, 1.0, testutil.ToFloat64(observability.PeerRPCBudgetWaitSkippedCounter("GetSignatures"))-beforeSkip)
}

func TestPeerRPCBudget_WaitExceedingDeadlineSkips(t *testing.T) {
	sleep := func(context.Context, time.Duration) error {
		t.Fatal("wait longer than the RPC deadline must not park")
		return nil
	}
	now := func() time.Time { return time.Unix(1_700_000_000, 0) }
	var b peerRPCBudget
	b.apply(&rpcpb.RateLimits{MessagesPerMin: 6000, MessagesBurst: 60}, now())
	require.NoError(t, b.take(context.Background(), rpcpbconnect.SessionServiceGetDiffsProcedure, now, sleep))
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	t.Cleanup(cancel)
	require.NoError(t, b.take(ctx, rpcpbconnect.SessionServiceGetDiffsProcedure, now, sleep),
		"0.6s refill vs 200ms deadline: ignore pacing (finding 20)")
}

func TestAdvertisedPeerMessages_Range(t *testing.T) {
	rate, burst, unlimited := advertisedPeerMessages(&rpcpb.RateLimits{MessagesPerMin: 6000})
	require.False(t, unlimited)
	require.Equal(t, 6000.0, rate)
	require.Equal(t, 600.0, burst)

	_, _, unlimited = advertisedPeerMessages(&rpcpb.RateLimits{MessagesPerMin: minAdvertisedPeerMessagesPerMin, MessagesBurst: 1})
	require.False(t, unlimited)

	_, _, unlimited = advertisedPeerMessages(&rpcpb.RateLimits{MessagesPerMin: maxAdvertisedPeerMessagesPerMin, MessagesBurst: maxAdvertisedPeerMessagesPerMin})
	require.False(t, unlimited)

	_, _, unlimited = advertisedPeerMessages(&rpcpb.RateLimits{MessagesPerMin: 1})
	require.True(t, unlimited)
	_, _, unlimited = advertisedPeerMessages(&rpcpb.RateLimits{MessagesPerMin: UnlimitedRPCLimit})
	require.True(t, unlimited)
}

func TestAdvertisedMaxStreams_Range(t *testing.T) {
	max, advertised := advertisedMaxStreams(&rpcpb.RateLimits{MaxStreams: 256})
	require.True(t, advertised)
	require.Equal(t, uint32(256), max)
	_, advertised = advertisedMaxStreams(nil)
	require.False(t, advertised)
	_, advertised = advertisedMaxStreams(&rpcpb.RateLimits{})
	require.False(t, advertised)
	_, advertised = advertisedMaxStreams(&rpcpb.RateLimits{MaxStreams: UnlimitedRPCLimit})
	require.False(t, advertised)
	_, advertised = advertisedMaxStreams(&rpcpb.RateLimits{MaxStreams: maxAdvertisedMaxStreams + 1})
	require.False(t, advertised)

	var b peerStreamBudget
	b.apply(&rpcpb.RateLimits{MaxStreams: 1})
	require.True(t, b.acquire())
	require.False(t, b.acquire())
	b.release()
	require.True(t, b.acquire())
	b.apply(&rpcpb.RateLimits{MaxStreams: 0})
	require.True(t, b.acquire(), "not advertised is unlimited on the client")
}

func TestPeerStreamBudget_ApplyPoolCapsAdvertised(t *testing.T) {
	var b peerStreamBudget
	b.applyPool(&rpcpb.RateLimits{MaxStreams: 256}, 4)
	for i := 0; i < 4; i++ {
		require.True(t, b.acquire())
	}
	require.False(t, b.acquire())
	b.release()
	require.True(t, b.acquire())

	var chats peerStreamBudget
	chats.applyPool(&rpcpb.RateLimits{MaxStreams: 256}, 4)
	require.True(t, chats.acquireChat())
	require.True(t, chats.acquireChat())
	require.True(t, chats.acquireChat())
	require.False(t, chats.acquireChat(), "Chat cap is min(advertised, MaxConns)-1")
	require.True(t, chats.acquire(), "Watch still connects")
}

func TestPeerStreamBudget_ChatReservesWatchSlot(t *testing.T) {
	var b peerStreamBudget
	b.apply(&rpcpb.RateLimits{MaxStreams: 2})
	require.True(t, b.acquireChat())
	require.False(t, b.acquireChat(), "Chat cap is max-1")
	require.True(t, b.acquire(), "Watch still connects at Chat cap")
	require.Equal(t, 2, b.inUse)
	require.Equal(t, 1, b.chats)

	b.release()
	require.True(t, b.acquire(), "Watch reconnect after Chat-full")
	b.release()
	b.releaseChat()

	var w peerStreamBudget
	w.apply(&rpcpb.RateLimits{MaxStreams: 2})
	require.True(t, w.acquire())
	require.True(t, w.acquireChat(), "Watch first still leaves a Chat slot")
	require.False(t, w.acquireChat())
	require.Equal(t, 2, w.inUse)
}

func TestRPCBudgetEndpoint(t *testing.T) {
	require.Equal(t, "GetDiffs", rpcBudgetEndpoint(rpcpbconnect.SessionServiceGetDiffsProcedure))
	require.Equal(t, "GetSignatures", rpcBudgetEndpoint(rpcpbconnect.SessionServiceGetSignaturesProcedure))
	require.Equal(t, "other", rpcBudgetEndpoint(""))
	require.Equal(t, "GetDiffs", rpcBudgetEndpoint("GetDiffs"))
}
