package chain

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
)

const epochInfoNumericJSON = `{
	"block_height": 1000,
	"phase": "PoCGenerate",
	"latest_epoch": {"index": 7, "poc_start_block_height": 950},
	"is_confirmation_poc_active": false
}`

const epochInfoStringScalarsJSON = `{
	"block_height": "1000",
	"phase": "Inference",
	"latest_epoch": {"index": "7", "poc_start_block_height": "950"},
	"is_confirmation_poc_active": true,
	"active_confirmation_poc_event": {"phase": "CONFIRMATION_POC_VALIDATION", "trigger_height": "555"}
}`

func epochInfoWithConfirmationPhase(phaseJSON string) string {
	return fmt.Sprintf(`{
		"block_height": 500,
		"phase": "Inference",
		"latest_epoch": {"index": 3, "poc_start_block_height": 400},
		"is_confirmation_poc_active": true,
		"active_confirmation_poc_event": {"phase": %s, "trigger_height": 123}
	}`, phaseJSON)
}

// Test flow:
//  1. Parse a numeric-JSON epoch info body.
//  2. Assert every decoded field matches the expected epochInfo value exactly.
func TestParseEpochInfoNumericFields(t *testing.T) {
	info, err := parseEpochInfo([]byte(epochInfoNumericJSON))
	if err != nil {
		t.Fatalf("parseEpochInfo(): %v", err)
	}
	want := epochInfo{
		BlockHeight:            1000,
		Phase:                  "PoCGenerate",
		EpochIndex:             7,
		PoCStartBlockHeight:    950,
		EpochSwitchBlockHeight: 950,
	}
	if info != want {
		t.Fatalf("parseEpochInfo() = %+v, want %+v", info, want)
	}
}

// Test flow:
//  1. Parse an epoch info body whose scalars are wire-typed as JSON strings, including a confirmation PoC event.
//  2. Assert every decoded field matches the expected epochInfo value exactly.
func TestParseEpochInfoStringTypedScalars(t *testing.T) {
	info, err := parseEpochInfo([]byte(epochInfoStringScalarsJSON))
	if err != nil {
		t.Fatalf("parseEpochInfo(): %v", err)
	}
	want := epochInfo{
		BlockHeight:                  1000,
		Phase:                        "Inference",
		EpochIndex:                   7,
		PoCStartBlockHeight:          950,
		EpochSwitchBlockHeight:       950,
		IsConfirmationPoCActive:      true,
		ConfirmationPoCPhase:         ConfirmationPoCValidation,
		ConfirmationPoCTriggerHeight: 555,
	}
	if info != want {
		t.Fatalf("parseEpochInfo() = %+v, want %+v", info, want)
	}
}

// Test flow:
//  1. Build a table of confirmation-phase JSON encodings: table varies between known integer codes, an unknown integer, a verbatim string, and an unsupported JSON type.
//  2. Parse an epoch info body embedding each phase encoding.
//  3. For the unsupported-type case, assert parseEpochInfo returns an error.
//  4. For the other cases, assert the decoded ConfirmationPoCPhase matches the expected value.
func TestParseEpochInfoConfirmationPhaseFlexibleDecode(t *testing.T) {
	cases := []struct {
		name      string
		phaseJSON string
		wantPhase ConfirmationPoCPhase
		wantErr   bool
	}{
		{"int 0 inactive", "0", ConfirmationPoCInactive, false},
		{"int 1 grace period", "1", ConfirmationPoCGracePeriod, false},
		{"int 2 generation", "2", ConfirmationPoCGeneration, false},
		{"int 3 validation", "3", ConfirmationPoCValidation, false},
		{"int 4 completed", "4", ConfirmationPoCCompleted, false},
		{"unknown int falls back to stringified value", "99", "99", false},
		{"string form passes through verbatim", `"CONFIRMATION_POC_VALIDATION"`, ConfirmationPoCValidation, false},
		{"unsupported json type errors", "true", "", true},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			body := []byte(epochInfoWithConfirmationPhase(testCase.phaseJSON))
			info, err := parseEpochInfo(body)
			if testCase.wantErr {
				if err == nil {
					t.Fatalf("parseEpochInfo() error = nil, want error for phase JSON %s", testCase.phaseJSON)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseEpochInfo(): %v", err)
			}
			if info.ConfirmationPoCPhase != testCase.wantPhase {
				t.Fatalf("ConfirmationPoCPhase = %q, want %q", info.ConfirmationPoCPhase, testCase.wantPhase)
			}
		})
	}
}

// Test flow:
//  1. Call parseEpochInfo with malformed JSON.
//  2. Assert it returns an error.
func TestParseEpochInfoMalformedJSONErrors(t *testing.T) {
	_, err := parseEpochInfo([]byte(`{not-json`))
	if err == nil {
		t.Fatal("parseEpochInfo() error = nil, want error for malformed JSON")
	}
}

