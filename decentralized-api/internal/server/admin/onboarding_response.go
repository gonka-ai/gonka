package admin

import (
	"decentralized-api/broker"
	"decentralized-api/chainphase"
)

// NodeWithOnboarding is the admin GET /nodes response item. It wraps
// the broker's view of a node with onboarding UX fields that are
// derived at the handler layer (not stored on broker state).
type NodeWithOnboarding struct {
	broker.NodeResponse
	Onboarding *OnboardingStatus `json:"onboarding,omitempty"`
}

// OnboardingStatus aggregates everything the admin UI needs to show
// human-friendly setup state without inspecting raw broker internals.
type OnboardingStatus struct {
	ParticipantState string                `json:"participant_state"`
	MLNodeState      MLNodeOnboardingState `json:"mlnode_state"`
	Timing           *TimingInfo           `json:"timing,omitempty"`
	UserMessage      string                `json:"user_message,omitempty"`
	Guidance         string                `json:"guidance,omitempty"`
}

// computeOnboarding derives onboarding UX fields for one node from
// the broker's NodeResponse + cached participant activity + the
// chain phase tracker. It performs no chain RPC calls and does not
// mutate broker state. Returns nil if neither timing nor activity
// is known yet (e.g. chain not yet synced and tracker not started).
func (s *Server) computeOnboarding(n broker.NodeResponse) *OnboardingStatus {
	var epochState *chainphase.EpochState
	if s.phaseTracker != nil {
		epochState = s.phaseTracker.GetCurrentEpochState()
	}
	timing := ComputeTiming(epochState)
	if timing == nil && s.activityTracker == nil {
		return nil
	}

	active := false
	known := true
	if s.activityTracker != nil {
		active = s.activityTracker.IsActive()
		known = s.activityTracker.IsKnown()
	}
	// EpochMLNodes already-populated also implies activity for this
	// participant in the current epoch; treat as a secondary signal that is
	// authoritative on its own (it is a real chain observation).
	if !active && len(n.State.EpochMLNodes) > 0 {
		active = true
		known = true
	}

	// Distinguish "known inactive" from "not yet known" so the UI never shows
	// "not active" when the activity tracker simply has not synced yet.
	participantState := DeriveParticipantState(active)
	if !active && !known {
		participantState = ParticipantState_UNKNOWN
	}

	// Use a sentinel (not 0) when the schedule is unknown so downstream
	// helpers never render a bogus "PoC starting soon (in 0s)".
	seconds := SecondsUntilPoCUnknown
	if timing != nil {
		seconds = timing.SecondsUntilNextPoC
	}

	// Onboarding test signal is owned by the MLNodeTester, not the
	// broker: IsTesting while a one-shot test is in flight, TEST_FAILED
	// (with the offending model) from the most recent failed result. A
	// broker-side FAILED status is kept as a secondary signal so genuine
	// operational failures still surface even without a recent test.
	// TEST_FAILED is derived ONLY from the MLnode validation test, never
	// from the broker's operational FAILED status. An inactive
	// participant's node is driven to INFERENCE and "fails" with
	// no-epoch-models — that is normal onboarding, not a test failure, so
	// surfacing it as TEST_FAILED would contradict the proposal.
	isTesting := false
	testFailed := false
	failingModel := ""
	// validated == the node's most recent test passed. Gates the
	// reassuring "waiting for PoC" wording per the proposal.
	validated := false
	if s.tester != nil {
		nodeId := n.Node.Id
		isTesting = s.tester.IsRunning(nodeId)
		if last := s.tester.LastResult(nodeId); last != nil {
			switch last.Status {
			case TestFailed:
				testFailed = true
				failingModel = last.FailingModel
			case TestSuccess:
				validated = true
			}
		}
	}
	// A node that is itself assigned/serving in the current epoch is already
	// proven, so treat it as validated. Use node-specific evidence only —
	// participant-wide activity must NOT validate a spare/untested node, or
	// a broken node could show "validated, safe to be offline".
	if len(n.State.EpochMLNodes) > 0 {
		validated = true
	}

	mlState, _ := DeriveMLNodeState(OnboardingStateInputs{
		ParticipantActive:   active,
		IsTesting:           isTesting,
		TestFailed:          testFailed,
		SecondsUntilNextPoC: seconds,
	})

	// Lead with test signals first. An assigned active node should show the
	// participant-active message, never the "safe to be offline" waiting copy.
	shouldBeOnline := timing != nil && timing.ShouldBeOnline
	var userMsg, guidance string
	if mlState == MLNodeState_TESTING || mlState == MLNodeState_TEST_FAILED {
		userMsg = BuildMLNodeMessage(mlState, seconds, failingModel, validated, shouldBeOnline)
		guidance = BuildParticipantMessage(participantState)
	} else if active {
		userMsg = BuildParticipantMessage(participantState)
	} else {
		userMsg = BuildParticipantMessage(participantState)
		// Only promise the inactive auto-test behavior when we actually know
		// the participant is inactive; when status is unknown, don't.
		if known {
			guidance = BuildInactiveGuidance(seconds)
		}
	}

	return &OnboardingStatus{
		ParticipantState: string(participantState),
		MLNodeState:      mlState,
		Timing:           timing,
		UserMessage:      userMsg,
		Guidance:         guidance,
	}
}
