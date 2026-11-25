package chain

import "testing"

// Test flow:
//  1. Build the table of all 25 epoch-phase x confirmation-phase combinations, each with a literal expected blocked flag and reason.
//  2. Assert the table has exactly 25 entries.
//  3. For each combination, call rawPoCBlockingState and assert it returns the expected blocked flag and reason.
func TestRawPoCBlockingStateAllCombinations(t *testing.T) {
	cases := []struct {
		name              string
		epochPhase        EpochPhase
		confirmationPhase ConfirmationPoCPhase
		wantBlocked       bool
		wantReason        BlockReason
	}{
		{"inference/inactive", EpochPhaseInference, ConfirmationPoCInactive, false, BlockReasonNone},
		{"inference/grace_period", EpochPhaseInference, ConfirmationPoCGracePeriod, true, BlockReasonConfirmationPoC},
		{"inference/generation", EpochPhaseInference, ConfirmationPoCGeneration, true, BlockReasonConfirmationPoC},
		{"inference/validation", EpochPhaseInference, ConfirmationPoCValidation, true, BlockReasonConfirmationPoC},
		{"inference/completed", EpochPhaseInference, ConfirmationPoCCompleted, false, BlockReasonNone},

		{"poc_generate/inactive", EpochPhasePoCGenerate, ConfirmationPoCInactive, true, BlockReasonPoC},
		{"poc_generate/grace_period", EpochPhasePoCGenerate, ConfirmationPoCGracePeriod, true, BlockReasonPoC},
		{"poc_generate/generation", EpochPhasePoCGenerate, ConfirmationPoCGeneration, true, BlockReasonPoC},
		{"poc_generate/validation", EpochPhasePoCGenerate, ConfirmationPoCValidation, true, BlockReasonPoC},
		{"poc_generate/completed", EpochPhasePoCGenerate, ConfirmationPoCCompleted, true, BlockReasonPoC},

		{"poc_generate_wind_down/inactive", EpochPhasePoCGenerateWindDown, ConfirmationPoCInactive, true, BlockReasonPoC},
		{"poc_generate_wind_down/grace_period", EpochPhasePoCGenerateWindDown, ConfirmationPoCGracePeriod, true, BlockReasonPoC},
		{"poc_generate_wind_down/generation", EpochPhasePoCGenerateWindDown, ConfirmationPoCGeneration, true, BlockReasonPoC},
		{"poc_generate_wind_down/validation", EpochPhasePoCGenerateWindDown, ConfirmationPoCValidation, true, BlockReasonPoC},
		{"poc_generate_wind_down/completed", EpochPhasePoCGenerateWindDown, ConfirmationPoCCompleted, true, BlockReasonPoC},

		{"poc_validate/inactive", EpochPhasePoCValidate, ConfirmationPoCInactive, true, BlockReasonPoC},
		{"poc_validate/grace_period", EpochPhasePoCValidate, ConfirmationPoCGracePeriod, true, BlockReasonPoC},
		{"poc_validate/generation", EpochPhasePoCValidate, ConfirmationPoCGeneration, true, BlockReasonPoC},
		{"poc_validate/validation", EpochPhasePoCValidate, ConfirmationPoCValidation, true, BlockReasonPoC},
		{"poc_validate/completed", EpochPhasePoCValidate, ConfirmationPoCCompleted, true, BlockReasonPoC},

		{"poc_validate_wind_down/inactive", EpochPhasePoCValidateWindDown, ConfirmationPoCInactive, true, BlockReasonPoC},
		{"poc_validate_wind_down/grace_period", EpochPhasePoCValidateWindDown, ConfirmationPoCGracePeriod, true, BlockReasonPoC},
		{"poc_validate_wind_down/generation", EpochPhasePoCValidateWindDown, ConfirmationPoCGeneration, true, BlockReasonPoC},
		{"poc_validate_wind_down/validation", EpochPhasePoCValidateWindDown, ConfirmationPoCValidation, true, BlockReasonPoC},
		{"poc_validate_wind_down/completed", EpochPhasePoCValidateWindDown, ConfirmationPoCCompleted, true, BlockReasonPoC},
	}
	if len(cases) != 25 {
		t.Fatalf("case count = %d, want 25 (5 epoch phases x 5 confirmation phases)", len(cases))
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			blocked, reason := rawPoCBlockingState(testCase.epochPhase, testCase.confirmationPhase)
			if blocked != testCase.wantBlocked || reason != testCase.wantReason {
				t.Fatalf("rawPoCBlockingState(%q, %q) = (%v, %q), want (%v, %q)",
					testCase.epochPhase, testCase.confirmationPhase, blocked, reason, testCase.wantBlocked, testCase.wantReason)
			}
		})
	}
}
