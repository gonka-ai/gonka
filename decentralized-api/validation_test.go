package main

import (
	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestValidationOdds(t *testing.T) {
	newExecutor := executor(0.0)
	odds := getOdds(newExecutor, 1, 1000)
	require.InEpsilon(t, 0.001, odds, 0.0001)
	odds = getOdds(newExecutor, 10, 1000)
	require.InEpsilon(t, 0.01, odds, 0.0001)
	fullExecutor := executor(1.0)
	odds = getOdds(fullExecutor, 1, 1000)
	require.InEpsilon(t, 0.0001, odds, 0.000001)
}

func getOdds(participant *types.Participant, numParticipants uint32, numValidators uint32) float64 {
	_, odds := ShouldValidate(participant, "validator", numParticipants, numValidators, 0)
	return float64(odds)
}

func executor(reputation float32) *types.Participant {
	newExecutor := &types.Participant{
		Index:      "executor",
		Reputation: reputation,
	}
	return newExecutor
}
