package sessionaffinity

import (
	"testing"
	"time"
)

func testTracker(cfg Config) (*Tracker, *time.Time) {
	now := time.Unix(1_700_000_000, 0)
	affinity := New(cfg, nil)
	affinity.now = func() time.Time { return now }
	return affinity, &now
}

func TestConfigFromEnvDefaultsDisabled(t *testing.T) {
	t.Setenv("DAPI_MLNODE_AFFINITY_ENABLED", "")
	if ConfigFromEnv().Enabled {
		t.Fatal("affinity must default to disabled when the env var is unset")
	}
}

func TestConfigFromEnvParsesBoolVariantsLikeDevshardd(t *testing.T) {
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
			if got := ConfigFromEnv().Enabled; got != testCase.want {
				t.Fatalf("DAPI_MLNODE_AFFINITY_ENABLED=%q: got Enabled=%v, want %v", testCase.value, got, testCase.want)
			}
		})
	}
}

func TestDisabledIsNoOp(t *testing.T) {
	cfg := defaultConfig() // Enabled=false
	affinity, _ := testTracker(cfg)
	affinity.Record("escrow-1", "sess", "nodeA")
	if affinity.StickyNode("escrow-1", "sess") != "" {
		t.Fatal("disabled affinity must never return a sticky node")
	}
}

func TestStickThenReRandomiseOnRequestBound(t *testing.T) {
	cfg := Config{Enabled: true, MaxRequests: 3, TTL: time.Hour, MaxEntries: 100}
	affinity, _ := testTracker(cfg)

	if affinity.StickyNode("escrow-1", "sess") != "" {
		t.Fatal("first pick must miss")
	}
	affinity.Record("escrow-1", "sess", "nodeA") // count=1
	for i := 2; i <= 3; i++ {
		got := affinity.StickyNode("escrow-1", "sess")
		if got != "nodeA" {
			t.Fatalf("req %d: want sticky nodeA, got %q", i, got)
		}
		affinity.Record("escrow-1", "sess", "nodeA") // count 2, then 3 -> evict
	}
	if affinity.StickyNode("escrow-1", "sess") != "" {
		t.Fatal("after the request bound the session must re-randomise (load rebalance)")
	}
}

func TestExpiresOnTTL(t *testing.T) {
	cfg := Config{Enabled: true, MaxRequests: 1000, TTL: time.Minute, MaxEntries: 100}
	affinity, now := testTracker(cfg)
	affinity.Record("escrow-1", "sess", "nodeA")
	if got := affinity.StickyNode("escrow-1", "sess"); got != "nodeA" {
		t.Fatalf("within TTL want nodeA, got %q", got)
	}
	*now = now.Add(cfg.TTL)
	if affinity.StickyNode("escrow-1", "sess") != "" {
		t.Fatal("binding must expire at TTL")
	}
}

func TestRebindsOnDifferentNode(t *testing.T) {
	cfg := Config{Enabled: true, MaxRequests: 5, TTL: time.Hour, MaxEntries: 100}
	affinity, _ := testTracker(cfg)
	affinity.Record("escrow-1", "sess", "nodeA")
	affinity.Record("escrow-1", "sess", "nodeA")
	affinity.Record("escrow-1", "sess", "nodeB") // fell back to a different node -> rebind fresh
	got := affinity.StickyNode("escrow-1", "sess")
	if got != "nodeB" {
		t.Fatalf("want rebind to nodeB, got %q", got)
	}
}

func TestMapIsBounded(t *testing.T) {
	cfg := Config{Enabled: true, MaxRequests: 1000, TTL: time.Hour, MaxEntries: 100}
	affinity, _ := testTracker(cfg)
	for i := 0; i < 10*cfg.MaxEntries; i++ {
		affinity.Record("escrow-1", sessionKey(i), "nodeA")
	}
	affinity.mu.Lock()
	bindings := len(affinity.byKey)
	affinity.mu.Unlock()
	if bindings > cfg.MaxEntries {
		t.Fatalf("map must stay bounded by MaxEntries=%d, got %d", cfg.MaxEntries, bindings)
	}
}

