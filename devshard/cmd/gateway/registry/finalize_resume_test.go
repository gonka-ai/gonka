package registry

import (
	"context"
	"errors"
	"io"
	"sync/atomic"
	"testing"
	"time"

	"devshard/host"
	"devshard/internal/statetest"
	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/stub"
	"devshard/types"
	"devshard/user"
)

type cancellingClient struct {
	user.HostClient
	armed  *atomic.Bool
	cancel context.CancelFunc
}

func (c cancellingClient) Send(ctx context.Context, request host.HostRequest, stream io.Writer, onReceipt func(*host.HostResponse)) (*host.HostResponse, error) {
	if c.armed.Load() {
		c.cancel()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return c.HostClient.Send(ctx, request, stream, onReceipt)
}

type hashBreakingClient struct {
	user.HostClient
	armed *atomic.Bool
}

func (c hashBreakingClient) Send(ctx context.Context, request host.HostRequest, stream io.Writer, onReceipt func(*host.HostResponse)) (*host.HostResponse, error) {
	if c.armed.Load() {
		return &host.HostResponse{Nonce: request.Nonce, StateHash: []byte{0xff}}, nil
	}
	return c.HostClient.Send(ctx, request, stream, onReceipt)
}

func interruptedFinalizeSession(t *testing.T) (EscrowSession, *user.Session) {
	t.Helper()
	handle, session, err := finalizeCutShort(t, false)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Finalize = %v, want the cancelled context to cut it short", err)
	}
	if phase := handle.Phase(); phase != types.PhaseFinalizing {
		t.Fatalf("phase = %v, want finalizing: the fixture must leave a finalize cut short", phase)
	}
	return handle, session
}

func finalizeCutShort(t *testing.T, answerBrokenHash bool) (EscrowSession, *user.Session, error) {
	t.Helper()
	const escrowID = "escrow-1"
	hostSigners := []*signing.Secp256k1Signer{testutil.MustGenerateKey(t), testutil.MustGenerateKey(t), testutil.MustGenerateKey(t)}
	group := testutil.MakeGroup(hostSigners)
	config := testutil.DefaultConfig(len(group))
	creator := testutil.MustGenerateKey(t)
	verifier := signing.NewSecp256k1Verifier()

	armed := &atomic.Bool{}
	cutCtx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	clients := make([]user.HostClient, len(hostSigners))
	for index, signer := range hostSigners {
		machine := statetest.MustStateMachine(t, escrowID, config, group, 100_000, creator.Address(), verifier)
		hostNode, err := host.NewHost(machine, signer, stub.NewInferenceEngine(), escrowID, group, nil, host.WithGrace(100))
		if err != nil {
			t.Fatalf("NewHost = %v, want nil", err)
		}
		if answerBrokenHash {
			clients[index] = hashBreakingClient{HostClient: &user.InProcessClient{Host: hostNode}, armed: armed}
		} else {
			clients[index] = cancellingClient{HostClient: &user.InProcessClient{Host: hostNode}, armed: armed, cancel: cancel}
		}
	}
	machine := statetest.MustStateMachine(t, escrowID, config, group, 100_000, creator.Address(), verifier)
	session, err := user.NewSession(machine, creator, escrowID, group, clients, verifier, user.WithCollectRetry(1, time.Millisecond, time.Second))
	if err != nil {
		t.Fatalf("NewSession = %v, want nil", err)
	}
	t.Cleanup(func() { _ = session.Close() })
	if _, err := session.SendInference(context.Background(), user.InferenceParams{
		Model: "llama", Prompt: testutil.TestPrompt,
		InputLength: 100, MaxTokens: testutil.TestMaxTokens, StartedAt: 1000,
	}); err != nil {
		t.Fatalf("SendInference = %v, want nil", err)
	}

	armed.Store(true)
	finalizeErr := session.Finalize(cutCtx)
	armed.Store(false)
	return NewSessionHandle(session, machine), session, finalizeErr
}

// Test flow:
//  1. Build an interrupted finalize session via `interruptedFinalizeSession`, whose finalize was cut short by a cancelled context and left mid-finalizing.
//  2. Resume finalize on the session handle.
//  3. Assert it completes with no error and the phase reaches settlement.
//  4. Assert the session has a signature quorum at its own nonce.
func TestFinalizeResumesAFinalizeARestartCutShort(t *testing.T) {
	handle, session := interruptedFinalizeSession(t)

	if err := handle.Finalize(context.Background()); err != nil {
		t.Fatalf("Finalize = %v, want the interrupted rounds finished and the quorum collected", err)
	}

	if phase := handle.Phase(); phase != types.PhaseSettlement {
		t.Fatalf("phase = %v, want settlement", phase)
	}
	if !session.HasQuorumAt(session.Nonce()) {
		t.Fatalf("no signature quorum at nonce %d", session.Nonce())
	}
}

// Test flow:
//  1. Build an interrupted finalize session and share the chain's max nonce with it, as every phase snapshot does for a live escrow.
//  2. Resume finalize on the session handle.
//  3. Assert it completes and the phase reaches settlement: the nonce cap reserves the remaining nonces for finalizing, it must not refuse them.
func TestFinalizeResumesUnderTheChainNonceCap(t *testing.T) {
	handle, session := interruptedFinalizeSession(t)
	session.SetChainMaxNonce(1700)

	if err := handle.Finalize(context.Background()); err != nil {
		t.Fatalf("Finalize = %v, want the interrupted rounds finished under the nonce cap", err)
	}

	if phase := handle.Phase(); phase != types.PhaseSettlement {
		t.Fatalf("phase = %v, want settlement", phase)
	}
}

// Test flow:
//  1. Build a finalize session via `finalizeCutShort` whose hosts answer a broken state hash: v7 skips each rejected round, so the finalize runs to settlement without a quorum and fails.
//  2. Assert that first finalize failed and left the phase at settlement with no signature quorum.
//  3. Resume finalize on the session handle with the hosts answering honestly again.
//  4. Assert it completes with no error and the session has a signature quorum at its own nonce.
func TestFinalizeResumesAfterAHostAnsweredABrokenHash(t *testing.T) {
	handle, session, firstErr := finalizeCutShort(t, true)
	if firstErr == nil {
		t.Fatal("first Finalize = nil, want the broken hashes to leave it without a quorum")
	}
	if phase := handle.Phase(); phase != types.PhaseSettlement {
		t.Fatalf("phase = %v, want settlement: v7 skips the rejected rounds", phase)
	}
	if session.HasQuorumAt(session.Nonce()) {
		t.Fatalf("signature quorum at nonce %d before the resume, want none", session.Nonce())
	}

	if err := handle.Finalize(context.Background()); err != nil {
		t.Fatalf("Finalize = %v, want the quorum collected from the recovered hosts", err)
	}

	if !session.HasQuorumAt(session.Nonce()) {
		t.Fatalf("no signature quorum at nonce %d", session.Nonce())
	}
}
