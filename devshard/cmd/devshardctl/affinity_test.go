package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"devshard/user"
)

func testAffinity(cfg affinityConfig) (*affinityTracker, *time.Time) {
	now := time.Unix(1_700_000_000, 0)
	tracker := newAffinityTracker(cfg)
	tracker.now = func() time.Time { return now }
	return tracker, &now
}

func alwaysMember(string) bool { return true }

func TestAffinityDisabledIsNoOp(t *testing.T) {
	cfg := defaultAffinityConfig() // Enabled=false
	tracker, _ := testAffinity(cfg)
	tracker.Record("sess", "hostA")
	if _, ok := tracker.Pick("sess", alwaysMember); ok {
		t.Fatal("disabled tracker must never return a sticky host")
	}
}

func TestAffinitySticksThenReRandomisesOnRequestBound(t *testing.T) {
	cfg := defaultAffinityConfig()
	cfg.Enabled = true
	cfg.MaxRequests = 3
	tracker, _ := testAffinity(cfg)

	// First request of the session: no binding yet -> route naturally, record.
	if _, ok := tracker.Pick("sess", alwaysMember); ok {
		t.Fatal("first Pick must miss")
	}
	tracker.Record("sess", "hostA") // count=1

	// Requests 2 and 3 stick to hostA.
	for i := 2; i <= 3; i++ {
		got, ok := tracker.Pick("sess", alwaysMember)
		if !ok || got != "hostA" {
			t.Fatalf("request %d: want sticky hostA, got %q ok=%v", i, got, ok)
		}
		tracker.Record("sess", "hostA") // count=2, then 3 -> evict on the 3rd
	}

	// After MaxRequests (3) the binding is evicted -> re-randomise.
	if _, ok := tracker.Pick("sess", alwaysMember); ok {
		t.Fatal("after the request bound the session must re-randomise")
	}
}

func TestAffinityExpiresOnTTL(t *testing.T) {
	cfg := defaultAffinityConfig()
	cfg.Enabled = true
	cfg.MaxRequests = 100 // ensure TTL is the binding constraint
	cfg.TTL = time.Minute
	tracker, now := testAffinity(cfg)

	tracker.Record("sess", "hostA")
	if got, ok := tracker.Pick("sess", alwaysMember); !ok || got != "hostA" {
		t.Fatalf("within TTL want hostA, got %q ok=%v", got, ok)
	}

	*now = now.Add(cfg.TTL) // exactly TTL later -> expired
	if _, ok := tracker.Pick("sess", alwaysMember); ok {
		t.Fatal("binding must expire at TTL")
	}
}

func TestAffinityDropsHostThatLeftGroup(t *testing.T) {
	cfg := defaultAffinityConfig()
	cfg.Enabled = true
	tracker, _ := testAffinity(cfg)
	tracker.Record("sess", "hostGone")

	onlyOthers := func(p string) bool { return p != "hostGone" }
	if _, ok := tracker.Pick("sess", onlyOthers); ok {
		t.Fatal("a sticky host no longer in the group must be dropped")
	}
	// And the entry is gone, so a fresh natural landing rebinds cleanly.
	tracker.Record("sess", "hostB")
	if got, ok := tracker.Pick("sess", alwaysMember); !ok || got != "hostB" {
		t.Fatalf("want rebind to hostB, got %q ok=%v", got, ok)
	}
}

func TestAffinityRebindsOnFallbackToDifferentHost(t *testing.T) {
	cfg := defaultAffinityConfig()
	cfg.Enabled = true
	cfg.MaxRequests = 5
	tracker, _ := testAffinity(cfg)

	tracker.Record("sess", "hostA") // count=1
	tracker.Record("sess", "hostA") // count=2
	// Primary fell back to a different host (sticky was busy): rebind fresh.
	tracker.Record("sess", "hostB")
	got, ok := tracker.Pick("sess", alwaysMember)
	if !ok || got != "hostB" {
		t.Fatalf("want rebind to hostB, got %q ok=%v", got, ok)
	}
}

