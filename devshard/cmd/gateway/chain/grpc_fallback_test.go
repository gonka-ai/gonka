package chain

import (
	"reflect"
	"testing"

	inferencetypes "github.com/productscience/inference/x/inference/types"
)

// Test flow:
//  1. Build a node epoch-info answer for epoch 7 at block 1000 with default epoch params and a COMPLETED confirmation-PoC event triggered at 555.
//  2. Convert it with `epochInfoPayloadFromResponse` and read it back through `epochInfoFromPayload`.
//  3. Assert the phase, the current and next set-new-validators heights, the block height, epoch index, PoC start, next PoC start, confirmation phase and trigger height match what the node reported and what the epoch context derives.
func TestEpochInfoPayloadCarriesWhatThePublicAPIWouldServe(t *testing.T) {
	params := inferencetypes.DefaultEpochParams()
	response := &inferencetypes.QueryEpochInfoResponse{
		BlockHeight:             1000,
		Params:                  inferencetypes.Params{EpochParams: params},
		LatestEpoch:             inferencetypes.Epoch{Index: 7, PocStartBlockHeight: 900},
		IsConfirmationPocActive: true,
		ActiveConfirmationPocEvent: &inferencetypes.ConfirmationPoCEvent{
			Phase:         inferencetypes.ConfirmationPoCPhase_CONFIRMATION_POC_COMPLETED,
			TriggerHeight: 555,
		},
	}

	payload, err := epochInfoPayloadFromResponse(response)
	if err != nil {
		t.Fatalf("epochInfoPayloadFromResponse: %v", err)
	}
	info := epochInfoFromPayload(payload)

	epochContext := inferencetypes.NewEpochContext(response.LatestEpoch, *params)
	nextPoCStart := epochContext.NextPoCStart()
	currentStages := epochContext.GetEpochStages()
	nextEpoch := epochContext.NextEpochContext()
	nextStages := nextEpoch.GetEpochStages()
	if payload.Phase != string(epochContext.GetCurrentPhase(1000)) {
		t.Errorf("Phase = %q, want %q", payload.Phase, epochContext.GetCurrentPhase(1000))
	}
	if int64(payload.EpochStages.SetNewValidators) != currentStages.SetNewValidators || int64(payload.NextEpochStages.SetNewValidators) != nextStages.SetNewValidators {
		t.Errorf("set-new-validators = %d / next %d, want %d / next %d", payload.EpochStages.SetNewValidators, payload.NextEpochStages.SetNewValidators, currentStages.SetNewValidators, nextStages.SetNewValidators)
	}
	if info.BlockHeight != 1000 || info.EpochIndex != 7 || info.PoCStartBlockHeight != 900 || info.NextPoCStartBlockHeight != nextPoCStart {
		t.Errorf("epoch info = %+v, want block 1000, epoch 7, PoC start 900, next PoC start %d", info, nextPoCStart)
	}
	if info.ConfirmationPoCPhase != ConfirmationPoCCompleted || info.ConfirmationPoCTriggerHeight != 555 {
		t.Errorf("confirmation PoC = %s at %d, want %s at 555", info.ConfirmationPoCPhase, info.ConfirmationPoCTriggerHeight, ConfirmationPoCCompleted)
	}
}

// Test flow:
//  1. Build a node epoch-info answer without epoch params.
//  2. Convert it with `epochInfoPayloadFromResponse`.
//  3. Assert it fails instead of deriving stages from zero params.
func TestEpochInfoPayloadRefusesAnAnswerWithoutEpochParams(t *testing.T) {
	if _, err := epochInfoPayloadFromResponse(&inferencetypes.QueryEpochInfoResponse{BlockHeight: 1000}); err == nil {
		t.Fatal("epochInfoPayloadFromResponse succeeded without epoch params, want an error")
	}
}

// Test flow:
//  1. Build the node's active participants: one miner with a node of weight 100 under model-a and a timeslot flag pair, and one nil entry.
//  2. Convert them with `participantsPayloadFromActive` and fold the payload with the all-nodes rule.
//  3. Assert the miner keeps its address, URL, model, node and weight, and the nil entry is skipped.
func TestActiveParticipantsConvertToThePublicAPIShape(t *testing.T) {
	active := &inferencetypes.ActiveParticipants{Participants: []*inferencetypes.ActiveParticipant{
		{
			Index:        "gonka1abc",
			InferenceUrl: "https://host-a.example",
			Models:       []string{"model-a"},
			MlNodes: []*inferencetypes.ModelMLNodes{{MlNodes: []*inferencetypes.MLNodeInfo{
				{NodeId: "node1", PocWeight: 100, TimeslotAllocation: []bool{true, false}},
			}}},
		},
		nil,
	}}

	state := foldParticipants(participantsPayloadFromActive(active), preservationModeAll, preservedSnapshotState{})

	if want := map[string]float64{"gonka1abc": 100}; !reflect.DeepEqual(state.Weights, want) {
		t.Errorf("Weights = %v, want %v", state.Weights, want)
	}
	if want := map[string]string{"gonka1abc": "https://host-a.example"}; !reflect.DeepEqual(state.InferenceURLs, want) {
		t.Errorf("InferenceURLs = %v, want %v", state.InferenceURLs, want)
	}
	if want := map[string]map[string]float64{"model-a": {"gonka1abc": 100}}; !reflect.DeepEqual(state.WeightsByModel, want) {
		t.Errorf("WeightsByModel = %v, want %v", state.WeightsByModel, want)
	}
}
