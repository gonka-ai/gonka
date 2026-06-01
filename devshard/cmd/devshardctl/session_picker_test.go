package main

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"devshard/user"
)

// fakeGhost records ghost-probe dispatches so tests can assert how
// many were burned, for what reason, and on which host/nonce.
// Production Redundancy.runGhostProbe is also non-sending (it logs
// and returns); the fake just captures the call so assertions can
// check the exact branch that fired.
type fakeGhost struct {
	mu      sync.Mutex
	count   int32
	reasons []string
	kinds   []ghostKind
	hosts   []int
	nonces  []uint64
}

func (g *fakeGhost) dispatch(prepared *user.PreparedInference, kind ghostKind, reason string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	atomic.AddInt32(&g.count, 1)
	g.reasons = append(g.reasons, reason)
	g.kinds = append(g.kinds, kind)
	g.hosts = append(g.hosts, prepared.HostIdx())
	g.nonces = append(g.nonces, prepared.Nonce())
}

func (g *fakeGhost) kindCount(want ghostKind) int {
	g.mu.Lock()
	defer g.mu.Unlock()
	n := 0
	for _, k := range g.kinds {
		if k == want {
			n++
		}
	}
	return n
}

func (g *fakeGhost) total() int { return int(atomic.LoadInt32(&g.count)) }

// pickerEnv builds a 3-host real Session and a sessionPicker bound to
// a fakeGhost dispatcher. Tests submit pickerRequests directly and
// observe both reply outcomes and ghost dispatches without going
// through Redundancy.RunInference.
func pickerEnv(t *testing.T) (*sessionPicker, *user.Session, *fakeGhost) {
	t.Helper()
	env := setupTestProxy(t, 3, nil, true)
	env.proxy.redundancy.picker.stop()
	ghost := &fakeGhost{}
	p := newSessionPicker(env.session, "llama", ghost.dispatch, nil, nil)
	p.start()
	t.Cleanup(p.stop)
	return p, env.session, ghost
}

func defaultPickerRequest() *pickerRequest {
	return &pickerRequest{
		params: defaultParams(),
		ctx:    context.Background(),
		reply:  make(chan pickerResult, 1),
	}
}

// TestPicker_NoExclude_DispatchesNormally is the baseline: with an empty
// exclude set, every submitted request gets a normal (non-probe)
// dispatch on the host the next nonce maps to, and no ghost is fired.
func TestPicker_NoExclude_DispatchesNormally(t *testing.T) {
	p, _, ghost := pickerEnv(t)

	req := defaultPickerRequest()
	p.submit(req)

	res := waitReply(t, req, 2*time.Second)
	require.NoError(t, res.err)
	require.NotNil(t, res.prepared)
	require.False(t, res.isProbe, "no exclude → no probe")
	require.Equal(t, uint64(1), res.prepared.Nonce())
	require.Equal(t, 1, res.prepared.HostIdx())
	require.Equal(t, 0, ghost.total(), "no ghost expected for clean dispatch")
}

func TestPicker_PrepareErrorRepliesToChosenRequest(t *testing.T) {
	env := setupTestProxyWithBalance(t, 3, nil, true, 100)
	env.proxy.redundancy.picker.stop()
	ghost := &fakeGhost{}
	p := newSessionPicker(env.session, "llama", ghost.dispatch, nil, nil)
	p.start()
	t.Cleanup(p.stop)

	req := defaultPickerRequest()
	p.submit(req)

	res := waitReply(t, req, 2*time.Second)
	require.Error(t, res.err)
	require.ErrorContains(t, res.err, "mandatory start inference")
	require.Nil(t, res.prepared)
	require.Equal(t, uint64(0), env.session.Nonce(), "failed prepare must not burn nonce")
	require.Equal(t, 0, ghost.total(), "real request prepare failure must not ghost-dispatch")
}

func TestPicker_PrepareErrorWhileGhostingDrainsQueue(t *testing.T) {
	env := setupTestProxyWithBalance(t, 3, nil, true, 0)
	env.proxy.redundancy.picker.stop()
	ghost := &fakeGhost{}
	p := newSessionPicker(env.session, "llama", ghost.dispatch, nil, nil)
	p.start()
	t.Cleanup(p.stop)

	req := defaultPickerRequest()
	req.excludeParticipants = map[string]bool{env.session.HostParticipantKey(1): true}
	p.submit(req)

	res := waitReply(t, req, 2*time.Second)
	require.Error(t, res.err)
	require.ErrorContains(t, res.err, "mandatory start inference")
	require.Nil(t, res.prepared)
	require.Equal(t, uint64(0), env.session.Nonce(), "failed ghost burn must not advance nonce")
	require.Equal(t, 0, ghost.total(), "failed ghost prepare must not dispatch")
}

