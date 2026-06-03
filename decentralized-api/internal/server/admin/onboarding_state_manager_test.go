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
		{
			name:      "timing unknown -> waiting, no alert",
			in:        OnboardingStateInputs{SecondsUntilNextPoC: SecondsUntilPoCUnknown},
			wantState: MLNodeState_WAITING_FOR_POC,
			wantAlert: false,
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
	got := BuildMLNodeMessage(MLNodeState_TEST_FAILED, 0, "Qwen2.5-7B", false, false)
	if !strings.Contains(got, "Qwen2.5-7B") {
		t.Errorf("failing model not surfaced: %q", got)
	}
	// Validated node within the alert window (shouldBeOnline): be online now.
	near := BuildMLNodeMessage(MLNodeState_WAITING_FOR_POC, apiconfig.OnlineAlertLeadSeconds-1, "", true, true)
	if !strings.Contains(near, "must be online") {
		t.Errorf("alert message missing online directive: %q", near)
	}
	// Validated node, far out, not in the online window: safe to be offline.
	far := BuildMLNodeMessage(MLNodeState_WAITING_FOR_POC, apiconfig.OnlineAlertLeadSeconds+3600, "", true, false)
	if !strings.Contains(far, "safe to be offline") {
		t.Errorf("non-alert validated message missing offline guidance: %q", far)
	}
	// Active PoC: shouldBeOnline true even though the countdown is large —
	// must NOT advertise offline safety.
	activePoC := BuildMLNodeMessage(MLNodeState_WAITING_FOR_POC, apiconfig.OnlineAlertLeadSeconds+3600, "", true, true)
	if strings.Contains(activePoC, "safe to be offline") || !strings.Contains(activePoC, "must be online") {
		t.Errorf("active-PoC message must tell operator to be online: %q", activePoC)
	}
	// Unknown schedule (validated, not online): no invented countdown.
	unknown := BuildMLNodeMessage(MLNodeState_WAITING_FOR_POC, SecondsUntilPoCUnknown, "", true, false)
	if !strings.Contains(unknown, "syncing") || strings.Contains(unknown, "0s") {
		t.Errorf("unknown-timing message should not invent a countdown: %q", unknown)
	}
	// Not validated, far out: must NOT show the reassuring "safe to be
	// offline" — should flag it isn't validated yet.
	unval := BuildMLNodeMessage(MLNodeState_WAITING_FOR_POC, apiconfig.OnlineAlertLeadSeconds+3600, "", false, false)
	if strings.Contains(unval, "safe to be offline") || !strings.Contains(unval, "not yet validated") {
		t.Errorf("unvalidated waiting message should withhold the ready reassurance: %q", unval)
	}
	// Not validated, near PoC: still tells operator to come online.
	unvalNear := BuildMLNodeMessage(MLNodeState_WAITING_FOR_POC, apiconfig.OnlineAlertLeadSeconds-1, "", false, true)
	if !strings.Contains(unvalNear, "online") || !strings.Contains(unvalNear, "not yet validated") {
		t.Errorf("unvalidated near-PoC message should flag unvalidated + online: %q", unvalNear)
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