// epochSwitchJSON builds an epoch info body with a configurable current block height and the
// three fallback-ladder inputs; a zero argument omits that field's contribution to the ladder.
func epochSwitchJSON(blockHeight, setNewValidators, nextSetNewValidators, nextPoCStart int64) string {
	return fmt.Sprintf(`{
		"block_height": %d,
		"phase": "Inference",
		"latest_epoch": {"index": 1, "poc_start_block_height": 900},
		"epoch_stages": {"set_new_validators": %d, "next_poc_start": %d},
		"next_epoch_stages": {"set_new_validators": %d},
		"is_confirmation_poc_active": false
	}`, blockHeight, setNewValidators, nextPoCStart, nextSetNewValidators)
}

// Test flow:
//  1. Build a table of block-height and fallback-ladder inputs: table varies which rung (current-epoch set_new_validators, next-epoch set_new_validators, next_poc_start, or latest_epoch poc_start_block_height) determines the expected switch height, including the rung-1 guard against a set_new_validators at or behind the block height, which has already happened.
//  2. Parse each generated epoch info body.
//  3. Assert the decoded EpochSwitchBlockHeight matches the case's expectation.
func TestParseEpochInfoEpochSwitchBlockHeightFallbackLadder(t *testing.T) {
	cases := []struct {
		name                 string
		blockHeight          int64
		setNewValidators     int64
		nextSetNewValidators int64
		nextPoCStart         int64
		want                 int64
	}{
		{"rung1: current epoch set_new_validators ahead of block height wins", 1000, 1200, 1300, 1100, 1200},
		{"rung1 boundary falls to rung2: at set_new_validators the switch has happened, so the next one is named", 1000, 1000, 1300, 1100, 1300},
		{"rung1 stale falls to rung2: set_new_validators behind block height is ignored", 1000, 500, 1300, 1100, 1300},
		{"rung2: next epoch set_new_validators wins when rung1 is zero", 1000, 0, 1300, 1100, 1300},
		{"rung3: next_poc_start wins when rungs 1-2 are zero", 1000, 0, 0, 1100, 1100},
		{"rung4: latest_epoch poc_start_block_height wins when rungs 1-3 are zero", 1000, 0, 0, 0, 900},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			body := epochSwitchJSON(testCase.blockHeight, testCase.setNewValidators, testCase.nextSetNewValidators, testCase.nextPoCStart)
			info, err := parseEpochInfo([]byte(body))
			if err != nil {
				t.Fatalf("parseEpochInfo(): %v", err)
			}
			if info.EpochSwitchBlockHeight != testCase.want {
				t.Fatalf("EpochSwitchBlockHeight = %d, want %d", info.EpochSwitchBlockHeight, testCase.want)
			}
		})
	}
}

// participantsMultiModelJSON has one participant with two models split across preserved and
// non-preserved nodes, one participant with only a non-preserved node, and one blank-index
// participant that must be dropped entirely.
const participantsMultiModelJSON = `{
	"active_participants": {
		"participants": [
			{
				"index": "gonka1abc",
				"inference_url": "http://host-a:8080",
				"models": ["model-x", "model-y"],
				"ml_nodes": [
					{"ml_nodes": [
						{"node_id": "node1", "timeslot_allocation": [true, true], "poc_weight": 100},
						{"node_id": "node2", "timeslot_allocation": [true, false], "poc_weight": 50}
					]},
					{"ml_nodes": [
						{"node_id": "node3", "timeslot_allocation": [true, true], "poc_weight": 30}
					]}
				]
			},
			{
				"index": "gonka1def",
				"inference_url": "http://host-b:8080",
				"models": ["model-x"],
				"ml_nodes": [
					{"ml_nodes": [
						{"node_id": "node4", "timeslot_allocation": [true, false], "poc_weight": 20}
					]}
				]
			},
			{
				"index": "   ",
				"inference_url": "http://ignored:8080",
				"models": [],
				"ml_nodes": []
			}
		]
	}
}`

