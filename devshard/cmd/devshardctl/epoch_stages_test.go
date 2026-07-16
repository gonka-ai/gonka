package main

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDeriveEpochStagesMatchesSetNewValidatorsMath(t *testing.T) {
	params := chainEpochParams{
		EpochLength:           500,
		PocStageDuration:      50,
		PocValidationDelay:    5,
		PocValidationDuration: 40,
		SetNewValidatorsDelay: 2,
	}
	epoch := chainLatestEpoch{Index: 12, PocStartBlockHeight: 1000}

	current, next := deriveEpochStages(epoch, params)
	// endPoC=50, valStart=55, valEnd=95, setNew=97
	require.Equal(t, int64(1097), int64(current.SetNewValidators))
	require.Equal(t, int64(1500), int64(current.NextPoCStart))
	require.Equal(t, uint64(13), uint64(next.EpochIndex))
	require.Equal(t, int64(1597), int64(next.SetNewValidators))
}

func TestEnrichEpochInfoStagesDerivesPhaseWhenMissing(t *testing.T) {
	payload := &chainEpochInfoResponse{
		BlockHeight: 1010,
		LatestEpoch: chainLatestEpoch{Index: 12, PocStartBlockHeight: 1000},
		Params: chainEpochInfoParams{EpochParams: chainEpochParams{
			EpochLength:           500,
			PocStageDuration:      50,
			PocValidationDelay:    5,
			PocValidationDuration: 40,
			SetNewValidatorsDelay: 2,
		}},
	}
	enrichEpochInfoStages(payload)
	require.Equal(t, epochPhasePoCGenerate, payload.Phase)
	require.Equal(t, int64(1097), int64(payload.EpochStages.SetNewValidators))
	require.Equal(t, int64(1500), int64(payload.EpochStages.NextPoCStart))
}

func TestNewChainPhaseGateUsesChainRESTPaths(t *testing.T) {
	gate := NewChainPhaseGate("http://node:1317/", 0)
	require.NotNil(t, gate)
	require.Equal(t, "http://node:1317/productscience/inference/inference/epoch_info", gate.endpoint)
	require.Equal(t, "http://node:1317/productscience/inference/inference/active_participants/0", gate.participantsEndpoint)
	gate.SetPreservedSnapshotBaseURL("http://node:1317/")
	require.Equal(t, "http://node:1317/productscience/inference/inference/preserved_nodes_snapshot", gate.preservedSnapshotURL())
}
