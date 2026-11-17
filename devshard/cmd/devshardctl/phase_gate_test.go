package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/cosmos/cosmos-sdk/client/grpc/cmtservice"
	"github.com/golang/protobuf/proto"
	inferencetypes "github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
)

func TestChainPhaseGateFetchEpochInfoParsesConfirmationPoC(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/v1/epochs/latest", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"block_height":"150",
			"phase":"Inference",
			"latest_epoch":{
				"index":"12",
				"poc_start_block_height":"100"
			},
			"is_confirmation_poc_active":true,
			"active_confirmation_poc_event":{
				"phase":"CONFIRMATION_POC_VALIDATION"
			}
		}`))
	}))
	defer server.Close()

	gate := NewChainPhaseGate(server.URL, 0)
	require.NotNil(t, gate)

	resp, err := gate.fetchEpochInfo()
	require.NoError(t, err)

	snapshot := deriveChainPhaseSnapshot(resp)
	require.Equal(t, int64(150), snapshot.BlockHeight)
	require.Equal(t, uint64(12), snapshot.EpochIndex)
	require.Equal(t, epochPhaseInference, snapshot.EpochPhase)
	require.Equal(t, confirmationPoCValidation, snapshot.ConfirmationPoCPhase)
	require.True(t, snapshot.RequestsBlocked)
	require.Equal(t, "confirmation_poc", snapshot.BlockReason)
}

func TestChainPhaseGateFetchEpochInfoFallsBackToChain(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	}))
	defer server.Close()

	gate := NewChainPhaseGate(server.URL, 0)
	gate.SetChainQueryClient(&preservedSnapshotStub{
		epochInfoResp: &inferencetypes.QueryEpochInfoResponse{
			BlockHeight: 105,
			Params: inferencetypes.Params{EpochParams: &inferencetypes.EpochParams{
				EpochLength:           100,
				PocStageDuration:      20,
				PocValidationDelay:    5,
				PocValidationDuration: 20,
				SetNewValidatorsDelay: 5,
			}},
			LatestEpoch: inferencetypes.Epoch{
				Index:               12,
				PocStartBlockHeight: 100,
			},
		},
	})

	resp, err := gate.fetchEpochInfo()
	require.NoError(t, err)
	snapshot := deriveChainPhaseSnapshot(resp)
	require.Equal(t, int64(105), snapshot.BlockHeight)
	require.Equal(t, uint64(12), snapshot.EpochIndex)
	require.Equal(t, epochPhasePoCGenerate, snapshot.EpochPhase)
}

func TestChainPhaseGateDerivesEpochSwitchFromCurrentSetNewValidators(t *testing.T) {
	resp := &chainEpochInfoResponse{
		BlockHeight: jsonInt64(150),
		Phase:       "PoCGenerate",
		LatestEpoch: chainLatestEpoch{
			Index:               jsonUint64(12),
			PocStartBlockHeight: jsonInt64(100),
		},
		EpochStages: chainEpochStages{
			SetNewValidators: jsonInt64(180),
			NextPoCStart:     jsonInt64(200),
		},
		NextEpochStages: chainEpochStages{
			EpochIndex:       jsonUint64(13),
			SetNewValidators: jsonInt64(600),
		},
	}

	snapshot := deriveChainPhaseSnapshot(resp)
	require.Equal(t, int64(180), snapshot.epochSwitchBlockHeight)
}

func TestChainPhaseGateDerivesEpochSwitchFromNextSetNewValidatorsAfterCurrentSwitch(t *testing.T) {
	resp := &chainEpochInfoResponse{
		BlockHeight: jsonInt64(250),
		Phase:       "Inference",
		LatestEpoch: chainLatestEpoch{
			Index:               jsonUint64(12),
			PocStartBlockHeight: jsonInt64(100),
		},
		EpochStages: chainEpochStages{
			SetNewValidators: jsonInt64(180),
			NextPoCStart:     jsonInt64(200),
		},
		NextEpochStages: chainEpochStages{
			EpochIndex:       jsonUint64(13),
			SetNewValidators: jsonInt64(600),
		},
	}

	snapshot := deriveChainPhaseSnapshot(resp)
	require.Equal(t, int64(600), snapshot.epochSwitchBlockHeight)
}

func TestChainPhaseGateFetchEpochInfoParsesNumericConfirmationPoCGracePeriod(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/v1/epochs/latest", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"block_height":"151",
			"phase":"Inference",
			"latest_epoch":{
				"index":"12",
				"poc_start_block_height":"100"
			},
			"is_confirmation_poc_active":true,
			"active_confirmation_poc_event":{
				"phase":1
			}
		}`))
	}))
	defer server.Close()

	gate := NewChainPhaseGate(server.URL, 0)
	require.NotNil(t, gate)

	resp, err := gate.fetchEpochInfo()
	require.NoError(t, err)

	snapshot := deriveChainPhaseSnapshot(resp)
	require.Equal(t, confirmationPoCGracePeriod, snapshot.ConfirmationPoCPhase)
	require.True(t, snapshot.RequestsBlocked)
	require.Equal(t, "confirmation_poc", snapshot.BlockReason)
	require.Equal(t, "confirmation PoC grace period", humanizePhaseBlockReason(snapshot))
}