// Test flow:
//  1. Parse the multi-model participants fixture in legacy preservation mode with an empty snapshot.
//  2. Assert the current and full weight maps, per-model weight maps, inference URLs, preserved/excluded participant lists, per-model preserved lists and per-participant node list all match the fixture's timeslot-preserved shares.
//  3. Assert the blank-index participant was dropped, leaving exactly two participants.
func TestParseParticipantsPoCActiveWeightsAndPreservation(t *testing.T) {
	state, err := parseParticipants([]byte(participantsMultiModelJSON), preservationModeLegacy, preservedSnapshotState{})
	if err != nil {
		t.Fatalf("parseParticipants(): %v", err)
	}

	wantWeights := map[string]float64{"gonka1abc": 130, "gonka1def": 0}
	if !reflect.DeepEqual(state.Weights, wantWeights) {
		t.Fatalf("Weights = %v, want %v", state.Weights, wantWeights)
	}

	wantFullWeights := map[string]float64{"gonka1abc": 180, "gonka1def": 20}
	if !reflect.DeepEqual(state.FullWeights, wantFullWeights) {
		t.Fatalf("FullWeights = %v, want %v", state.FullWeights, wantFullWeights)
	}

	wantWeightsByModel := map[string]map[string]float64{
		"model-x": {"gonka1abc": 100, "gonka1def": 0},
		"model-y": {"gonka1abc": 30},
	}
	if !reflect.DeepEqual(state.WeightsByModel, wantWeightsByModel) {
		t.Fatalf("WeightsByModel = %v, want %v", state.WeightsByModel, wantWeightsByModel)
	}

	wantFullWeightsByModel := map[string]map[string]float64{
		"model-x": {"gonka1abc": 150, "gonka1def": 20},
		"model-y": {"gonka1abc": 30},
	}
	if !reflect.DeepEqual(state.FullWeightsByModel, wantFullWeightsByModel) {
		t.Fatalf("FullWeightsByModel = %v, want %v", state.FullWeightsByModel, wantFullWeightsByModel)
	}

	wantInferenceURLs := map[string]string{"gonka1abc": "http://host-a:8080", "gonka1def": "http://host-b:8080"}
	if !reflect.DeepEqual(state.InferenceURLs, wantInferenceURLs) {
		t.Fatalf("InferenceURLs = %v, want %v", state.InferenceURLs, wantInferenceURLs)
	}

	wantPreserved := []string{"gonka1abc"}
	if !reflect.DeepEqual(state.Preserved, wantPreserved) {
		t.Fatalf("Preserved = %v, want %v", state.Preserved, wantPreserved)
	}
	wantExcluded := []string{"gonka1def"}
	if !reflect.DeepEqual(state.Excluded, wantExcluded) {
		t.Fatalf("Excluded = %v, want %v", state.Excluded, wantExcluded)
	}

	wantPreservedByModel := map[string][]string{"model-x": {"gonka1abc"}, "model-y": {"gonka1abc"}}
	if !reflect.DeepEqual(state.PreservedByModel, wantPreservedByModel) {
		t.Fatalf("PreservedByModel = %v, want %v", state.PreservedByModel, wantPreservedByModel)
	}

	wantNodes := []participantNode{
		{Model: "model-x", NodeID: "node1", Weight: 100},
		{Model: "model-x", NodeID: "node2", Weight: 50},
		{Model: "model-y", NodeID: "node3", Weight: 30},
	}
	if !reflect.DeepEqual(state.NodesByParticipant["gonka1abc"], wantNodes) {
		t.Fatalf("NodesByParticipant[gonka1abc] = %v, want %v", state.NodesByParticipant["gonka1abc"], wantNodes)
	}

	if len(state.Weights) != 2 {
		t.Fatalf("len(Weights) = %d, want 2 (blank-index participant must be dropped)", len(state.Weights))
	}
}

// Test flow:
//  1. Parse the multi-model participants fixture in preservationModeAll (PoC inactive) with an empty snapshot.
//  2. Assert current weights equal the full all-node weights for both participants.
//  3. Assert every participant is preserved and none is excluded.
func TestParseParticipantsPoCInactiveTreatsAllNodesAsPreserved(t *testing.T) {
	state, err := parseParticipants([]byte(participantsMultiModelJSON), preservationModeAll, preservedSnapshotState{})
	if err != nil {
		t.Fatalf("parseParticipants(): %v", err)
	}
	wantWeights := map[string]float64{"gonka1abc": 180, "gonka1def": 20}
	if !reflect.DeepEqual(state.Weights, wantWeights) {
		t.Fatalf("Weights = %v, want %v", state.Weights, wantWeights)
	}
	if !reflect.DeepEqual(state.Weights, state.FullWeights) {
		t.Fatalf("Weights = %v, FullWeights = %v; want equal when PoC inactive", state.Weights, state.FullWeights)
	}
	wantPreserved := []string{"gonka1abc", "gonka1def"}
	if !reflect.DeepEqual(state.Preserved, wantPreserved) {
		t.Fatalf("Preserved = %v, want %v", state.Preserved, wantPreserved)
	}
	if len(state.Excluded) != 0 {
		t.Fatalf("Excluded = %v, want empty when PoC inactive", state.Excluded)
	}
}

