package user

import (
	"context"
	"io"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"devshard/host"
	"devshard/internal/testutil"
	"devshard/types"
)

type cancellableClient struct {
	inner      HostClient
	beforeSend func()
}

func (c cancellableClient) Send(ctx context.Context, req host.HostRequest, stream io.Writer, onReceipt func(*host.HostResponse)) (*host.HostResponse, error) {
	if c.beforeSend != nil {
		c.beforeSend()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return c.inner.Send(ctx, req, stream, onReceipt)
}

func (c cancellableClient) GetSignatures(ctx context.Context, nonce uint64) (map[uint32][]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if fetcher, ok := c.inner.(SignatureFetcher); ok {
		return fetcher.GetSignatures(ctx, nonce)
	}
	return nil, nil
}

func sessionWithCancellableClients(t *testing.T, beforeSend func()) *Session {
	t.Helper()
	session, _, _ := setupSession(t, 3, 100000, 100)
	params := InferenceParams{Model: "llama", Prompt: testutil.TestPrompt, InputLength: 100, MaxTokens: testutil.TestMaxTokens, StartedAt: 1000}
	for range 3 {
		_, err := session.SendInference(context.Background(), params)
		require.NoError(t, err)
	}
	for i := range session.clients {
		session.clients[i] = cancellableClient{inner: session.clients[i], beforeSend: beforeSend}
	}
	return session
}

// Test flow:
//  1. Build a session with three in-process hosts whose clients fail a send on a cancelled context, as HTTP does, and send three inferences.
//  2. Finalize it on a context that is already cancelled.
//  3. Assert Finalize returns the context's error and the session kept its nonce and its active phase.
func TestAFinalizeOnACancelledContextCommitsNothing(t *testing.T) {
	session := sessionWithCancellableClients(t, nil)
	nonce := session.Nonce()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := session.Finalize(ctx)

	require.ErrorIs(t, err, context.Canceled)
	require.Equal(t, nonce, session.Nonce())
	require.Equal(t, types.PhaseActive, session.StateMachine().Phase())
}

// Test flow:
//  1. Build a session with three in-process hosts whose clients fail a send on a cancelled context, and arm them to cancel the finalize's context as its first round is sent.
//  2. Finalize it; assert it returns the context's error with the session left finalizing, one nonce on.
//  3. Finalize it again on a live context; assert it resumes, reaches settlement and holds a signature quorum at its nonce.
func TestAFinalizeACancelCutShortResumesOnTheNextFinalize(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var once sync.Once
	session := sessionWithCancellableClients(t, func() { once.Do(cancel) })
	nonce := session.Nonce()

	err := session.Finalize(ctx)

	require.ErrorIs(t, err, context.Canceled)
	require.Equal(t, types.PhaseFinalizing, session.StateMachine().Phase())
	require.Equal(t, nonce+1, session.Nonce())
	require.NoError(t, session.Finalize(context.Background()))
	require.Equal(t, types.PhaseSettlement, session.StateMachine().Phase())
	require.True(t, session.HasQuorumAt(session.Nonce()))
}