// TestAffinityKeyFromDocument covers affinityKeyFromDocument in isolation, on an
// already-decoded document (malformed/empty bodies never reach it -- decodeChatRequestRaw
// rejects them earlier, exercised by the "parse request" pipeline tests instead).
func TestAffinityKeyFromDocument(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{"prompt_cache_key wins", `{"prompt_cache_key":"conv-1","user":"u-9"}`, "conv-1"},
		{"user fallback", `{"user":"u-9"}`, "u-9"},
		{"prefer prompt_cache_key over user", `{"user":"u-9","prompt_cache_key":"conv-1"}`, "conv-1"},
		{"neither", `{"model":"m","messages":[]}`, ""},
		{"non-string prompt_cache_key skips to user", `{"prompt_cache_key":123,"user":"u-9"}`, "u-9"},
		{"non-string both", `{"prompt_cache_key":123,"user":{"id":"x"}}`, ""},
		{"whitespace trimmed to empty", `{"prompt_cache_key":"   "}`, ""},
	}
	for _, c := range cases {
		document, err := decodeChatRequestDocument([]byte(c.body))
		require.NoError(t, err)
		if got := affinityKeyFromDocument(document); got != c.want {
			t.Errorf("%s: affinityKeyFromDocument(%q) = %q, want %q", c.name, c.body, got, c.want)
		}
	}
}

func TestDeriveSessionTokenSeparatesEscrows(t *testing.T) {
	secret := []byte("test-secret")

	first := deriveSessionToken(secret, "escrow-1", "", "user-9")
	second := deriveSessionToken(secret, "escrow-2", "", "user-9")

	require.NotEmpty(t, first)
	require.NotEqual(t, first, second, "one client string under two escrows must derive two session tokens")
}

func TestDeriveSessionTokenSeparatesAuthenticatedCallers(t *testing.T) {
	secret := []byte("test-secret")

	first := deriveSessionToken(secret, "escrow-1", "key-alice", "user-9")
	second := deriveSessionToken(secret, "escrow-1", "key-bob", "user-9")

	require.NotEqual(t, first, second, "two credentials guessing one client string must not share a token")
	require.NotEqual(t, first, deriveSessionToken(secret, "escrow-1", "", "user-9"),
		"an authenticated caller must not share the token of an anonymous one")
}

func TestDeriveSessionTokenIsStableAndHidesTheClientString(t *testing.T) {
	secret := []byte("test-secret")

	token := deriveSessionToken(secret, "escrow-1", "key-alice", "user-9")

	require.Equal(t, token, deriveSessionToken(secret, "escrow-1", "key-alice", "user-9"),
		"a client's own follow-ups must land on the same token, or the cache never warms")
	require.NotContains(t, token, "user-9", "the client's own string must not survive into the wire value")
	require.NotEqual(t, token, deriveSessionToken([]byte("other-secret"), "escrow-1", "key-alice", "user-9"),
		"without the server secret the token must not be reproducible")
}

func TestDeriveSessionTokenEmptyWithoutKeyOrSecret(t *testing.T) {
	require.Empty(t, deriveSessionToken([]byte("test-secret"), "escrow-1", "key-alice", ""),
		"no client string means no affinity")
	require.Empty(t, deriveSessionToken(nil, "escrow-1", "key-alice", "user-9"),
		"a missing secret must fail closed, not fall back to a guessable token")
}

// The participant drops a session id longer than its own maxSessionIDLength (512) and
// salts nothing, so a token that outgrows that bound would disable the feature in silence.
func TestDeriveSessionTokenFitsTheParticipantLengthBound(t *testing.T) {
	const participantMaxSessionIDLength = 512

	token := deriveSessionToken([]byte("test-secret"), "escrow-1", "key-alice", strings.Repeat("k", 4096))

	require.Len(t, token, 64, "the token is a fixed-width sha256 digest, whatever the client sent")
	require.LessOrEqual(t, len(token), participantMaxSessionIDLength,
		"a token over the participant's bound is dropped there, silently turning affinity off")
}

func TestAffinityMapIsBounded(t *testing.T) {
	cfg := defaultAffinityConfig()
	cfg.Enabled = true
	cfg.MaxRequests = 100 // don't let the request bound evict during this test
	cfg.TTL = time.Hour
	cfg.MaxEntries = 100
	tracker, _ := testAffinity(cfg)

	// A stream of distinct, never-repeated session keys (the leak scenario:
	// short conversations that are never Picked again).
	for i := 0; i < 10*cfg.MaxEntries; i++ {
		tracker.Record(sessionKey(i), "hostA")
	}
	tracker.mu.Lock()
	bindings := len(tracker.byKey)
	tracker.mu.Unlock()
	if bindings > cfg.MaxEntries {
		t.Fatalf("map must stay bounded by MaxEntries=%d, got %d", cfg.MaxEntries, bindings)
	}
}

