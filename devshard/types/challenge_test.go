package types

import (
	"math"
	"testing"
	"time"
)

func TestChallengeFetchDeadline(t *testing.T) {
	confirmed := time.Date(2026, 11, 5, 10, 0, 0, 123000000, time.UTC)
	for _, tc := range []struct {
		name      string
		confirmed int64
		timeout   int64
		grace     uint32
		want      time.Duration
		armed     bool
	}{
		{"session execution budget", confirmed.UnixMilli(), 17, 7200, 17*time.Second + 30*time.Minute, true},
		{"short seal grace", confirmed.UnixMilli(), 17, 120, 17*time.Second + time.Minute, true},
		{"zero timeout is not the default", confirmed.UnixMilli(), 0, 7200, 30 * time.Minute, true},
		{"negative timeout follows execution timeout", confirmed.UnixMilli(), -1, 7200, 30 * time.Minute, true},
		{"missing confirmed time", 0, 17, 7200, 0, false},
		{"invalid confirmed time", -1, 17, 7200, 0, false},
		{"unrepresentable budget", confirmed.UnixMilli(), math.MaxInt64, 7200, 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, armed := ChallengeFetchDeadline(tc.confirmed, SessionConfig{ExecutionTimeout: tc.timeout, InferenceSealGraceSeconds: tc.grace})
			if armed != tc.armed {
				t.Fatalf("armed = %v, want %v", armed, tc.armed)
			}
			if armed && !got.Equal(confirmed.Add(tc.want)) {
				t.Fatalf("deadline = %v, want %v", got, confirmed.Add(tc.want))
			}
			if !armed && !got.IsZero() {
				t.Fatalf("unarmed deadline = %v", got)
			}
		})
	}
}

func TestProtocolKnowsChallenge(t *testing.T) {
	saved := EffectiveStateRootAndProtocolVersion
	t.Cleanup(func() { EffectiveStateRootAndProtocolVersion = saved })
	for _, testCase := range []struct {
		name    string
		binary  string
		session string
		want    bool
	}{
		{name: "v6 session is before the tx", binary: "v7", session: "v6", want: false},
		{name: "v7 session under a v7 binary", binary: "v7", session: "v7", want: true},
		{name: "v7 session under a v8 binary", binary: "v8", session: "v7", want: true},
		{name: "v7.1 session", binary: "v8", session: "v7.1", want: true},
		{name: "v8 session under a v7 binary", binary: "v7", session: "v8", want: true},
		{name: "named stamp of this build", binary: "dev", session: "dev", want: true},
		{name: "named stamp of another build", binary: "dev", session: "dev-next", want: false},
		{name: "named stamp under a numeric binary", binary: "v7", session: "mainnet-canary", want: false},
		{name: "empty", binary: "v7", session: "", want: false},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			EffectiveStateRootAndProtocolVersion = testCase.binary
			if got := ProtocolKnowsChallenge(testCase.session); got != testCase.want {
				t.Fatalf("ProtocolKnowsChallenge(%q) under %q = %v, want %v", testCase.session, testCase.binary, got, testCase.want)
			}
		})
	}
}

func TestChallengeWindow(t *testing.T) {
	if got := ChallengeWindow(0); got != 30*time.Minute {
		t.Fatalf("no grace: %v, want 30m", got)
	}
	if got := ChallengeWindow(7200); got != 30*time.Minute {
		t.Fatalf("grace 2h: %v, want 30m", got)
	}
	if got := ChallengeWindow(3600); got != 30*time.Minute {
		t.Fatalf("mainnet grace 1h: %v, want 30m (the cap)", got)
	}
	if got := ChallengeWindow(120); got != time.Minute {
		t.Fatalf("grace 2min: %v, want 1m", got)
	}
}

// Every value of the challenge enums has a label of its own, so a value added to the proto without one
// does not fold into another in the metrics.
func TestChallengeLabelsCoverEveryValue(t *testing.T) {
	labels := func(t *testing.T, names map[int32]string, label func(int32) string, fallback string) {
		t.Helper()
		seen := map[string]string{}
		for value, name := range names {
			got := label(value)
			if got == fallback && value != 0 {
				t.Errorf("%s has no label", name)
			}
			if other, ok := seen[got]; ok {
				t.Errorf("%s and %s share the label %q", name, other, got)
			}
			seen[got] = name
		}
		if got := label(int32(len(names) + 100)); got != fallback {
			t.Errorf("unknown value labelled %q, want %q", got, fallback)
		}
	}
	t.Run("reason", func(t *testing.T) {
		labels(t, ChallengeReason_name, func(v int32) string { return ChallengeReasonLabel(ChallengeReason(v)) }, "unspecified")
	})
	t.Run("fault", func(t *testing.T) {
		labels(t, ChallengeFault_name, func(v int32) string { return ChallengeFaultLabel(ChallengeFault(v)) }, "unknown")
	})
	t.Run("check", func(t *testing.T) {
		labels(t, ChallengeCheck_name, func(v int32) string { return ChallengeCheckLabel(ChallengeCheck(v)) }, "unknown")
	})
}
