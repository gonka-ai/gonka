package admin

import "decentralized-api/apiconfig"

// Import types and constants from apiconfig package
// These are defined there to avoid import cycles

type MLNodeOnboardingState = apiconfig.MLNodeOnboardingState

const (
	MLNodeState_WAITING_FOR_POC = apiconfig.MLNodeState_WAITING_FOR_POC
	MLNodeState_TESTING         = apiconfig.MLNodeState_TESTING
	MLNodeState_TEST_FAILED     = apiconfig.MLNodeState_TEST_FAILED
)

type ParticipantState = apiconfig.ParticipantState

const (
	ParticipantState_INACTIVE_WAITING     = apiconfig.ParticipantState_INACTIVE_WAITING
	ParticipantState_ACTIVE_PARTICIPATING = apiconfig.ParticipantState_ACTIVE_PARTICIPATING
)

type OnboardingStateManager struct {
	timing            *TimingCalculator
	blockTimeSeconds  float64
	alertLeadSeconds  int64
	safeOfflineMinSec int64
}

func NewOnboardingStateManager() *OnboardingStateManager {
	return &OnboardingStateManager{
		timing:            NewTimingCalculator(),
		blockTimeSeconds:  6.0,
		alertLeadSeconds:  600,
		safeOfflineMinSec: 600,
	}
}

// func (m *OnboardingStateManager) MLNodeStatus(es *chainphase.EpochState, isTesting bool, testFailed bool) (MLNodeOnboardingState, string, bool) {
// 	if testFailed {
// 		return MLNodeState_TEST_FAILED, "Validation testing failed", true
// 	}
// 	if isTesting {
// 		return MLNodeState_TESTING, "Running pre-PoC validation testing", true
// 	}
// 	c := m.timing.Countdown(es, m.blockTimeSeconds, m.alertLeadSeconds)
// 	if c.ShouldBeOnline {
// 		return MLNodeState_WAITING_FOR_POC, "PoC starting soon (in " + formatShortDuration(c.NextPoCSeconds) + ") - MLnode must be online now", true
// 	}
// 	return MLNodeState_WAITING_FOR_POC, "Waiting for next PoC cycle (starts in " + formatShortDuration(c.NextPoCSeconds) + ") - you can safely turn off the server and restart it 10 minutes before PoC", false
// }

func (m *OnboardingStateManager) ParticipantStatus(isActive bool) ParticipantState {
	if isActive {
		return ParticipantState_ACTIVE_PARTICIPATING
	}
	return ParticipantState_INACTIVE_WAITING
}

func (m *OnboardingStateManager) MLNodeStatus(secondsUntilNextPoC int64, isTesting bool, testFailed bool) (MLNodeOnboardingState, string, bool) {
	if testFailed {
		return MLNodeState_TEST_FAILED, "Validation testing failed", true
	}
	if isTesting {
		return MLNodeState_TESTING, "Running pre-PoC validation testing", true
	}
	if secondsUntilNextPoC <= m.alertLeadSeconds {
		return MLNodeState_WAITING_FOR_POC, "PoC starting soon (in " + formatShortDuration(secondsUntilNextPoC) + ") - MLnode must be online now", true
	}
	return MLNodeState_WAITING_FOR_POC, "Waiting for next PoC cycle (starts in " + formatShortDuration(secondsUntilNextPoC) + ") - you can safely turn off the server and restart it 10 minutes before PoC", false
}

func formatShortDuration(seconds int64) string {
	if seconds <= 0 {
		return "0s"
	}
	h := seconds / 3600
	m := (seconds % 3600) / 60
	s := seconds % 60
	if h > 0 && m > 0 {
		return itoa(h) + "h " + itoa(m) + "m"
	}
	if h > 0 {
		return itoa(h) + "h"
	}
	if m > 0 && s > 0 {
		return itoa(m) + "m " + itoa(s) + "s"
	}
	if m > 0 {
		return itoa(m) + "m"
	}
	return itoa(s) + "s"
}

func itoa(v int64) string {
	return fmtInt(v)
}

func fmtInt(v int64) string {
	var buf [20]byte
	i := len(buf)
	n := v
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