func TestAffinitySweepReclaimsExpiredFirst(t *testing.T) {
	cfg := defaultAffinityConfig()
	cfg.Enabled = true
	cfg.MaxRequests = 100
	cfg.TTL = time.Minute
	cfg.MaxEntries = 10
	tracker, now := testAffinity(cfg)

	// Fill to the cap, then let them all expire.
	for i := 0; i < cfg.MaxEntries; i++ {
		tracker.Record(sessionKey(i), "hostA")
	}
	*now = now.Add(cfg.TTL) // everything above is now expired

	// One more insert triggers a sweep that should reclaim the expired ones,
	// leaving just the fresh entry.
	tracker.Record("fresh", "hostB")
	tracker.mu.Lock()
	bindings := len(tracker.byKey)
	tracker.mu.Unlock()
	if bindings != 1 {
		t.Fatalf("sweep should reclaim expired entries first; want 1, got %d", bindings)
	}
}

func sessionKey(i int) string { return "sess-" + string(rune('a'+i%26)) + "-" + itoa(i) }

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b [20]byte
	pos := len(b)
	for i > 0 {
		pos--
		b[pos] = byte('0' + i%10)
		i /= 10
	}
	return string(b[pos:])
}

// affinityRedundancyEnv wires a Redundancy whose ghost burns are observable and
// whose affinity tracker is enabled.
func affinityRedundancyEnv(t *testing.T) (*testProxyEnv, *fakeGhost) {
	t.Helper()
	env, ghost, picker := stagedAffinityRedundancyEnv(t)
	startPicker(t, picker)
	return env, ghost
}

// stagedAffinityRedundancyEnv is affinityRedundancyEnv with the dispatcher still
// stopped, so a test can stage competing requests. Caller must startPicker.
func stagedAffinityRedundancyEnv(t *testing.T) (*testProxyEnv, *fakeGhost, *sessionPicker) {
	t.Helper()
	env := setupTestProxy(t, 3, nil, true)
	env.proxy.redundancy.picker.stop()
	ghost := &fakeGhost{}
	picker := newSessionPicker(env.session, "llama", ghost.dispatch, nil, nil)
	env.proxy.redundancy.picker = picker
	cfg := defaultAffinityConfig()
	cfg.Enabled = true
	env.proxy.redundancy.affinity = newAffinityTracker(cfg)
	return env, ghost, picker
}

// TestAffinitySessionReturnsToRememberedParticipant: a remembered session takes
// its own participant's nonce ahead of an unbound request queued before it.
func TestAffinitySessionReturnsToRememberedParticipant(t *testing.T) {
	env, ghost, picker := stagedAffinityRedundancyEnv(t)
	remembered := env.session.HostParticipantKey(1)
	env.proxy.redundancy.affinity.Record("sess", remembered)
	params := defaultParams()
	params.AffinityKey = "sess"

	unbound := defaultPickerRequest()
	picker.submit(unbound)
	prepared := make(chan *inflight, 1)
	failed := make(chan error, 1)
	go func() {
		inf, err := env.proxy.redundancy.preparePrimaryWithAffinity(context.Background(), params, map[string]bool{})
		if err != nil {
			failed <- err
			return
		}
		prepared <- inf
	}()
	require.Eventually(t, func() bool { return picker.queueLen() == 2 }, 2*time.Second, time.Millisecond,
		"both requests must be queued before the first nonce is matched")
	startPicker(t, picker)

	select {
	case err := <-failed:
		require.NoError(t, err)
	case inf := <-prepared:
		require.Equal(t, remembered, env.session.HostParticipantKey(inf.hostIdx),
			"the session must land back on the participant it was bound to")
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for the affinity-steered prepare")
	}
	require.NoError(t, waitReply(t, unbound, 2*time.Second).err)
	require.Equal(t, 0, ghost.total())
}

// TestAffinityPrimaryYieldsForeignNonceWithoutBurning: a session bound to a
// participant the next nonce does not bind still costs exactly one nonce.
func TestAffinityPrimaryYieldsForeignNonceWithoutBurning(t *testing.T) {
	env, ghost := affinityRedundancyEnv(t)
	env.proxy.redundancy.affinity.Record("sess", env.session.HostParticipantKey(2))
	params := defaultParams()
	params.AffinityKey = "sess"

	inf, err := env.proxy.redundancy.preparePrimaryWithAffinity(context.Background(), params, map[string]bool{})

	require.NoError(t, err)
	require.Equal(t, 1, inf.hostIdx, "nonce 1 binds host 1, so the sticky preference must yield to it")
	require.Equal(t, 0, ghost.total(), "affinity must not burn a nonce")
	require.EqualValues(t, 1, env.session.Nonce(), "one served request must cost exactly one nonce")
}

