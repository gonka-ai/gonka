package broker

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"decentralized-api/apiconfig"

	"github.com/stretchr/testify/require"
)

func affinityNodeConfig(i int) apiconfig.InferenceNodeConfig {
	return apiconfig.InferenceNodeConfig{
		Id: fmt.Sprintf("node-%d", i), Host: "localhost", InferencePort: 8080 + i,
		InferenceSegment: "/v1", PoCPort: 9080 + i,
		Models:        map[string]apiconfig.ModelConfig{affinityTestModel: {}},
		MaxConcurrent: 500,
	}
}

func acquireWithPrefix(t *testing.T, b *Broker, sessionKey string, hashes []uint64, skip ...string) (lockID, nodeID string) {
	t.Helper()
	lockID, _, nodeID, err := b.AcquireMLNode(context.Background(), affinityTestModel, skip, sessionKey, hashes)
	require.NoError(t, err)
	return lockID, nodeID
}

func acquireWithPrefixAndRelease(t *testing.T, b *Broker, sessionKey string, hashes []uint64, skip ...string) string {
	t.Helper()
	lockID, nodeID := acquireWithPrefix(t, b, sessionKey, hashes, skip...)
	require.NoError(t, b.ReleaseMLNode(lockID, InferenceSuccess{}))
	_, err := b.GetNodes()
	require.NoError(t, err)
	return nodeID
}

func hashRange(from, to uint64) []uint64 {
	hashes := make([]uint64, 0, to-from+1)
	for h := from; h <= to; h++ {
		hashes = append(hashes, h)
	}
	return hashes
}

func indexedPrefix(b *Broker, nodeID string, hashes []uint64) int {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.nodes[nodeID].prefixes.matchedPrefix(hashes)
}

func hasIndex(b *Broker, nodeID string) bool {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.nodes[nodeID].prefixes != nil
}

func seedPrefixHolders(t *testing.T, b *Broker, key string, ranked []string) {
	t.Helper()
	require.Equal(t, ranked[1], acquireWithPrefixAndRelease(t, b, key, hashRange(1, 4), ranked[0]))
	require.Equal(t, ranked[2], acquireWithPrefixAndRelease(t, b, key, hashRange(1, 2), ranked[0], ranked[1]))
}

// Test flow:
// 1. Register four nodes, rank them for ten keys and acquire each key once with nil hashes and once with an empty slice.
// 2. Assert every acquire lands on the top-ranked node and no node got an index.
// 3. Acquire without a key and without hashes and assert the node with the fewest locks is chosen.
func TestPrefixRouting_NoHashesKeepsRendezvousRanking(t *testing.T) {
	b := newAffinityTestBroker(t, 4, 500)

	for k := 0; k < 10; k++ {
		key := fmt.Sprintf("conversation-%d", k)
		ranked := rankedNodeIDs(b, key)
		require.Equal(t, ranked[0], acquireWithPrefixAndRelease(t, b, key, nil))
		require.Equal(t, ranked[0], acquireWithPrefixAndRelease(t, b, key, []uint64{}))
	}
	for id := range lockCounts(b) {
		require.False(t, hasIndex(b, id), "node %s", id)
	}

	setLockCount(b, "node-0", 3)
	setLockCount(b, "node-1", 3)
	setLockCount(b, "node-2", 3)
	_, nodeID := acquireWithPrefix(t, b, "", nil)
	require.Equal(t, "node-3", nodeID)
}

// Test flow:
// 1. Register four nodes, rank them for a key and route two requests with skip lists so the second-ranked node holds hashes 1-4 and the third-ranked node holds hashes 1-2.
// 2. Acquire the key with hashes 1-4 and assert the second-ranked node wins over the top-ranked node.
// 3. Acquire the key with hashes 1-2 and assert the tie on the run length falls back to the rendezvous order between the two holders.
// 4. Acquire the key with a first hash nobody holds followed by hashes 2-4 and assert the run is zero everywhere, so the top-ranked node wins.
func TestPrefixRouting_LongestLeadingRunWinsInsideTheBound(t *testing.T) {
	b := newAffinityTestBroker(t, 4, 500)
	ranked := rankedNodeIDs(b, "conversation")
	seedPrefixHolders(t, b, "conversation", ranked)

	require.Equal(t, ranked[1], acquireWithPrefixAndRelease(t, b, "conversation", hashRange(1, 4)))
	require.Equal(t, ranked[1], acquireWithPrefixAndRelease(t, b, "conversation", hashRange(1, 2)))
	require.Equal(t, ranked[0], acquireWithPrefixAndRelease(t, b, "conversation", []uint64{99, 2, 3, 4}))
}

