package inference

import (
	"context"
	"fmt"
	"testing"

	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
)

func TestCarriedNodeIds(t *testing.T) {
	current := []*types.MLNodeInfo{
		{NodeId: "same", PocWeight: 10},
		{NodeId: "changed", PocWeight: 11},
		{NodeId: "new", PocWeight: 12},
		{NodeId: "zero", PocWeight: 0},
		nil,
	}
	previous := []*types.MLNodeInfo{
		{NodeId: "same", PocWeight: 10},
		{NodeId: "changed", PocWeight: 9},
		{NodeId: "zero", PocWeight: 0},
		{NodeId: "gone", PocWeight: 7},
		nil,
	}
	require.Equal(t, map[string]struct{}{"same": {}}, carriedNodeIds(current, previous))
	require.Nil(t, carriedNodeIds(nil, previous))
	require.Nil(t, carriedNodeIds(current, nil))
	require.Nil(t, carriedNodeIds(current[1:2], previous))
}

func TestWithoutCarriedNodes(t *testing.T) {
	nodes := []*types.MLNodeInfo{{NodeId: "a"}, {NodeId: "b"}, {NodeId: "c"}}
	require.Equal(t, nodes, withoutCarriedNodes(nodes, nil))
	filtered := withoutCarriedNodes(nodes, map[string]struct{}{"b": {}})
	require.Len(t, filtered, 2)
	require.Equal(t, "a", filtered[0].NodeId)
	require.Equal(t, "c", filtered[1].NodeId)
	require.Len(t, nodes, 3)
}

type carriedSamplerFixture struct {
	modelID string
	addrs   []string
	current map[string][]*types.MLNodeInfo
}

func newCarriedSamplerFixture(modelID string) carriedSamplerFixture {
	f := carriedSamplerFixture{modelID: modelID, current: make(map[string][]*types.MLNodeInfo)}
	for i := 0; i < 4; i++ {
		addr := fmt.Sprintf("participant-carry-%d", i)
		f.addrs = append(f.addrs, addr)
		f.current[addr] = []*types.MLNodeInfo{
			{NodeId: addr + "-small", PocWeight: 5},
			{NodeId: addr + "-mid", PocWeight: 10},
			{NodeId: addr + "-large", PocWeight: 12},
		}
	}
	return f
}

func (f carriedSamplerFixture) sample(t *testing.T, previousWeight func(node *types.MLNodeInfo) int64, anchor int64) types.PreservedNodesSnapshot {
	t.Helper()
	participants := make([]*types.ActiveParticipant, 0, len(f.addrs))
	previous := make([]*types.ValidationWeight, 0, len(f.addrs))
	perfSummaries := make(map[string]map[uint64]types.EpochPerformanceSummary, len(f.addrs))
	for _, addr := range f.addrs {
		currentNodes := make([]*types.MLNodeInfo, 0, len(f.current[addr]))
		previousNodes := make([]*types.MLNodeInfo, 0, len(f.current[addr]))
		for _, node := range f.current[addr] {
			currentNodes = append(currentNodes, &types.MLNodeInfo{NodeId: node.NodeId, PocWeight: node.PocWeight})
			previousNodes = append(previousNodes, &types.MLNodeInfo{NodeId: node.NodeId, PocWeight: previousWeight(node)})
		}
		participants = append(participants, &types.ActiveParticipant{
			Index:   addr,
			Models:  []string{f.modelID},
			MlNodes: []*types.ModelMLNodes{{MlNodes: currentNodes}},
		})
		previous = append(previous, &types.ValidationWeight{MemberAddress: addr, MlNodes: previousNodes})
		perfSummaries[addr] = map[uint64]types.EpochPerformanceSummary{
			0: {ParticipantId: addr, EpochIndex: 0, RewardedCoins: 1},
		}
	}

	mockKeeper := &mockKeeperForModelAssigner{
		governanceModels: []types.Model{{Id: f.modelID, ThroughputPerNonce: 1000, VRam: 32}},
		epochGroupData:   map[string]map[uint64]types.EpochGroupData{f.modelID: {0: {ValidationWeights: previous}}},
		perfSummaries:    perfSummaries,
		params:           &types.Params{EpochParams: &types.EpochParams{PocSlotAllocation: &types.Decimal{Value: 5, Exponent: -1}}},
	}
	epoch := types.Epoch{Index: 1}
	mockKeeper.populateSubgroupsFromParticipants(epoch.Index, participants)

	snapshot, err := NewModelAssigner(mockKeeper, mockLogger{}).SamplePreservedForEpisode(context.Background(), epoch, anchor)
	require.NoError(t, err)
	return snapshot
}

func preservedNodeIdSet(snapshot types.PreservedNodesSnapshot) map[string]bool {
	ids := make(map[string]bool)
	for _, mp := range snapshot.ModelPreservedNodes {
		for _, pp := range mp.Participants {
			for _, id := range pp.NodeIds {
				ids[pp.ParticipantId+"/"+id] = true
			}
		}
	}
	return ids
}