// participantsSnapshotOverride is the snapshot whose membership replaces the timeslot rule.
func participantsSnapshotOverride() *PreservedNodes {
	return &PreservedNodes{
		EpisodeAnchorHeight: 950,
		Models: []PreservedModel{{
			ModelID: "model-x",
			Participants: []PreservedParticipant{
				{ParticipantID: "gonka1abc", NodeIDs: []string{"node2"}},
				{ParticipantID: "gonka1def", NodeIDs: []string{"node4"}},
			},
		}},
	}
}

// Test flow:
//  1. Build a preserved-nodes snapshot for model-x whose membership deliberately contradicts the timeslot flags: it lists node2/node4 (timeslot-non-preserved) and omits node1/node3 (timeslot-preserved).
//  2. Read that snapshot through readPreserved and assert it comes back current.
//  3. Parse the multi-model participants fixture in preservationModeSnapshot with that snapshot.
//  4. Assert current weights and per-model weights follow the snapshot's membership rather than the timeslot flags, while full weights stay the all-node totals.
//  5. Assert both participants are preserved, none is excluded, and the per-model preserved list reflects the snapshot.
func TestParseParticipantsSnapshotModeUsesSnapshotMembership(t *testing.T) {
	preservedNodes, status, err := readPreserved(t, fakeReader{preserved: participantsSnapshotOverride(), found: true}, 950)
	if err != nil || status != preservedSnapshotCurrent {
		t.Fatalf("fetchPreservedSnapshot() = (status %v, err %v), want current/nil", status, err)
	}

	state, err := parseParticipants([]byte(participantsMultiModelJSON), preservationModeSnapshot, preservedNodes)
	if err != nil {
		t.Fatalf("parseParticipants(): %v", err)
	}

	wantWeights := map[string]float64{"gonka1abc": 50, "gonka1def": 20}
	if !reflect.DeepEqual(state.Weights, wantWeights) {
		t.Fatalf("Weights = %v, want %v (snapshot membership, not timeslots)", state.Weights, wantWeights)
	}
	wantWeightsByModel := map[string]map[string]float64{
		"model-x": {"gonka1abc": 50, "gonka1def": 20},
		"model-y": {"gonka1abc": 0},
	}
	if !reflect.DeepEqual(state.WeightsByModel, wantWeightsByModel) {
		t.Fatalf("WeightsByModel = %v, want %v", state.WeightsByModel, wantWeightsByModel)
	}
	wantFullWeights := map[string]float64{"gonka1abc": 180, "gonka1def": 20}
	if !reflect.DeepEqual(state.FullWeights, wantFullWeights) {
		t.Fatalf("FullWeights = %v, want %v (all-node, mode-independent)", state.FullWeights, wantFullWeights)
	}
	wantPreserved := []string{"gonka1abc", "gonka1def"}
	if !reflect.DeepEqual(state.Preserved, wantPreserved) {
		t.Fatalf("Preserved = %v, want %v", state.Preserved, wantPreserved)
	}
	if len(state.Excluded) != 0 {
		t.Fatalf("Excluded = %v, want empty", state.Excluded)
	}
	wantPreservedByModel := map[string][]string{"model-x": {"gonka1abc", "gonka1def"}}
	if !reflect.DeepEqual(state.PreservedByModel, wantPreservedByModel) {
		t.Fatalf("PreservedByModel = %v, want %v", state.PreservedByModel, wantPreservedByModel)
	}
}

// Test flow:
//  1. Parse a participants body listing two participants in reverse alphabetical wire order.
//  2. Assert the Preserved list comes back sorted, not in wire order.
func TestParseParticipantsPreservedIsSorted(t *testing.T) {
	reverseOrderJSON := `{
		"active_participants": {
			"participants": [
				{
					"index": "gonka1zzz",
					"inference_url": "http://host-z:8080",
					"models": ["model-a"],
					"ml_nodes": [{"ml_nodes": [{"node_id": "nz", "timeslot_allocation": [true, true], "poc_weight": 1}]}]
				},
				{
					"index": "gonka1aaa",
					"inference_url": "http://host-a:8080",
					"models": ["model-a"],
					"ml_nodes": [{"ml_nodes": [{"node_id": "na", "timeslot_allocation": [true, true], "poc_weight": 2}]}]
				}
			]
		}
	}`
	state, err := parseParticipants([]byte(reverseOrderJSON), preservationModeLegacy, preservedSnapshotState{})
	if err != nil {
		t.Fatalf("parseParticipants(): %v", err)
	}
	wantPreserved := []string{"gonka1aaa", "gonka1zzz"}
	if !reflect.DeepEqual(state.Preserved, wantPreserved) {
		t.Fatalf("Preserved = %v, want sorted %v", state.Preserved, wantPreserved)
	}
}

