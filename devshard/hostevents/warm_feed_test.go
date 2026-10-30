package hostevents_test

import (
	"context"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"common/nodemanager/gen"
	"devshard/bridge"
	"devshard/hostevents"

	abci "github.com/cometbft/cometbft/abci/types"
	ctypes "github.com/cometbft/cometbft/rpc/core/types"
	cmttypes "github.com/cometbft/cometbft/types"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func bothWarmKinds() []gen.HostEventKind {
	return []gen.HostEventKind{
		gen.HostEventKind_HOST_EVENT_KIND_BLOCK,
		gen.HostEventKind_HOST_EVENT_KIND_WARM_KEY_GRANTED,
		gen.HostEventKind_HOST_EVENT_KIND_WARM_KEY_REVOKED,
	}
}

func warmHostEvent(seq uint64, kind gen.HostEventKind, granter, grantee string, id uint64, height int64, end string) *gen.HostEvent {
	return &gen.HostEvent{
		Seq:  seq,
		Kind: kind,
		WarmKey: &gen.WarmKeyPayload{
			Granter: granter,
			Grantee: grantee,
			GrantId: id,
			Height:  height,
			End:     end,
		},
	}
}

func TestPoller_DAPIAndCometBothApplyWarmKeys(t *testing.T) {
	warm := bridge.NewWarmKeyLogCopy(nil, time.Hour)
	srv := &fakeHostEventsServer{}
	srv.SetHandlers(func(context.Context, *gen.GetHostEventsRequest) (*gen.GetHostEventsResponse, error) {
		return &gen.GetHostEventsResponse{
			Generation: 1, NextCursor: 2, SupportedKinds: bothWarmKinds(),
			Events: []*gen.HostEvent{
				warmHostEvent(1, gen.HostEventKind_HOST_EVENT_KIND_WARM_KEY_GRANTED, "cold", "warm", 1, 10, ""),
				warmHostEvent(2, gen.HostEventKind_HOST_EVENT_KIND_WARM_KEY_REVOKED, "cold", "warm", 1, 11, "WARM_KEY_LOG_END_REVOKED"),
			},
		}, nil
	})
	srv.SetSticky(func(context.Context, *gen.GetHostEventsRequest) (*gen.GetHostEventsResponse, error) {
		return &gen.GetHostEventsResponse{Unchanged: true, Generation: 1, NextCursor: 2, SupportedKinds: bothWarmKinds()}, nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p := hostevents.NewPoller(hostevents.PollerConfig{
		Config: hostevents.Config{
			Client: dialFake(t, srv), ServerMaxWait: 20 * time.Millisecond,
			ErrorBackoffMin: time.Millisecond, ErrorBackoffMax: 2 * time.Millisecond,
		},
		Warm: warm,
	})
	go p.Run(ctx)
	require.Eventually(t, func() bool {
		e, found, err := warm.WarmKeyEntry("cold", 1)
		return err == nil && found && e.Ended()
	}, 2*time.Second, 10*time.Millisecond)
	require.True(t, p.WarmFeedLive())
	p.OfferCometWarm([]bridge.WarmKeyEntry{{
		Granter: "cold", ID: 2, Grantee: "other", GrantedAtHeight: 12,
	}})
	e, found, err := warm.WarmKeyEntry("cold", 2)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, "other", e.Grantee)
}

func TestPoller_UnsetHostAppliesCometWarmKey(t *testing.T) {
	warm := bridge.NewWarmKeyLogCopy(nil, time.Hour)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p := hostevents.NewPoller(hostevents.PollerConfig{Warm: warm})
	go p.Run(ctx)
	require.Eventually(t, func() bool { return !p.WarmFeedLive() }, time.Second, 10*time.Millisecond)
	p.OfferCometWarm(hostevents.WarmKeysFromResult(warmGrantResult("cold", "warm", 4, 20)))
	require.Eventually(t, func() bool {
		e, found, err := warm.WarmKeyEntry("cold", 4)
		return err == nil && found && e.Grantee == "warm" && !e.Ended()
	}, time.Second, 10*time.Millisecond)
}

func TestPoller_WarmLiveFollowsFeedThenComet(t *testing.T) {
	fetch := &granterCounter{}
	warm := bridge.NewWarmKeyLogCopy(fetch, time.Hour)
	exp := time.Now().Add(time.Hour).Unix()
	warm.Apply(bridge.WarmKeyEntry{Granter: "cold", ID: 1, Grantee: "kept", ExpiresAtUnix: exp})
	warm.Apply(bridge.WarmKeyEntry{Granter: "other", ID: 1, Grantee: "kept", ExpiresAtUnix: exp})
	phase := atomic.Int32{}
	unimplemented := atomic.Int32{}
	srv := &fakeHostEventsServer{}
	srv.SetSticky(func(context.Context, *gen.GetHostEventsRequest) (*gen.GetHostEventsResponse, error) {
		switch phase.Load() {
		case 0:
			if phase.CompareAndSwap(0, 1) {
				return &gen.GetHostEventsResponse{
					Generation: 1, NextCursor: 1, SupportedKinds: bothWarmKinds(),
					Events: []*gen.HostEvent{
						warmHostEvent(1, gen.HostEventKind_HOST_EVENT_KIND_WARM_KEY_GRANTED, "cold", "from-dapi", 7, 30, ""),
					},
				}, nil
			}
			return &gen.GetHostEventsResponse{Unchanged: true, Generation: 1, NextCursor: 1, SupportedKinds: bothWarmKinds()}, nil
		case 1:
			return &gen.GetHostEventsResponse{Unchanged: true, Generation: 1, NextCursor: 1, SupportedKinds: bothWarmKinds()}, nil
		case 2:
			return nil, status.Error(codes.Unavailable, "stall")
		case 3:
			return &gen.GetHostEventsResponse{Unchanged: true, Generation: 1, NextCursor: 1, SupportedKinds: bothWarmKinds()}, nil
		case 4:
			return &gen.GetHostEventsResponse{Unchanged: true, Generation: 1, NextCursor: 1, SupportedKinds: blockKinds()}, nil
		case 5:
			unimplemented.Add(1)
			return nil, status.Error(codes.Unimplemented, "old")
		case 6:
			return &gen.GetHostEventsResponse{Unchanged: true, Generation: 1, NextCursor: 1, SupportedKinds: bothWarmKinds()}, nil
		default:
			return nil, status.Error(codes.Internal, "boom")
		}
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p := hostevents.NewPoller(hostevents.PollerConfig{
		Config: hostevents.Config{
			Client: dialFake(t, srv), ServerMaxWait: 20 * time.Millisecond,
			ErrorBackoffMin: time.Millisecond, ErrorBackoffMax: 2 * time.Millisecond,
		},
		Warm: warm,
	})
	go p.Run(ctx)

	require.Eventually(t, func() bool {
		e, found := liveGrant(warm, "cold", 7)
		return found && e.Grantee == "from-dapi" && p.WarmFeedLive()
	}, 2*time.Second, 10*time.Millisecond)
	p.OfferCometWarm([]bridge.WarmKeyEntry{{Granter: "cold", ID: 8, Grantee: "comet", GrantedAtHeight: 31, ExpiresAtUnix: exp}})
	_, found := liveGrant(warm, "cold", 8)
	require.True(t, found, "comet applies while dapi lists both warm-key kinds")

	coldBefore := fetch.granter("cold")
	otherBefore := fetch.granter("other")
	phase.Store(2)
	require.Eventually(t, func() bool { return !p.WarmFeedLive() }, 2*time.Second, 10*time.Millisecond)
	require.Equal(t, coldBefore, fetch.granter("cold"), "a gap resets the copy and does not read every granter")
	require.Equal(t, otherBefore, fetch.granter("other"))
	require.Zero(t, fetch.granter("stranger"))
	_, err := warm.LiveWarmKeys("cold")
	require.NoError(t, err)
	require.Eventually(t, func() bool { return fetch.granter("cold") == coldBefore+1 }, time.Second, time.Millisecond)
	require.Equal(t, otherBefore, fetch.granter("other"), "a granter nobody asked for is not read")
	p.OfferCometWarm([]bridge.WarmKeyEntry{{Granter: "cold", ID: 8, Grantee: "comet", GrantedAtHeight: 31, ExpiresAtUnix: exp}})
	require.Eventually(t, func() bool {
		e, found := liveGrant(warm, "cold", 8)
		return found && e.Grantee == "comet"
	}, time.Second, 10*time.Millisecond)

	phase.Store(3)
	require.Eventually(t, func() bool { return p.WarmFeedLive() }, 2*time.Second, 10*time.Millisecond)
	p.OfferCometWarm([]bridge.WarmKeyEntry{{Granter: "cold", ID: 9, Grantee: "later", GrantedAtHeight: 32, ExpiresAtUnix: exp}})
	_, found = liveGrant(warm, "cold", 9)
	require.True(t, found)

	beforeOmitted := fetch.granter("cold")
	phase.Store(4)
	require.Eventually(t, func() bool { return !p.WarmFeedLive() }, 2*time.Second, 10*time.Millisecond)
	p.OfferCometWarm([]bridge.WarmKeyEntry{{Granter: "cold", ID: 10, Grantee: "omitted", GrantedAtHeight: 33, ExpiresAtUnix: exp}})
	require.Eventually(t, func() bool {
		_, found := liveGrant(warm, "cold", 10)
		return found
	}, time.Second, 10*time.Millisecond)
	require.Eventually(t, func() bool { return fetch.granter("cold") == beforeOmitted+1 }, time.Second, time.Millisecond,
		"the read after the gap reloads in the background")

	beforeUnimpl := fetch.granter("cold")
	phase.Store(5)
	// The second Unimplemented request is sent only after the first answer,
	// and the fallback reset it starts, were handled.
	require.Eventually(t, func() bool { return unimplemented.Load() >= 2 }, 2*time.Second, time.Millisecond)
	require.False(t, p.WarmFeedLive())
	require.Equal(t, beforeUnimpl, fetch.granter("cold"))
	p.OfferCometWarm([]bridge.WarmKeyEntry{{Granter: "cold", ID: 11, Grantee: "old-dapi", GrantedAtHeight: 34, ExpiresAtUnix: exp}})
	require.Eventually(t, func() bool {
		_, found := liveGrant(warm, "cold", 11)
		return found
	}, time.Second, 10*time.Millisecond)
	require.Eventually(t, func() bool { return fetch.granter("cold") == beforeUnimpl+1 }, time.Second, time.Millisecond)

	phase.Store(6)
	require.Eventually(t, func() bool { return p.WarmFeedLive() }, 2*time.Second, 10*time.Millisecond)
	reads := fetch.total()
	phase.Store(7)
	time.Sleep(30 * time.Millisecond)
	require.True(t, p.WarmFeedLive(), "a generic RPC error leaves dapi as the warm-key writer")
	require.Equal(t, reads, fetch.total())
	p.OfferCometWarm([]bridge.WarmKeyEntry{{Granter: "cold", ID: 12, Grantee: "generic", GrantedAtHeight: 35, ExpiresAtUnix: exp}})
	_, found = liveGrant(warm, "cold", 12)
	require.True(t, found, "a generic RPC error does not drop the Comet socket")

	fetch.setErr(status.Error(codes.Unavailable, "log down"))
	phase.Store(4)
	require.Eventually(t, func() bool { return !p.WarmFeedLive() }, 2*time.Second, 10*time.Millisecond)
	_, held := liveGrant(warm, "cold", 7)
	require.True(t, held, "a failed WarmKeyLog read leaves the held grant answering")
	cached, found, err := warm.WarmKeyEntry("cold", 7)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, "from-dapi", cached.Grantee, "a failed WarmKeyLog read is not cached as not a grant")
}

func TestNoteCometHeight_JumpResetsWithoutReading(t *testing.T) {
	fetch := &granterCounter{}
	warm := bridge.NewWarmKeyLogCopy(fetch, time.Hour)
	exp := time.Now().Add(time.Hour).Unix()
	warm.Apply(bridge.WarmKeyEntry{Granter: "cold", ID: 1, Grantee: "warm", ExpiresAtUnix: exp})
	_, err := warm.LiveWarmKeys("cold")
	require.NoError(t, err)
	before := fetch.granter("cold")
	p := hostevents.NewPoller(hostevents.PollerConfig{Warm: warm})

	p.NoteCometHeight(10)
	p.NoteCometHeight(11)
	require.Equal(t, before, fetch.granter("cold"))
	_, err = warm.LiveWarmKeys("cold")
	require.NoError(t, err)
	require.Equal(t, before, fetch.granter("cold"), "a step of one does not reset")

	p.NoteCometHeight(13)
	require.Equal(t, before, fetch.granter("cold"), "the height jump does not read the chain")
	_, err = warm.LiveWarmKeys("cold")
	require.NoError(t, err)
	require.Eventually(t, func() bool { return fetch.granter("cold") == before+1 }, time.Second, time.Millisecond)
	_, err = warm.LiveWarmKeys("cold")
	require.NoError(t, err)
	require.Equal(t, before+1, fetch.granter("cold"))
	require.Zero(t, fetch.granter("other"))
}

func TestOnCometConnected_ResetsWithoutReading(t *testing.T) {
	fetch := &granterCounter{}
	warm := bridge.NewWarmKeyLogCopy(fetch, time.Hour)
	exp := time.Now().Add(time.Hour).Unix()
	warm.Apply(bridge.WarmKeyEntry{Granter: "cold", ID: 1, Grantee: "warm", ExpiresAtUnix: exp})
	_, err := warm.LiveWarmKeys("cold")
	require.NoError(t, err)
	before := fetch.granter("cold")
	p := hostevents.NewPoller(hostevents.PollerConfig{Warm: warm})
	p.OnCometConnected()
	require.Equal(t, before, fetch.granter("cold"))
	_, err = warm.LiveWarmKeys("cold")
	require.NoError(t, err)
	require.Eventually(t, func() bool { return fetch.granter("cold") == before+1 }, time.Second, time.Millisecond)
}

func TestPoller_UnavailableDoesNotRehydrateEscrow(t *testing.T) {
	warm := bridge.NewWarmKeyLogCopy(nil, time.Hour)
	sink := &copySink{warm: warm}
	phase := atomic.Int32{}
	srv := &fakeHostEventsServer{}
	srv.SetSticky(func(context.Context, *gen.GetHostEventsRequest) (*gen.GetHostEventsResponse, error) {
		switch phase.Load() {
		case 0:
			if phase.CompareAndSwap(0, 1) {
				return &gen.GetHostEventsResponse{
					Generation: 1, NextCursor: 1, SupportedKinds: bothWarmKinds(),
					Events: []*gen.HostEvent{
						{
							Seq: 1, Kind: gen.HostEventKind_HOST_EVENT_KIND_BLOCK,
							Block: &gen.BlockPayload{Height: 8, Hash: []byte{8}, HeaderTimeUnixMs: 8_000},
						},
						warmHostEvent(2, gen.HostEventKind_HOST_EVENT_KIND_WARM_KEY_GRANTED, "cold", "dapi", 3, 8, ""),
					},
				}, nil
			}
			return &gen.GetHostEventsResponse{Unchanged: true, Generation: 1, NextCursor: 1, SupportedKinds: bothWarmKinds()}, nil
		case 2:
			return nil, status.Error(codes.Unavailable, "stall")
		default:
			return &gen.GetHostEventsResponse{Unchanged: true, Generation: 1, NextCursor: 1, SupportedKinds: bothWarmKinds()}, nil
		}
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p := hostevents.NewPoller(hostevents.PollerConfig{
		Config: hostevents.Config{
			Client: dialFake(t, srv), ServerMaxWait: 20 * time.Millisecond,
			ErrorBackoffMin: time.Millisecond, ErrorBackoffMax: 2 * time.Millisecond,
		},
		IncludeEscrow:  true,
		BlocksVerified: true,
		Escrow:         sink,
		Warm:           warm,
	})
	go p.Run(ctx)
	require.Eventually(t, func() bool {
		_, found, err := warm.WarmKeyEntry("cold", 3)
		return err == nil && found && p.WarmFeedLive() && p.BlockFeedLive()
	}, 2*time.Second, 10*time.Millisecond)
	_, _, rehydrate := sink.snapshot()
	require.Equal(t, 1, rehydrate)
	phase.Store(2)
	require.Eventually(t, func() bool { return !p.WarmFeedLive() && !p.BlockFeedLive() }, 2*time.Second, 10*time.Millisecond)
	_, _, rehydrate = sink.snapshot()
	require.Equal(t, 1, rehydrate, "a stall does not rehydrate escrow")
	e, found, err := warm.WarmKeyEntry("cold", 3)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, "dapi", e.Grantee)
	p.OfferCometWarm([]bridge.WarmKeyEntry{{Granter: "cold", ID: 4, Grantee: "comet", GrantedAtHeight: 9}})
	_, found, err = warm.WarmKeyEntry("cold", 4)
	require.NoError(t, err)
	require.True(t, found)
	phase.Store(3)
	require.Eventually(t, func() bool { return p.WarmFeedLive() }, 2*time.Second, 10*time.Millisecond)
	p.OfferCometWarm([]bridge.WarmKeyEntry{{Granter: "cold", ID: 5, Grantee: "after", GrantedAtHeight: 10}})
	_, found, err = warm.WarmKeyEntry("cold", 5)
	require.NoError(t, err)
	require.True(t, found)
}

func TestWarmKeysFromResult_PointerGrantSkipsFailedTx(t *testing.T) {
	warm := bridge.NewWarmKeyLogCopy(nil, time.Hour)
	p := hostevents.NewPoller(hostevents.PollerConfig{Warm: warm})
	tx := &cmttypes.EventDataTx{TxResult: abci.TxResult{
		Height: 10,
		Result: abci.ExecTxResult{Events: []abci.Event{
			warmABCI("warm_key_granted", "cold", "warm", "4", "10", ""),
		}},
	}}
	p.OfferCometWarm(hostevents.WarmKeysFromResult(ctypes.ResultEvent{Data: tx}))
	got, found, err := warm.WarmKeyEntry("cold", 4)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, "warm", got.Grantee)

	failed := *tx
	failed.TxResult.Result.Code = 1
	require.Empty(t, hostevents.WarmKeysFromResult(ctypes.ResultEvent{Data: &failed}))
	_, found, err = warm.WarmKeyEntry("cold", 4)
	require.NoError(t, err)
	require.True(t, found, "the failed delivery does not remove the grant already applied")

	block := cmttypes.MakeBlock(21, nil, nil, nil)
	expiry := hostevents.WarmKeysFromResult(ctypes.ResultEvent{Data: &cmttypes.EventDataNewBlock{
		Block: block,
		ResultFinalizeBlock: abci.ResponseFinalizeBlock{Events: []abci.Event{
			warmABCI("warm_key_revoked", "cold", "warm", "4", "", "WARM_KEY_LOG_END_EXPIRED"),
		}},
	}})
	require.Len(t, expiry, 1)
	require.Equal(t, int64(21), expiry[0].RevokedAtHeight)
}

func TestWarmKeysFromResult_ReplaceAndExpiry(t *testing.T) {
	warm := bridge.NewWarmKeyLogCopy(nil, time.Hour)
	p := hostevents.NewPoller(hostevents.PollerConfig{Warm: warm})
	replace := ctypes.ResultEvent{Data: cmttypes.EventDataTx{TxResult: abci.TxResult{
		Height: 10,
		Result: abci.ExecTxResult{Events: []abci.Event{
			{Type: "message", Attributes: []abci.EventAttribute{{Key: "action", Value: "grant"}}},
			warmABCI("warm_key_revoked", "cold", "old", "1", "10", "WARM_KEY_LOG_END_REVOKED"),
			warmABCI("warm_key_granted", "cold", "new", "2", "10", ""),
		}},
	}}}
	first := hostevents.WarmKeysFromResult(replace)
	require.Len(t, first, 2)
	require.True(t, first[0].Ended())
	require.Equal(t, uint64(1), first[0].ID)
	require.Equal(t, uint64(2), first[1].ID)
	require.False(t, first[1].Ended())
	p.OfferCometWarm(first)
	p.OfferCometWarm(hostevents.WarmKeysFromResult(replace))
	ended, found, err := warm.WarmKeyEntry("cold", 1)
	require.NoError(t, err)
	require.True(t, found)
	require.True(t, ended.Ended())
	live, found, err := warm.WarmKeyEntry("cold", 2)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, "new", live.Grantee)
	p.OfferCometWarm(hostevents.WarmKeysFromResult(warmGrantResult("cold", "old", 1, 10)))
	ended, found, err = warm.WarmKeyEntry("cold", 1)
	require.NoError(t, err)
	require.True(t, found)
	require.True(t, ended.Ended(), "replaying the grant does not revive the ended id")

	block := cmttypes.MakeBlock(21, nil, nil, nil)
	expiry := ctypes.ResultEvent{Data: cmttypes.EventDataNewBlock{
		Block: block,
		ResultFinalizeBlock: abci.ResponseFinalizeBlock{Events: []abci.Event{
			warmABCI("warm_key_revoked", "cold", "new", "2", "21", "WARM_KEY_LOG_END_EXPIRED"),
		}},
	}}
	got := hostevents.WarmKeysFromResult(expiry)
	require.Len(t, got, 1)
	require.Equal(t, "WARM_KEY_LOG_END_EXPIRED", got[0].End)
	require.Equal(t, int64(21), got[0].RevokedAtHeight)
	p.OfferCometWarm(got)
	ended, found, err = warm.WarmKeyEntry("cold", 2)
	require.NoError(t, err)
	require.True(t, found)
	require.True(t, ended.Ended())
	require.False(t, p.WarmFeedLive())
}

// copySink records escrow rehydrates and applies warm-key events to the copy,
// which is what devshardd's escrow sink does.
type copySink struct {
	recordingSink
	warm *bridge.WarmKeyLogCopy
}

func (s *copySink) OnWarmKeyEvent(e bridge.WarmKeyEntry) error {
	if err := s.recordingSink.OnWarmKeyEvent(e); err != nil {
		return err
	}
	if s.warm != nil {
		s.warm.Apply(e)
	}
	return nil
}

// liveGrant reads the copy. WarmKeyEntry falls through to a chain poll when
// the id is absent, which hides a grant that arrives while that poll waits.
func liveGrant(cp *bridge.WarmKeyLogCopy, granter string, id uint64) (bridge.WarmKeyEntry, bool) {
	live, err := cp.LiveWarmKeys(granter)
	if err != nil {
		return bridge.WarmKeyEntry{}, false
	}
	for _, e := range live {
		if e.ID == id {
			return e, true
		}
	}
	return bridge.WarmKeyEntry{}, false
}

func warmGrantResult(granter, grantee string, id uint64, height int64) ctypes.ResultEvent {
	return ctypes.ResultEvent{Data: cmttypes.EventDataTx{TxResult: abci.TxResult{
		Height: height,
		Result: abci.ExecTxResult{Events: []abci.Event{
			warmABCI("warm_key_granted", granter, grantee, u64(id), i64(height), ""),
		}},
	}}}
}

func warmABCI(typ, granter, grantee, id, height, end string) abci.Event {
	attrs := []abci.EventAttribute{
		{Key: "granter", Value: granter},
		{Key: "grantee", Value: grantee},
		{Key: "grant_id", Value: id},
		{Key: "height", Value: height},
	}
	if end != "" {
		attrs = append(attrs, abci.EventAttribute{Key: "end", Value: end})
	}
	return abci.Event{Type: typ, Attributes: attrs}
}

func u64(n uint64) string { return strconv.FormatUint(n, 10) }

func i64(n int64) string { return strconv.FormatInt(n, 10) }

type granterCounter struct {
	mu   sync.Mutex
	n    map[string]int
	err  error
	logs map[string][]bridge.WarmKeyEntry
}

func (f *granterCounter) setErr(err error) {
	f.mu.Lock()
	f.err = err
	f.mu.Unlock()
}

func (f *granterCounter) granter(g string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.n[g]
}

func (f *granterCounter) total() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	sum := 0
	for _, n := range f.n {
		sum += n
	}
	return sum
}

func (f *granterCounter) FetchWarmKeyLog(_ context.Context, granter string) ([]bridge.WarmKeyEntry, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.n == nil {
		f.n = make(map[string]int)
	}
	f.n[granter]++
	if f.err != nil {
		return nil, f.err
	}
	return append([]bridge.WarmKeyEntry(nil), f.logs[granter]...), nil
}

func (f *granterCounter) FetchWarmKeyEntry(ctx context.Context, granter string, id uint64) (bridge.WarmKeyEntry, bool, error) {
	entries, err := f.FetchWarmKeyLog(ctx, granter)
	if err != nil {
		return bridge.WarmKeyEntry{}, false, err
	}
	for _, e := range entries {
		if e.ID == id {
			return e, true, nil
		}
	}
	return bridge.WarmKeyEntry{}, false, nil
}
