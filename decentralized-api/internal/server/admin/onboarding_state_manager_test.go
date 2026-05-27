package admin

import (
	"decentralized-api/apiconfig"
	"strings"
	"testing"
)

func TestDeriveParticipantState(t *testing.T) {
	if got := DeriveParticipantState(true); got != ParticipantState_ACTIVE_PARTICIPATING {
		t.Errorf("active=true: got %q want %q", got, ParticipantState_ACTIVE_PARTICIPATING)
	}
	if got := DeriveParticipantState(false); got != ParticipantState_INACTIVE_WAITING {
		t.Errorf("active=false: got %q want %q", got, ParticipantState_INACTIVE_WAITING)
	}
}

func TestDeriveMLNodeState(t *testing.T) {
	tests := []struct {
		name      string
		in        OnboardingStateInputs
		wantState MLNodeOnboardingState
		wantAlert bool
	}{
		{
			name:      "test failed takes precedence",
			in:        OnboardingStateInputs{TestFailed: true, IsTesting: true, SecondsUntilNextPoC: 99999},
			wantState: MLNodeState_TEST_FAILED,
			wantAlert: true,
		},
		{
			name:      "testing in progress",
			in:        OnboardingStateInputs{IsTesting: true, SecondsUntilNextPoC: 99999},
			wantState: MLNodeState_TESTING,
			wantAlert: true,
		},
		{
			name:      "waiting, plenty of time -> no alert",
			in:        OnboardingStateInputs{SecondsUntilNextPoC: apiconfig.OnlineAlertLeadSeconds + 1},
			wantState: MLNodeState_WAITING_FOR_POC,
			wantAlert: false,
		},
		{
			name:      "waiting, within alert lead -> alert",
			in:        OnboardingStateInputs{SecondsUntilNextPoC: apiconfig.OnlineAlertLeadSeconds},
			wantState: MLNodeState_WAITING_FOR_POC,
			wantAlert: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gotState, gotAlert := DeriveMLNodeState(tc.in)
			if gotState != tc.wantState || gotAlert != tc.wantAlert {
				t.Errorf("got (%q, %v) want (%q, %v)", gotState, gotAlert, tc.wantState, tc.wantAlert)
			}
		})
	}
}

func TestBuildMLNodeMessage(t *testing.T) {
	got := BuildMLNodeMessage(MLNodeState_TEST_FAILED, 0, "Qwen2.5-7B")
	if !strings.Contains(got, "Qwen2.5-7B") {
		t.Errorf("failing model not surfaced: %q", got)
	}
	near := BuildMLNodeMessage(MLNodeState_WAITING_FOR_POC, apiconfig.OnlineAlertLeadSeconds-1, "")
	if !strings.Contains(near, "must be online") {
		t.Errorf("alert message missing online directive: %q", near)
	}
	far := BuildMLNodeMessage(MLNodeState_WAITING_FOR_POC, apiconfig.OnlineAlertLeadSeconds+3600, "")
	if !strings.Contains(far, "safe to be offline") {
		t.Errorf("non-alert message missing offline guidance: %q", far)
	}
}

func TestFormatShortDuration(t *testing.T) {
	tests := []struct {
		seconds int64
		want    string
	}{
		{0, "0s"},
		{-1, "0s"},
		{45, "45s"},
		{120, "2m"},
		{125, "2m 5s"},
		{3600, "1h"},
		{3660, "1h 1m"},
	}
	for _, tc := range tests {
		if got := formatShortDuration(tc.seconds); got != tc.want {
			t.Errorf("formatShortDuration(%d) = %q, want %q", tc.seconds, got, tc.want)
		}
	}
}
