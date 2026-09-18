package mlnodeclient

import (
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

func TestKStepsBytesRoundTrip(t *testing.T) {
	steps := []int{0, 15, 7, 255}
	packed, err := KStepsToBytes(steps)
	require.NoError(t, err)
	got := BytesToKSteps(packed)
	require.Equal(t, steps, got)
}

func TestPoCParamsForScheme_PrefillIgnoresN(t *testing.T) {
	prefill := PoCParamsForScheme("m", 128, 256, types.PocScheme_POC_SCHEME_PREFILL)
	require.False(t, prefill.Decode)
	require.Equal(t, int64(128), prefill.SeqLen)
	require.Equal(t, int64(0), prefill.MaxTokens)

	decode := PoCParamsForScheme("m", 128, 256, types.PocScheme_POC_SCHEME_DECODE)
	require.True(t, decode.Decode)
	require.Equal(t, int64(DecodeSeqLen), decode.SeqLen)
	require.Equal(t, int64(256), decode.MaxTokens)
}

func TestKStepsToBytes_RejectsOutOfRange(t *testing.T) {
	_, err := KStepsToBytes([]int{256})
	require.Error(t, err)
	_, err = KStepsToBytes([]int{-1})
	require.Error(t, err)
}
