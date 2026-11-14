package inference

import (
	"fmt"
	"testing"

	"github.com/productscience/inference/x/inference/types"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

func nodesOf(weights ...int64) []*types.MLNodeInfo {
	out := make([]*types.MLNodeInfo, len(weights))
	for i, weight := range weights {
		out[i] = &types.MLNodeInfo{NodeId: fmt.Sprintf("n%d", i), PocWeight: weight}
	}
	return out
}

func legacySamplePreserved(modelId string, current, eligible *EpochMLNodeData, fraction *types.Decimal) map[string]map[string]struct{} {
	allocated := make(map[string]map[string]struct{})
	totalWeight := positiveModelWeight(current, modelId)
	target := fraction.ToDecimal().Mul(decimal.NewFromInt(totalWeight)).IntPart()
	addrs := sortedKeys(eligible.GetForModel(modelId))
	if len(addrs) == 0 {
		return allocated
	}
	var currentWeight int64
	idx := 0
	allocatedInRound := false
	for currentWeight < target {
		addr := addrs[idx]
		next := getSmallestUnallocatedMLNode(positiveWeightMLNodes(eligible.GetForParticipant(modelId, addr)), allocated[addr])
		if next == nil {
			idx = (idx + 1) % len(addrs)
			if idx == 0 {
				if !allocatedInRound {
					break
				}
				allocatedInRound = false
			}
			continue
		}
		if allocated[addr] == nil {
			allocated[addr] = map[string]struct{}{}
		}
		allocated[addr][next.NodeId] = struct{}{}
		currentWeight += next.PocWeight
		allocatedInRound = true
		idx = (idx + 1) % len(addrs)
		if idx == 0 {
			allocatedInRound = false
		}
	}
	return allocated
}

func halfFraction() *types.Decimal {
	return &types.Decimal{Value: 5, Exponent: -1}
}

func TestSamplePreservedMatchesLegacyOnUniqueWeights(t *testing.T) {
	current := NewEpochMLNodeData()
	eligible := NewEpochMLNodeData()
	current.Set("model", "alice", nodesOf(5, 9, 2))
	current.Set("model", "bob", nodesOf(4, 7))
	eligible.Set("model", "alice", nodesOf(5, 9, 2))
	eligible.Set("model", "bob", nodesOf(4, 7))

	ma := &ModelAssigner{InferenceLogger: noopLogger{}}
	got := ma.samplePreservedForModel("model", current, eligible, halfFraction())
	want := legacySamplePreserved("model", current, eligible, halfFraction())
	require.Equal(t, want, got)
}

func TestSamplePreservedTieBreaksByNodeID(t *testing.T) {
	current := NewEpochMLNodeData()
	eligible := NewEpochMLNodeData()
	nodes := []*types.MLNodeInfo{
		{NodeId: "b", PocWeight: 10},
		{NodeId: "a", PocWeight: 10},
	}
	current.Set("model", "alice", nodes)
	eligible.Set("model", "alice", nodes)
	ma := &ModelAssigner{InferenceLogger: noopLogger{}}
	// Target is half of 20, so the first node meets it.
	got := ma.samplePreservedForModel("model", current, eligible, halfFraction())
	require.Contains(t, got["alice"], "a")
	require.NotContains(t, got["alice"], "b")
}

func TestSamplePreservedStopsWhenParticipantsAreExhausted(t *testing.T) {
	current := NewEpochMLNodeData()
	eligible := NewEpochMLNodeData()
	current.Set("model", "alice", nodesOf(1))
	eligible.Set("model", "alice", nodesOf(1))
	ma := &ModelAssigner{InferenceLogger: noopLogger{}}
	fraction := &types.Decimal{Value: 1, Exponent: 0}
	got := ma.samplePreservedForModel("model", current, eligible, fraction)
	require.Len(t, got["alice"], 1)
}

func TestSamplePreservedIgnoresLegacyZeros(t *testing.T) {
	withZeros := NewEpochMLNodeData()
	without := NewEpochMLNodeData()
	positive := nodesOf(4, 6)
	mixed := append([]*types.MLNodeInfo{{NodeId: "z", PocWeight: 0}, nil}, positive...)
	withZeros.Set("model", "alice", mixed)
	without.Set("model", "alice", positive)
	ma := &ModelAssigner{InferenceLogger: noopLogger{}}
	fraction := &types.Decimal{Value: 1, Exponent: 0}
	require.Equal(t,
		ma.samplePreservedForModel("model", without, without, fraction),
		ma.samplePreservedForModel("model", withZeros, withZeros, fraction),
	)
	require.Equal(t, GetParticipantMedianNodeWeights(without), GetParticipantMedianNodeWeights(withZeros))
}

func TestSamplePreservedRoundRobinDoesNotDrainTheLargeParticipantFirst(t *testing.T) {
	current := NewEpochMLNodeData()
	eligible := NewEpochMLNodeData()
	many := make([]*types.MLNodeInfo, 6)
	for i := range many {
		many[i] = &types.MLNodeInfo{NodeId: fmt.Sprintf("big-%d", i), PocWeight: 1}
	}
	small := []*types.MLNodeInfo{{NodeId: "small", PocWeight: 1}}
	current.Set("model", "big", many)
	current.Set("model", "small", small)
	eligible.Set("model", "big", many)
	eligible.Set("model", "small", small)
	ma := &ModelAssigner{InferenceLogger: noopLogger{}}
	got := ma.samplePreservedForModel("model", current, eligible, &types.Decimal{Value: 3, Exponent: -1})
	require.Contains(t, got["small"], "small")
	require.Less(t, len(got["big"]), len(many))
}

func TestGetParticipantMedianNodeWeightsSkipsNonPositive(t *testing.T) {
	data := NewEpochMLNodeData()
	data.Set("model", "alice", []*types.MLNodeInfo{
		{NodeId: "z", PocWeight: 0},
		{NodeId: "a", PocWeight: 2},
		{NodeId: "b", PocWeight: 8},
	})
	require.Equal(t, []int64{2}, GetParticipantMedianNodeWeights(data))
}

func BenchmarkSamplePreservedAtCeilings(b *testing.B) {
	for _, n := range []int{types.DefaultMaxPositiveDistributionRows, types.MaxMLNodeWeightRawRowsPerEntry} {
		b.Run(fmt.Sprintf("nodes_%d", n), func(b *testing.B) {
			current := NewEpochMLNodeData()
			eligible := NewEpochMLNodeData()
			nodes := make([]*types.MLNodeInfo, n)
			for i := range nodes {
				nodes[i] = &types.MLNodeInfo{NodeId: fmt.Sprintf("n%06d", i), PocWeight: 1}
			}
			current.Set("model", "alice", nodes)
			eligible.Set("model", "alice", nodes)
			ma := &ModelAssigner{InferenceLogger: noopLogger{}}
			fraction := halfFraction()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				ma.samplePreservedForModel("model", current, eligible, fraction)
			}
		})
	}
}
