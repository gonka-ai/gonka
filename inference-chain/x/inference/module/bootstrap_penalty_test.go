package inference

import (
	"testing"

	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
)

// commitKey builds a PoCParticipantModelKey for tests; the store-commit value
// itself is irrelevant to validatedDirectCommitters (only key presence and
// validation presence matter).
func commitKey(participant, model string) types.PoCParticipantModelKey {
	return types.PoCParticipantModelKey{ParticipantAddress: participant, ModelID: model}
}

func TestValidatedDirectCommitters_ValidatedCommitIncluded(t *testing.T) {
	commits := map[types.PoCParticipantModelKey]types.PoCV2StoreCommit{
		commitKey("alice", "bootstrap-model"): {},
	}
	validations := map[types.PoCParticipantModelKey][]types.PoCValidationV2{
		commitKey("alice", "bootstrap-model"): {
			{ParticipantAddress: "alice", ValidatorParticipantAddress: "val1", ModelId: "bootstrap-model", ValidatedWeight: 100},
		},
	}

	got := validatedDirectCommitters(commits, validations, map[string]bool{"bootstrap-model": true})

	require.True(t, got["bootstrap-model"]["alice"], "validated commit must be a direct committer")
}

// TestValidatedDirectCommitters_GarbageCommitExcluded is the B-1 regression
// test: a syntactically valid store commit with no validator votes must NOT
// earn the direct-committer set (previously it did, dodging the
// NoParticipationPenalty at the cost of one transaction fee).
func TestValidatedDirectCommitters_GarbageCommitExcluded(t *testing.T) {
	commits := map[types.PoCParticipantModelKey]types.PoCV2StoreCommit{
		commitKey("attacker", "bootstrap-model"): {},
	}
	validations := map[types.PoCParticipantModelKey][]types.PoCValidationV2{}

	got := validatedDirectCommitters(commits, validations, map[string]bool{"bootstrap-model": true})

	require.NotContains(t, got["bootstrap-model"], "attacker", "unvalidated garbage commit must not be a direct committer")
}

func TestValidatedDirectCommitters_NonBootstrapModelExcluded(t *testing.T) {
	commits := map[types.PoCParticipantModelKey]types.PoCV2StoreCommit{
		commitKey("alice", "other-model"): {},
	}
	validations := map[types.PoCParticipantModelKey][]types.PoCValidationV2{
		commitKey("alice", "other-model"): {
			{ParticipantAddress: "alice", ValidatorParticipantAddress: "val1", ModelId: "other-model", ValidatedWeight: 100},
		},
	}

	got := validatedDirectCommitters(commits, validations, map[string]bool{"bootstrap-model": true})

	require.Empty(t, got, "commits for models outside the bootstrap set must be ignored")
}

func TestValidatedDirectCommitters_EmptyValidationListExcluded(t *testing.T) {
	commits := map[types.PoCParticipantModelKey]types.PoCV2StoreCommit{
		commitKey("alice", "bootstrap-model"): {},
	}
	// Key present but with an explicitly empty vote list: same as no votes.
	validations := map[types.PoCParticipantModelKey][]types.PoCValidationV2{
		commitKey("alice", "bootstrap-model"): {},
	}

	got := validatedDirectCommitters(commits, validations, map[string]bool{"bootstrap-model": true})

	require.NotContains(t, got["bootstrap-model"], "alice")
}

// TestBootstrapPenaltyModes_UnvalidatedCommitterFallsThroughToNone wires the
// filtered committer set through ResolveBootstrapPenaltyModes end to end: the
// B-1 attacker (garbage commit, no validations) resolves to
// BootstrapPenaltyNone — which AccumulateBootstrapPenalties penalizes —
// instead of the previous BootstrapPenaltyDirect exemption.
func TestBootstrapPenaltyModes_UnvalidatedCommitterFallsThroughToNone(t *testing.T) {
	participants := []*types.ActiveParticipant{
		{Index: "attacker", Weight: 100},
		{Index: "honest", Weight: 100},
	}
	reportByModel := map[string]*types.BootstrapModelPreEligibility{
		"bootstrap-model": {ModelId: "bootstrap-model", PreEligible: true},
	}
	commits := map[types.PoCParticipantModelKey]types.PoCV2StoreCommit{
		commitKey("attacker", "bootstrap-model"): {},
		commitKey("honest", "bootstrap-model"):   {},
	}
	validations := map[types.PoCParticipantModelKey][]types.PoCValidationV2{
		commitKey("honest", "bootstrap-model"): {
			{ParticipantAddress: "honest", ValidatorParticipantAddress: "val1", ModelId: "bootstrap-model", ValidatedWeight: 50},
		},
	}
	directCommitters := validatedDirectCommitters(commits, validations, map[string]bool{"bootstrap-model": true})

	modes := ResolveBootstrapPenaltyModes(
		participants,
		map[string]bool{"attacker": true, "honest": true},
		reportByModel,
		nil,
		nil,
		directCommitters,
	)

	require.Equal(t, BootstrapPenaltyNone, modes["bootstrap-model"]["attacker"],
		"garbage committer must lose the Direct exemption")
	require.Equal(t, BootstrapPenaltyDirect, modes["bootstrap-model"]["honest"],
		"validated committer keeps the Direct exemption")
}

// TestBootstrapPenaltyModes_UnvalidatedCommitterWithDelegationKeepsDelegate
// documents the deliberate fall-through semantics: filtering happens at load
// time, so a participant whose commit was never validated but who genuinely
// delegated still resolves to Delegate (not force-demoted to IntentMissed).
func TestBootstrapPenaltyModes_UnvalidatedCommitterWithDelegationKeepsDelegate(t *testing.T) {
	participants := []*types.ActiveParticipant{
		{Index: "delegator", Weight: 100},
	}
	reportByModel := map[string]*types.BootstrapModelPreEligibility{
		"bootstrap-model": {ModelId: "bootstrap-model", PreEligible: true},
	}
	commits := map[types.PoCParticipantModelKey]types.PoCV2StoreCommit{
		commitKey("delegator", "bootstrap-model"): {},
	}
	directCommitters := validatedDirectCommitters(commits, nil, map[string]bool{"bootstrap-model": true})
	delegations := map[string]map[string]string{
		"bootstrap-model": {"delegator": "some-worker"},
	}

	modes := ResolveBootstrapPenaltyModes(
		participants,
		map[string]bool{"delegator": true},
		reportByModel,
		delegations,
		nil,
		directCommitters,
	)

	require.Equal(t, BootstrapPenaltyDelegate, modes["bootstrap-model"]["delegator"])
}
