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

// stateReceiptClient answers a receipt that carries the host's applied nonce and state root, then holds the stream open.
type stateReceiptClient struct {
	holdingClient
	appliedNonce uint64
	stateHash    []byte
}

func (c *stateReceiptClient) Send(_ context.Context, _ host.HostRequest, _ io.Writer, receiptHandler func(*host.HostResponse)) (*host.HostResponse, error) {
	response := &host.HostResponse{Nonce: c.appliedNonce, StateHash: c.stateHash, Receipt: c.receipt, ConfirmedAt: 1000}
	if receiptHandler != nil {
		receiptHandler(response)
	}
	close(c.receiptSent)
	<-c.release
	return response, nil
}

func (c *stateReceiptClient) VerifyTimeout(context.Context, uint64, types.TimeoutReason, *host.InferencePayload, []types.Diff, host.TimeoutArtifacts) (bool, []byte, uint32, []*types.DevshardTx, string, error) {
	return false, nil, 0, nil, "", nil
}

func receiptCursorSession(t *testing.T) (*Session, *PreparedInference, *stateReceiptClient) {
	t.Helper()
	session, hosts, _ := setupSession(t, 3, 100000, 10)
	prepared, err := session.PrepareInference(context.Background(), InferenceParams{
		Model: "llama", Prompt: testutil.TestPrompt,
		InputLength: 100, MaxTokens: testutil.TestMaxTokens, StartedAt: 1000,
	})
	require.NoError(t, err)
	session.mu.Lock()
	root, err := session.expectedStateRootLocked(prepared.Nonce())
	session.hostSyncNonce[prepared.HostIdx()] = 0
	session.mu.Unlock()
	require.NoError(t, err)
	client := &stateReceiptClient{
		holdingClient: holdingClient{receiptSent: make(chan struct{}), release: make(chan struct{}), receipt: signedExecutorReceipt(t, session, hosts, prepared.Nonce(), 1000)},
		appliedNonce:  prepared.Nonce(),
		stateHash:     root,
	}
	session.clients[prepared.HostIdx()] = client
	return session, prepared, client
}

func cursorWhileStreamOpen(t *testing.T, session *Session, prepared *PreparedInference, client *stateReceiptClient) uint64 {
	t.Helper()
	sent := make(chan struct{})
	go func() {
		defer close(sent)
		_, _ = session.SendOnly(context.Background(), prepared, nil, nil)
	}()
	<-client.receiptSent
	session.mu.Lock()
	cursor := session.hostSyncNonce[prepared.HostIdx()]
	session.mu.Unlock()
	close(client.release)
	<-sent
	return cursor
}

// Test flow:
//  1. Prepare a nonce, put its host's catch-up cursor back to 0, and give it a client whose receipt carries the nonce it applied and the matching state root, then holds the stream open.
//  2. Send, and read the host's cursor while the stream is still open.
//  3. Assert the cursor already stands at the nonce: the receipt proves the host applied it, so the next request to that host carries no diff it already holds, however long this stream runs.
func TestAReceiptWithAMatchingStateRootMovesTheHostCursor(t *testing.T) {
	session, prepared, client := receiptCursorSession(t)

	cursor := cursorWhileStreamOpen(t, session, prepared, client)

	require.Equal(t, prepared.Nonce(), cursor)
}

// Test flow:
//  1. Prepare the same receipt, but with a state root that does not match the session's.
//  2. Send, and read the host's cursor while the stream is still open.
//  3. Assert the cursor has not moved: only a root the session can check vouches for what the host applied.
func TestAReceiptWithAForeignStateRootLeavesTheHostCursor(t *testing.T) {
	session, prepared, client := receiptCursorSession(t)
	client.stateHash = make([]byte, len(client.stateHash))

	cursor := cursorWhileStreamOpen(t, session, prepared, client)

	require.Zero(t, cursor)
}