// TestPicker_ExcludeNextHost_GhostBurnsThenMatches: when the only
// queued request excludes the participant on the next host, the
// picker holds for the stale threshold, ghost-burns the nonce on that
// host (request stays queued), then matches the request on the next
// compatible nonce.
//
// Critical property: the real request is NEVER dispatched on the
// excluded participant, and the request is NEVER turned into a probe.
func TestPicker_ExcludeNextHost_GhostBurnsThenMatches(t *testing.T) {
	p, session, ghost := pickerEnv(t)

	// Next nonce is 1 → host 1. Exclude that participant.
	req := defaultPickerRequest()
	req.excludeParticipants = map[string]bool{session.HostParticipantKey(1): true}
	p.submit(req)

	res := waitReply(t, req, 2*time.Second)
	require.NoError(t, res.err)
	require.False(t, res.isProbe, "real request must never be probe-burned")
	require.NotEqual(t, 1, res.prepared.HostIdx(), "must not dispatch on excluded host 1")

	// At least one ghost should have been fired on the excluded host
	// to keep the nonce stream moving.
	require.GreaterOrEqual(t, ghost.total(), 1, "ghost expected for excluded host")
	require.Contains(t, ghost.reasons, ghostExclude.reason())
}

// TestPicker_PrefersCompatibleOverGhost: among multiple queued
// requests, the picker dispatches the one whose exclude set permits
// the nonce's host. The compatible request must be picked even when
// it is not the head of the queue.
func TestPicker_PrefersCompatibleOverGhost(t *testing.T) {
	p, session, _ := pickerEnv(t)

	req1 := defaultPickerRequest()
	req1.excludeParticipants = map[string]bool{session.HostParticipantKey(1): true}
	req2 := defaultPickerRequest()

	p.submit(req1)
	// Submit req2 within the stale-hold window so the picker can match
	// it on nonce 1 instead of ghost-burning. With pickerStaleThreshold=200ms
	// a few-millisecond gap is plenty.
	time.Sleep(20 * time.Millisecond)
	p.submit(req2)

	r1 := waitReply(t, req1, 2*time.Second)
	r2 := waitReply(t, req2, 2*time.Second)

	require.NoError(t, r1.err)
	require.NoError(t, r2.err)
	require.False(t, r1.isProbe, "req1 must dispatch as a real request, never probe")
	require.False(t, r2.isProbe, "req2 must dispatch as a real request, never probe")
	require.NotEqual(t, 1, r1.prepared.HostIdx(), "req1 excludes host 1")
}

// TestPicker_DropsCanceledRequest verifies that a request whose
// context is canceled before it is dispatched receives the cancel
// error and does not consume a nonce.
func TestPicker_DropsCanceledRequest(t *testing.T) {
	p, session, ghost := pickerEnv(t)
	nonceBefore := session.Nonce()

	ctx, cancel := context.WithCancel(context.Background())
	req := defaultPickerRequest()
	req.ctx = ctx
	cancel()

	p.submit(req)

	res := waitReply(t, req, 2*time.Second)
	require.Error(t, res.err)
	require.True(t, errors.Is(res.err, context.Canceled),
		"canceled request should report context.Canceled, got %v", res.err)
	require.Nil(t, res.prepared)
	require.Equal(t, nonceBefore, session.Nonce(),
		"canceled request must not consume a nonce")
	require.Equal(t, 0, ghost.total(), "no ghost should fire when queue is fully canceled")
}

// TestPicker_StopRejectsSubmissions: after Stop() returns, submit()
// must immediately reject new requests with errPickerStopped instead
// of enqueueing them and leaking.
func TestPicker_StopRejectsSubmissions(t *testing.T) {
	env := setupTestProxy(t, 3, nil, true)
	env.proxy.redundancy.picker.stop()
	p := newSessionPicker(env.session, "llama", (&fakeGhost{}).dispatch, nil, nil)
	p.start()

	p.stop()

	req := defaultPickerRequest()
	p.submit(req)

	res := waitReply(t, req, 2*time.Second)
	require.Error(t, res.err)
	require.True(t, errors.Is(res.err, errPickerStopped),
		"submit after stop should return errPickerStopped, got %v", res.err)
	require.Nil(t, res.prepared)
}