// TestAffinityRecordsParticipantThatActuallyServed: the binding follows the
// participant the nonce went to, not the preference that yielded.
func TestAffinityRecordsParticipantThatActuallyServed(t *testing.T) {
	env, _ := affinityRedundancyEnv(t)
	tracker := env.proxy.redundancy.affinity
	tracker.Record("sess", env.session.HostParticipantKey(2))
	params := defaultParams()
	params.AffinityKey = "sess"

	inf, err := env.proxy.redundancy.preparePrimaryWithAffinity(context.Background(), params, map[string]bool{})
	require.NoError(t, err)

	sticky, ok := tracker.Pick("sess", alwaysMember)
	require.True(t, ok, "a served request must leave a binding")
	require.Equal(t, env.session.HostParticipantKey(inf.hostIdx), sticky,
		"binding must name the participant that actually got the nonce")
	require.NotEqual(t, env.session.HostParticipantKey(2), sticky,
		"the yielded preference must not survive as the binding")
}

func TestAffinityEmptyKeyIsNoOp(t *testing.T) {
	cfg := defaultAffinityConfig()
	cfg.Enabled = true
	tracker, _ := testAffinity(cfg)
	tracker.Record("", "hostA")
	if _, ok := tracker.Pick("", alwaysMember); ok {
		t.Fatal("empty affinity key must never bind")
	}
}

// TestAffinityDecisionHitRecordsMetric: the sticky preference actually serves the
// primary, so the decision counter must record "hit" and the gauge one binding.
func TestAffinityDecisionHitRecordsMetric(t *testing.T) {
	env, _ := affinityRedundancyEnv(t)
	env.proxy.redundancy.metrics = NewDevshardMetrics()
	env.proxy.redundancy.devshardID = "escrow-affinity-hit"
	remembered := env.session.HostParticipantKey(1)
	env.proxy.redundancy.affinity.Record("sess", remembered)
	params := defaultParams()
	params.AffinityKey = "sess"

	inf, err := env.proxy.redundancy.preparePrimaryWithAffinity(context.Background(), params, map[string]bool{})

	require.NoError(t, err)
	require.Equal(t, remembered, env.session.HostParticipantKey(inf.hostIdx), "the sticky preference must serve the primary")
	families, err := env.proxy.redundancy.metrics.registry.Gather()
	require.NoError(t, err)
	requireMetricCounterValue(t, families, "devshard_gateway_affinity_decision_total",
		map[string]string{"devshard_id": "escrow-affinity-hit", "decision": "hit"}, 1)
	requireMetricGaugeValue(t, families, "devshard_gateway_affinity_bindings",
		map[string]string{"devshard_id": "escrow-affinity-hit"}, 1)
}

// TestAffinityDecisionYieldedRecordsMetric: a sticky preference existed but the
// picker took a different, foreign nonce -- the decision counter must record "yielded".
func TestAffinityDecisionYieldedRecordsMetric(t *testing.T) {
	env, ghost := affinityRedundancyEnv(t)
	env.proxy.redundancy.metrics = NewDevshardMetrics()
	env.proxy.redundancy.devshardID = "escrow-affinity-yielded"
	env.proxy.redundancy.affinity.Record("sess", env.session.HostParticipantKey(2))
	params := defaultParams()
	params.AffinityKey = "sess"

	inf, err := env.proxy.redundancy.preparePrimaryWithAffinity(context.Background(), params, map[string]bool{})

	require.NoError(t, err)
	require.Equal(t, 1, inf.hostIdx, "nonce 1 binds host 1, so the sticky preference must yield to it")
	require.Equal(t, 0, ghost.total(), "affinity must not burn a nonce")
	families, err := env.proxy.redundancy.metrics.registry.Gather()
	require.NoError(t, err)
	requireMetricCounterValue(t, families, "devshard_gateway_affinity_decision_total",
		map[string]string{"devshard_id": "escrow-affinity-yielded", "decision": "yielded"}, 1)
	requireMetricGaugeValue(t, families, "devshard_gateway_affinity_bindings",
		map[string]string{"devshard_id": "escrow-affinity-yielded"}, 1)
}

