package user

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"devshard/host"
)

// A verifier recomputes the refusal deadline from the record's own StartedAt, so a round raised before
// that deadline is rejected by every verifier it asks. Raising it anyway spends a whole serialized round
// on a vote that cannot pass.
func TestARefusalNoVerifierCanAcceptIsNotRaised(t *testing.T) {
	session, _, _ := setupSession(t, 3, 1_000_000, 0)
	nonce := prepareNonce(t, session)
	// The gateway's own deadline has passed, but the record says the host was asked a moment ago.
	sendTime := time.Now().Add(-time.Hour)
	payload := &host.InferencePayload{StartedAt: time.Now().Unix()}

	result, err := session.HandleTimeout(context.Background(), nonce, sendTime, payload)

	require.Error(t, err)
	require.Equal(t, "skipped", result.Outcome)
	require.Equal(t, "refusal_deadline_unreachable", result.DetailReason)
}

// The same round once the record's own deadline has passed is the one every verifier can accept.
func TestARefusalPastTheRecordsDeadlineIsRaised(t *testing.T) {
	session, _, _ := setupSession(t, 3, 1_000_000, 0)
	nonce := prepareNonce(t, session)
	sendTime := time.Now().Add(-time.Hour)
	payload := &host.InferencePayload{StartedAt: time.Now().Add(-time.Hour).Unix()}

	result, _ := session.HandleTimeout(context.Background(), nonce, sendTime, payload)

	require.NotEqual(t, "refusal_deadline_unreachable", result.DetailReason)
}