// TestPicker_StaleThresholdHonored: a single request that excludes
// the next host should NOT see a ghost burn for at least the stale
// threshold. The picker holds the nonce hoping for compatible
// traffic before falling back to the ghost.
func TestPicker_StaleThresholdHonored(t *testing.T) {
	p, session, ghost := pickerEnv(t)

	req := defaultPickerRequest()
	// Exclude the participant on host 1; next nonce maps to host 1.
	req.excludeParticipants = map[string]bool{session.HostParticipantKey(1): true}
	start := time.Now()
	p.submit(req)

	res := waitReply(t, req, 2*time.Second)
	require.NoError(t, res.err)
	require.False(t, res.isProbe)

	// Either:
	//  (a) we got matched to host 2 after the stale threshold burned
	//      the host-1 nonce as ghostExclude, OR
	//  (b) we got matched to a later compatible nonce because the
	//      session advanced on its own.
	// In either case, the wait should be at least pickerStaleThreshold
	// (the hold) when a ghost burn happened.
	if ghost.total() > 0 {
		elapsed := time.Since(start)
		require.GreaterOrEqual(t, elapsed, pickerStaleThreshold-50*time.Millisecond,
			"ghost burn should respect stale threshold; burned after %s", elapsed)
	}
}

// TestPicker_NoAvailableHost_DropsImmediately: a request whose
// exclude set covers every currently-available host (non-PoC AND
// non-throttled; see computeAvailableParticipants) is dropped
// immediately by the picker's per-iteration exhaustion sweep. No
// queue wait, no ghost burn, no nonce consumed.
//
// This is the static case (every host appears available on the
// wire, but the request has already tried them all). The dynamic
// counterparts -- a host flipping OUT of availability mid-queue --
// are exercised by:
//
//   - TestPicker_PoCFlipDropsQueuedRequest                (PoC flip)
//   - TestPicker_AllRemainingHostsThrottled_DropsExhausted (throttle flip)
func TestPicker_NoAvailableHost_DropsImmediately(t *testing.T) {
	p, session, ghost := pickerEnv(t)
	nonceBefore := session.Nonce()

	// Exclude every distinct participant in the 3-host group.
	req := defaultPickerRequest()
	req.excludeParticipants = map[string]bool{}
	for _, key := range session.ParticipantKeys() {
		req.excludeParticipants[key] = true
	}

	start := time.Now()
	p.submit(req)

	res := waitReply(t, req, 2*time.Second)
	elapsed := time.Since(start)
	require.Error(t, res.err)
	require.True(t, errors.Is(res.err, ErrNoAvailableHost),
		"expected ErrNoAvailableHost, got %v", res.err)
	require.Less(t, elapsed, 200*time.Millisecond,
		"exhaustion should be near-instant; took %s", elapsed)
	require.Equal(t, nonceBefore, session.Nonce(),
		"dropped request must not consume a real nonce")
	require.Equal(t, 0, ghost.total(),
		"dropped request must not trigger a ghost burn")
}

