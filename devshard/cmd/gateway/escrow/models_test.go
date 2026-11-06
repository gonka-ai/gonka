package escrow

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"devshard/cmd/gateway/chain"
)

// Test flow:
//  1. Run parseModels against a raw JSON string, varied across cases: a valid multi-model array, a missing target_count, blank/whitespace input, malformed JSON, and each field-validation rejection (empty model_id, zero/negative target_count, zero amount), and the funding fields: max_unsettled at and under its floor, zero and negative full_context_slots.
//  2. For an error case, assert parseModels returns an error.
//  3. For a success case, assert the returned models match the expected slice (or nil for blank input).
func TestParseModels(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		want    []ModelConfig
		wantErr bool
	}{
		{
			name: "valid multi-model JSON populates every field",
			raw: `[
				{"model_id":"model-a","temp_count":2,"target_count":5,"amount":1000000,"private_key_env":"MODEL_A_KEY"},
				{"model_id":"model-b","temp_count":1,"target_count":3,"amount":2000000,"private_key_env":"MODEL_B_KEY"}
			]`,
			want: []ModelConfig{
				{ModelID: "model-a", TempCount: 2, TargetCount: 5, ReserveCount: 1, Amount: 1000000, PrivateKeyEnv: "MODEL_A_KEY", MaxUnsettled: 12, FullContextSlots: 2},
				{ModelID: "model-b", TempCount: 1, TargetCount: 3, ReserveCount: 1, Amount: 2000000, PrivateKeyEnv: "MODEL_B_KEY", MaxUnsettled: 9, FullContextSlots: 2},
			},
		},
		{
			name: "missing target_count defaults to one",
			raw:  `[{"model_id":"m","temp_count":1,"amount":1,"private_key_env":"K"}]`,
			want: []ModelConfig{{ModelID: "m", TempCount: 1, TargetCount: 1, ReserveCount: 1, Amount: 1, PrivateKeyEnv: "K", MaxUnsettled: 7, FullContextSlots: 2}},
		},
		{name: "blank string returns nil", raw: "", want: nil},
		{name: "whitespace-only returns nil", raw: "   \n\t ", want: nil},
		{name: "malformed JSON errors", raw: "{not valid", wantErr: true},
		{name: "empty model_id rejected", raw: `[{"model_id":"","temp_count":1,"target_count":1,"amount":1,"private_key_env":"K"}]`, wantErr: true},
		{name: "zero target_count rejected", raw: `[{"model_id":"m","temp_count":1,"target_count":0,"amount":1,"private_key_env":"K"}]`, wantErr: true},
		{name: "negative target_count rejected", raw: `[{"model_id":"m","temp_count":1,"target_count":-1,"amount":1,"private_key_env":"K"}]`, wantErr: true},
		{
			name: "max_unsettled at its floor is kept",
			raw:  `[{"model_id":"m","temp_count":1,"target_count":2,"reserve_count":1,"amount":1,"private_key_env":"K","max_unsettled":5,"full_context_slots":3}]`,
			want: []ModelConfig{{ModelID: "m", TempCount: 1, TargetCount: 2, ReserveCount: 1, Amount: 1, PrivateKeyEnv: "K", MaxUnsettled: 5, FullContextSlots: 3}},
		},
		{name: "max_unsettled under target, temp and reserve plus one rejected", raw: `[{"model_id":"m","temp_count":1,"target_count":2,"reserve_count":1,"amount":1,"private_key_env":"K","max_unsettled":4}]`, wantErr: true},
		{name: "zero full_context_slots rejected", raw: `[{"model_id":"m","target_count":1,"amount":1,"private_key_env":"K","full_context_slots":0}]`, wantErr: true},
		{name: "negative full_context_slots rejected", raw: `[{"model_id":"m","target_count":1,"amount":1,"private_key_env":"K","full_context_slots":-1}]`, wantErr: true},
		{name: "zero amount rejected", raw: `[{"model_id":"m","temp_count":1,"target_count":1,"amount":0,"private_key_env":"K"}]`, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := parseModels(tt.raw)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("parseModels(%q) error = nil, want error", tt.raw)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseModels(%q) unexpected error: %v", tt.raw, err)
			}
			if tt.want == nil {
				if got != nil {
					t.Fatalf("parseModels(%q) = %+v, want nil", tt.raw, got)
				}
				return
			}
			if len(got) != len(tt.want) {
				t.Fatalf("parseModels(%q) len = %d, want %d", tt.raw, len(got), len(tt.want))
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("parseModels(%q)[%d] = %+v, want %+v", tt.raw, i, got[i], tt.want[i])
				}
			}
		})
	}
}

