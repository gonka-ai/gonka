package app

import (
	"runtime"
	"testing"

	"devshard/cmd/gateway/internal/logcapture"
)

const contentionProfilesOnMessage = "lock contention and blocking are sampled for /debug/pprof"

// Test flow:
//  1. For each case (switch unset, false, or true), reset the mutex sampling to off and capture the log.
//  2. Call applyContentionProfiles with the case's switch.
//  3. Assert the mutex sampling fraction matches the case's want.
//  4. Assert the log announces sampling only when it was switched on.
func TestContentionProfilesAreSampledOnlyWhenSwitchedOn(t *testing.T) {
	switchedOff, switchedOn := false, true
	testCases := []struct {
		name         string
		enabled      *bool
		wantFraction int
		wantLogged   bool
	}{
		{name: "unset samples nothing and says nothing", enabled: nil, wantFraction: 0, wantLogged: false},
		{name: "false samples nothing and says nothing", enabled: &switchedOff, wantFraction: 0, wantLogged: false},
		{name: "true samples and is announced", enabled: &switchedOn, wantFraction: contentionMutexProfileFraction, wantLogged: true},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Cleanup(func() {
				runtime.SetMutexProfileFraction(0)
				runtime.SetBlockProfileRate(0)
			})
			runtime.SetMutexProfileFraction(0)
			logs := logcapture.Install(t)

			applyContentionProfiles(testCase.enabled)

			if got := runtime.SetMutexProfileFraction(-1); got != testCase.wantFraction {
				t.Fatalf("mutex profile fraction = %d, want %d", got, testCase.wantFraction)
			}
			if _, logged := logs.Find(contentionProfilesOnMessage); logged != testCase.wantLogged {
				t.Fatalf("the log announced sampling = %v, want %v", logged, testCase.wantLogged)
			}
		})
	}
}