// TestAffinityDecisionMissRecordsMetric: a session's first request has no prior
// binding, so the decision counter must record "miss".
func TestAffinityDecisionMissRecordsMetric(t *testing.T) {
	env, _ := affinityRedundancyEnv(t)
	env.proxy.redundancy.metrics = NewDevshardMetrics()
	env.proxy.redundancy.devshardID = "escrow-affinity-miss"
	params := defaultParams()
	params.AffinityKey = "sess-never-seen"

	_, err := env.proxy.redundancy.preparePrimaryWithAffinity(context.Background(), params, map[string]bool{})

	require.NoError(t, err)
	families, err := env.proxy.redundancy.metrics.registry.Gather()
	require.NoError(t, err)
	requireMetricCounterValue(t, families, "devshard_gateway_affinity_decision_total",
		map[string]string{"devshard_id": "escrow-affinity-miss", "decision": "miss"}, 1)
	requireMetricGaugeValue(t, families, "devshard_gateway_affinity_bindings",
		map[string]string{"devshard_id": "escrow-affinity-miss"}, 1)
}

// TestAffinityDisabledEmitsNoMetrics: the whole point of the counter is to answer
// "is affinity working" -- it must stay silent when the feature is off.
func TestAffinityDisabledEmitsNoMetrics(t *testing.T) {
	env := setupTestProxy(t, 3, nil, true)
	env.proxy.redundancy.metrics = NewDevshardMetrics()
	env.proxy.redundancy.devshardID = "escrow-affinity-disabled"
	params := defaultParams()
	params.AffinityKey = "sess"

	_, err := env.proxy.redundancy.preparePrimaryWithAffinity(context.Background(), params, map[string]bool{})
	require.NoError(t, err)

	families, err := env.proxy.redundancy.metrics.registry.Gather()
	require.NoError(t, err)
	for _, family := range families {
		require.NotEqual(t, "devshard_gateway_affinity_decision_total", family.GetName(), "disabled affinity must never write the decision counter")
		require.NotEqual(t, "devshard_gateway_affinity_bindings", family.GetName(), "disabled affinity must never write the bindings gauge")
	}
}

// TestAffinityBindingsGaugeTracksTrackerSize: after N bindings the gauge must equal
// the tracker's live map size, read through its own mutex-guarded Size method.
func TestAffinityBindingsGaugeTracksTrackerSize(t *testing.T) {
	cfg := defaultAffinityConfig()
	cfg.Enabled = true
	cfg.MaxRequests = 100 // don't let the request bound evict during this test
	cfg.TTL = time.Hour
	tracker, _ := testAffinity(cfg)
	metrics := NewDevshardMetrics()

	const bindingCount = 5
	for i := 0; i < bindingCount; i++ {
		tracker.Record(sessionKey(i), "hostA")
		metrics.SetAffinityBindings("escrow-gauge", tracker.Size())
	}

	families, err := metrics.registry.Gather()
	require.NoError(t, err)
	requireMetricGaugeValue(t, families, "devshard_gateway_affinity_bindings",
		map[string]string{"devshard_id": "escrow-gauge"}, bindingCount)
}

// A timeout re-execution that loses the session id runs the same prompt into the shared,
// unsalted cache namespace -- the isolation the first attempt bought, given back.
func TestTimeoutPayloadCarriesSessionID(t *testing.T) {
	params := user.InferenceParams{
		Prompt:      []byte(`{"model":"m"}`),
		Model:       "model-a",
		InputLength: 12,
		MaxTokens:   50,
		StartedAt:   1000,
		AffinityKey: "sess-A",
	}

	payload := timeoutPayload(params)

	require.Equal(t, "sess-A", payload.SessionID, "a timeout re-execution must salt the same cache namespace as the first attempt")
	require.Equal(t, params.Model, payload.Model)
	require.Equal(t, params.MaxTokens, payload.MaxTokens)
}

func TestMembershipTestRejectsNonMembers(t *testing.T) {
	isMember := membershipTest([]string{"hostA", "hostB"})

	require.True(t, isMember("hostA"))
	require.False(t, isMember("hostGone"), "a participant that left the group must not stay sticky")
	require.False(t, membershipTest(nil)("hostA"), "an empty group has no members")
}
