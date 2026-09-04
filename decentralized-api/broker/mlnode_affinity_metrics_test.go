package broker

import (
	"testing"
	"time"

	"decentralized-api/apiconfig"
	"decentralized-api/broker/sessionaffinity"
	"decentralized-api/observability"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/require"
)

const affinityMetricsModel = "model1"

const affinityMetricsMaxConcurrent = 4

func enableNodeAffinity(b *Broker) {
	b.sessionAffinity = sessionaffinity.New(
		sessionaffinity.Config{Enabled: true, MaxRequests: 100, TTL: time.Hour, MaxEntries: 100},
		observability.RecordMLNodeAffinityDecision,
	)
}

func registerAvailableNode(t *testing.T, b *Broker, nodeID string, portOffset int) {
	t.Helper()
	registerNodeAndSetInferenceStatus(t, b, apiconfig.InferenceNodeConfig{
		Id: nodeID, Host: "localhost", InferencePort: 8080 + 2*portOffset,
		InferenceSegment: "/v1", PoCPort: 8081 + 2*portOffset,
		Models:        map[string]apiconfig.ModelConfig{affinityMetricsModel: {}},
		MaxConcurrent: affinityMetricsMaxConcurrent,
	})
}

func setLockCount(t *testing.T, b *Broker, nodeID string, lockCount int) {
	t.Helper()
	b.mu.Lock()
	defer b.mu.Unlock()
	node, exists := b.nodes[nodeID]
	if !exists {
		t.Fatalf("node %s is not registered", nodeID)
	}
	node.State.LockCount = lockCount
}

func lockedNodeFor(t *testing.T, b *Broker, sessionID string) *Node {
	t.Helper()
	availableNode := make(chan *Node, 1)
	queueMessage(t, b, LockAvailableNode{Model: affinityMetricsModel, EscrowID: "escrow-1", SessionID: sessionID, Response: availableNode})
	return <-availableNode
}

func TestMLNodeAffinityDecisionHit(t *testing.T) {
	b := NewTestBroker()
	enableNodeAffinity(b)
	registerAvailableNode(t, b, "node1", 0)
	b.sessionAffinity.Record("escrow-1", "sess", "node1")
	before := affinityDecisionCount(t, sessionaffinity.DecisionHit)

	availableNode := make(chan *Node, 1)
	queueMessage(t, b, LockAvailableNode{Model: affinityMetricsModel, EscrowID: "escrow-1", SessionID: "sess", Response: availableNode})
	node := <-availableNode

	require.NotNil(t, node)
	require.Equal(t, "node1", node.Id)
	require.Equal(t, before+1, affinityDecisionCount(t, sessionaffinity.DecisionHit))
}

func TestMLNodeAffinityDecisionYielded(t *testing.T) {
	b := NewTestBroker()
	enableNodeAffinity(b)
	registerAvailableNode(t, b, "node1", 0)
	b.sessionAffinity.Record("escrow-1", "sess", "node-gone")
	before := affinityDecisionCount(t, sessionaffinity.DecisionYielded)

	availableNode := make(chan *Node, 1)
	queueMessage(t, b, LockAvailableNode{Model: affinityMetricsModel, EscrowID: "escrow-1", SessionID: "sess", Response: availableNode})
	node := <-availableNode

	require.NotNil(t, node)
	require.Equal(t, "node1", node.Id, "must fall back to the only registered node")
	require.Equal(t, before+1, affinityDecisionCount(t, sessionaffinity.DecisionYielded))
}

func TestMLNodeAffinityDecisionMiss(t *testing.T) {
	b := NewTestBroker()
	enableNodeAffinity(b)
	registerAvailableNode(t, b, "node1", 0)
	before := affinityDecisionCount(t, sessionaffinity.DecisionMiss)

	availableNode := make(chan *Node, 1)
	queueMessage(t, b, LockAvailableNode{Model: affinityMetricsModel, EscrowID: "escrow-1", SessionID: "sess-never-seen", Response: availableNode})
	node := <-availableNode

	require.NotNil(t, node)
	require.Equal(t, "node1", node.Id)
	require.Equal(t, before+1, affinityDecisionCount(t, sessionaffinity.DecisionMiss))
}

func TestMLNodeAffinityDecisionDisabledEmitsNoMetric(t *testing.T) {
	t.Setenv("DAPI_MLNODE_AFFINITY_ENABLED", "false") // independent of whatever the process env happens to have
	b := NewTestBroker()
	registerAvailableNode(t, b, "node1", 0)
	beforeHit := affinityDecisionCount(t, sessionaffinity.DecisionHit)
	beforeYielded := affinityDecisionCount(t, sessionaffinity.DecisionYielded)
	beforeMiss := affinityDecisionCount(t, sessionaffinity.DecisionMiss)

	availableNode := make(chan *Node, 1)
	queueMessage(t, b, LockAvailableNode{Model: affinityMetricsModel, EscrowID: "escrow-1", SessionID: "sess", Response: availableNode})
	node := <-availableNode

	require.NotNil(t, node)
	require.Equal(t, beforeHit, affinityDecisionCount(t, sessionaffinity.DecisionHit), "disabled affinity must never write the decision counter")
	require.Equal(t, beforeYielded, affinityDecisionCount(t, sessionaffinity.DecisionYielded), "disabled affinity must never write the decision counter")
	require.Equal(t, beforeMiss, affinityDecisionCount(t, sessionaffinity.DecisionMiss), "disabled affinity must never write the decision counter")
}

