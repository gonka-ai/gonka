package inference

import (
	"testing"

	mathsdk "cosmossdk.io/math"
	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
)

func TestSumNodeWeights(t *testing.T) {
	tests := []struct {
		name     string
		nodes    []*types.MLNodeInfo
		expected int64
	}{
		{
			name:     "nil nodes",
			nodes:    nil,
			expected: 0,
		},
		{
			name:     "empty nodes",
			nodes:    []*types.MLNodeInfo{},
			expected: 0,
		},
		{
			name: "single node",
			nodes: []*types.MLNodeInfo{
				{NodeId: "n1", PocWeight: 100},
			},
			expected: 100,
		},
		{
			name: "multiple nodes",
			nodes: []*types.MLNodeInfo{
				{NodeId: "n1", PocWeight: 100},
				{NodeId: "n2", PocWeight: 250},
				{NodeId: "n3", PocWeight: 50},
			},
			expected: 400,
		},
		{
			name: "nil node in list",
			nodes: []*types.MLNodeInfo{
				{NodeId: "n1", PocWeight: 100},
				nil,
				{NodeId: "n3", PocWeight: 50},
			},
			expected: 150,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := SumNodeWeights(tt.nodes)
			require.Equal(t, tt.expected, result)
		})
	}
}

func TestExtractModelWeights(t *testing.T) {
	tests := []struct {
		name     string
		p        *types.ActiveParticipant
		expected []ModelWeight
	}{
		{
			name:     "nil participant",
			p:        nil,
			expected: nil,
		},
		{
			name: "single model",
			p: &types.ActiveParticipant{
				Models: []string{"model-a"},
				MlNodes: []*types.ModelMLNodes{
					{MlNodes: []*types.MLNodeInfo{
						{NodeId: "n1", PocWeight: 100},
						{NodeId: "n2", PocWeight: 200},
					}},
				},
			},
			expected: []ModelWeight{
				{ModelID: "model-a", RawWeight: 300},
			},
		},
		{
			name: "two models",
			p: &types.ActiveParticipant{
				Models: []string{"model-a", "model-b"},
				MlNodes: []*types.ModelMLNodes{
					{MlNodes: []*types.MLNodeInfo{
						{NodeId: "n1", PocWeight: 100},
					}},
					{MlNodes: []*types.MLNodeInfo{
						{NodeId: "n2", PocWeight: 50},
					}},
				},
			},
			expected: []ModelWeight{
				{ModelID: "model-a", RawWeight: 100},
				{ModelID: "model-b", RawWeight: 50},
			},
		},
		{
			name: "models longer than MlNodes",
			p: &types.ActiveParticipant{
				Models: []string{"model-a", "model-b"},
				MlNodes: []*types.ModelMLNodes{
					{MlNodes: []*types.MLNodeInfo{
						{NodeId: "n1", PocWeight: 100},
					}},
				},
			},
			expected: []ModelWeight{
				{ModelID: "model-a", RawWeight: 100},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ExtractModelWeights(tt.p)
			require.Equal(t, tt.expected, result)
		})
	}
}

func TestAggregateConsensusWeight(t *testing.T) {
	tests := []struct {
		name         string
		modelWeights []ModelWeight
		coefficients map[string]mathsdk.LegacyDec
		expected     int64
	}{
		{
			name:         "empty",
			modelWeights: nil,
			coefficients: nil,
			expected:     0,
		},
		{
			name: "single model, coeff 1.0",
			modelWeights: []ModelWeight{
				{ModelID: "model-a", RawWeight: 100},
			},
			coefficients: map[string]mathsdk.LegacyDec{
				"model-a": mathsdk.LegacyOneDec(),
			},
			expected: 100,
		},
		{
			name: "single model, coeff 2.0",
			modelWeights: []ModelWeight{
				{ModelID: "model-a", RawWeight: 100},
			},
			coefficients: map[string]mathsdk.LegacyDec{
				"model-a": mathsdk.LegacyNewDec(2),
			},
			expected: 200,
		},
		{
			name: "two models, different coefficients",
			modelWeights: []ModelWeight{
				{ModelID: "model-a", RawWeight: 100},
				{ModelID: "model-b", RawWeight: 50},
			},
			coefficients: map[string]mathsdk.LegacyDec{
				"model-a": mathsdk.LegacyOneDec(),
				"model-b": mathsdk.LegacyNewDec(2),
			},
			// 1.0*100 + 2.0*50 = 200
			expected: 200,
		},
		{
			name: "missing coefficient defaults to 1.0",
			modelWeights: []ModelWeight{
				{ModelID: "model-a", RawWeight: 100},
				{ModelID: "model-unknown", RawWeight: 50},
			},
			coefficients: map[string]mathsdk.LegacyDec{
				"model-a": mathsdk.LegacyNewDec(3),
			},
			// 3.0*100 + 1.0*50 = 350
			expected: 350,
		},
		{
			name: "fractional coefficient truncates",
			modelWeights: []ModelWeight{
				{ModelID: "model-a", RawWeight: 100},
			},
			coefficients: map[string]mathsdk.LegacyDec{
				// 1.5 * 100 = 150
				"model-a": mathsdk.LegacyNewDecWithPrec(15, 1),
			},
			expected: 150,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := AggregateConsensusWeight(tt.modelWeights, tt.coefficients)
			require.Equal(t, tt.expected, result)
		})
	}
}

func TestModelCoefficients(t *testing.T) {
	t.Run("nil params", func(t *testing.T) {
		coeffs := ModelCoefficients(nil)
		require.Empty(t, coeffs)
	})

	t.Run("extracts weight scale factors", func(t *testing.T) {
		params := &types.PocParams{
			Models: []*types.PoCModelConfig{
				{ModelId: "model-a", WeightScaleFactor: &types.Decimal{Value: 1, Exponent: 0}},
				{ModelId: "model-b", WeightScaleFactor: &types.Decimal{Value: 2, Exponent: 0}},
			},
		}
		coeffs := ModelCoefficients(params)
		require.Len(t, coeffs, 2)
		require.True(t, coeffs["model-a"].Equal(mathsdk.LegacyOneDec()))
		require.True(t, coeffs["model-b"].Equal(mathsdk.LegacyNewDec(2)))
	})
}
