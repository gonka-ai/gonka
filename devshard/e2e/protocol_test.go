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

	latestNonce := testutil.LatestSessionNonce(t, client, env.clientURL)
	require.NotZero(t, latestNonce, "completion requests should advance the session nonce")

	beforeCollection := testutil.GetSignatureStatus(t, client, env.clientURL)
	require.Equal(t, latestNonce, testutil.NumericField(t, beforeCollection, "current_nonce"))
	require.Equal(t, uint64(len(testutil.HostPrivateKeys)), testutil.NumericField(t, beforeCollection, "total_slots"))

	collection := testutil.CollectSignatures(t, client, env.clientURL, latestNonce)
	testutil.RequireSignatureCollectionQuorum(t, collection, latestNonce, len(testutil.HostPrivateKeys))

	afterCollection := testutil.GetSignatureStatus(t, client, env.clientURL)
	testutil.RequireSignatureStatusQuorum(t, afterCollection, latestNonce, len(testutil.HostPrivateKeys))
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