// TestPicker_PoCFlipDropsQueuedRequest: a request that was viable at
// submit time is dropped on the next iteration if a PoC transition
// strands it. Setup: 3 hosts, request excludes hosts 0 and 2, host 1
// becomes PoC-required → only-viable host disappears → request drops
// with ErrNoAvailableHost without ever dispatching.
//
// The throttle-flip equivalent (same shape, different source of the
// flip) is TestPicker_AllRemainingHostsThrottled_DropsExhausted.
func TestPicker_PoCFlipDropsQueuedRequest(t *testing.T) {
	// Enable relaxed PoC so shouldUseProbeForParticipant can return
	// true for unpreserved participants.
	setPoCModeForTest(t, pocRequestModeRelaxed)
	t.Cleanup(func() { setPoCPreservedParticipantsByModel(nil) })

	env := setupTestProxy(t, 3, nil, true)
	env.proxy.phaseGate = &ChainPhaseGate{}
	env.proxy.phaseGate.storeSnapshot(ChainPhaseSnapshot{
		EpochPhase:      epochPhasePoCGenerate,
		RequestsBlocked: false,
		BlockReason:     "poc",
	})
	// Initially: every participant preserved (no host is PoC).
	keys := env.session.ParticipantKeys()
	setPoCPreservedParticipantsByModel(map[string][]string{"llama": keys})
	env.proxy.redundancy.picker.stop()

	ghost := &fakeGhost{}
	p := newSessionPicker(env.session, "llama", ghost.dispatch, nil, nil)
	p.start()
	t.Cleanup(p.stop)

	// Submit: excludes participants on slots 0 and 2; only the
	// participant on slot 1 is viable. With every participant
	// preserved, this is fine and would eventually match.
	req := defaultPickerRequest()
	req.excludeParticipants = map[string]bool{
		env.session.HostParticipantKey(0): true,
		env.session.HostParticipantKey(2): true,
	}
	p.submit(req)

	// Flip the participant for host 1 out of the preserved set.
	// All other hosts stay preserved; only host 1 becomes PoC-required.
	host1Key := env.session.HostParticipantKey(1)
	var remaining []string
	for _, k := range keys {
		if k != host1Key {
			remaining = append(remaining, k)
		}
	}
	setPoCPreservedParticipantsByModel(map[string][]string{"llama": remaining})
	p.wakeUp() // force re-evaluation

	res := waitReply(t, req, 2*time.Second)
	require.Error(t, res.err)
	require.True(t, errors.Is(res.err, ErrNoAvailableHost),
		"PoC flip stranding the only viable host should drop with ErrNoAvailableHost, got %v", res.err)
}

// TestPicker_MultiSlotParticipantTreatedAsOne: when a single
// participant occupies multiple slots in the group, excluding that
// participant must prevent dispatch on every slot it owns. This is
// the regression guard for the slot-vs-participant bug: if exclude
// were keyed by slot index, a participant with 3 slots could serve
// the same request 3 times.
//
// Setup: 4 slots, but slots 0 and 2 share a participant key. A
// request that has tried that shared participant should NEVER land
// on either slot 0 or slot 2; it must match slot 1 or slot 3.
func TestPicker_MultiSlotParticipantTreatedAsOne(t *testing.T) {
	env := setupTestProxy(t, 4, nil, true)
	env.proxy.redundancy.picker.stop()

	// Override the participant keys so slot 2 reports the same key as
	// slot 0 -- simulating a host that registered for two group slots.
	originalKeys := make([]string, 4)
	for i := range originalKeys {
		originalKeys[i] = env.session.HostParticipantKey(i)
	}
	sharedKey := originalKeys[0]
	env.session.SetParticipantKeys([]string{
		originalKeys[0],
		originalKeys[1],
		sharedKey, // slot 2 mirrors slot 0
		originalKeys[3],
	})

	ghost := &fakeGhost{}
	p := newSessionPicker(env.session, "llama", ghost.dispatch, nil, nil)
	p.start()
	t.Cleanup(p.stop)

	// Exclude the shared participant. Picker must dispatch on slot 1
	// or slot 3, never on 0 or 2 (which both map to sharedKey).
	req := defaultPickerRequest()
	req.excludeParticipants = map[string]bool{sharedKey: true}
	p.submit(req)

	res := waitReply(t, req, 2*time.Second)
	require.NoError(t, res.err)
	require.False(t, res.isProbe, "real request must never be probe-burned")
	gotIdx := res.prepared.HostIdx()
	require.NotEqual(t, 0, gotIdx,
		"slot 0 belongs to excluded participant; must not be picked")
	require.NotEqual(t, 2, gotIdx,
		"slot 2 also belongs to excluded participant; must not be picked")
	require.Contains(t, []int{1, 3}, gotIdx,
		"only non-shared slots should be eligible, got slot %d", gotIdx)
}

