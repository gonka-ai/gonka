package user

import (
	"context"
	"crypto/sha256"
	"sync"
	"testing"

	"devshard/host"
	"devshard/internal/statetest"
	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/stub"

	"github.com/stretchr/testify/require"
)

// recoveryCounter counts secp256k1 recoveries per signed message and signature.
type recoveryCounter struct {
	inner signing.Verifier
	mu    sync.Mutex
	calls map[string]int
}

func (c *recoveryCounter) RecoverAddress(message []byte, sig []byte) (string, error) {
	digest := sha256.Sum256(message)
	c.mu.Lock()
	c.calls[string(digest[:])+string(sig)]++
	c.mu.Unlock()
	return c.inner.RecoverAddress(message, sig)
}

// Test flow:
//  1. Build a session whose state machine and session share the gateway's session verifier, over in-process hosts.
//  2. Run one inference to an applied Finish.
//  3. Expect every signature recovered once: the receipt and the Finish are checked on receipt, as evidence and on apply.
func TestSessionVerifierRecoversEachSignatureOnce(t *testing.T) {
	hosts := make([]*signing.Secp256k1Signer, 3)
	for i := range hosts {
		hosts[i] = testutil.MustGenerateKey(t)
	}
	creator := testutil.MustGenerateKey(t)
	group := testutil.MakeGroup(hosts)
	config := testutil.DefaultConfig(len(hosts))
	clients := make([]HostClient, len(hosts))
	for i := range hosts {
		sm := statetest.MustStateMachine(t, "escrow-1", config, group, 100000, creator.Address(), signing.NewSecp256k1Verifier())
		h, err := host.NewHost(sm, hosts[i], stub.NewInferenceEngine(), "escrow-1", group, nil, host.WithGrace(10))
		require.NoError(t, err)
		clients[i] = &InProcessClient{Host: h}
	}
	counter := &recoveryCounter{inner: signing.NewSecp256k1Verifier(), calls: map[string]int{}}
	verifier := newSessionVerifier(counter)
	sm := statetest.MustStateMachine(t, "escrow-1", config, group, 100000, creator.Address(), verifier)
	s, err := NewSession(sm, creator, "escrow-1", group, clients, verifier)
	require.NoError(t, err)

	_, err = s.SendInference(context.Background(), completionParams())
	require.NoError(t, err)
	require.NoError(t, s.SendPendingDiff(context.Background()))
	rec, ok := s.sm.Inference(1)
	require.True(t, ok)
	require.True(t, finishAppliedStatus(rec.Status))

	counter.mu.Lock()
	defer counter.mu.Unlock()
	require.NotEmpty(t, counter.calls)
	var again []int
	for _, n := range counter.calls {
		if n > 1 {
			again = append(again, n)
		}
	}
	require.Empty(t, again, "recoveries of each signature recovered more than once, of %d signatures", len(counter.calls))
}
