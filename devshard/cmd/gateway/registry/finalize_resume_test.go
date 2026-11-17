package registry

import (
	"context"
	"io"
	"sync/atomic"
	"testing"

	"devshard/host"
	"devshard/internal/statetest"
	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/stub"
	"devshard/types"
	"devshard/user"
)

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
	const escrowID = "escrow-1"
	hostSigners := []*signing.Secp256k1Signer{testutil.MustGenerateKey(t), testutil.MustGenerateKey(t), testutil.MustGenerateKey(t)}
	group := testutil.MakeGroup(hostSigners)
	config := testutil.DefaultConfig(len(group))
	creator := testutil.MustGenerateKey(t)
	verifier := signing.NewSecp256k1Verifier()

	armed := &atomic.Bool{}
	clients := make([]user.HostClient, len(hostSigners))
	for index, signer := range hostSigners {
		machine := statetest.MustStateMachine(t, escrowID, config, group, 100_000, creator.Address(), verifier)
		hostNode, err := host.NewHost(machine, signer, stub.NewInferenceEngine(), escrowID, group, nil, host.WithGrace(100))
		if err != nil {
			t.Fatalf("NewHost = %v, want nil", err)
		}
		clients[index] = hashBreakingClient{HostClient: &user.InProcessClient{Host: hostNode}, armed: armed}
	}
	machine := statetest.MustStateMachine(t, escrowID, config, group, 100_000, creator.Address(), verifier)
	session, err := user.NewSession(machine, creator, escrowID, group, clients, verifier)
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
	if err := session.Finalize(context.Background()); err == nil {
		t.Fatal("Finalize = nil, want the broken hash to cut it short")
	}
	armed.Store(false)
	if phase := machine.Phase(); phase != types.PhaseFinalizing {
		t.Fatalf("phase = %v, want finalizing: the fixture must leave a finalize cut short", phase)
	}
	return NewSessionHandle(session, machine), session
}

// Test flow:
//  1. Build an interrupted finalize session via `interruptedFinalizeSession`, whose finalize was cut short by a broken state hash and left mid-finalizing.
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
