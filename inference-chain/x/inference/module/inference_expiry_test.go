package inference_test

import (
	"testing"

	inference "github.com/productscience/inference/x/inference/module"
	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
)

func TestShouldCheckPreserveNodeUsesAssignmentBlockOnly(t *testing.T) {
	expiryCtx := &inference.InferenceExpiryContext{
		CurrentBlockHeight: 105,
		TimeoutDuration:    5,
		PoCRange: &inference.PoCTimeRange{
			StartBlock: 100,
			EndBlock:   110,
			IsActive:   true,
		},
	}

	require.True(t, expiryCtx.ShouldCheckPreserveNode(types.Inference{
		StartBlockHeight: 95,
	}))

	require.True(t, expiryCtx.ShouldCheckPreserveNode(types.Inference{
		StartBlockHeight: 102,
	}))
}
