package mlnodeclient

import (
	"testing"

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
