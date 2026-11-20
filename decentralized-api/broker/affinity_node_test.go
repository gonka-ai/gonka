package broker

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"testing"
	"time"

	"decentralized-api/apiconfig"
	"decentralized-api/mlnodeclient"

	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
)

const affinityTestModel = "model1"

func registerInferenceNode(t *testing.T, b *Broker, node apiconfig.InferenceNodeConfig) {
	t.Helper()
	factory := b.mlNodeClientFactory.(*mlnodeclient.MockClientFactory)
	client := factory.CreateClient(fmt.Sprintf("http://%s:%d", node.Host, node.PoCPort), fmt.Sprintf("http://%s:%d", node.Host, node.InferencePort)).(*mlnodeclient.MockClient)
	client.Mu.Lock()
	client.CurrentState = mlnodeclient.MlNodeState_INFERENCE
	client.InferenceIsHealthy = true
	client.Mu.Unlock()

	register := NewRegisterNodeCommand(node)
	queueMessage(t, b, register)
	<-register.Response

	mlNode := types.MLNodeInfo{NodeId: node.Id, PocWeight: 10, TimeslotAllocation: []bool{true, false}}
	b.UpdateNodeEpochData([]*types.MLNodeInfo{&mlNode}, affinityTestModel, types.Model{Id: affinityTestModel})

	inferenceUp := NewInferenceUpAllCommand()
	queueMessage(t, b, inferenceUp)
	<-inferenceUp.Response

	setStatus := NewSetNodesActualStatusCommand([]StatusUpdate{{
		NodeId:          node.Id,
		RegistrationSeq: nodeRegistrationSeq(t, b, node.Id),
		PrevStatus:      types.HardwareNodeStatus_UNKNOWN,
		NewStatus:       types.HardwareNodeStatus_INFERENCE,
		Timestamp:       time.Now(),
	}})
	queueMessage(t, b, setStatus)
	<-setStatus.Response

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		nodes, err := b.GetNodes()
		require.NoError(t, err)
		for _, n := range nodes {
			if n.Node.Id == node.Id && n.State.IntendedStatus == types.HardwareNodeStatus_INFERENCE &&
				n.State.CurrentStatus == types.HardwareNodeStatus_INFERENCE && n.State.ReconcileInfo == nil {
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("node %s did not reach INFERENCE status within 2s", node.Id)
}

func newAffinityTestBroker(t *testing.T, nodeCount int, maxConcurrent int) *Broker {
	t.Helper()
	b := NewTestBroker()
	for i := 0; i < nodeCount; i++ {
		registerInferenceNode(t, b, apiconfig.InferenceNodeConfig{
			Id: fmt.Sprintf("node-%d", i), Host: "localhost", InferencePort: 8080 + i,
			InferenceSegment: "/v1", PoCPort: 9080 + i,
			Models:        map[string]apiconfig.ModelConfig{affinityTestModel: {}},
			MaxConcurrent: maxConcurrent,
		})
	}
	return b
}

func acquireNode(t *testing.T, b *Broker, sessionKey string, skip ...string) (lockID, nodeID string) {
	t.Helper()
	lockID, _, nodeID, err := b.AcquireMLNode(context.Background(), affinityTestModel, skip, sessionKey, nil)
	require.NoError(t, err)
	return lockID, nodeID
}

func acquireAndRelease(t *testing.T, b *Broker, sessionKey string, skip ...string) string {
	t.Helper()
	lockID, nodeID := acquireNode(t, b, sessionKey, skip...)
	require.NoError(t, b.ReleaseMLNode(lockID, InferenceSuccess{}))
	return nodeID
}

func lockCounts(b *Broker) map[string]int {
	b.mu.RLock()
	defer b.mu.RUnlock()
	counts := make(map[string]int, len(b.nodes))
	for id, node := range b.nodes {
		counts[id] = node.State.LockCount
	}
	return counts
}

func setLockCount(b *Broker, nodeID string, count int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.nodes[nodeID].State.LockCount = count
}

func setNodeEnabled(b *Broker, nodeID string, enabled bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.nodes[nodeID].State.AdminState = AdminState{Enabled: enabled, Epoch: 99}
}

// Test flow:
// 1. Register four nodes and, for forty session keys, acquire and release the node thirty times.
// 2. Assert every acquire of a key returns the node its first acquire returned.
// 3. Assert the forty keys together use more than one node.
// 4. Build a second broker with the same nodes and assert it maps every key to the same node.
func TestAffinitySession_SameKeyLandsOnSameNode(t *testing.T) {
	b := newAffinityTestBroker(t, 4, 2)

	homes := make(map[string]string)
	distinctNodes := make(map[string]struct{})
	for k := 0; k < 40; k++ {
		key := fmt.Sprintf("conversation-%d", k)
		homes[key] = acquireAndRelease(t, b, key)
		distinctNodes[homes[key]] = struct{}{}
		for i := 0; i < 30; i++ {
			require.Equal(t, homes[key], acquireAndRelease(t, b, key), "key %s moved on acquire %d", key, i)
		}
	}
	require.Greater(t, len(distinctNodes), 1)

	restarted := newAffinityTestBroker(t, 4, 2)
	for key, home := range homes {
		require.Equal(t, home, acquireAndRelease(t, restarted, key), "key %s differs after restart", key)
	}
}

func rankedNodeIDs(b *Broker, sessionKey string) []string {
	ids := make([]string, 0)
	for id := range lockCounts(b) {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool {
		si, sj := rendezvousScore(sessionKey, ids[i]), rendezvousScore(sessionKey, ids[j])
		if si != sj {
			return si > sj
		}
		return ids[i] < ids[j]
	})
	return ids
}

func spread(counts map[string]int) (lowest, highest, total int) {
	lowest = -1
	for _, c := range counts {
		if lowest < 0 || c < lowest {
			lowest = c
		}
		if c > highest {
			highest = c
		}
		total += c
	}
	return lowest, highest, total
}

// Test flow:
// 1. Register three nodes with three slots each and rank them for a key.
// 2. Fill the home node with three acquires of the key and assert each lock count rose by exactly one.
// 3. Give the runner-up a lock and acquire the key again with the home node full.
// 4. Assert the answer is the runner-up, not the idle third node, and the home count is unchanged.
// 5. Release one home lock and assert the key returns to its home node.
func TestAffinitySession_FullHomeOverflowsToNextRankedNode(t *testing.T) {
	b := newAffinityTestBroker(t, 3, 3)
	ranked := rankedNodeIDs(b, "busy-conversation")
	home, runnerUp := ranked[0], ranked[1]

	var homeLockID string
	for i := 1; i <= 3; i++ {
		lockID, nodeID := acquireNode(t, b, "busy-conversation")
		require.Equal(t, home, nodeID)
		require.Equal(t, i, lockCounts(b)[home])
		homeLockID = lockID
	}

	setLockCount(b, runnerUp, 1)

	_, overflowNodeID := acquireNode(t, b, "busy-conversation")
	require.Equal(t, runnerUp, overflowNodeID)
	require.Equal(t, 3, lockCounts(b)[home])

	require.NoError(t, b.ReleaseMLNode(homeLockID, InferenceSuccess{}))
	require.Equal(t, home, acquireAndRelease(t, b, "busy-conversation"))
}

// Test flow:
// 1. Register one node with one slot, lock it and assert a keyed acquire still answers ErrNoNodesAvailable.
func TestAffinitySession_NoCapacityStillRefusesWithoutQueueing(t *testing.T) {
	b := newAffinityTestBroker(t, 1, 1)
	acquireNode(t, b, "conversation")

	_, _, _, err := b.AcquireMLNode(context.Background(), affinityTestModel, nil, "conversation", nil)

	require.ErrorIs(t, err, ErrNoNodesAvailable)
}

func setReconcileInfo(b *Broker, nodeID string, info *ReconcileInfo) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.nodes[nodeID].State.ReconcileInfo = info
}

func setNodeStatus(b *Broker, nodeID string, status types.HardwareNodeStatus) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.nodes[nodeID].State.IntendedStatus = status
	b.nodes[nodeID].State.CurrentStatus = status
}