// Test flow:
// 1. Register four nodes, rank them for a key and seed only the second-ranked node with hashes 1-8.
// 2. Give the second-ranked node affinityMaxLead+1 locks while the others hold none.
// 3. Acquire the key with hashes 1-4 and assert the second-ranked node is not chosen and the top-ranked node is, which then holds hashes 1-4.
// 4. Lower the second-ranked node's locks to exactly affinityMaxLead, acquire with hashes 1-8 and assert it is chosen again.
func TestPrefixRouting_NodeOverTheBoundIsNotChosenForItsPrefix(t *testing.T) {
	b := newAffinityTestBroker(t, 4, 500)
	ranked := rankedNodeIDs(b, "conversation")
	require.Equal(t, ranked[1], acquireWithPrefixAndRelease(t, b, "conversation", hashRange(1, 8), ranked[0]))

	setLockCount(b, ranked[1], affinityMaxLead+1)
	require.Equal(t, ranked[0], acquireWithPrefixAndRelease(t, b, "conversation", hashRange(1, 4)))

	setLockCount(b, ranked[1], affinityMaxLead)
	require.Equal(t, ranked[1], acquireWithPrefixAndRelease(t, b, "conversation", hashRange(1, 8)))
}

// Test flow:
// 1. Register four nodes, seed one node with hashes 1-4 and give it two locks while the others hold none.
// 2. Acquire without a session key and with hashes 1-4 and assert the holder is chosen.
// 3. Acquire without a session key and with hashes nobody holds and assert the node with the fewest locks is chosen, not the lowest id.
func TestPrefixRouting_HashesWithoutSessionKey(t *testing.T) {
	b := newAffinityTestBroker(t, 4, 500)
	require.Equal(t, "node-1", acquireWithPrefixAndRelease(t, b, "", hashRange(1, 4), "node-0", "node-2", "node-3"))
	setLockCount(b, "node-1", 2)
	setLockCount(b, "node-0", 1)
	setLockCount(b, "node-2", 1)

	require.Equal(t, "node-1", acquireWithPrefixAndRelease(t, b, "", hashRange(1, 4)))
	setLockCount(b, "node-1", 2)
	require.Equal(t, "node-3", acquireWithPrefixAndRelease(t, b, "", hashRange(50, 53)))
}

// Test flow:
// 1. Register two nodes and acquire a key with hashes 1-4 and skip the top-ranked node.
// 2. Assert only the chosen node indexed all four hashes and the other node has no index.
// 3. Acquire without hashes and assert no hash was added to any index.
func TestPrefixRouting_IndexIsFedByTheBrokersOwnChoice(t *testing.T) {
	b := newAffinityTestBroker(t, 2, 500)
	ranked := rankedNodeIDs(b, "conversation")

	require.Equal(t, ranked[1], acquireWithPrefixAndRelease(t, b, "conversation", hashRange(1, 4), ranked[0]))
	require.Equal(t, 4, indexedPrefix(b, ranked[1], hashRange(1, 4)))
	require.False(t, hasIndex(b, ranked[0]))

	acquireWithPrefixAndRelease(t, b, "conversation", nil, ranked[0])
	require.Equal(t, 0, indexedPrefix(b, ranked[1], hashRange(5, 8)))
}

// Test flow:
// 1. Register two nodes and acquire a key with 513 hashes and assert no node indexed any of them and the top-ranked node was chosen.
// 2. Acquire the key with exactly 512 hashes and skip the top-ranked node and assert the chosen node indexed all 512.
func TestPrefixRouting_MoreThanMaxHashesAreIgnored(t *testing.T) {
	b := newAffinityTestBroker(t, 2, 500)
	ranked := rankedNodeIDs(b, "conversation")

	require.Equal(t, ranked[0], acquireWithPrefixAndRelease(t, b, "conversation", hashRange(1, maxPrefixHashes+1)))
	require.False(t, hasIndex(b, ranked[0]))
	require.False(t, hasIndex(b, ranked[1]))

	require.Equal(t, ranked[1], acquireWithPrefixAndRelease(t, b, "conversation", hashRange(1, maxPrefixHashes), ranked[0]))
	require.Equal(t, maxPrefixHashes, indexedPrefix(b, ranked[1], hashRange(1, maxPrefixHashes)))
}