func TestSamplePreservedForEpisode_SkipsNodesCarriedFromPreviousEpoch(t *testing.T) {
	f := newCarriedSamplerFixture("model-carried-test")
	carriedSmall := func(node *types.MLNodeInfo) int64 {
		if node.PocWeight == 5 {
			return node.PocWeight
		}
		return node.PocWeight + 1
	}
	allFresh := func(node *types.MLNodeInfo) int64 { return node.PocWeight + 1 }

	smallPreservedWhenFresh := false
	for anchor := int64(1); anchor <= 30; anchor++ {
		gated := preservedNodeIdSet(f.sample(t, carriedSmall, anchor))
		require.NotEmpty(t, gated, "anchor %d", anchor)
		for _, addr := range f.addrs {
			require.False(t, gated[addr+"/"+addr+"-small"], "carried node preserved again at anchor %d", anchor)
		}

		fresh := preservedNodeIdSet(f.sample(t, allFresh, anchor))
		for _, addr := range f.addrs {
			if fresh[addr+"/"+addr+"-small"] {
				smallPreservedWhenFresh = true
			}
		}
	}
	require.True(t, smallPreservedWhenFresh, "smallest node must be preservable when its weight is fresh")
}

func TestSamplePreservedForEpisode_AllCarriedFallsBackToUngatedSample(t *testing.T) {
	f := newCarriedSamplerFixture("model-carried-fallback")
	allCarried := func(node *types.MLNodeInfo) int64 { return node.PocWeight }
	allFresh := func(node *types.MLNodeInfo) int64 { return node.PocWeight + 1 }

	for anchor := int64(1); anchor <= 10; anchor++ {
		carried := f.sample(t, allCarried, anchor)
		fresh := f.sample(t, allFresh, anchor)
		require.NotEmpty(t, carried.ModelPreservedNodes, "anchor %d", anchor)
		require.Equal(t, fresh, carried, "anchor %d", anchor)
	}
}

func TestSamplePreservedForEpisode_CarriedParticipantStillMinesWhenOthersPreservable(t *testing.T) {
	f := newCarriedSamplerFixture("model-carried-participant")
	carriedAddr := f.addrs[0]
	carriedParticipant := func(node *types.MLNodeInfo) int64 {
		if len(node.NodeId) > len(carriedAddr) && node.NodeId[:len(carriedAddr)] == carriedAddr {
			return node.PocWeight
		}
		return node.PocWeight + 1
	}

	for anchor := int64(1); anchor <= 30; anchor++ {
		snapshot := f.sample(t, carriedParticipant, anchor)
		require.NotEmpty(t, snapshot.ModelPreservedNodes, "anchor %d", anchor)
		require.False(t, collectPreservedParticipants(snapshot)[carriedAddr], "fully carried participant preserved again at anchor %d", anchor)
	}
}

type carriedHostSpec struct {
	addr     string
	vp       int64
	rewarded bool
	current  []*types.MLNodeInfo
	previous []*types.MLNodeInfo
}

func sampleCarriedHosts(t *testing.T, modelID string, hosts []carriedHostSpec, anchor int64) types.PreservedNodesSnapshot {
	t.Helper()
	var root, current, previous []*types.ValidationWeight
	perfSummaries := make(map[string]map[uint64]types.EpochPerformanceSummary)
	clone := func(nodes []*types.MLNodeInfo) []*types.MLNodeInfo {
		out := make([]*types.MLNodeInfo, 0, len(nodes))
		for _, n := range nodes {
			c := *n
			out = append(out, &c)
		}
		return out
	}
	for _, h := range hosts {
		root = append(root, &types.ValidationWeight{MemberAddress: h.addr, Weight: h.vp})
		current = append(current, &types.ValidationWeight{MemberAddress: h.addr, VotingPower: h.vp, MlNodes: clone(h.current)})
		previous = append(previous, &types.ValidationWeight{MemberAddress: h.addr, VotingPower: h.vp, MlNodes: clone(h.previous)})
		if h.rewarded {
			perfSummaries[h.addr] = map[uint64]types.EpochPerformanceSummary{0: {ParticipantId: h.addr, EpochIndex: 0, RewardedCoins: 1}}
		}
	}
	mockKeeper := &mockKeeperForModelAssigner{
		governanceModels: []types.Model{{Id: modelID, ThroughputPerNonce: 1000, VRam: 32}},
		epochGroupData: map[string]map[uint64]types.EpochGroupData{
			"":      {1: {EpochIndex: 1, SubGroupModels: []string{modelID}, ValidationWeights: root}},
			modelID: {0: {EpochIndex: 0, ModelId: modelID, ValidationWeights: previous}, 1: {EpochIndex: 1, ModelId: modelID, ValidationWeights: current}},
		},
		perfSummaries: perfSummaries,
		params:        &types.Params{EpochParams: &types.EpochParams{PocSlotAllocation: &types.Decimal{Value: 5, Exponent: -1}}},
	}
	snapshot, err := NewModelAssigner(mockKeeper, mockLogger{}).SamplePreservedForEpisode(context.Background(), types.Epoch{Index: 1}, anchor)
	require.NoError(t, err)
	return snapshot
}