// Test flow:
//  1. Build a base participants state from the multi-model fixture, where gonka1def starts excluded.
//  2. Merge with a capability check that marks gonka1def's node4 as validation-capable; assert it rejoins preserved/preservedByModel with its node's weight added, while the input state's own fields stay unmutated.
//  3. Merge with a capability check that marks no node capable; assert gonka1def stays excluded with zero weight.
//  4. Merge with a nil capability check; assert the preserved list and weights are unchanged copies of the input state.
func TestMergePreservedWithValidationCapable(t *testing.T) {
	buildState := func(t *testing.T) participantsState {
		t.Helper()
		state, err := parseParticipants([]byte(participantsMultiModelJSON), preservationModeLegacy, preservedSnapshotState{})
		if err != nil {
			t.Fatalf("parseParticipants(): %v", err)
		}
		return state
	}
	defCapable := func(miner, nodeID string) bool { return miner == "gonka1def" && nodeID == "node4" }
	noneCapable := func(miner, nodeID string) bool { return false }

	t.Run("capable excluded miner rejoins with capable-node weight", func(t *testing.T) {
		state := buildState(t)

		preserved, preservedByModel, currentWeights, currentWeightsByModel := mergePreservedWithValidationCapable(state, defCapable)

		if want := []string{"gonka1abc", "gonka1def"}; !reflect.DeepEqual(preserved, want) {
			t.Fatalf("preserved = %v, want %v", preserved, want)
		}
		if want := map[string]float64{"gonka1abc": 130, "gonka1def": 20}; !reflect.DeepEqual(currentWeights, want) {
			t.Fatalf("currentWeights = %v, want %v", currentWeights, want)
		}
		wantByModel := map[string]map[string]float64{
			"model-x": {"gonka1abc": 100, "gonka1def": 20},
			"model-y": {"gonka1abc": 30},
		}
		if !reflect.DeepEqual(currentWeightsByModel, wantByModel) {
			t.Fatalf("currentWeightsByModel = %v, want %v", currentWeightsByModel, wantByModel)
		}
		wantPreservedByModel := map[string][]string{"model-x": {"gonka1abc", "gonka1def"}, "model-y": {"gonka1abc"}}
		if !reflect.DeepEqual(preservedByModel, wantPreservedByModel) {
			t.Fatalf("preservedByModel = %v, want %v", preservedByModel, wantPreservedByModel)
		}

		if want := []string{"gonka1abc"}; !reflect.DeepEqual(state.Preserved, want) {
			t.Fatalf("input state.Preserved mutated to %v, want %v", state.Preserved, want)
		}
		if state.Weights["gonka1def"] != 0 || state.WeightsByModel["model-x"]["gonka1def"] != 0 {
			t.Fatalf("input state weights mutated: %v / %v", state.Weights, state.WeightsByModel)
		}
		if want := map[string][]string{"model-x": {"gonka1abc"}, "model-y": {"gonka1abc"}}; !reflect.DeepEqual(state.PreservedByModel, want) {
			t.Fatalf("input state.PreservedByModel mutated to %v, want %v", state.PreservedByModel, want)
		}
	})

	t.Run("non-capable excluded miner stays out", func(t *testing.T) {
		state := buildState(t)

		preserved, preservedByModel, currentWeights, _ := mergePreservedWithValidationCapable(state, noneCapable)

		if want := []string{"gonka1abc"}; !reflect.DeepEqual(preserved, want) {
			t.Fatalf("preserved = %v, want %v", preserved, want)
		}
		if currentWeights["gonka1def"] != 0 {
			t.Fatalf("currentWeights[gonka1def] = %v, want 0", currentWeights["gonka1def"])
		}
		if want := map[string][]string{"model-x": {"gonka1abc"}, "model-y": {"gonka1abc"}}; !reflect.DeepEqual(preservedByModel, want) {
			t.Fatalf("preservedByModel = %v, want %v", preservedByModel, want)
		}
	})

	t.Run("nil capability check keeps views unchanged", func(t *testing.T) {
		state := buildState(t)

		preserved, _, currentWeights, _ := mergePreservedWithValidationCapable(state, nil)

		if want := []string{"gonka1abc"}; !reflect.DeepEqual(preserved, want) {
			t.Fatalf("preserved = %v, want %v", preserved, want)
		}
		if !reflect.DeepEqual(currentWeights, state.Weights) {
			t.Fatalf("currentWeights = %v, want copy of %v", currentWeights, state.Weights)
		}
	})
}