// Test flow:
// 1. Build an index of capacity three and add the chain 1, 2, 3, then add hash 9.
// 2. Assert the tail of the chain (3) was evicted while 1 and 2 remain, and that matchedPrefix counts only the leading run.
// 3. Look hash 2 up, add hash 8 and assert 2 was evicted, so a lookup does not refresh recency.
// 4. Add hash 1 again, then hash 7, and assert the refreshed 1 stayed while 9 was evicted.
// 5. Build an index with the default capacity, add one chain of 40000 hashes and assert it holds exactly prefixIndexCapacity, the root kept and the tail gone.
func TestPrefixIndex_EvictsLeastRecentlyUsedAtCapacity(t *testing.T) {
	idx := newPrefixIndex(3)
	idx.add([]uint64{1, 2, 3})
	idx.add([]uint64{9})

	require.Equal(t, 0, idx.matchedPrefix([]uint64{3}))
	require.Equal(t, 2, idx.matchedPrefix([]uint64{1, 2, 3}))
	idx.matchedPrefix([]uint64{2})
	idx.add([]uint64{8})
	require.Equal(t, 0, idx.matchedPrefix([]uint64{2}))
	require.Equal(t, 1, idx.matchedPrefix([]uint64{1}))

	idx.add([]uint64{1})
	idx.add([]uint64{7})
	require.Equal(t, 1, idx.matchedPrefix([]uint64{1}))
	require.Equal(t, 0, idx.matchedPrefix([]uint64{9}))
	require.Equal(t, 3, idx.order.Len())

	full := newPrefixIndex(prefixIndexCapacity)
	full.add(hashRange(1, 40000))
	require.Equal(t, prefixIndexCapacity, full.order.Len())
	require.Equal(t, prefixIndexCapacity, len(full.entries))
	require.Equal(t, prefixIndexCapacity, full.matchedPrefix(hashRange(1, 40000)))
	require.Equal(t, 0, full.matchedPrefix([]uint64{40000}))
}

// Test flow:
// 1. Register two nodes and seed the second-ranked node with hashes 1-4.
// 2. Remove that node, register the same id again and assert its index is gone and the key's top-ranked node wins for hashes 1-4.
// 3. Seed it again, update the node with a different inference port and assert its index is dropped.
func TestPrefixRouting_RemovedOrMovedNodeLosesItsIndex(t *testing.T) {
	b := newAffinityTestBroker(t, 2, 500)
	ranked := rankedNodeIDs(b, "conversation")
	require.Equal(t, ranked[1], acquireWithPrefixAndRelease(t, b, "conversation", hashRange(1, 4), ranked[0]))
	require.True(t, hasIndex(b, ranked[1]))

	remove := make(chan bool, 2)
	queueMessage(t, b, RemoveNode{NodeId: ranked[1], Response: remove})
	select {
	case existed := <-remove:
		require.True(t, existed)
	case <-time.After(2 * time.Second):
		t.Fatal("RemoveNode did not answer within 2s")
	}
	var idx int
	_, err := fmt.Sscanf(ranked[1], "node-%d", &idx)
	require.NoError(t, err)
	registerInferenceNode(t, b, affinityNodeConfig(idx))
	require.False(t, hasIndex(b, ranked[1]))
	require.Equal(t, ranked[0], acquireWithPrefixAndRelease(t, b, "conversation", hashRange(1, 4)))

	require.Equal(t, ranked[1], acquireWithPrefixAndRelease(t, b, "conversation", hashRange(1, 4), ranked[0]))
	require.True(t, hasIndex(b, ranked[1]))
	moved := affinityNodeConfig(idx)
	moved.InferencePort = 7777
	update := NewUpdateNodeCommand(moved)
	queueMessage(t, b, update)
	select {
	case out := <-update.Response:
		require.Nil(t, out.Error)
	case <-time.After(2 * time.Second):
		t.Fatal("UpdateNode did not answer within 2s")
	}
	require.False(t, hasIndex(b, ranked[1]))
}

// Test flow:
// 1. For each of a positive value, unset, empty, zero, negative, non-numeric and fractional INFERENCE_PREFIX_INDEX_CAPACITY, set the variable with t.Setenv (or unset it) before building the broker.
// 2. Acquire a node with a hash list so the broker creates the node's prefix index.
// 3. Assert the index capacity is the variable's value when it is a positive integer and prefixIndexCapacity (31250) otherwise.
func TestPrefixRouting_IndexCapacityComesFromEnv(t *testing.T) {
	cases := []struct {
		name  string
		value *string
		want  int
	}{
		{"positive", strPtr("77"), 77},
		{"positive with spaces", strPtr(" 12 "), 12},
		{"unset", nil, 31250},
		{"empty", strPtr(""), 31250},
		{"zero", strPtr("0"), 31250},
		{"negative", strPtr("-5"), 31250},
		{"non-numeric", strPtr("many"), 31250},
		{"fractional", strPtr("1.5"), 31250},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.value != nil {
				t.Setenv("INFERENCE_PREFIX_INDEX_CAPACITY", *tc.value)
			} else {
				t.Setenv("INFERENCE_PREFIX_INDEX_CAPACITY", "")
				require.NoError(t, os.Unsetenv("INFERENCE_PREFIX_INDEX_CAPACITY"))
			}
			b := newAffinityTestBroker(t, 1, 500)

			nodeID := acquireWithPrefixAndRelease(t, b, "", hashRange(1, 2))

			b.mu.RLock()
			defer b.mu.RUnlock()
			require.Equal(t, tc.want, b.nodes[nodeID].prefixes.capacity)
		})
	}
}

func strPtr(s string) *string { return &s }
