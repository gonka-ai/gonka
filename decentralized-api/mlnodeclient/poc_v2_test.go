package mlnodeclient

import (
	"encoding/json"
	"testing"

	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
)

func TestToValidatedWeight_FraudDetected(t *testing.T) {
	v := &ValidatedResultV2{
		NTotal:        100,
		FraudDetected: true,
	}
	require.Equal(t, int64(-1), v.ToValidatedWeight())
}

func TestToValidatedWeight_NTotalZero(t *testing.T) {
	// NTotal <= 0 should be treated as invalid (same as fraud)
	v := &ValidatedResultV2{
		NTotal:        0,
		FraudDetected: false,
	}
	require.Equal(t, int64(-1), v.ToValidatedWeight())
}

func TestToValidatedWeight_NTotalNegative(t *testing.T) {
	v := &ValidatedResultV2{
		NTotal:        -5,
		FraudDetected: false,
	}
	require.Equal(t, int64(-1), v.ToValidatedWeight())
}

func TestToValidatedWeight_Valid(t *testing.T) {
	v := &ValidatedResultV2{
		NTotal:        100,
		FraudDetected: false,
	}
	require.Equal(t, int64(100), v.ToValidatedWeight())
}

func TestToValidatedWeight_ValidSmall(t *testing.T) {
	// Even NTotal=1 should be considered valid
	v := &ValidatedResultV2{
		NTotal:        1,
		FraudDetected: false,
	}
	require.Equal(t, int64(1), v.ToValidatedWeight())
}

func TestStatTestParamsFromChain_AlphaReachesMLNodeAsFraudThreshold(t *testing.T) {
	body, err := json.Marshal(StatTestParamsFromChain(&types.PoCStatTestParams{PValueThreshold: types.DecimalFromFloat(0.02)}))
	require.NoError(t, err)
	var wire map[string]float64
	require.NoError(t, json.Unmarshal(body, &wire))
	require.InDelta(t, 0.02, wire["fraud_threshold"], 1e-9)
	require.NotContains(t, wire, "p_value_threshold")
}