// Test flow:
// 1. Register four nodes with MaxConcurrent 500 and find the home node of a key.
// 2. Exclude the home node, disable it, set it reconciling, or move it out of the inference state, and in each case acquire the key several times.
// 3. Assert the answer is never the home node and is the same node every time.
// 4. Lift the exclusion or restore the node and assert the key returns to its home node.
func TestAffinitySession_MovesWhenHomeIsExcludedOrDisabled(t *testing.T) {
	cases := []struct {
		name  string
		start func(b *Broker, home string) []string
		end   func(b *Broker, home string)
	}{
		{
			name:  "excluded",
			start: func(_ *Broker, home string) []string { return []string{home} },
			end:   func(_ *Broker, _ string) {},
		},
		{
			name: "disabled",
			start: func(b *Broker, home string) []string {
				setNodeEnabled(b, home, false)
				return nil
			},
			end: func(b *Broker, home string) { setNodeEnabled(b, home, true) },
		},
		{
			name: "reconciling",
			start: func(b *Broker, home string) []string {
				setReconcileInfo(b, home, &ReconcileInfo{Status: types.HardwareNodeStatus_STOPPED})
				return nil
			},
			end: func(b *Broker, home string) { setReconcileInfo(b, home, nil) },
		},
		{
			name: "left inference state",
			start: func(b *Broker, home string) []string {
				setNodeStatus(b, home, types.HardwareNodeStatus_POC)
				return nil
			},
			end: func(b *Broker, home string) { setNodeStatus(b, home, types.HardwareNodeStatus_INFERENCE) },
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := newAffinityTestBroker(t, 4, 500)
			home := acquireAndRelease(t, b, "conversation-a")

			skip := tc.start(b, home)
			moved := acquireAndRelease(t, b, "conversation-a", skip...)
			require.NotEqual(t, home, moved)
			for i := 0; i < 5; i++ {
				require.Equal(t, moved, acquireAndRelease(t, b, "conversation-a", skip...))
			}

			tc.end(b, home)
			require.Equal(t, home, acquireAndRelease(t, b, "conversation-a"))
		})
	}
}

