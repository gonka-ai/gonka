package e2e

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"devshard/e2e/testutil"
)

// e2e host images are built with DEVSHARD_E2E_VERSION=dev.
const e2eHostRoutePrefix = "/devshard/dev"

// Test flow:
//  1. Send several non-streaming chat completion requests through devshardctl.
//  2. Read the signature accumulation status from /v1/debug/signatures.
//  3. Collect signatures for the latest session nonce through
//     /v1/debug/signatures/collect.
//  4. Assert the latest nonce reaches quorum in both collection and status
//     responses.
func TestE2E_ProtocolSignatureQuorumConvergence(t *testing.T) {
	env, client := startNonStreamingEnv(t)
	testutil.SendCompletions(t, client, env.clientURL, "signature quorum", 3)

	// Heartbeats can raise LatestSessionNonce between reads. Wait until a
	// collect for some nonce at or past the post-completion tip reports quorum.
	trafficNonce := testutil.LatestSessionNonce(t, client, env.clientURL)
	require.NotZero(t, trafficNonce, "completion requests should advance the session nonce")

	var (
		status      map[string]any
		collection  map[string]any
		quorumNonce uint64
	)
	require.Eventually(t, func() bool {
		status = testutil.GetSignatureStatus(t, client, env.clientURL)
		if testutil.NumericField(t, status, "total_slots") != uint64(len(testutil.HostPrivateKeys)) {
			return false
		}
		quorumNonce = testutil.NumericField(t, status, "highest_quorum_nonce")
		if quorumNonce < trafficNonce {
			quorumNonce = testutil.NumericField(t, status, "current_nonce")
		}
		if quorumNonce < trafficNonce {
			return false
		}
		collection = testutil.CollectSignatures(t, client, env.clientURL, quorumNonce)
		threshold := testutil.NumericField(t, collection, "quorum_threshold")
		weight := testutil.NumericField(t, collection, "sig_weight")
		hasQuorum, ok := collection["has_quorum"].(bool)
		return ok && hasQuorum && weight >= threshold
	}, 45*time.Second, 500*time.Millisecond, "a nonce at or past traffic should reach signature quorum")
	testutil.RequireSignatureCollectionQuorum(t, collection, quorumNonce, len(testutil.HostPrivateKeys))
	_ = status
}

// Test flow:
//  1. Send several non-streaming chat completion requests through devshardctl.
//  2. Read the latest session nonce from devshardctl.
//  3. Poll the E2E-only gossip diagnostic on every participant for that nonce.
//  4. Assert every participant observed the same signed state hash.
func TestE2E_ProtocolGossipConvergence(t *testing.T) {
	env, client := startNonStreamingEnv(t)
	testutil.SendCompletions(t, client, env.clientURL, "gossip convergence", 3)

	latestNonce := testutil.LatestSessionNonce(t, client, env.clientURL)
	require.Greater(t, latestNonce, uint64(1), "completion requests should advance the session nonce")
	// devshardctl applies the newest diff after sending a request to hosts. The
	// preceding nonce is therefore the latest fully dispatched diff to inspect.
	convergedNonce := latestNonce - 1

	var statuses []testutil.GossipNonceStatus
	converged := assert.Eventually(t, func() bool {
		statuses = make([]testutil.GossipNonceStatus, len(env.hostControlURLs))
		for i, hostURL := range env.hostControlURLs {
			statuses[i] = testutil.GetGossipNonceStatus(t, client, hostURL, e2eHostRoutePrefix, convergedNonce)
			if !statuses[i].Seen {
				return false
			}
		}
		return true
	}, 10*time.Second, 250*time.Millisecond, "hosts should observe the gossiped nonce")
	for i, status := range statuses {
		t.Logf("host %d gossip status: nonce=%d seen=%t state_hash=%s sender_slot=%d",
			i, status.Nonce, status.Seen, status.StateHash, status.SenderSlot)
	}
	require.True(t, converged, "hosts should observe the gossiped nonce")

	testutil.RequireGossipNonceConvergence(t, statuses, convergedNonce)
}
