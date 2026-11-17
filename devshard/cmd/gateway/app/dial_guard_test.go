package app

import (
	"testing"

	"common/httpguard"

	"devshard/cmd/gateway/env"
	"devshard/cmd/gateway/internal/logcapture"
)

const dialGuardOffMessage = "dials to private addresses are allowed: the SSRF guard is off"

// Test flow:
//  1. For each case (DEVSHARD_ALLOW_PRIVATE_ADDRESSES unset, "false", or "true"), set httpguard to the opposite of the expected outcome and set the env var.
//  2. Call applyDialGuard.
//  3. Assert httpguard.AllowPrivate() matches the case's wantOpen.
//  4. Assert the log carries the guard-off warning only when wantWarns is set.
func TestTheDialGuardIsArmedBeforeAnythingDials(t *testing.T) {
	testCases := []struct {
		name      string
		raw       string
		wantOpen  bool
		wantWarns bool
	}{
		{name: "unset keeps the guard on and says nothing", raw: "", wantOpen: false, wantWarns: false},
		{name: "false keeps the guard on and says nothing", raw: "false", wantOpen: false, wantWarns: false},
		{name: "an opt-out is armed and announced", raw: "true", wantOpen: true, wantWarns: true},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Cleanup(func() { httpguard.SetAllowPrivate(false) })
			httpguard.SetAllowPrivate(!testCase.wantOpen)
			t.Setenv(env.AllowPrivateAddressesVariable, testCase.raw)
			logs := logcapture.Install(t)

			applyDialGuard()

			if got := httpguard.AllowPrivate(); got != testCase.wantOpen {
				t.Fatalf("httpguard.AllowPrivate() = %v, want %v", got, testCase.wantOpen)
			}
			if _, warned := logs.Find(dialGuardOffMessage); warned != testCase.wantWarns {
				t.Fatalf("the log warned = %v, want %v", warned, testCase.wantWarns)
			}
		})
	}
}