// TestPicker_AllSlotsOneParticipantExhaustsImmediately: an extreme
// version of the multi-slot case -- a 3-slot group where every slot
// is held by the same participant. Excluding that one participant
// strands the request immediately with ErrNoAvailableHost, even
// though the slot count is 3 (the old slot-keyed precheck would have
// let this request sit in the queue forever).
func TestPicker_AllSlotsOneParticipantExhaustsImmediately(t *testing.T) {
	env := setupTestProxy(t, 3, nil, true)
	env.proxy.redundancy.picker.stop()

	soleKey := env.session.HostParticipantKey(0)
	env.session.SetParticipantKeys([]string{soleKey, soleKey, soleKey})

	ghost := &fakeGhost{}
	p := newSessionPicker(env.session, "llama", ghost.dispatch, nil, nil)
	p.start()
	t.Cleanup(p.stop)

	req := defaultPickerRequest()
	req.excludeParticipants = map[string]bool{soleKey: true}
	p.submit(req)

	res := waitReply(t, req, 2*time.Second)
	require.Error(t, res.err)
	require.True(t, errors.Is(res.err, ErrNoAvailableHost),
		"single-participant escrow with that participant excluded must drop, got %v", res.err)
	require.Equal(t, 0, ghost.total(),
		"no ghost should fire when the queue is dropped on first sweep")
}

// TestPicker_ThrottledHost_BurnsGhostNoSend is the core regression
// guard for the no-send-on-503 behavior: when a host is reactively
// throttled (limiter bucket below 1), the picker burns the next
// nonce that maps to that host as a ghostThrottled probe instead of
// dispatching a real request to it. The dispatcher is expected to
// skip the HTTP call entirely (verified separately at the redundancy
// layer) -- here we assert that the picker:
//
//   - never marks the queued real request as a probe (real request
//     stays a real request, just on a different host),
//   - never picks the throttled host for the real request,
//   - fires at least one ghostThrottled burn so the nonce stream
//     advances past the dead host.
func TestPicker_ThrottledHost_BurnsGhostNoSend(t *testing.T) {
	env := setupTestProxy(t, 3, nil, true)
	env.proxy.redundancy.picker.stop()

	// Throttle the participant on slot 1 (the host the next nonce
	// will bind to). The checker the picker consults is a closure
	// over a map so we can flip throttle state without standing up
	// the real ParticipantRequestLimiter machinery.
	throttledKey := env.session.HostParticipantKey(1)
	throttled := map[string]bool{throttledKey: true}
	checker := func(key string) bool { return throttled[key] }

	ghost := &fakeGhost{}
	p := newSessionPicker(env.session, "llama", ghost.dispatch, checker, nil)
	p.start()
	t.Cleanup(p.stop)

	req := defaultPickerRequest()
	p.submit(req)

	res := waitReply(t, req, 2*time.Second)
	require.NoError(t, res.err)
	require.False(t, res.isProbe, "real request must not be probe-burned")
	require.NotEqual(t, 1, res.prepared.HostIdx(),
		"real request must never land on a throttled host (slot 1)")
	require.GreaterOrEqual(t, ghost.kindCount(ghostThrottled), 1,
		"expected at least one ghostThrottled burn for the dead host")
	// Belt-and-suspenders: the throttled burn must carry the
	// participant_throttled_no_send reason string so operators can
	// distinguish it in logs from PoC / stale-exclude burns.
	require.Contains(t, ghost.reasons, ghostThrottled.reason())
}

func TestPicker_CapabilityBlockedHost_BurnsGhostNoSend(t *testing.T) {
	env := setupTestProxy(t, 3, nil, true)
	env.proxy.redundancy.picker.stop()

	blockedKey := env.session.HostParticipantKey(1)
	checker := func(key string, _ user.InferenceParams) (string, bool) {
		if key == blockedKey {
			return "context_limit_exceeded", true
		}
		return "", false
	}

	ghost := &fakeGhost{}
	p := newSessionPicker(env.session, "llama", ghost.dispatch, nil, checker)
	p.start()
	t.Cleanup(p.stop)

	req := defaultPickerRequest()
	p.submit(req)

	res := waitReply(t, req, 2*time.Second)
	require.NoError(t, res.err)
	require.False(t, res.isProbe)
	require.NotEqual(t, 1, res.prepared.HostIdx(),
		"real request must skip a known capability-incompatible host while other hosts remain")
	require.GreaterOrEqual(t, ghost.kindCount(ghostCapability), 1)
	require.Contains(t, ghost.reasons, ghostCapability.reason())
}

