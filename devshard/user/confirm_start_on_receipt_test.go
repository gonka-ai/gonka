package user

import (
	"context"
	"io"
	"testing"

	"devshard/host"
	"devshard/internal/testutil"
	"devshard/types"

	"github.com/stretchr/testify/require"
)

// holdingClient answers the receipt and then blocks, the way a host that keeps an empty stream open
// does. The gateway's other nonces pass while it holds, which is exactly when the receipt must already
// be queued: it rides the next diff, and after the last one there is none left to ride.
type holdingClient struct {
	receiptSent chan struct{}
	release     chan struct{}
	receipt     []byte
}

func (c *holdingClient) Send(_ context.Context, _ host.HostRequest, _ io.Writer, receiptHandler func(*host.HostResponse)) (*host.HostResponse, error) {
	response := &host.HostResponse{Receipt: c.receipt, ConfirmedAt: 1000}
	if receiptHandler != nil {
		receiptHandler(response)
	}
	close(c.receiptSent)
	<-c.release
	return response, nil
}

func (c *holdingClient) VerifyTimeout(context.Context, uint64, types.TimeoutReason, *host.InferencePayload, []types.Diff) (bool, []byte, uint32, error) {
	return false, nil, 0, nil
}

func (c *holdingClient) ChallengeReceipt(context.Context, uint64, *host.InferencePayload, []types.Diff) ([]byte, error) {
	return nil, nil
}

func pendingConfirmStarts(session *Session) []uint64 {
	var confirmed []uint64
	for _, tx := range session.PendingTxs() {
		if inner := tx.GetConfirmStart(); inner != nil {
			confirmed = append(confirmed, inner.InferenceId)
		}
	}
	return confirmed
}

func TestTheExecutorReceiptIsQueuedWhileTheStreamIsStillOpen(t *testing.T) {
	session, _, _ := setupSession(t, 3, 100000, 10)
	holding := &holdingClient{receiptSent: make(chan struct{}), release: make(chan struct{}), receipt: []byte("receipt")}
	session.clients[1] = holding

	prepared, err := session.PrepareInference(InferenceParams{
		Model: "llama", Prompt: testutil.TestPrompt,
		InputLength: 100, MaxTokens: testutil.TestMaxTokens, StartedAt: 1000,
	})
	require.NoError(t, err)
	require.Equal(t, 1, prepared.HostIdx())

	sent := make(chan struct{})
	go func() {
		defer close(sent)
		_, _ = session.SendOnly(context.Background(), prepared, nil, nil)
	}()
	<-holding.receiptSent

	require.Equal(t, []uint64{prepared.Nonce()}, pendingConfirmStarts(session),
		"the receipt must ride the next diff, and a host holding the stream can outlast every diff left")

	close(holding.release)
	<-sent
}

func TestAResponseWithoutAReceiptQueuesNothing(t *testing.T) {
	session, _, _ := setupSession(t, 3, 100000, 10)

	session.confirmStartOnReceipt(1, &host.HostResponse{})
	session.confirmStartOnReceipt(1, nil)

	require.Empty(t, pendingConfirmStarts(session))
}