// Test flow:
//  1. Call parseModels with a model whose temp_count and target_count are both zero.
//  2. Assert parseModels returns an error.
//  3. Assert the error message names the offending model ID.
func TestParseModelsErrorNamesOffendingModel(t *testing.T) {
	_, err := parseModels(`[{"model_id":"broken-model","temp_count":0,"target_count":0,"amount":5,"private_key_env":"K"}]`)
	if err == nil {
		t.Fatal("parseModels() error = nil, want error naming the offending model")
	}
	if !strings.Contains(err.Error(), "broken-model") {
		t.Fatalf("parseModels() error = %q, want it to mention model %q", err.Error(), "broken-model")
	}
}

// Test flow:
//  1. Call servedByNetwork with a chain.PhaseSnapshot and a model ID, varied across cases: model present in FullWeightsByModel, model absent with a non-empty network, an empty snapshot, and a model present only in CurrentWeightsByModel.
//  2. Assert the returned served and known flags match the expected values for that case.
func TestServedByNetwork(t *testing.T) {
	tests := []struct {
		name       string
		snapshot   chain.PhaseSnapshot
		modelID    string
		wantServed bool
		wantKnown  bool
	}{
		{
			name: "model present in FullWeightsByModel",
			snapshot: chain.PhaseSnapshot{
				FullWeightsByModel: map[string]map[string]float64{
					"model-a": {"participantA": 100},
				},
			},
			modelID:    "model-a",
			wantServed: true,
			wantKnown:  true,
		},
		{
			name: "model absent but network non-empty",
			snapshot: chain.PhaseSnapshot{
				FullWeightsByModel: map[string]map[string]float64{
					"model-a": {"participantA": 100},
				},
			},
			modelID:    "model-b",
			wantServed: false,
			wantKnown:  true,
		},
		{
			name:       "empty snapshot is cold start",
			snapshot:   chain.PhaseSnapshot{},
			modelID:    "model-a",
			wantServed: false,
			wantKnown:  false,
		},
		{
			name: "model present only in CurrentWeightsByModel counts as served",
			snapshot: chain.PhaseSnapshot{
				CurrentWeightsByModel: map[string]map[string]float64{
					"model-b": {"participantB": 50},
				},
			},
			modelID:    "model-b",
			wantServed: true,
			wantKnown:  true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			served, known := servedByNetwork(tt.snapshot, tt.modelID)
			if served != tt.wantServed || known != tt.wantKnown {
				t.Errorf("servedByNetwork(%q) = (served=%v, known=%v), want (served=%v, known=%v)",
					tt.modelID, served, known, tt.wantServed, tt.wantKnown)
			}
		})
	}
}

// Test flow:
//  1. For each table case's rotation models JSON (reserve_count absent, 0, 3, negative), call parseModels.
//  2. Assert an absent key reads as 1, an explicit 0 and 3 are kept, and a negative count is refused naming the model.
func TestReserveCountDefaultsToOneAndRefusesANegativeCount(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name    string
		raw     string
		want    int
		wantErr string
	}{
		{name: "absent", raw: `[{"model_id":"m","amount":5}]`, want: 1},
		{name: "explicit_zero_turns_reserves_off", raw: `[{"model_id":"m","amount":5,"reserve_count":0}]`, want: 0},
		{name: "several", raw: `[{"model_id":"m","amount":5,"reserve_count":3}]`, want: 3},
		{name: "negative", raw: `[{"model_id":"m","amount":5,"reserve_count":-1}]`, wantErr: `rotation model "m": reserve_count must be >= 0`},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			models, err := parseModels(testCase.raw)
			if testCase.wantErr != "" {
				require.ErrorContains(t, err, testCase.wantErr)
				return
			}
			require.NoError(t, err)
			require.Equal(t, testCase.want, models[0].ReserveCount)
		})
	}
}