func TestPicker_AllRemainingHostsCapabilityBlocked_DropsExhausted(t *testing.T) {
	env := setupTestProxy(t, 3, nil, true)
	env.proxy.redundancy.picker.stop()

	key0 := env.session.HostParticipantKey(0)
	key1 := env.session.HostParticipantKey(1)
	key2 := env.session.HostParticipantKey(2)
	blocked := map[string]bool{key0: true, key2: true}
	checker := func(key string, _ user.InferenceParams) (string, bool) {
		if blocked[key] {
			return "context_limit_exceeded", true
		}
		return "", false
	}

	ghost := &fakeGhost{}
	p := newSessionPicker(env.session, "llama", ghost.dispatch, nil, checker)
	p.start()
	t.Cleanup(p.stop)

	req := defaultPickerRequest()
	req.excludeParticipants = map[string]bool{key1: true}
	p.submit(req)

	res := waitReply(t, req, 2*time.Second)
	require.ErrorIs(t, res.err, ErrNoAvailableHost)
	require.Equal(t, 0, ghost.kindCount(ghostCapability),
		"exhaustion sweep should drop before burning known-incompatible hosts")
}

// TestPicker_PoCWinsOverThrottle: when a host is BOTH PoC-required
// and throttled the chooser must fire the PoC branch first. Every
// ghost kind is silent on the wire after the all-silent refactor --
// the ordering matters only for log-label correctness, where PoC
// (a phase-level constraint) is the more informative cause than
// throttle (a reactive, host-level signal that will be subsumed by
// PoC until the phase ends).
func TestPicker_PoCWinsOverThrottle(t *testing.T) {
	setPoCModeForTest(t, pocRequestModeRelaxed)
	t.Cleanup(func() { setPoCPreservedParticipantsByModel(nil) })

	env := setupTestProxy(t, 3, nil, true)
	env.proxy.phaseGate = &ChainPhaseGate{}
	env.proxy.phaseGate.storeSnapshot(ChainPhaseSnapshot{
		EpochPhase:      epochPhasePoCGenerate,
		RequestsBlocked: false,
		BlockReason:     "poc",
	})
	// Mark slot 1 as PoC-required by excluding it from preserved.
	keys := env.session.ParticipantKeys()
	host1Key := env.session.HostParticipantKey(1)
	var preserved []string
	for _, k := range keys {
		if k != host1Key {
			preserved = append(preserved, k)
		}
	}
	setPoCPreservedParticipantsByModel(map[string][]string{"llama": preserved})
	env.proxy.redundancy.picker.stop()

	// Also mark slot 1 as throttled. Both gates would fire; the
	// chooser must pick PoC.
	throttled := map[string]bool{host1Key: true}
	checker := func(key string) bool { return throttled[key] }

	ghost := &fakeGhost{}
	p := newSessionPicker(env.session, "llama", ghost.dispatch, checker, nil)
	p.start()
	t.Cleanup(p.stop)

	req := defaultPickerRequest()
	p.submit(req)

	res := waitReply(t, req, 2*time.Second)
	require.NoError(t, res.err)
	require.NotEqual(t, 1, res.prepared.HostIdx(),
		"real request must not land on doubly-disabled slot 1")
	require.GreaterOrEqual(t, ghost.kindCount(ghostPoC), 1,
		"PoC must take precedence over throttle so the log label reflects the phase-level cause")
	require.Equal(t, 0, ghost.kindCount(ghostThrottled),
		"throttled branch must NOT fire when PoC already fired for this host")
}

func TestPicker_PoCFilteringIsModelAware(t *testing.T) {
	setPoCModeForTest(t, pocRequestModeRelaxed)
	t.Cleanup(func() { setPoCPreservedParticipantsByModel(nil) })

	env := setupTestProxy(t, 3, nil, true)
	env.proxy.phaseGate = &ChainPhaseGate{}
	env.proxy.phaseGate.storeSnapshot(ChainPhaseSnapshot{
		EpochPhase:      epochPhasePoCGenerate,
		RequestsBlocked: false,
		BlockReason:     "poc",
	})
	keys := env.session.ParticipantKeys()
	host1Key := env.session.HostParticipantKey(1)
	var llamaPreserved []string
	for _, key := range keys {
		if key != host1Key {
			llamaPreserved = append(llamaPreserved, key)
		}
	}
	setPoCPreservedParticipantsByModel(map[string][]string{
		"llama":       llamaPreserved,
		"other-model": keys,
	})
	env.proxy.redundancy.picker.stop()

	ghost := &fakeGhost{}
	p := newSessionPicker(env.session, "llama", ghost.dispatch, nil, nil)
	p.start()
	t.Cleanup(p.stop)

	req := defaultPickerRequest()
	p.submit(req)

	res := waitReply(t, req, 2*time.Second)
	require.NoError(t, res.err)
	require.False(t, res.isProbe)
	require.NotEqual(t, 1, res.prepared.HostIdx(),
		"host 1 is preserved for another model, but not for llama")
	require.GreaterOrEqual(t, ghost.kindCount(ghostPoC), 1,
		"first llama nonce on host 1 should burn a PoC ghost")
}