func TestChainPhaseGateFetchEpochInfoParsesNumericConfirmationPoCCompleted(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/v1/epochs/latest", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"block_height":"152",
			"phase":"Inference",
			"latest_epoch":{
				"index":"12",
				"poc_start_block_height":"100"
			},
			"is_confirmation_poc_active":true,
			"active_confirmation_poc_event":{
				"phase":4
			}
		}`))
	}))
	defer server.Close()

	gate := NewChainPhaseGate(server.URL, 0)
	require.NotNil(t, gate)

	resp, err := gate.fetchEpochInfo()
	require.NoError(t, err)

	snapshot := deriveChainPhaseSnapshot(resp)
	require.Equal(t, confirmationPoCCompleted, snapshot.ConfirmationPoCPhase)
	require.False(t, snapshot.RequestsBlocked)
	require.Empty(t, snapshot.BlockReason)
}

func TestChainPhaseGateBlocksDuringRegularPoC(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/v1/epochs/latest", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"block_height":"105",
			"phase":"PoCGenerate",
			"latest_epoch":{
				"index":"12",
				"poc_start_block_height":"100"
			},
			"is_confirmation_poc_active":false
		}`))
	}))
	defer server.Close()

	gate := NewChainPhaseGate(server.URL, 0)
	require.NotNil(t, gate)

	resp, err := gate.fetchEpochInfo()
	require.NoError(t, err)

	snapshot := deriveChainPhaseSnapshot(resp)
	require.Equal(t, epochPhasePoCGenerate, snapshot.EpochPhase)
	require.True(t, snapshot.RequestsBlocked)
	require.Equal(t, "poc", snapshot.BlockReason)
}

func TestChainPhaseGateTemporarilyLimitsSpeculativeAttempts(t *testing.T) {
	previous := CurrentMaxSpeculativeAttempts()
	SetMaxSpeculativeAttempts(4)
	t.Cleanup(func() {
		SetMaxSpeculativeAttempts(previous)
	})

	gate := NewChainPhaseGate("http://api:9000", 0)
	require.NotNil(t, gate)
	require.Equal(t, 4, gate.defaultMaxSpeculativeAttempts)

	gate.applySpeculativeAttemptPolicy(ChainPhaseSnapshot{
		EpochPhase:      epochPhasePoCGenerate,
		RequestsBlocked: true,
		BlockReason:     "poc",
	})
	require.Equal(t, 1, CurrentMaxSpeculativeAttempts())

	gate.applySpeculativeAttemptPolicy(ChainPhaseSnapshot{
		EpochPhase:      epochPhaseInference,
		RequestsBlocked: false,
	})
	require.Equal(t, 4, CurrentMaxSpeculativeAttempts())
}