// Test flow:
//  1. Parse a numeric string with surrounding whitespace through parseFlexibleInt64 and parseFlexibleUint64 and assert both return the trimmed value.
//  2. Parse a string with embedded whitespace through both decoders and assert both return an error.
func TestParseFlexibleIntegersUseStrictDecimalParsing(t *testing.T) {
	if value, err := parseFlexibleInt64([]byte(`" 950 "`)); err != nil || value != 950 {
		t.Fatalf(`parseFlexibleInt64(" 950 ") = (%d, %v), want (950, nil)`, value, err)
	}
	if value, err := parseFlexibleUint64([]byte(`" 950 "`)); err != nil || value != 950 {
		t.Fatalf(`parseFlexibleUint64(" 950 ") = (%d, %v), want (950, nil)`, value, err)
	}
	if _, err := parseFlexibleInt64([]byte(`"12 34"`)); err == nil {
		t.Fatal(`parseFlexibleInt64("12 34") error = nil, want error`)
	}
	if _, err := parseFlexibleUint64([]byte(`"12 34"`)); err == nil {
		t.Fatal(`parseFlexibleUint64("12 34") error = nil, want error`)
	}
}

// Test flow:
//  1. Parse a participants body with an empty participants array.
//  2. Assert every map field (Weights, FullWeights, WeightsByModel, FullWeightsByModel, InferenceURLs, PreservedByModel, NodesByParticipant) has length zero and is non-nil.
//  3. Assert the Preserved and Excluded lists are both empty.
func TestParseParticipantsEmptyArrayYieldsEmptyMapsNotNil(t *testing.T) {
	state, err := parseParticipants([]byte(`{"active_participants":{"participants":[]}}`), preservationModeLegacy, preservedSnapshotState{})
	if err != nil {
		t.Fatalf("parseParticipants(): %v", err)
	}

	mapLengths := map[string]int{
		"Weights":            len(state.Weights),
		"FullWeights":        len(state.FullWeights),
		"WeightsByModel":     len(state.WeightsByModel),
		"FullWeightsByModel": len(state.FullWeightsByModel),
		"InferenceURLs":      len(state.InferenceURLs),
		"PreservedByModel":   len(state.PreservedByModel),
		"NodesByParticipant": len(state.NodesByParticipant),
	}
	for name, length := range mapLengths {
		if length != 0 {
			t.Fatalf("%s len = %d, want 0", name, length)
		}
	}
	if state.Weights == nil || state.FullWeights == nil || state.WeightsByModel == nil ||
		state.FullWeightsByModel == nil || state.InferenceURLs == nil || state.PreservedByModel == nil ||
		state.NodesByParticipant == nil {
		t.Fatal("maps must be non-nil even for an empty participants array")
	}
	if len(state.Preserved) != 0 || len(state.Excluded) != 0 {
		t.Fatalf("Preserved/Excluded = %v/%v, want both empty", state.Preserved, state.Excluded)
	}
}

// Test flow:
//  1. Call parseParticipants with malformed JSON.
//  2. Assert it returns an error.
func TestParseParticipantsMalformedJSONErrors(t *testing.T) {
	_, err := parseParticipants([]byte(`{not-json`), preservationModeLegacy, preservedSnapshotState{})
	if err == nil {
		t.Fatal("parseParticipants() error = nil, want error for malformed JSON")
	}
}

// Test flow:
//  1. Build a table of reader outcomes and expected anchor heights: table varies between an anchor match, a skipped anchor check, an anchor mismatch, no snapshot on chain, a found-but-empty snapshot, and a failed chain read.
//  2. Call readPreserved for each case.
//  3. Assert the returned status matches the case's expectation (current, missing, or unavailable) and that an error is returned only for the failed read.
func TestPreservedSnapshotStatusMatrix(t *testing.T) {
	stale := preservedFixture()
	testCases := []struct {
		name           string
		reader         fakeReader
		expectedAnchor int64
		wantStatus     preservedSnapshotStatus
		wantErr        bool
	}{
		{name: "anchor_matches", reader: fakeReader{preserved: preservedFixture(), found: true}, expectedAnchor: 950, wantStatus: preservedSnapshotCurrent},
		{name: "zero_expected_anchor_skips_the_check", reader: fakeReader{preserved: preservedFixture(), found: true}, wantStatus: preservedSnapshotCurrent},
		{name: "anchor_mismatch", reader: fakeReader{preserved: stale, found: true}, expectedAnchor: 999, wantStatus: preservedSnapshotMissingCurrent},
		{name: "chain_has_none", reader: fakeReader{}, expectedAnchor: 950, wantStatus: preservedSnapshotMissingCurrent},
		{name: "found_but_empty", reader: fakeReader{preserved: &PreservedNodes{EpisodeAnchorHeight: 950}, found: true}, expectedAnchor: 950, wantStatus: preservedSnapshotCurrent},
		{name: "read_failed", reader: fakeReader{err: errors.New("chain unreachable")}, expectedAnchor: 950, wantStatus: preservedSnapshotUnavailable, wantErr: true},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			_, status, err := readPreserved(t, testCase.reader, testCase.expectedAnchor)

			if testCase.wantErr != (err != nil) {
				t.Fatalf("err = %v, wantErr %v", err, testCase.wantErr)
			}
			if status != testCase.wantStatus {
				t.Fatalf("status = %v, want %v", status, testCase.wantStatus)
			}
		})
	}
}