// TestPicker_NilThrottleChecker_NoOp: a nil throttleChecker is the
// historical default (NewRedundancy without throttle wiring). It
// must NOT cause the picker to misbehave -- every host is treated
// as non-throttled and the chooser falls through to branch 2.
func TestPicker_NilThrottleChecker_NoOp(t *testing.T) {
	p, _, ghost := pickerEnv(t) // pickerEnv passes nil throttleChecker

	req := defaultPickerRequest()
	p.submit(req)
	res := waitReply(t, req, 2*time.Second)
	require.NoError(t, res.err)
	require.False(t, res.isProbe)
	require.Equal(t, 0, ghost.kindCount(ghostThrottled),
		"nil checker must never fire ghostThrottled")
}

// TestPicker_AllRemainingHostsThrottled_DropsExhausted verifies the
// throttle-flip availability semantics: when every host NOT in the
// request's exclude set is reactively throttled, the exhaustion
// sweep drops the request with ErrNoAvailableHost instead of
// parking it on an endless run of ghostThrottled burns. Recovery at
// 10 tokens/minute is slow enough that fast-failing is the intended
// behavior (see computeAvailableParticipants doc for rationale).
//
// Setup: 3 hosts; the request excludes slot 1; slots 0 and 2 are
// throttled; therefore the available set is empty and the request
// must drop on the first iteration.
//
// This is the throttle analogue of TestPicker_PoCFlipDropsQueuedRequest.
func TestPicker_AllRemainingHostsThrottled_DropsExhausted(t *testing.T) {
	env := setupTestProxy(t, 3, nil, true)
	env.proxy.redundancy.picker.stop()

	key0 := env.session.HostParticipantKey(0)
	key1 := env.session.HostParticipantKey(1)
	key2 := env.session.HostParticipantKey(2)
	throttled := map[string]bool{key0: true, key2: true}
	checker := func(key string) bool { return throttled[key] }

	ghost := &fakeGhost{}
	p := newSessionPicker(env.session, "llama", ghost.dispatch, checker, nil)
	p.start()
	t.Cleanup(p.stop)

	req := defaultPickerRequest()
	req.excludeParticipants = map[string]bool{key1: true}
	p.submit(req)

	res := waitReply(t, req, 2*time.Second)
	require.ErrorIs(t, res.err, ErrNoAvailableHost,
		"request with every non-excluded host throttled must fast-fail, got err=%v prepared=%v", res.err, res.prepared)
	require.Equal(t, 0, ghost.kindCount(ghostThrottled),
		"exhaustion sweep should drop the request before any ghostThrottled burn")
}

// TestPicker_ConcurrentSubmissionsAreSerialized is a smoke test for
// the race detector: many goroutines submit concurrently and expect
// each reply slot to receive exactly one outcome.
func TestPicker_ConcurrentSubmissionsAreSerialized(t *testing.T) {
	p, _, _ := pickerEnv(t)

	const N = 32
	reqs := make([]*pickerRequest, N)
	var wg sync.WaitGroup
	for i := 0; i < N; i++ {
		i := i
		reqs[i] = defaultPickerRequest()
		wg.Add(1)
		go func() {
			defer wg.Done()
			p.submit(reqs[i])
		}()
	}
	wg.Wait()

	for i, req := range reqs {
		res := waitReply(t, req, 5*time.Second)
		require.NoError(t, res.err, "request %d", i)
		require.NotNil(t, res.prepared, "request %d", i)
	}
}

func waitReply(t *testing.T, req *pickerRequest, timeout time.Duration) pickerResult {
	t.Helper()
	select {
	case res := <-req.reply:
		return res
	case <-time.After(timeout):
		t.Fatalf("timed out waiting for picker reply after %s", timeout)
		return pickerResult{}
	}
}