func affinityDecisionCount(t *testing.T, decision string) float64 {
	t.Helper()
	families, err := prometheus.DefaultGatherer.Gather()
	require.NoError(t, err)
	for _, family := range families {
		if family.GetName() != "decentralized_api_mlnode_affinity_decision_total" {
			continue
		}
		for _, metric := range family.GetMetric() {
			if metricHasLabels(metric, map[string]string{"decision": decision, "model": affinityMetricsModel}) {
				return metric.GetCounter().GetValue()
			}
		}
	}
	return 0
}

func metricHasLabels(metric *dto.Metric, want map[string]string) bool {
	for _, pair := range metric.GetLabel() {
		if expected, ok := want[pair.GetName()]; ok && expected != pair.GetValue() {
			return false
		}
	}
	return true
}

func TestMLNodeAffinityDecisionCongested(t *testing.T) {
	b := NewTestBroker()
	enableNodeAffinity(b)
	registerAvailableNode(t, b, "node-sticky", 0)
	registerAvailableNode(t, b, "node-idle", 1)
	b.sessionAffinity.Record("escrow-1", "sess", "node-sticky")
	setLockCount(t, b, "node-sticky", affinityMetricsMaxConcurrent-1)
	before := affinityDecisionCount(t, sessionaffinity.DecisionCongested)

	node := lockedNodeFor(t, b, "sess")

	require.NotNil(t, node)
	require.Equal(t, "node-idle", node.Id,
		"past the margin a warm cache is not worth the queue in front of it")
	require.Equal(t, before+1, affinityDecisionCount(t, sessionaffinity.DecisionCongested))
}

func TestMLNodeAffinityYieldsWhenStickyNodeIsSaturated(t *testing.T) {
	b := NewTestBroker()
	enableNodeAffinity(b)
	registerAvailableNode(t, b, "node-sticky", 0)
	registerAvailableNode(t, b, "node-idle", 1)
	b.sessionAffinity.Record("escrow-1", "sess", "node-sticky")
	setLockCount(t, b, "node-sticky", affinityMetricsMaxConcurrent)
	before := affinityDecisionCount(t, sessionaffinity.DecisionYielded)

	node := lockedNodeFor(t, b, "sess")

	require.NotNil(t, node)
	require.Equal(t, "node-idle", node.Id)
	require.Equal(t, before+1, affinityDecisionCount(t, sessionaffinity.DecisionYielded),
		"a node at its cap is unusable, which is a different signal from congestion")
}

func TestMLNodeAffinityBindsOnFirstAcquire(t *testing.T) {
	b := NewTestBroker()
	enableNodeAffinity(b)
	registerAvailableNode(t, b, "node-a", 0)
	registerAvailableNode(t, b, "node-b", 1)
	before := affinityDecisionCount(t, sessionaffinity.DecisionHit)

	firstNode := lockedNodeFor(t, b, "sess")
	secondNode := lockedNodeFor(t, b, "sess")

	require.NotNil(t, firstNode)
	require.NotNil(t, secondNode)
	require.Equal(t, firstNode.Id, secondNode.Id,
		"the second request of a session must return to the node the first one bound")
	require.Equal(t, before+1, affinityDecisionCount(t, sessionaffinity.DecisionHit))
}

func TestMLNodeAffinityHonoursTheSkipList(t *testing.T) {
	b := NewTestBroker()
	enableNodeAffinity(b)
	registerAvailableNode(t, b, "node-sticky", 0)
	registerAvailableNode(t, b, "node-idle", 1)
	b.sessionAffinity.Record("escrow-1", "sess", "node-sticky")
	before := affinityDecisionCount(t, sessionaffinity.DecisionYielded)

	availableNode := make(chan *Node, 1)
	queueMessage(t, b, LockAvailableNode{
		Model: affinityMetricsModel, EscrowID: "escrow-1", SessionID: "sess",
		SkipNodeIDs: []string{"node-sticky"}, Response: availableNode,
	})
	node := <-availableNode

	require.NotNil(t, node)
	require.Equal(t, "node-idle", node.Id, "an excluded node must not win on stickiness")
	require.Equal(t, before+1, affinityDecisionCount(t, sessionaffinity.DecisionYielded))
}

func TestMLNodeAffinityToleratesAMiscountedPeer(t *testing.T) {
	b := NewTestBroker()
	enableNodeAffinity(b)
	registerAvailableNode(t, b, "node-sticky", 0)
	registerAvailableNode(t, b, "node-idle", 1)
	b.sessionAffinity.Record("escrow-1", "sess", "node-sticky")
	setLockCount(t, b, "node-idle", -5)
	before := affinityDecisionCount(t, sessionaffinity.DecisionHit)

	node := lockedNodeFor(t, b, "sess")

	require.NotNil(t, node)
	require.Equal(t, "node-sticky", node.Id, "a miscounted peer must not read as idle")
	require.Equal(t, before+1, affinityDecisionCount(t, sessionaffinity.DecisionHit))
}