// Test flow:
//  1. Read the preserved-nodes fixture through readPreserved and assert it comes back current.
//  2. Build a table of (model, participant, node) lookups: table varies between a preserved node, an absent node, the wrong model, and the wrong participant.
//  3. Call Has for each case and assert it returns the expected boolean.
func TestParsePreservedSnapshotHasLooksUpByModelParticipantNode(t *testing.T) {
	state, status, err := readPreserved(t, fakeReader{preserved: preservedFixture(), found: true}, 950)
	if err != nil {
		t.Fatalf("fetchPreservedSnapshot(): %v", err)
	}
	if status != preservedSnapshotCurrent {
		t.Fatalf("status = %v, want preservedSnapshotCurrent", status)
	}
	cases := []struct {
		name          string
		model         string
		participantID string
		nodeID        string
		want          bool
	}{
		{"preserved node", "model-x", "gonka1abc", "node1", true},
		{"preserved node, second id", "model-x", "gonka1abc", "node3", true},
		{"node absent from snapshot", "model-x", "gonka1abc", "node2", false},
		{"wrong model", "model-y", "gonka1abc", "node1", false},
		{"wrong participant", "model-x", "gonka1xyz", "node1", false},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := state.Has(testCase.model, testCase.participantID, testCase.nodeID); got != testCase.want {
				t.Fatalf("Has(%q, %q, %q) = %v, want %v", testCase.model, testCase.participantID, testCase.nodeID, got, testCase.want)
			}
		})
	}
}

// Test flow:
//  1. Read an empty-but-current preserved-nodes snapshot (no models) through readPreserved.
//  2. Assert the status comes back current.
//  3. Call Has against it and assert it returns false without panicking.
func TestParsePreservedSnapshotEmptyModelsHasIsSafe(t *testing.T) {
	state, status, err := readPreserved(t, fakeReader{preserved: &PreservedNodes{EpisodeAnchorHeight: 950}, found: true}, 950)
	if err != nil {
		t.Fatalf("fetchPreservedSnapshot(): %v", err)
	}
	if status != preservedSnapshotCurrent {
		t.Fatalf("status = %v, want preservedSnapshotCurrent", status)
	}
	if state.Has("model-x", "gonka1abc", "node1") {
		t.Fatal("Has() = true against an empty snapshot, want false")
	}
}

// Test flow:
//  1. Parse two epoch info bodies carrying the same values, one with numeric JSON integers and one with the same values as JSON strings.
//  2. Assert both parses produce identical epochInfo values.
//  3. Assert the numeric parse's BlockHeight is the expected 1000.
func TestParseEpochAcceptsStringAndNumericIntegers(t *testing.T) {
	numeric := `{"block_height": 1000, "phase": "Inference",
		"latest_epoch": {"index": 7, "poc_start_block_height": 900},
		"epoch_stages": {"set_new_validators": 950, "next_poc_start": 1900},
		"is_confirmation_poc_active": false}`
	stringified := `{"block_height": "1000", "phase": "Inference",
		"latest_epoch": {"index": "7", "poc_start_block_height": "900"},
		"epoch_stages": {"set_new_validators": "950", "next_poc_start": "1900"},
		"is_confirmation_poc_active": false}`

	fromNumeric, err := parseEpochInfo([]byte(numeric))
	if err != nil {
		t.Fatalf("parseEpochInfo(numeric): %v", err)
	}
	fromString, err := parseEpochInfo([]byte(stringified))
	if err != nil {
		t.Fatalf("parseEpochInfo(stringified): %v", err)
	}

	if !reflect.DeepEqual(fromNumeric, fromString) {
		t.Fatalf("the two wire shapes parsed differently:\n numeric = %+v\n string  = %+v", fromNumeric, fromString)
	}
	if fromNumeric.BlockHeight != 1000 {
		t.Fatalf("BlockHeight = %d, want 1000", fromNumeric.BlockHeight)
	}
}

// fakeReader answers the observer's chain reads in process.
type fakeReader struct {
	preserved *PreservedNodes
	found     bool
	err       error
	maxNonce  uint64
	nonceHeld bool
	nonceErr  error
	models    map[string]ModelParams
	modelsErr error
}

func (f fakeReader) PreservedNodes(context.Context) (*PreservedNodes, bool, error) {
	return f.preserved, f.found, f.err
}

func (f fakeReader) EscrowParams(context.Context) (EscrowParams, bool, error) {
	return EscrowParams{MaxNonce: f.maxNonce}, f.nonceHeld, f.nonceErr
}