// Test flow:
// 1. Register four nodes with two slots each and acquire four times with an empty key without releasing.
// 2. Assert every node holds exactly one lock, the least-busy spread.
// 3. Release all, then acquire and release two hundred times with an empty key.
// 4. Assert more than one node was used, so ties are still broken without a key.
// 5. Acquire twice with one session key and assert both land on the same node, which then holds two locks.
func TestAffinitySession_EmptyKeyKeepsLeastBusyDistribution(t *testing.T) {
	b := newAffinityTestBroker(t, 4, 2)

	var lockIDs []string
	for i := 0; i < 4; i++ {
		lockID, _ := acquireNode(t, b, "")
		lockIDs = append(lockIDs, lockID)
	}
	for id, count := range lockCounts(b) {
		require.Equal(t, 1, count, "node %s", id)
	}
	for _, lockID := range lockIDs {
		require.NoError(t, b.ReleaseMLNode(lockID, InferenceSuccess{}))
	}

	usedNodes := make(map[string]struct{})
	for i := 0; i < 200; i++ {
		usedNodes[acquireAndRelease(t, b, "")] = struct{}{}
	}
	require.Greater(t, len(usedNodes), 1)

	_, first := acquireNode(t, b, "conversation")
	_, second := acquireNode(t, b, "conversation")
	require.Equal(t, first, second)
	require.Equal(t, 2, lockCounts(b)[first])
}

// Test flow:
// 1. Register four nodes with MaxConcurrent 500 and acquire one key two hundred times without releasing.
// 2. Assert the locks add up to two hundred and no node trails another by more than the lead plus one.
// 3. Assert every node took part, so one hot key does not sit on one node.
func TestAffinitySession_OneHotKeySpreadsAcrossNodes(t *testing.T) {
	b := newAffinityTestBroker(t, 4, 500)

	for i := 0; i < 200; i++ {
		acquireNode(t, b, "hot-conversation")
	}

	lowest, highest, total := spread(lockCounts(b))
	require.Equal(t, 200, total)
	require.LessOrEqual(t, highest-lowest, affinityMaxLead+1, "locks: %v", lockCounts(b))
	require.Greater(t, lowest, 0, "locks: %v", lockCounts(b))
}