func TestChainPhaseGateRelaxedModeAllowsRequestsDuringPoC(t *testing.T) {
	setPoCModeForTest(t, pocRequestModeRelaxed)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/v1/epochs/latest", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"block_height":"105",
			"phase":"PoCGenerate",
			"latest_epoch":{
				"index":"12",
				"poc_start_block_height":"100"
			},
			"is_confirmation_poc_active":false
		}`))
	}))
	defer server.Close()

	gate := NewChainPhaseGate(server.URL, 0)
	require.NotNil(t, gate)

	resp, err := gate.fetchEpochInfo()
	require.NoError(t, err)

	snapshot := deriveChainPhaseSnapshot(resp)
	require.Equal(t, epochPhasePoCGenerate, snapshot.EpochPhase)
	require.False(t, snapshot.RequestsBlocked)
	require.Equal(t, "poc", snapshot.BlockReason)
}

func TestChainPhaseGateRelaxedModeKeepsSpeculativeAttemptsUnclamped(t *testing.T) {
	setPoCModeForTest(t, pocRequestModeRelaxed)

	previous := CurrentMaxSpeculativeAttempts()
	SetMaxSpeculativeAttempts(4)
	t.Cleanup(func() {
		SetMaxSpeculativeAttempts(previous)
	})

	gate := NewChainPhaseGate("http://api:9000", 0)
	require.NotNil(t, gate)
	require.Equal(t, 4, gate.defaultMaxSpeculativeAttempts)

	gate.applySpeculativeAttemptPolicy(ChainPhaseSnapshot{
		EpochPhase:      epochPhasePoCGenerate,
		RequestsBlocked: false,
		BlockReason:     "poc",
	})
	require.Equal(t, 4, CurrentMaxSpeculativeAttempts())
}

func TestChainPhaseGateFetchPreservedParticipantKeys(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/v1/epochs/current/participants", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		// Two preserved entries for the same gonka address dedupe to
		// one in the response, mirroring how multi-slot validators
		// appear on chain. The participant with no preserved MLNode
		// times slots flows to the excluded list.
		_, _ = w.Write([]byte(`{
			"active_participants": {
				"participants": [
					{
						"index": "gonka1aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa1",
						"inference_url": "http://preserved.example:8080",
						"ml_nodes": [
							{"ml_nodes": [{"timeslot_allocation": [true, true]}]}
						]
					},
					{
						"index": "gonka1aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa1",
						"inference_url": "http://preserved.example:8081",
						"ml_nodes": [
							{"ml_nodes": [{"timeslot_allocation": [true, true]}]}
						]
					},
					{
						"index": "gonka1bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb2",
						"inference_url": "http://regular.example:8080",
						"ml_nodes": [
							{"ml_nodes": [{"timeslot_allocation": [true, false]}]}
						]
					}
				]
			}
		}`))
	}))
	defer server.Close()

	gate := NewChainPhaseGate(server.URL, 0)
	require.NotNil(t, gate)

	keys, excluded, err := gate.fetchPreservedParticipantKeys()
	require.NoError(t, err)
	require.Equal(t, []string{
		"gonka1aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa1",
	}, keys)
	require.Equal(t, []string{
		"gonka1bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb2",
	}, excluded)
}

func TestChainPhaseGateFetchParticipantsFallsBackToChain(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	}))
	defer server.Close()

	active := &inferencetypes.ActiveParticipants{
		EpochId: 12,
		Participants: []*inferencetypes.ActiveParticipant{{
			Index:        "gonka1participant",
			InferenceUrl: "http://participant.example:8080",
			Models:       []string{"Model/A"},
			MlNodes: []*inferencetypes.ModelMLNodes{{
				MlNodes: []*inferencetypes.MLNodeInfo{{
					NodeId:             "node-1",
					PocWeight:          7,
					TimeslotAllocation: []bool{true},
				}},
			}},
		}},
	}
	value, err := proto.Marshal(active)
	require.NoError(t, err)

	storeQuery := &phaseGateStoreQueryStub{resp: &cmtservice.ABCIQueryResponse{Value: value}}
	gate := NewChainPhaseGate(server.URL, 0)
	gate.SetChainQueryClient(&preservedSnapshotStub{
		currentEpochResp: &inferencetypes.QueryGetCurrentEpochResponse{Epoch: 12},
	})
	gate.SetStoreQueryClient(storeQuery)

	state, err := gate.fetchParticipantsState(false, 0, false)
	require.NoError(t, err)
	require.Equal(t, float64(7), state.weights["gonka1participant"])
	require.Equal(t, "http://participant.example:8080", state.inferenceURLs["gonka1participant"])
	require.Equal(t, inferencetypes.ActiveParticipantsFullKey(12), storeQuery.req.Data)

	payload, err := gate.fetchParticipants()
	require.NoError(t, err)
	require.Equal(t, jsonUint64(12), payload.ActiveParticipants.EpochID)
}

func TestChainPhaseGateUsesPreservedNodePoCWeightDuringPoC(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/v1/epochs/current/participants", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"active_participants": {
				"participants": [
					{
						"index": "gonka1aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa1",
						"weight": 100,
						"models": ["Model/A"],
						"ml_nodes": [
							{"ml_nodes": [
								{"poc_weight": 40, "timeslot_allocation": [true, true]},
								{"poc_weight": 60, "timeslot_allocation": [true, false]}
							]}
						]
					}
				]
			}
		}`))
	}))
	defer server.Close()

	gate := NewChainPhaseGate(server.URL, 0)
	require.NotNil(t, gate)

	state, err := gate.fetchParticipantsState(true, 0, false)
	require.NoError(t, err)
	require.Equal(t, []string{"gonka1aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa1"}, state.preserved)
	require.Empty(t, state.excluded)
	require.Equal(t, map[string]float64{
		"gonka1aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa1": 40,
	}, state.weights)
	require.Equal(t, map[string]float64{
		"gonka1aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa1": 100,
	}, state.fullWeights)
	require.Equal(t, map[string]map[string]float64{
		"Model/A": {
			"gonka1aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa1": 40,
		},
	}, state.weightsByModel)
	require.Equal(t, map[string]map[string]float64{
		"Model/A": {
			"gonka1aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa1": 100,
		},
	}, state.fullWeightsByModel)
}

