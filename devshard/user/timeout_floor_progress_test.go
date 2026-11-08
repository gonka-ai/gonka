package user

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/types"
)

func TestExecutionTimeoutAdvancesTheFloorWithHeartbeatsOff(t *testing.T) {
	saved := TimeoutBuffer
	TimeoutBuffer = 10 * time.Millisecond
	t.Cleanup(func() { TimeoutBuffer = saved })

	signers := []*signing.Secp256k1Signer{testutil.MustGenerateKey(t), testutil.MustGenerateKey(t), testutil.MustGenerateKey(t)}
	group := testutil.MakeGroup(signers)
	config := testutil.DefaultConfig(3)
	config.ExecutionTimeout = 1
	session, _, rig := armedSessionFor(t, group, signers, config, 100000, 10, WithDisableHeightSyncHeartbeat(true))

	prepared, err := session.PrepareInference(context.Background(), InferenceParams{
		Model: "llama", Prompt: testutil.TestPrompt, InputLength: 100, MaxTokens: testutil.TestMaxTokens,
	})
	require.NoError(t, err)
	nonce := prepared.Nonce()
	resp, _ := stampedReceiptResponse(t, session, signers[prepared.HostIdx()], nonce)
	require.NoError(t, session.ProcessResponse(prepared.HostIdx(), resp, nonce))
	require.NoError(t, session.SendPendingDiff(context.Background()))
	rec, ok := session.StateMachine().GetInference(nonce)
	require.True(t, ok)
	require.Equal(t, types.StatusStarted, rec.Status)
	require.NotZero(t, rec.ConfirmedAtHeight)

	for i := range session.clients {
		session.clients[i] = &verifyingHost{HostClient: session.clients[i], mockTimeoutVerifier: &mockTimeoutVerifier{
			accept: true, signer: signers[i], group: group, slotIdx: i,
		}}
	}
	rig.set(rec.ConfirmedAtHeight + 50)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	result, _ := session.HandleTimeout(ctx, nonce, time.Now(), prepared.Payload())
	require.True(t, result.Applied, "outcome=%s detail=%s", result.Outcome, result.DetailReason)
	rec, _ = session.StateMachine().GetInference(nonce)
	require.Equal(t, types.StatusTimedOut, rec.Status)
}

type verifyingHost struct {
	HostClient
	*mockTimeoutVerifier
}