// Test flow:
// 1. Register four nodes with MaxConcurrent 500 and start two hundred goroutines that each acquire one hot key without releasing.
// 2. Wait for all of them and assert every acquire succeeded.
// 3. Assert the locks add up to two hundred and no node trails another by more than the lead plus one.
func TestAffinitySession_ConcurrentAcquiresOfOneKeySpread(t *testing.T) {
	b := newAffinityTestBroker(t, 4, 500)

	var wg sync.WaitGroup
	errs := make(chan error, 200)
	for i := 0; i < 200; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _, _, err := b.AcquireMLNode(context.Background(), affinityTestModel, nil, "hot-conversation", nil)
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)

	for err := range errs {
		require.NoError(t, err)
	}
	lowest, highest, total := spread(lockCounts(b))
	require.Equal(t, 200, total)
	require.LessOrEqual(t, highest-lowest, affinityMaxLead+1, "locks: %v", lockCounts(b))
}

// Test flow:
// 1. Register four nodes with MaxConcurrent 500 and find the home node of forty session keys.
// 2. Acquire the keys in order for five rounds, keeping the last ten locks in flight and releasing the oldest.
// 3. Assert every acquire returned the key's home node.
func TestAffinitySession_OrdinarySessionsStayOnHomeNodes(t *testing.T) {
	b := newAffinityTestBroker(t, 4, 500)

	var keys, homes []string
	for k := 0; k < 40; k++ {
		keys = append(keys, fmt.Sprintf("conversation-%d", k))
		homes = append(homes, rankedNodeIDs(b, keys[k])[0])
	}
	var inFlight []string
	for i := 0; i < 5*len(keys); i++ {
		lockID, nodeID := acquireNode(t, b, keys[i%len(keys)])
		require.Equal(t, homes[i%len(keys)], nodeID, "acquire %d left the home node, locks: %v", i, lockCounts(b))
		inFlight = append(inFlight, lockID)
		if len(inFlight) > 10 {
			require.NoError(t, b.ReleaseMLNode(inFlight[0], InferenceSuccess{}))
			inFlight = inFlight[1:]
		}
	}
}

// Test flow:
// 1. Register four nodes with MaxConcurrent 500 and rank them for a key.
// 2. Put the lead on the home node and assert the key still lands on it.
// 3. Put one more lock on the home node and assert the key goes to the runner-up for nine acquires in a row.
// 4. Give the runner-up the same excess and assert the key goes to the third-ranked node.
func TestAffinitySession_OverflowFollowsRendezvousRanking(t *testing.T) {
	b := newAffinityTestBroker(t, 4, 500)
	ranked := rankedNodeIDs(b, "hot-conversation")

	setLockCount(b, ranked[0], affinityMaxLead)
	_, nodeID := acquireNode(t, b, "hot-conversation")
	require.Equal(t, ranked[0], nodeID)

	setLockCount(b, ranked[0], affinityMaxLead+1)
	for i := 0; i < affinityMaxLead+1; i++ {
		_, nodeID := acquireNode(t, b, "hot-conversation")
		require.Equal(t, ranked[1], nodeID, "acquire %d, locks: %v", i, lockCounts(b))
	}

	setLockCount(b, ranked[1], affinityMaxLead+1)
	_, nodeID = acquireNode(t, b, "hot-conversation")
	require.Equal(t, ranked[2], nodeID, "locks: %v", lockCounts(b))
}

// Test flow:
// 1. Register three nodes with MaxConcurrent 500, rank them for a key and give the two higher-ranked nodes twenty locks each.
// 2. Shrink the lowest-ranked node to one slot and fill it, so it is full with a lock count far below the others.
// 3. Acquire the key and assert the home node is still used, so a full node does not set the lead baseline.
func TestAffinitySession_FullNodeDoesNotSetLeadBaseline(t *testing.T) {
	b := newAffinityTestBroker(t, 3, 500)
	ranked := rankedNodeIDs(b, "conversation")
	setLockCount(b, ranked[0], 20)
	setLockCount(b, ranked[1], 20)
	b.mu.Lock()
	b.nodes[ranked[2]].Node.MaxConcurrent = 1
	b.nodes[ranked[2]].State.LockCount = 1
	b.mu.Unlock()

	_, nodeID := acquireNode(t, b, "conversation")

	require.Equal(t, ranked[0], nodeID)
}