func TestChainPhaseGateUsesRawPoCWeightOutsidePoC(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/v1/epochs/current/participants", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"active_participants": {
				"participants": [
					{
						"index": "gonka1aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa1",
						"weight": 999,
						"models": ["Model/A", "Model/B"],
						"ml_nodes": [
							{"ml_nodes": [
								{"node_id": "a1", "poc_weight": 40, "timeslot_allocation": [true, false]},
								{"node_id": "a2", "poc_weight": 10, "timeslot_allocation": [true, false]}
							]},
							{"ml_nodes": [
								{"node_id": "b1", "poc_weight": 60, "timeslot_allocation": [true, false]}
							]}
						]
					}
				]
			}
		}`))
	}))
	defer server.Close()

	gate := NewChainPhaseGate(server.URL, 0)
	require.NotNil(t, gate)

	state, err := gate.fetchParticipantsState(false, 0, false)
	require.NoError(t, err)
	require.Equal(t, map[string]float64{
		"gonka1aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa1": 110,
	}, state.weights)
	require.Equal(t, map[string]float64{
		"gonka1aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa1": 110,
	}, state.fullWeights)
	require.Equal(t, map[string]map[string]float64{
		"Model/A": {
			"gonka1aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa1": 50,
		},
		"Model/B": {
			"gonka1aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa1": 60,
		},
	}, state.weightsByModel)
	require.Equal(t, map[string]map[string]float64{
		"Model/A": {
			"gonka1aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa1": 50,
		},
		"Model/B": {
			"gonka1aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa1": 60,
		},
	}, state.fullWeightsByModel)
}

func TestChainPhaseGateUsesPreservedSnapshotDuringPoC(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/epochs/current/participants":
			_, _ = w.Write([]byte(`{
				"active_participants": {
					"participants": [
						{
							"index": "gonka1aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa1",
							"weight": 100,
							"models": ["Model/A"],
							"ml_nodes": [
								{"ml_nodes": [
									{"node_id": "node-a", "poc_weight": 40, "timeslot_allocation": [true, false]},
									{"node_id": "node-b", "poc_weight": 60, "timeslot_allocation": [true, false]}
								]}
							]
						},
						{
							"index": "gonka1bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb2",
							"weight": 100,
							"models": ["Model/A"],
							"ml_nodes": [
								{"ml_nodes": [
									{"node_id": "node-c", "poc_weight": 70, "timeslot_allocation": [true, false]}
								]}
							]
						}
					]
				}
			}`))
		case "/productscience/inference/inference/preserved_nodes_snapshot":
			_, _ = w.Write([]byte(`{
				"found": true,
				"snapshot": {
					"episode_anchor_height": 123,
					"model_preserved_nodes": [
						{
							"model_id": "Model/A",
							"participants": [
								{
									"participant_id": "gonka1aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa1",
									"node_ids": ["node-b"]
								}
							]
						}
					]
				}
			}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	gate := NewChainPhaseGate(server.URL, 0)
	require.NotNil(t, gate)
	gate.SetChainQueryClient(&preservedSnapshotStub{
		snapshotResp: preservedSnapshotGRPCResponse(123, "Model/A", "gonka1aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa1", []string{"node-b"}),
	})

	state, err := gate.fetchParticipantsState(true, 123, false)
	require.NoError(t, err)
	require.Equal(t, []string{"gonka1aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa1"}, state.preserved)
	require.Equal(t, map[string][]string{
		"Model/A": []string{"gonka1aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa1"},
	}, state.preservedByModel)
	require.Equal(t, []string{"gonka1bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb2"}, state.excluded)
	require.Equal(t, map[string]float64{
		"gonka1aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa1": 60,
		"gonka1bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb2": 0,
	}, state.weights)
	require.Equal(t, map[string]float64{
		"gonka1aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa1": 100,
		"gonka1bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb2": 70,
	}, state.fullWeights)
	require.Equal(t, map[string]map[string]float64{
		"Model/A": {
			"gonka1aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa1": 60,
			"gonka1bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb2": 0,
		},
	}, state.weightsByModel)
	require.Equal(t, map[string]map[string]float64{
		"Model/A": {
			"gonka1aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa1": 100,
			"gonka1bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb2": 70,
		},
	}, state.fullWeightsByModel)
}

func TestShouldRefreshPoCPreservedParticipantsOnConfirmationGenerationTransition(t *testing.T) {
	previous := ChainPhaseSnapshot{
		EpochPhase:           epochPhaseInference,
		ConfirmationPoCPhase: confirmationPoCGracePeriod,
		BlockReason:          "confirmation_poc",
	}
	next := ChainPhaseSnapshot{
		EpochPhase:           epochPhaseInference,
		ConfirmationPoCPhase: confirmationPoCGeneration,
		BlockReason:          "confirmation_poc",
	}

	require.True(t, shouldRefreshPoCPreservedParticipants(previous, next))
	require.False(t, shouldRefreshPoCPreservedParticipants(next, next))
	require.True(t, shouldRefreshPoCPreservedParticipants(ChainPhaseSnapshot{}, previous))
	require.False(t, shouldRefreshPoCPreservedParticipants(previous, ChainPhaseSnapshot{
		EpochPhase:           epochPhaseInference,
		ConfirmationPoCPhase: confirmationPoCInactive,
	}))
}

