package broker

import (
	"testing"
	"time"

	"decentralized-api/apiconfig"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/require"
)

// affinityMetricsModel is the only model NewTestBroker's mocked governance query
// allows through node registration (see broker_test.go's mockChainBridge setup),
// so every test here must route through it.
const affinityMetricsModel = "model1"

// enableNodeAffinity swaps in an enabled tracker so a test can pre-record a
// binding without depending on DAPI_MLNODE_AFFINITY_ENABLED in the process env.
func enableNodeAffinity(b *Broker) {
	b.sessionAffinity = newNodeSessionAffinity(nodeAffinityConfig{
		Enabled: true, MaxRequests: 100, TTL: time.Hour, MaxEntries: 100,
	})
}

func registerAvailableNode(t *testing.T, b *Broker, nodeID string) {
	t.Helper()
	registerNodeAndSetInferenceStatus(t, b, apiconfig.InferenceNodeConfig{
		Id: nodeID, Host: "localhost", InferencePort: 8080,
		InferenceSegment: "/v1", PoCPort: 8081,
		Models:        map[string]apiconfig.ModelConfig{affinityMetricsModel: {}},
		MaxConcurrent: 4,
	})
}

// TestMLNodeAffinityDecisionHit: the session's remembered node is registered and
// available, so lockAvailableNode must serve it and record "hit".
func TestMLNodeAffinityDecisionHit(t *testing.T) {
	b := NewTestBroker()
	enableNodeAffinity(b)
	registerAvailableNode(t, b, "node1")
	b.sessionAffinity.record("escrow-1", "sess", "node1")
	before := affinityDecisionCount(t, "hit")

	availableNode := make(chan *Node, 1)
	queueMessage(t, b, LockAvailableNode{Model: affinityMetricsModel, EscrowID: "escrow-1", SessionID: "sess", Response: availableNode})
	node := <-availableNode

	require.NotNil(t, node)
	require.Equal(t, "node1", node.Id)
	require.Equal(t, before+1, affinityDecisionCount(t, "hit"))
}

// TestMLNodeAffinityDecisionYielded: the session's remembered node was never
// registered (gone from the fleet), so the broker must fall back to least-busy
// and record "yielded".
func TestMLNodeAffinityDecisionYielded(t *testing.T) {
	b := NewTestBroker()
	enableNodeAffinity(b)
	registerAvailableNode(t, b, "node1")
	b.sessionAffinity.record("escrow-1", "sess", "node-gone")
	before := affinityDecisionCount(t, "yielded")

	availableNode := make(chan *Node, 1)
	queueMessage(t, b, LockAvailableNode{Model: affinityMetricsModel, EscrowID: "escrow-1", SessionID: "sess", Response: availableNode})
	node := <-availableNode

	require.NotNil(t, node)
	require.Equal(t, "node1", node.Id, "must fall back to the only registered node")
	require.Equal(t, before+1, affinityDecisionCount(t, "yielded"))
}

// TestMLNodeAffinityDecisionMiss: the session has no prior binding, so the
// broker must record "miss" even though it still serves the request normally.
func TestMLNodeAffinityDecisionMiss(t *testing.T) {
	b := NewTestBroker()
	enableNodeAffinity(b)
	registerAvailableNode(t, b, "node1")
	before := affinityDecisionCount(t, "miss")

	availableNode := make(chan *Node, 1)
	queueMessage(t, b, LockAvailableNode{Model: affinityMetricsModel, EscrowID: "escrow-1", SessionID: "sess-never-seen", Response: availableNode})
	node := <-availableNode

	require.NotNil(t, node)
	require.Equal(t, "node1", node.Id)
	require.Equal(t, before+1, affinityDecisionCount(t, "miss"))
}

// TestMLNodeAffinityDecisionDisabledEmitsNoMetric: with affinity off (NewTestBroker's
// default), a request that would otherwise be a "miss" must not touch the counter.
func TestMLNodeAffinityDecisionDisabledEmitsNoMetric(t *testing.T) {
	t.Setenv("DAPI_MLNODE_AFFINITY_ENABLED", "false") // independent of whatever the process env happens to have
	b := NewTestBroker()
	registerAvailableNode(t, b, "node1")
	beforeHit := affinityDecisionCount(t, "hit")
	beforeYielded := affinityDecisionCount(t, "yielded")
	beforeMiss := affinityDecisionCount(t, "miss")

	availableNode := make(chan *Node, 1)
	queueMessage(t, b, LockAvailableNode{Model: affinityMetricsModel, EscrowID: "escrow-1", SessionID: "sess", Response: availableNode})
	node := <-availableNode

	require.NotNil(t, node)
	require.Equal(t, beforeHit, affinityDecisionCount(t, "hit"), "disabled affinity must never write the decision counter")
	require.Equal(t, beforeYielded, affinityDecisionCount(t, "yielded"), "disabled affinity must never write the decision counter")
	require.Equal(t, beforeMiss, affinityDecisionCount(t, "miss"), "disabled affinity must never write the decision counter")
}

// affinityDecisionCount reads decentralized_api_mlnode_affinity_decision_total off the
// default Prometheus gatherer that RecordMLNodeAffinityDecision writes to. The counter
// is process-global (promOnce-registered against prometheus.DefaultRegisterer), so
// every assertion here is a before/after diff rather than an absolute value.
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