func (f fakeReader) Models(context.Context) (map[string]ModelParams, error) {
	return f.models, f.modelsErr
}

// readPreserved drives the observer's own fetch, so these tests cover the path production takes rather
// than the derivation alone.
func readPreserved(t *testing.T, reader Reader, expectedAnchor int64) (preservedSnapshotState, preservedSnapshotStatus, error) {
	t.Helper()
	observer, err := NewPhaseObserver(ObserverConfig{PublicAPIBaseURL: "http://public.invalid", Chain: reader})
	if err != nil {
		t.Fatalf("NewPhaseObserver: %v", err)
	}
	return observer.fetchPreservedSnapshot(context.Background(), expectedAnchor)
}

// preservedFixture is the snapshot the JSON fixtures used to carry.
func preservedFixture() *PreservedNodes {
	return &PreservedNodes{
		EpisodeAnchorHeight: 950,
		Models: []PreservedModel{{
			ModelID: "model-x",
			Participants: []PreservedParticipant{
				{ParticipantID: "gonka1abc", NodeIDs: []string{"node1", "node3"}},
				{ParticipantID: "gonka1def", NodeIDs: []string{"node9"}},
			},
		}},
	}
}

// Test flow:
//  1. Table-driven: each case is an epoch info body for latest epoch 8 at one height: before its PoC, inside its PoC, at its set_new_validators, and two bodies missing a height the derivation needs.
//  2. Parse each body.
//  3. Assert the effective epoch is 8, 7, 8, or unknown (0), as the case says.
func TestParseEpochInfoDerivesTheEffectiveEpoch(t *testing.T) {
	body := func(height, pocStart, setNewValidators int64) string {
		return fmt.Sprintf(`{"block_height": %d, "phase": "Inference", "latest_epoch": {"index": 8, "poc_start_block_height": %d}, "epoch_stages": {"set_new_validators": %d}}`,
			height, pocStart, setNewValidators)
	}
	testCases := []struct {
		name string
		body string
		want uint64
	}{
		{name: "past the switch the latest epoch is effective", body: body(3000, 2000, 2100), want: 8},
		{name: "inside PoC the epoch before the latest is effective", body: body(2050, 2000, 2100), want: 7},
		{name: "at set_new_validators the latest epoch is effective", body: body(2100, 2000, 2100), want: 8},
		{name: "no PoC start reads unknown", body: body(2050, 0, 2100), want: 0},
		{name: "no set_new_validators reads unknown", body: body(2050, 2000, 0), want: 0},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			info, err := parseEpochInfo([]byte(testCase.body))
			if err != nil {
				t.Fatalf("parseEpochInfo(): %v", err)
			}
			if info.EffectiveEpochIndex != testCase.want {
				t.Fatalf("parseEpochInfo(%s).EffectiveEpochIndex = %d, want %d", testCase.name, info.EffectiveEpochIndex, testCase.want)
			}
		})
	}
}

// Test flow:
//  1. Table-driven: epoch info bodies for latest epoch 8 (PoC start 2000, set_new_validators 2100, the next one 3100) one block before, exactly at, and one block after set_new_validators.
//  2. Parse each body.
//  3. Assert the effective epoch and the switch height agree: before it, effective 7 with the switch at 2100; at and after it, effective 8 with the switch at the next one, 3100.
func TestParseEpochInfoAgreesOnTheSwitchAroundSetNewValidators(t *testing.T) {
	body := func(height int64) string {
		return fmt.Sprintf(`{"block_height": %d, "phase": "Inference", "latest_epoch": {"index": 8, "poc_start_block_height": 2000}, "epoch_stages": {"set_new_validators": 2100, "next_poc_start": 2900}, "next_epoch_stages": {"set_new_validators": 3100}}`, height)
	}
	testCases := []struct {
		name          string
		height        int64
		wantEffective uint64
		wantSwitch    int64
	}{
		{name: "one block before set_new_validators", height: 2099, wantEffective: 7, wantSwitch: 2100},
		{name: "exactly at set_new_validators", height: 2100, wantEffective: 8, wantSwitch: 3100},
		{name: "one block after set_new_validators", height: 2101, wantEffective: 8, wantSwitch: 3100},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			info, err := parseEpochInfo([]byte(body(testCase.height)))
			if err != nil {
				t.Fatalf("parseEpochInfo(): %v", err)
			}
			if info.EffectiveEpochIndex != testCase.wantEffective || info.EpochSwitchBlockHeight != testCase.wantSwitch {
				t.Fatalf("parseEpochInfo(height %d) = effective %d switch %d, want effective %d switch %d", testCase.height, info.EffectiveEpochIndex, info.EpochSwitchBlockHeight, testCase.wantEffective, testCase.wantSwitch)
			}
		})
	}
}