func TestChainPhaseGateFallsBackToTimeslotAllocationWhenPreservedSnapshotMissing(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/epochs/current/participants":
			_, _ = w.Write([]byte(`{
				"active_participants": {
					"participants": [
						{
							"index": "gonka1aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa1",
							"weight": 100,
							"models": ["Model/A"],
							"ml_nodes": [
								{"ml_nodes": [
									{"node_id": "node-a", "poc_weight": 40, "timeslot_allocation": [true, true]},
									{"node_id": "node-b", "poc_weight": 60, "timeslot_allocation": [true, false]}
								]}
							]
						}
					]
				}
			}`))
		case "/productscience/inference/inference/preserved_nodes_snapshot":
			_, _ = w.Write([]byte(`{"found": false}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	gate := NewChainPhaseGate(server.URL, 0)
	require.NotNil(t, gate)
	gate.SetChainQueryClient(&preservedSnapshotStub{})

	state, err := gate.fetchParticipantsState(true, 0, false)
	require.NoError(t, err)
	require.Equal(t, []string{"gonka1aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa1"}, state.preserved)
	require.Empty(t, state.excluded)
	require.Equal(t, map[string]float64{
		"gonka1aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa1": 40,
	}, state.weights)
}

func TestChainPhaseGateIgnoresStalePreservedSnapshot(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/epochs/current/participants":
			_, _ = w.Write([]byte(`{
				"active_participants": {
					"participants": [
						{
							"index": "gonka1aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa1",
							"weight": 100,
							"models": ["Model/A"],
							"ml_nodes": [
								{"ml_nodes": [
									{"node_id": "node-a", "poc_weight": 40, "timeslot_allocation": [true, false]}
								]}
							]
						}
					]
				}
			}`))
		case "/productscience/inference/inference/preserved_nodes_snapshot":
			_, _ = w.Write([]byte(`{
				"found": true,
				"snapshot": {
					"episode_anchor_height": 122,
					"model_preserved_nodes": [
						{
							"model_id": "Model/A",
							"participants": [
								{
									"participant_id": "gonka1aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa1",
									"node_ids": ["node-a"]
								}
							]
						}
					]
				}
			}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	gate := NewChainPhaseGate(server.URL, 0)
	require.NotNil(t, gate)
	gate.SetChainQueryClient(&preservedSnapshotStub{
		snapshotResp: preservedSnapshotGRPCResponse(122, "Model/A", "gonka1aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa1", []string{"node-a"}),
	})

	state, err := gate.fetchParticipantsState(true, 123, false)
	require.NoError(t, err)
	require.Empty(t, state.preserved)
	require.Equal(t, []string{"gonka1aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa1"}, state.excluded)
	require.Equal(t, map[string]float64{
		"gonka1aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa1": 0,
	}, state.weights)
}

func TestChainPhaseGateKeepsAllParticipantsAvailableDuringConfirmationGraceBeforeSnapshot(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/epochs/current/participants":
			_, _ = w.Write([]byte(`{
				"active_participants": {
					"participants": [
						{
							"index": "gonka1aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa1",
							"weight": 100,
							"models": ["Model/A"],
							"ml_nodes": [
								{"ml_nodes": [
									{"node_id": "node-a", "poc_weight": 40, "timeslot_allocation": [true, false]}
								]}
							]
						}
					]
				}
			}`))
		case "/productscience/inference/inference/preserved_nodes_snapshot":
			_, _ = w.Write([]byte(`{"found": false}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	gate := NewChainPhaseGate(server.URL, 0)
	require.NotNil(t, gate)
	gate.SetChainQueryClient(&preservedSnapshotStub{})

	state, err := gate.fetchParticipantsState(true, 123, true)
	require.NoError(t, err)
	require.Equal(t, []string{"gonka1aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa1"}, state.preserved)
	require.Empty(t, state.excluded)
	require.Equal(t, map[string]float64{
		"gonka1aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa1": 40,
	}, state.weights)
	require.Equal(t, map[string]map[string]float64{
		"Model/A": {
			"gonka1aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa1": 40,
		},
	}, state.weightsByModel)
}

func TestChainPhaseGateExcludedParticipantContributesZeroDuringPoC(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/v1/epochs/current/participants", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"active_participants": {
				"participants": [
					{
						"index": "gonka1bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb2",
						"weight": 100,
						"models": ["Model/A"],
						"ml_nodes": [
							{"ml_nodes": [
								{"poc_weight": 40, "timeslot_allocation": [true, false]},
								{"poc_weight": 60, "timeslot_allocation": [true, false]}
							]}
						]
					}
				]
			}
		}`))
	}))
	defer server.Close()

	gate := NewChainPhaseGate(server.URL, 0)
	require.NotNil(t, gate)

	state, err := gate.fetchParticipantsState(true, 0, false)
	require.NoError(t, err)
	require.Empty(t, state.preserved)
	require.Equal(t, []string{"gonka1bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb2"}, state.excluded)
	require.Equal(t, map[string]float64{
		"gonka1bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb2": 0,
	}, state.weights)
	require.Equal(t, map[string]map[string]float64{
		"Model/A": {
			"gonka1bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb2": 0,
		},
	}, state.weightsByModel)
}

