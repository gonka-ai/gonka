package poc

import (
	"context"
	"testing"
	"time"

	"decentralized-api/broker"
	"decentralized-api/mlnodeclient"

	"github.com/stretchr/testify/require"
)

const testVerdictHeight = int64(1000)

func verdictNode(num uint64, host string) broker.NodeResponse {
	return broker.NodeResponse{Node: broker.Node{
		Host:    host,
		PoCPort: 8080,
		NodeNum: num,
		Models:  map[string]broker.ModelArgs{"test-model": {}},
	}}
}

func verdictWork(address string) participantWork {
	return participantWork{
		address:      address,
		modelId:      "test-model",
		pubKey:       "testpubkey",
		count:        100,
		url:          "http://participant",
		checksPassed: true,
		verified:     []VerifiedArtifact{{LeafIndex: 0, Nonce: 1}},
	}
}

func TestVerdictTracker_CountsAnsweredAndLostJobs(t *testing.T) {
	tr := NewVerdictTracker()
	t0 := time.Now()
	tr.BeginStage(testVerdictHeight)
	tr.Sent(testVerdictHeight, verdictWork("a"), dispatchContext{}, 1, "node-1", t0)
	tr.Sent(testVerdictHeight, verdictWork("b"), dispatchContext{}, 2, "node-2", t0)
	tr.Answered(testVerdictHeight, "a", "test-model", 1, t0.Add(40*time.Second))

	s := tr.Stats(testVerdictHeight)
	require.Equal(t, 2, s.Dispatched)
	require.Equal(t, 1, s.Answered)
	require.Equal(t, 1, s.Lost)
	require.Equal(t, map[string]int{"node-2 (node 2)": 1}, s.LostByNode)
	require.Equal(t, 40*time.Second, s.AnswerTimeMax)
}

func TestVerdictTracker_OverdueReturnsEachJobOnce(t *testing.T) {
	tr := NewVerdictTracker()
	t0 := time.Now()
	tr.BeginStage(testVerdictHeight)
	tr.Sent(testVerdictHeight, verdictWork("a"), dispatchContext{}, 1, "node-1", t0)

	require.Empty(t, tr.overdue(testVerdictHeight, t0.Add(9*time.Minute), 10*time.Minute))
	jobs := tr.overdue(testVerdictHeight, t0.Add(10*time.Minute), 10*time.Minute)
	require.Len(t, jobs, 1)
	require.Equal(t, "a", jobs[0].work.address)
	require.Equal(t, uint64(1), jobs[0].nodeNum)
	require.Empty(t, tr.overdue(testVerdictHeight, t0.Add(20*time.Minute), 10*time.Minute))
	require.Empty(t, tr.overdue(testVerdictHeight, t0.Add(20*time.Minute), 0), "zero timeout turns re-dispatch off")
}

func TestVerdictTracker_AnswerBeforeSentIsNotLost(t *testing.T) {
	tr := NewVerdictTracker()
	t0 := time.Now()
	tr.BeginStage(testVerdictHeight)
	// The callback can arrive before GenerateV2 returns and Sent runs.
	tr.Answered(testVerdictHeight, "a", "test-model", 1, t0)
	tr.Sent(testVerdictHeight, verdictWork("a"), dispatchContext{}, 1, "node-1", t0)

	require.Empty(t, tr.overdue(testVerdictHeight, t0.Add(time.Hour), 10*time.Minute))
	s := tr.Stats(testVerdictHeight)
	require.Equal(t, 1, s.Dispatched)
	require.Equal(t, 1, s.Answered)
	require.Zero(t, s.Lost)
}

func TestVerdictTracker_IgnoresStagesNotBegun(t *testing.T) {
	tr := NewVerdictTracker()
	t0 := time.Now()
	tr.Sent(testVerdictHeight, verdictWork("a"), dispatchContext{}, 1, "node-1", t0)
	tr.Answered(testVerdictHeight, "a", "test-model", 1, t0)
	require.Zero(t, tr.Stats(testVerdictHeight).Dispatched)

	tr.BeginStage(testVerdictHeight)
	tr.Sent(testVerdictHeight, verdictWork("a"), dispatchContext{}, 1, "node-1", t0)
	tr.EndStage(testVerdictHeight)
	require.Zero(t, tr.Stats(testVerdictHeight).Dispatched)
}

