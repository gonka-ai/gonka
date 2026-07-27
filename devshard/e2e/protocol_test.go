package e2e

import (
	"testing"

	"github.com/stretchr/testify/require"

	"devshard/e2e/testutil"
)

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
