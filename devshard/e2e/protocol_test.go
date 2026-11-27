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
//  1. Send several non-streaming chat completion requests through the gateway.
//  2. Collect signatures for the latest session nonce through the escrow's signature collection route.
//  3. Assert the collection reached quorum for that nonce.
//  4. Read the escrow's signature status and assert it holds quorum through that nonce.
func TestE2E_ProtocolSignatureQuorumConvergence(t *testing.T) {
	env, client := startGatewayEnv(t, e2eEnvOptions{})
	testutil.SendCompletions(t, client, env.clientURL, "signature quorum", 3)

	latestNonce := gatewaySessionNonce(t, client, env.clientURL)
	require.NotZero(t, latestNonce, "completion requests should advance the session nonce")

	collection := testutil.CollectSignatures(t, client, env.escrowURL(), latestNonce)
	testutil.RequireSignatureCollectionQuorum(t, collection, latestNonce, len(testutil.HostPrivateKeys))

	afterCollection := testutil.GetSignatureStatus(t, client, env.escrowURL())
	testutil.RequireSignatureStatusQuorum(t, afterCollection, latestNonce, len(testutil.HostPrivateKeys))
}

// Test flow:
//  1. Send several non-streaming chat completion requests through the gateway.
//  2. Read the latest session nonce from the gateway.
//  3. Poll the E2E-only gossip diagnostic on every participant for the nonce before it.
//  4. Assert every participant observed the same signed state hash.
func TestE2E_ProtocolGossipConvergence(t *testing.T) {
	env, client := startGatewayEnv(t, e2eEnvOptions{})
	testutil.SendCompletions(t, client, env.clientURL, "gossip convergence", 3)

	latestNonce := gatewaySessionNonce(t, client, env.clientURL)
	require.Greater(t, latestNonce, uint64(1), "completion requests should advance the session nonce")
	convergedNonce := latestNonce - 1

	var statuses []testutil.GossipNonceStatus
	converged := assert.Eventually(t, func() bool {
		statuses = make([]testutil.GossipNonceStatus, len(env.hostControlURLs))
		for index, hostURL := range env.hostControlURLs {
			statuses[index] = testutil.GetGossipNonceStatus(t, client, hostURL, e2eHostRoutePrefix, convergedNonce)
			if !statuses[index].Seen {
				return false
			}
		}
		return true
	}, 10*time.Second, 250*time.Millisecond, "hosts should observe the gossiped nonce")
	for index, status := range statuses {
		t.Logf("host %d gossip status: nonce=%d seen=%t state_hash=%s sender_slot=%d",
			index, status.Nonce, status.Seen, status.StateHash, status.SenderSlot)
	}
	require.True(t, converged, "hosts should observe the gossiped nonce")

	testutil.RequireGossipNonceConvergence(t, statuses, convergedNonce)
}