func TestVerdictTracker_PickNodeTakesTheLeastBusyAnsweringNode(t *testing.T) {
	tr := NewVerdictTracker()
	t0 := time.Now()
	tr.BeginStage(testVerdictHeight)
	nodes := []broker.NodeResponse{verdictNode(1, "node-1"), verdictNode(2, "node-2"), verdictNode(3, "node-3"), verdictNode(4, "node-4")}

	// node-1 lost the job; node-2 answered 1 of 3; node-3 answered 1 of 1; node-4 never answered.
	tr.Sent(testVerdictHeight, verdictWork("lost"), dispatchContext{}, 1, "node-1", t0)
	for _, a := range []string{"b1", "b2", "b3"} {
		tr.Sent(testVerdictHeight, verdictWork(a), dispatchContext{}, 2, "node-2", t0)
	}
	tr.Answered(testVerdictHeight, "b1", "test-model", 2, t0)
	tr.Sent(testVerdictHeight, verdictWork("c1"), dispatchContext{}, 3, "node-3", t0)
	tr.Answered(testVerdictHeight, "c1", "test-model", 3, t0)
	tr.Sent(testVerdictHeight, verdictWork("d1"), dispatchContext{}, 4, "node-4", t0)

	idx, ok := tr.pickNode(testVerdictHeight, nodes, 1)
	require.True(t, ok)
	require.Equal(t, "node-3", nodes[idx].Node.Host)

	_, ok = tr.pickNode(testVerdictHeight, nodes[:1], 1)
	require.False(t, ok, "the node that lost the job is never picked")
	_, ok = tr.pickNode(testVerdictHeight, []broker.NodeResponse{nodes[0], nodes[3]}, 1)
	require.False(t, ok, "a node that has not answered in this stage is not picked")
}

func newRedispatchValidator(client *mlnodeclient.MockClient, tr *VerdictTracker) *OffChainValidator {
	return &OffChainValidator{
		nodeBroker:  &stubNodeBroker{client: client},
		callbackUrl: "http://callback",
		config:      ValidationConfig{VerdictTimeout: 10 * time.Minute},
		verdicts:    tr,
	}
}

func TestRedispatchOverdue_SendsALostJobOnceToAnotherAnsweringNode(t *testing.T) {
	client := mlnodeclient.NewMockClient()
	tr := NewVerdictTracker()
	v := newRedispatchValidator(client, tr)
	nodes := []broker.NodeResponse{verdictNode(1, "node-1"), verdictNode(2, "node-2")}
	params := testModelPocParams()
	t0 := time.Now()
	tr.BeginStage(testVerdictHeight)

	// The worker path dispatches "lost" to node-1 and "fine" to node-2; only node-2 answers.
	counter := 0
	lost, fine := verdictWork("lost"), verdictWork("fine")
	require.Equal(t, validateDispatched, v.dispatchToMLNode(context.Background(), &lost, nodes, &counter, testVerdictHeight, "start-hash", params))
	require.Equal(t, validateDispatched, v.dispatchToMLNode(context.Background(), &fine, nodes, &counter, testVerdictHeight, "start-hash", params))
	tr.Answered(testVerdictHeight, "fine", "test-model", 2, time.Now())

	v.redispatchOverdue(context.Background(), testVerdictHeight, t0.Add(9*time.Minute))
	require.Equal(t, 2, client.GenerateV2Called, "nothing is overdue before the timeout")

	v.redispatchOverdue(context.Background(), testVerdictHeight, time.Now().Add(11*time.Minute))
	require.Equal(t, 3, client.GenerateV2Called)
	require.Equal(t, 2, client.LastGenerateV2Req.NodeId, "re-dispatched to the node that answers")
	require.Equal(t, "testpubkey", client.LastGenerateV2Req.PublicKey)
	require.Len(t, client.LastGenerateV2Req.Validation.Artifacts, 1, "the verified artifacts are sent again")

	v.redispatchOverdue(context.Background(), testVerdictHeight, time.Now().Add(30*time.Minute))
	require.Equal(t, 3, client.GenerateV2Called, "a job is re-dispatched once")

	s := tr.Stats(testVerdictHeight)
	require.Equal(t, 2, s.Dispatched)
	require.Equal(t, 1, s.Redispatched)
	require.Equal(t, 1, s.Lost)

	tr.Answered(testVerdictHeight, "lost", "test-model", 2, time.Now())
	s = tr.Stats(testVerdictHeight)
	require.Zero(t, s.Lost)
	require.Equal(t, 1, s.AnsweredRedispatch)
}