func carriedNodes(id string, weights ...int64) []*types.MLNodeInfo {
	nodes := make([]*types.MLNodeInfo, 0, len(weights))
	for i, w := range weights {
		nodes = append(nodes, &types.MLNodeInfo{NodeId: fmt.Sprintf("%s-%d", id, i), PocWeight: w})
	}
	return nodes
}

func freshHosts(hosts []carriedHostSpec) []carriedHostSpec {
	out := make([]carriedHostSpec, 0, len(hosts))
	for _, h := range hosts {
		prev := make([]*types.MLNodeInfo, 0, len(h.current))
		for _, n := range h.current {
			prev = append(prev, &types.MLNodeInfo{NodeId: n.NodeId, PocWeight: n.PocWeight + 1})
		}
		h.previous = prev
		out = append(out, h)
	}
	return out
}

func TestSamplePreservedForEpisode_FallbackWhenFreshCandidateLacksHistory(t *testing.T) {
	modelID := "model-carried-history"
	var hosts []carriedHostSpec
	for i := 0; i < 3; i++ {
		addr := fmt.Sprintf("carried-host-%d", i)
		hosts = append(hosts, carriedHostSpec{addr: addr, vp: 25, rewarded: true, current: carriedNodes(addr, 5, 10, 12), previous: carriedNodes(addr, 5, 10, 12)})
	}
	hosts = append(hosts, carriedHostSpec{addr: "fresh-host-unrewarded", vp: 25, current: carriedNodes("fresh-host-unrewarded", 5, 10, 12), previous: carriedNodes("fresh-host-unrewarded", 6, 11, 13)})

	for anchor := int64(1); anchor <= 20; anchor++ {
		reference := sampleCarriedHosts(t, modelID, freshHosts(hosts), anchor)
		require.NotEmpty(t, reference.ModelPreservedNodes, "anchor %d", anchor)
		require.Equal(t, reference, sampleCarriedHosts(t, modelID, hosts, anchor), "anchor %d", anchor)
	}
}

func TestSamplePreservedForEpisode_CountLimitPrefersFreshEqualWeightNode(t *testing.T) {
	modelID := "model-carried-countlimit"
	var hosts []carriedHostSpec
	for i := 0; i < 4; i++ {
		addr := fmt.Sprintf("host-%d", i)
		hosts = append(hosts, carriedHostSpec{
			addr:     addr,
			vp:       25,
			rewarded: true,
			current:  []*types.MLNodeInfo{{NodeId: "a", PocWeight: 10}, {NodeId: "b", PocWeight: 10}},
			previous: []*types.MLNodeInfo{{NodeId: "a", PocWeight: 10}, {NodeId: "b", PocWeight: 9}},
		})
	}

	freshPreserved := false
	for anchor := int64(1); anchor <= 20; anchor++ {
		for id := range preservedNodeIdSet(sampleCarriedHosts(t, modelID, hosts, anchor)) {
			require.NotEqual(t, "/a", id[len(id)-2:], "carried node %s preserved at anchor %d", id, anchor)
			freshPreserved = true
		}
	}
	require.True(t, freshPreserved, "fresh equal-weight nodes must remain preservable")
}

func TestSamplePreservedForEpisode_DuplicatePreviousNodesUseDedupedWeight(t *testing.T) {
	modelID := "model-carried-dedup"
	var hosts []carriedHostSpec
	for i := 0; i < 4; i++ {
		addr := fmt.Sprintf("host-%d", i)
		hosts = append(hosts, carriedHostSpec{
			addr:     addr,
			vp:       10,
			rewarded: true,
			current:  []*types.MLNodeInfo{{NodeId: "small", PocWeight: 5}, {NodeId: "mid", PocWeight: 10}, {NodeId: "large", PocWeight: 12}},
			previous: []*types.MLNodeInfo{{NodeId: "small", PocWeight: 5}, {NodeId: "small", PocWeight: 3}, {NodeId: "mid", PocWeight: 11}, {NodeId: "large", PocWeight: 13}},
		})
	}

	for anchor := int64(1); anchor <= 20; anchor++ {
		snapshot := sampleCarriedHosts(t, modelID, hosts, anchor)
		require.NotEmpty(t, snapshot.ModelPreservedNodes, "anchor %d", anchor)
		for id := range preservedNodeIdSet(snapshot) {
			require.NotEqual(t, "/small", id[len(id)-6:], "carried node %s preserved at anchor %d", id, anchor)
		}
	}
}