func TestChainPhaseGateMapsOuterMLNodesToModels(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/v1/epochs/current/participants", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"active_participants": {
				"participants": [
					{
						"index": "gonka1cccccccccccccccccccccccccccccccccccc3",
						"weight": 100,
						"models": ["Model/A", "Model/B"],
						"ml_nodes": [
							{"ml_nodes": [
								{"node_id": "a1", "poc_weight": 40, "timeslot_allocation": [true, true]}
							]},
							{"ml_nodes": [
								{"node_id": "b1", "poc_weight": 60, "timeslot_allocation": [true, false]}
							]}
						]
					}
				]
			}
		}`))
	}))
	defer server.Close()

	gate := NewChainPhaseGate(server.URL, 0)
	require.NotNil(t, gate)

	state, err := gate.fetchParticipantsState(true, 0, false)
	require.NoError(t, err)
	require.Equal(t, map[string]map[string]float64{
		"Model/A": {
			"gonka1cccccccccccccccccccccccccccccccccccc3": 40,
		},
		"Model/B": {
			"gonka1cccccccccccccccccccccccccccccccccccc3": 0,
		},
	}, state.weightsByModel)
}

func TestChainPhaseGateLogsConfirmationPoCTransitionInRelaxedMode(t *testing.T) {
	setPoCModeForTest(t, pocRequestModeRelaxed)

	var buf bytes.Buffer
	previousWriter := log.Writer()
	previousFlags := log.Flags()
	log.SetOutput(&buf)
	log.SetFlags(0)
	t.Cleanup(func() {
		log.SetOutput(previousWriter)
		log.SetFlags(previousFlags)
	})

	gate := NewChainPhaseGate("http://api:9000", 0)
	require.NotNil(t, gate)

	gate.logSnapshotTransition(ChainPhaseSnapshot{}, ChainPhaseSnapshot{
		BlockHeight:          150,
		EpochIndex:           12,
		EpochPhase:           epochPhaseInference,
		ConfirmationPoCPhase: confirmationPoCGeneration,
		RequestsBlocked:      false,
		BlockReason:          "confirmation_poc",
	})

	output := buf.String()
	require.Contains(t, output, "chain phase gate: phase active")
	require.Contains(t, output, "reason=confirmation_poc")
	require.Contains(t, output, "confirmation_poc_phase=CONFIRMATION_POC_GENERATION")
	require.Contains(t, output, "requests_blocked=false")
	require.NotContains(t, output, "blocking new requests")
}

func TestChainPhaseGateLogsEmptyPreservedParticipants(t *testing.T) {
	var buf bytes.Buffer
	previousWriter := log.Writer()
	previousFlags := log.Flags()
	log.SetOutput(&buf)
	log.SetFlags(0)
	t.Cleanup(func() {
		log.SetOutput(previousWriter)
		log.SetFlags(previousFlags)
	})

	gate := NewChainPhaseGate("http://api:9000", 0)
	require.NotNil(t, gate)

	gate.logPreservedParticipantsLoaded(ChainPhaseSnapshot{
		BlockHeight:          150,
		EpochIndex:           12,
		EpochPhase:           epochPhaseInference,
		ConfirmationPoCPhase: confirmationPoCGeneration,
		BlockReason:          "confirmation_poc",
	}, nil, []string{"gonka1cccccccccccccccccccccccccccccccccccc3"})

	require.Contains(t, buf.String(), "chain phase gate: preserved participant poll empty")
	require.Contains(t, buf.String(), "excluded_count=1")
	// Log labels are short suffixes (last 8 chars of the bech32
	// address) for compact log lines.
	require.Contains(t, buf.String(), "excluded_participants=ccccccc3")
}

func TestChainPhaseGateLogsLoadedPreservedParticipantsSorted(t *testing.T) {
	var buf bytes.Buffer
	previousWriter := log.Writer()
	previousFlags := log.Flags()
	log.SetOutput(&buf)
	log.SetFlags(0)
	t.Cleanup(func() {
		log.SetOutput(previousWriter)
		log.SetFlags(previousFlags)
	})

	gate := NewChainPhaseGate("http://api:9000", 0)
	require.NotNil(t, gate)

	gate.logPreservedParticipantsLoaded(ChainPhaseSnapshot{
		BlockHeight:          150,
		EpochIndex:           12,
		EpochPhase:           epochPhaseInference,
		ConfirmationPoCPhase: confirmationPoCGeneration,
		BlockReason:          "confirmation_poc",
	}, []string{
		"gonka1zzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzz",
		"gonka1aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	}, []string{
		"gonka1yyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyy",
		"gonka1bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
	})

	output := buf.String()
	require.Contains(t, output, "chain phase gate: preserved participants loaded")
	// Sorted ASCII order of the last-8-char short labels: aaaaaaaa < zzzzzzzz, bbbbbbbb < yyyyyyyy.
	require.True(t, strings.Contains(output, "participants=aaaaaaaa,zzzzzzzz"), output)
	require.True(t, strings.Contains(output, "excluded_participants=bbbbbbbb,yyyyyyyy"), output)
}

func TestRawPoCGenerationState(t *testing.T) {
	cases := []struct {
		name              string
		epochPhase        string
		confirmationPhase string
		want              bool
	}{
		{"poc generate", epochPhasePoCGenerate, confirmationPoCInactive, true},
		{"poc generate winddown", epochPhasePoCGenerateWindDown, confirmationPoCInactive, true},
		{"confirmation grace", epochPhaseInference, confirmationPoCGracePeriod, true},
		{"confirmation generation", epochPhaseInference, confirmationPoCGeneration, true},
		{"poc validate", epochPhasePoCValidate, confirmationPoCInactive, false},
		{"poc validate winddown", epochPhasePoCValidateWindDown, confirmationPoCInactive, false},
		{"confirmation validation", epochPhaseInference, confirmationPoCValidation, false},
		{"steady inference", epochPhaseInference, confirmationPoCInactive, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := rawPoCGenerationState(tc.epochPhase, tc.confirmationPhase); got != tc.want {
				t.Fatalf("rawPoCGenerationState(%q,%q)=%v want %v", tc.epochPhase, tc.confirmationPhase, got, tc.want)
			}
		})
	}
}

func TestRawPoCValidationState(t *testing.T) {
	cases := []struct {
		name              string
		epochPhase        string
		confirmationPhase string
		want              bool
	}{
		{"poc validate", epochPhasePoCValidate, confirmationPoCInactive, true},
		{"poc validate winddown", epochPhasePoCValidateWindDown, confirmationPoCInactive, true},
		{"confirmation validation", epochPhaseInference, confirmationPoCValidation, true},
		{"poc generate", epochPhasePoCGenerate, confirmationPoCInactive, false},
		{"poc generate winddown", epochPhasePoCGenerateWindDown, confirmationPoCInactive, false},
		{"confirmation generation", epochPhaseInference, confirmationPoCGeneration, false},
		{"confirmation grace", epochPhaseInference, confirmationPoCGracePeriod, false},
		{"steady inference", epochPhaseInference, confirmationPoCInactive, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := rawPoCValidationState(tc.epochPhase, tc.confirmationPhase); got != tc.want {
				t.Fatalf("rawPoCValidationState(%q,%q)=%v want %v", tc.epochPhase, tc.confirmationPhase, got, tc.want)
			}
		})
	}
}

func newTestGatewayWithSnapshot(t *testing.T, snap ChainPhaseSnapshot) *Gateway {
	g := &Gateway{
		phaseGate: &ChainPhaseGate{},
	}
	g.phaseGate.storeSnapshot(snap)
	return g
}

func TestCurrentMaxConcurrent_GenerationOnly(t *testing.T) {
	g := newTestGatewayWithSnapshot(t, ChainPhaseSnapshot{EpochPhase: epochPhasePoCGenerate, ConfirmationPoCPhase: confirmationPoCInactive})
	g.settings.MaxConcurrentPer10000Weight = 5
	g.settings.PoCMaxConcurrentPer10000Weight = 10
	if got := g.currentMaxConcurrentPer10000Weight(); got != 10 {
		t.Fatalf("generation: got %v want 10", got)
	}

	g2 := newTestGatewayWithSnapshot(t, ChainPhaseSnapshot{EpochPhase: epochPhasePoCValidate, ConfirmationPoCPhase: confirmationPoCInactive})
	g2.settings.MaxConcurrentPer10000Weight = 5
	g2.settings.PoCMaxConcurrentPer10000Weight = 10
	if got := g2.currentMaxConcurrentPer10000Weight(); got != 5 {
		t.Fatalf("validation: got %v want 5", got)
	}
}

func TestMergePreservedWithValidationCapable(t *testing.T) {
	state := &participantsState{
		preserved:        []string{"p1"},
		excluded:         []string{"x1", "x2"},
		preservedByModel: map[string][]string{"m": {"p1"}},
		weights:          map[string]float64{"p1": 100},
		weightsByModel:   map[string]map[string]float64{"m": {"p1": 100}},
		nodesByParticipant: map[string][]participantNode{
			"x1": {{model: "m", nodeID: "x1n1", weight: 30}, {model: "m", nodeID: "x1n2", weight: 20}},
			"x2": {{model: "m", nodeID: "x2n1", weight: 70}},
		},
	}
	// x1n1 capable; x1n2 not; x2n1 not -> x1 available with weight 30, x2 excluded.
	capable := func(miner, nodeID string) bool { return miner == "x1" && nodeID == "x1n1" }

	preserved, preservedByModel, weights, weightsByModel := mergePreservedWithValidationCapable(state, capable)

	if !contains(preserved, "p1") || !contains(preserved, "x1") || contains(preserved, "x2") {
		t.Fatalf("preserved = %v, want p1+x1, not x2", preserved)
	}
	if weights["x1"] != 30 {
		t.Fatalf("x1 weight = %v want 30 (only capable node x1n1)", weights["x1"])
	}
	if weights["p1"] != 100 {
		t.Fatalf("p1 weight = %v want 100 (preserved keeps PoC weight)", weights["p1"])
	}
	if _, ok := weights["x2"]; ok {
		t.Fatalf("x2 must not be weighted (no capable node)")
	}
	if weightsByModel["m"]["x1"] != 30 {
		t.Fatalf("weightsByModel[m][x1] = %v want 30", weightsByModel["m"]["x1"])
	}
	if !contains(preservedByModel["m"], "x1") {
		t.Fatalf("preservedByModel[m] = %v want x1 included", preservedByModel["m"])
	}
	// purity: inputs unmutated
	if _, ok := state.weights["x1"]; ok {
		t.Fatalf("input state.weights mutated")
	}
	if len(state.preserved) != 1 {
		t.Fatalf("input state.preserved mutated")
	}
}

func TestParticipantValidationInferenceWeights(t *testing.T) {
	nodes := []participantNode{
		{model: "m", nodeID: "n1", weight: 30},
		{model: "m", nodeID: "n2", weight: 20},
		{model: "other", nodeID: "n3", weight: 40},
	}

	capable := func(miner, nodeID string) bool { return nodeID == "n1" || nodeID == "n3" }
	weights, total := participantValidationInferenceWeights(nodes, "p1", capable)
	if total != 70 {
		t.Fatalf("total = %v want 70", total)
	}
	if weights["m"] != 30 || weights["other"] != 40 {
		t.Fatalf("weights = %v want m=30 other=40", weights)
	}

	weights, total = participantValidationInferenceWeights(nodes, "p1", nil)
	if total != 0 || len(weights) != 0 {
		t.Fatalf("nil capable weights=%v total=%v, want empty/0", weights, total)
	}
}

func contains(ss []string, s string) bool {
	for _, v := range ss {
		if v == s {
			return true
		}
	}
	return false
}

type activeParticipantsRecordStub struct {
	mu      sync.Mutex
	records map[string][]byte
	reads   int
}

func (s *activeParticipantsRecordStub) set(t *testing.T, epoch uint64, url string, weight int64) {
	t.Helper()
	value, err := proto.Marshal(&inferencetypes.ActiveParticipants{
		EpochId: epoch,
		Participants: []*inferencetypes.ActiveParticipant{{
			Index:        "gonka1a",
			InferenceUrl: url,
			MlNodes: []*inferencetypes.ModelMLNodes{{
				MlNodes: []*inferencetypes.MLNodeInfo{{NodeId: "n1", PocWeight: weight}},
			}},
		}},
	})
	require.NoError(t, err)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.records[string(inferencetypes.ActiveParticipantsFullKey(epoch))] = value
}

func (s *activeParticipantsRecordStub) ABCIQuery(_ context.Context, req *cmtservice.ABCIQueryRequest, _ ...grpc.CallOption) (*cmtservice.ABCIQueryResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reads++
	return &cmtservice.ABCIQueryResponse{Value: s.records[string(req.Data)]}, nil
}

func newParticipantsCacheServer(t *testing.T, height, participantsEpoch, participantsHits *atomic.Int64) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/epochs/latest":
			_, _ = fmt.Fprintf(w, `{
				"block_height":"%d",
				"phase":"Inference",
				"latest_epoch":{"index":"12","poc_start_block_height":"100"},
				"epoch_stages":{"epoch_index":"12","set_new_validators":"160"}
			}`, height.Load())
		case "/v1/epochs/current/participants":
			participantsHits.Add(1)
			_, _ = fmt.Fprintf(w, `{"active_participants":{"epoch_id":%d,"participants":[
				{"index":"gonka1a","inference_url":"http://a.example","ml_nodes":[{"ml_nodes":[{"node_id":"n1","poc_weight":"%d"}]}]}
			]}}`, participantsEpoch.Load(), participantsEpoch.Load())
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

func TestChainPhaseGateBuildsParticipantsFromRecord(t *testing.T) {
	var height, participantsEpoch, participantsHits atomic.Int64
	height.Store(150)
	participantsEpoch.Store(11)
	server := newParticipantsCacheServer(t, &height, &participantsEpoch, &participantsHits)
	records := &activeParticipantsRecordStub{records: map[string][]byte{}}
	records.set(t, 11, "http://a11.example", 7)
	records.set(t, 12, "http://a12.example", 9)

	gate := NewChainPhaseGate(server.URL, 0)
	gate.SetStoreQueryClient(records)
	gate.SetCapacityState(NewCapacityState(), nil)

	// Epoch 11 is current until set_new_validators of epoch 12 (height 160).
	// The payload comes from the record; dapi is not asked, so a dapi node
	// lagging behind the record cannot pin an older payload.
	gate.refresh()
	first := gate.participants.payload
	gate.refresh()
	require.Same(t, first, gate.participants.payload)
	require.Equal(t, "http://a11.example", gate.InferenceURLs()["gonka1a"])

	// A rewrite of the current record (an upgrade handler) shows on the next poll.
	records.set(t, 11, "http://a11-rewritten.example", 7)
	gate.refresh()
	require.Equal(t, "http://a11-rewritten.example", gate.InferenceURLs()["gonka1a"])

	// From the switch height on, epoch 12's record is current.
	height.Store(160)
	gate.refresh()
	require.Equal(t, "http://a12.example", gate.InferenceURLs()["gonka1a"])
	require.Equal(t, jsonUint64(12), gate.participants.payload.ActiveParticipants.EpochID)
	require.Equal(t, int64(0), participantsHits.Load())
	require.Equal(t, 4, records.reads)
}

func TestChainPhaseGateFetchesParticipantsWithoutRecord(t *testing.T) {
	var height, participantsEpoch, participantsHits atomic.Int64
	height.Store(150)
	participantsEpoch.Store(11)
	server := newParticipantsCacheServer(t, &height, &participantsEpoch, &participantsHits)

	// No store query client: every poll fetches, as before.
	gate := NewChainPhaseGate(server.URL, 0)
	gate.SetCapacityState(NewCapacityState(), nil)
	gate.refresh()
	gate.refresh()
	require.Equal(t, int64(2), participantsHits.Load())

	// Record missing, unreadable or epoch unknown: every poll fetches too.
	gate.SetStoreQueryClient(&activeParticipantsRecordStub{records: map[string][]byte{}})
	gate.refresh()
	gate.refresh()
	require.Equal(t, int64(4), participantsHits.Load())
	gate.SetStoreQueryClient(&phaseGateStoreQueryStub{err: errors.New("unavailable")})
	gate.refresh()
	require.Equal(t, int64(5), participantsHits.Load())
}

func TestCurrentEpochIndex(t *testing.T) {
	resp := func(height, latest, stagesEpoch, snv int64) *chainEpochInfoResponse {
		return &chainEpochInfoResponse{
			BlockHeight: jsonInt64(height),
			LatestEpoch: chainLatestEpoch{Index: jsonUint64(latest)},
			EpochStages: chainEpochStages{EpochIndex: jsonUint64(stagesEpoch), SetNewValidators: jsonInt64(snv)},
		}
	}
	cases := []struct {
		name  string
		resp  *chainEpochInfoResponse
		epoch uint64
		known bool
	}{
		{"before switch", resp(159, 12, 12, 160), 11, true},
		{"at switch", resp(160, 12, 12, 160), 12, true},
		{"after switch", resp(161, 12, 12, 160), 12, true},
		{"stages of another epoch", resp(161, 12, 11, 160), 0, false},
		{"no switch height", resp(161, 12, 12, 0), 0, false},
		{"first epoch before switch", resp(10, 1, 1, 20), 0, false},
		{"nil", nil, 0, false},
	}
	for _, tc := range cases {
		epoch, known := currentEpochIndex(tc.resp)
		require.Equal(t, tc.epoch, epoch, tc.name)
		require.Equal(t, tc.known, known, tc.name)
	}
}