// A challenge can share its start height with a regular stage, but it validates
// with its own seed and frozen recipe. A lost job must be sent again with the
// ones it was first sent with, whichever pass dispatched it.
func TestRedispatchOverdue_SendsTheJobAgainWithItsOwnSeedAndRecipe(t *testing.T) {
	client := mlnodeclient.NewMockClient()
	tr := NewVerdictTracker()
	v := newRedispatchValidator(client, tr)
	nodes := []broker.NodeResponse{verdictNode(1, "node-1"), verdictNode(2, "node-2")}
	require.True(t, tr.BeginStage(testVerdictHeight))
	require.False(t, tr.BeginStage(testVerdictHeight), "a second pass at the same height shares the stage and its watcher")

	regular := testModelPocParams()
	challenge := testModelPocParams()
	challenge.Models[0].SeqLen = 512

	counter := 0
	lost, fine := verdictWork("challenge-target"), verdictWork("fine")
	require.Equal(t, validateDispatched, v.dispatchToMLNode(context.Background(), &lost, nodes, &counter, testVerdictHeight, "challenge-seed", challenge))
	require.Equal(t, validateDispatched, v.dispatchToMLNode(context.Background(), &fine, nodes, &counter, testVerdictHeight, "start-hash", regular))
	tr.Answered(testVerdictHeight, "fine", "test-model", 2, time.Now())

	v.redispatchOverdue(context.Background(), testVerdictHeight, time.Now().Add(11*time.Minute))
	require.Equal(t, 3, client.GenerateV2Called)
	require.Equal(t, 2, client.LastGenerateV2Req.NodeId)
	require.Equal(t, "challenge-seed", client.LastGenerateV2Req.BlockHash)
	require.Equal(t, int64(512), client.LastGenerateV2Req.Params.SeqLen)
}

func TestRedispatchOverdue_KeepsTheJobLostWithoutAnotherAnsweringNode(t *testing.T) {
	client := mlnodeclient.NewMockClient()
	tr := NewVerdictTracker()
	v := newRedispatchValidator(client, tr)
	nodes := []broker.NodeResponse{verdictNode(1, "node-1"), verdictNode(2, "node-2")}
	params := testModelPocParams()
	tr.BeginStage(testVerdictHeight)

	// Both nodes take a job and neither answers, as on a host whose nodes all fail validation.
	counter := 0
	a, b := verdictWork("a"), verdictWork("b")
	require.Equal(t, validateDispatched, v.dispatchToMLNode(context.Background(), &a, nodes, &counter, testVerdictHeight, "start-hash", params))
	require.Equal(t, validateDispatched, v.dispatchToMLNode(context.Background(), &b, nodes, &counter, testVerdictHeight, "start-hash", params))

	v.redispatchOverdue(context.Background(), testVerdictHeight, time.Now().Add(11*time.Minute))
	require.Equal(t, 2, client.GenerateV2Called, "no node has answered, so nothing is sent again")

	s := tr.Stats(testVerdictHeight)
	require.Equal(t, 2, s.Lost)
	require.Equal(t, map[string]int{"node-1 (node 1)": 1, "node-2 (node 2)": 1}, s.LostByNode)
}
