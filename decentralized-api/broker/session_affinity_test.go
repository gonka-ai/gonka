package broker

import (
	"testing"
	"time"
)

func testNodeAffinity(cfg nodeAffinityConfig) (*nodeSessionAffinity, *time.Time) {
	now := time.Unix(1_700_000_000, 0)
	affinity := newNodeSessionAffinity(cfg)
	affinity.now = func() time.Time { return now }
	return affinity, &now
}

func TestNodeAffinityConfigFromEnvDefaultsDisabled(t *testing.T) {
	t.Setenv("DAPI_MLNODE_AFFINITY_ENABLED", "")
	if nodeAffinityConfigFromEnv().Enabled {
		t.Fatal("affinity must default to disabled when the env var is unset")
	}
}

func TestNodeAffinityConfigFromEnvParsesBoolVariantsLikeDevshardd(t *testing.T) {
	// devshardd's envBoolOr reads this same var, so both processes must agree on what "on" means.
	cases := []struct {
		value string
		want  bool
	}{
		{"true", true},
		{"True", true},
		{"TRUE", true},
		{"1", true},
		{"t", true},
		{"T", true},
		{"false", false},
		{"False", false},
		{"0", false},
		{"f", false},
		{" true ", true},   // a compose/env-file value keeps its padding
		{"\ttrue\n", true}, // and so does a heredoc one
		{"", false},        // unset -> default off
		{"yes", false},     // unparseable -> default off, not an error
	}
	for _, testCase := range cases {
		t.Run(testCase.value, func(t *testing.T) {
			t.Setenv("DAPI_MLNODE_AFFINITY_ENABLED", testCase.value)
			if got := nodeAffinityConfigFromEnv().Enabled; got != testCase.want {
				t.Fatalf("DAPI_MLNODE_AFFINITY_ENABLED=%q: got Enabled=%v, want %v", testCase.value, got, testCase.want)
			}
		})
	}
}

func TestNodeAffinityDisabledIsNoOp(t *testing.T) {
	cfg := defaultNodeAffinityConfig() // Enabled=false
	affinity, _ := testNodeAffinity(cfg)
	affinity.record("escrow-1", "sess", "nodeA")
	if _, ok := affinity.pick("escrow-1", "sess"); ok {
		t.Fatal("disabled affinity must never return a sticky node")
	}
}

func TestNodeAffinityStickThenReRandomiseOnRequestBound(t *testing.T) {
	cfg := nodeAffinityConfig{Enabled: true, MaxRequests: 3, TTL: time.Hour, MaxEntries: 100}
	affinity, _ := testNodeAffinity(cfg)

	if _, ok := affinity.pick("escrow-1", "sess"); ok {
		t.Fatal("first pick must miss")
	}
	affinity.record("escrow-1", "sess", "nodeA") // count=1
	for i := 2; i <= 3; i++ {
		got, ok := affinity.pick("escrow-1", "sess")
		if !ok || got != "nodeA" {
			t.Fatalf("req %d: want sticky nodeA, got %q ok=%v", i, got, ok)
		}
		affinity.record("escrow-1", "sess", "nodeA") // count 2, then 3 -> evict
	}
	if _, ok := affinity.pick("escrow-1", "sess"); ok {
		t.Fatal("after the request bound the session must re-randomise (load rebalance)")
	}
}

func TestNodeAffinityExpiresOnTTL(t *testing.T) {
	cfg := nodeAffinityConfig{Enabled: true, MaxRequests: 1000, TTL: time.Minute, MaxEntries: 100}
	affinity, now := testNodeAffinity(cfg)
	affinity.record("escrow-1", "sess", "nodeA")
	if got, ok := affinity.pick("escrow-1", "sess"); !ok || got != "nodeA" {
		t.Fatalf("within TTL want nodeA, got %q ok=%v", got, ok)
	}
	*now = now.Add(cfg.TTL)
	if _, ok := affinity.pick("escrow-1", "sess"); ok {
		t.Fatal("binding must expire at TTL")
	}
}

func TestNodeAffinityRebindsOnDifferentNode(t *testing.T) {
	cfg := nodeAffinityConfig{Enabled: true, MaxRequests: 5, TTL: time.Hour, MaxEntries: 100}
	affinity, _ := testNodeAffinity(cfg)
	affinity.record("escrow-1", "sess", "nodeA")
	affinity.record("escrow-1", "sess", "nodeA")
	affinity.record("escrow-1", "sess", "nodeB") // fell back to a different node -> rebind fresh
	got, ok := affinity.pick("escrow-1", "sess")
	if !ok || got != "nodeB" {
		t.Fatalf("want rebind to nodeB, got %q ok=%v", got, ok)
	}
}

func TestNodeAffinityMapIsBounded(t *testing.T) {
	cfg := nodeAffinityConfig{Enabled: true, MaxRequests: 1000, TTL: time.Hour, MaxEntries: 100}
	affinity, _ := testNodeAffinity(cfg)
	for i := 0; i < 10*cfg.MaxEntries; i++ {
		affinity.record("escrow-1", sessionKey(i), "nodeA")
	}
	affinity.mu.Lock()
	bindings := len(affinity.byKey)
	affinity.mu.Unlock()
	if bindings > cfg.MaxEntries {
		t.Fatalf("map must stay bounded by MaxEntries=%d, got %d", cfg.MaxEntries, bindings)
	}
}

func TestNodeAffinitySeparatesEscrowsSharingOneSessionID(t *testing.T) {
	cfg := nodeAffinityConfig{Enabled: true, MaxRequests: 5, TTL: time.Hour, MaxEntries: 100}
	affinity, _ := testNodeAffinity(cfg)

	affinity.record("escrow-1", "sess", "nodeA")

	if _, ok := affinity.pick("escrow-2", "sess"); ok {
		t.Fatal("one escrow's binding must not steer another escrow's identical session id")
	}
	affinity.record("escrow-2", "sess", "nodeB")
	if got, ok := affinity.pick("escrow-1", "sess"); !ok || got != "nodeA" {
		t.Fatalf("the first escrow's binding must survive the second's, got %q ok=%v", got, ok)
	}
}

func TestNodeAffinityEmptySessionIsNoOp(t *testing.T) {
	cfg := nodeAffinityConfig{Enabled: true, MaxRequests: 5, TTL: time.Hour, MaxEntries: 100}
	affinity, _ := testNodeAffinity(cfg)
	affinity.record("escrow-1", "", "nodeA")
	if _, ok := affinity.pick("escrow-1", ""); ok {
		t.Fatal("empty session id must never bind")
	}
}

func sessionKey(i int) string {
	if i == 0 {
		return "s0"
	}
	var b [20]byte
	pos := len(b)
	for i > 0 {
		pos--
		b[pos] = byte('0' + i%10)
		i /= 10
	}
	return "s" + string(b[pos:])
}