func TestSeparatesEscrowsSharingOneSessionID(t *testing.T) {
	cfg := Config{Enabled: true, MaxRequests: 5, TTL: time.Hour, MaxEntries: 100}
	affinity, _ := testTracker(cfg)

	affinity.Record("escrow-1", "sess", "nodeA")

	if affinity.StickyNode("escrow-2", "sess") != "" {
		t.Fatal("one escrow's binding must not steer another escrow's identical session id")
	}
	affinity.Record("escrow-2", "sess", "nodeB")
	if got := affinity.StickyNode("escrow-1", "sess"); got != "nodeA" {
		t.Fatalf("the first escrow's binding must survive the second's, got %q", got)
	}
}

func TestEmptySessionIsNoOp(t *testing.T) {
	cfg := Config{Enabled: true, MaxRequests: 5, TTL: time.Hour, MaxEntries: 100}
	affinity, _ := testTracker(cfg)
	affinity.Record("escrow-1", "", "nodeA")
	if affinity.StickyNode("escrow-1", "") != "" {
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

func TestServeStickyDecisions(t *testing.T) {
	tests := []struct {
		name         string
		enabled      bool
		selection    Selection
		wantSticky   bool
		wantDecision string
	}{
		{
			name:         "no binding but a session id is a miss",
			enabled:      true,
			selection:    Selection{Model: "m", SessionID: "sess"},
			wantDecision: DecisionMiss,
		},
		{
			name:      "no binding and no session id records nothing",
			enabled:   true,
			selection: Selection{Model: "m"},
		},
		{
			name:         "a bound node that is gone yields",
			enabled:      true,
			selection:    Selection{Model: "m", SessionID: "sess", StickyNodeID: "node-a"},
			wantDecision: DecisionYielded,
		},
		{
			name:         "three requests deeper than the peer is congested",
			enabled:      true,
			selection:    Selection{Model: "m", SessionID: "sess", StickyNodeID: "node-a", StickyUsable: true, StickyLoad: 3},
			wantDecision: DecisionCongested,
		},
		{
			name:         "two requests deeper, exactly the margin, is still a hit",
			enabled:      true,
			selection:    Selection{Model: "m", SessionID: "sess", StickyNodeID: "node-a", StickyUsable: true, StickyLoad: 2},
			wantSticky:   true,
			wantDecision: DecisionHit,
		},
		{
			name:         "the margin is a difference, not an absolute load",
			enabled:      true,
			selection:    Selection{Model: "m", SessionID: "sess", StickyNodeID: "node-a", StickyUsable: true, StickyLoad: 50, LeastBusyLoad: 50},
			wantSticky:   true,
			wantDecision: DecisionHit,
		},
		{
			name:      "a disabled tracker decides nothing and records nothing",
			enabled:   false,
			selection: Selection{Model: "m", SessionID: "sess", StickyNodeID: "node-a", StickyUsable: true},
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			var decisions []string
			tracker := New(Config{Enabled: testCase.enabled, MaxRequests: 10, TTL: time.Hour, MaxEntries: 10},
				func(decision, _ string) { decisions = append(decisions, decision) })

			serveSticky := tracker.ServeSticky(testCase.selection)

			if serveSticky != testCase.wantSticky {
				t.Fatalf("ServeSticky = %v, want %v", serveSticky, testCase.wantSticky)
			}
			if testCase.wantDecision == "" {
				if len(decisions) != 0 {
					t.Fatalf("recorded %v, want silence", decisions)
				}
				return
			}
			if len(decisions) != 1 || decisions[0] != testCase.wantDecision {
				t.Fatalf("recorded %v, want exactly one %q", decisions, testCase.wantDecision)
			}
		})
	}
}

func TestNilTrackerIsInert(t *testing.T) {
	var tracker *Tracker

	tracker.Record("escrow-1", "sess", "nodeA")

	if got := tracker.StickyNode("escrow-1", "sess"); got != "" {
		t.Fatalf("nil tracker must remember nothing, got %q", got)
	}
	if tracker.ServeSticky(Selection{Model: "m", SessionID: "sess", StickyNodeID: "nodeA", StickyUsable: true}) {
		t.Fatal("nil tracker must never claim a node")
	}
}

func TestNilRecorderIsAccepted(t *testing.T) {
	tracker := New(Config{Enabled: true, MaxRequests: 10, TTL: time.Hour, MaxEntries: 10}, nil)

	if !tracker.ServeSticky(Selection{Model: "m", SessionID: "sess", StickyNodeID: "nodeA", StickyUsable: true}) {
		t.Fatal("a nil recorder must not change the decision")
	}
}
