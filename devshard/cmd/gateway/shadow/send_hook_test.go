package shadow

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"devshard/cmd/gateway/accounting"
	"devshard/cmd/gateway/internal/leakcheck"
	"devshard/cmd/gateway/registry"
	"devshard/host"
	"devshard/internal/statetest"
	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/user"
)

const (
	hookEscrow       = "escrow-hook"
	hookDeposit      = 100_000
	hookCatchUpTime  = 10 * time.Second
	hookReceiptTime  = time.Millisecond
	hookContentGap   = time.Millisecond
	hookSessionSlots = 3
	roleEvent        = "data: {\"choices\":[{\"delta\":{\"role\":\"assistant\"}}]}\n\n"
)

var errHookHostRefused = errors.New("the host refused the request")

type refusingHost struct{}

func (refusingHost) Send(context.Context, host.HostRequest, io.Writer, func(*host.HostResponse)) (*host.HostResponse, error) {
	return nil, errHookHostRefused
}

type userBackedSession struct {
	*stubSession
	session *user.Session
}

func (s userBackedSession) UserSession() *user.Session { return s.session }

func newHookSession(t *testing.T) *user.Session {
	t.Helper()
	return newHookSessionOver(t, refusingHost{})
}

func newHookSessionOver(t *testing.T, hostClient user.HostClient) *user.Session {
	t.Helper()
	signers := make([]*signing.Secp256k1Signer, hookSessionSlots)
	clients := make([]user.HostClient, hookSessionSlots)
	for index := range signers {
		signers[index] = testutil.MustGenerateKey(t)
		clients[index] = hostClient
	}
	group := testutil.MakeGroup(signers)
	creator, verifier := testutil.MustGenerateKey(t), signing.NewSecp256k1Verifier()
	machine := statetest.MustStateMachine(t, hookEscrow, testutil.DefaultConfig(len(group)), group, hookDeposit, creator.Address(), verifier)
	session, err := user.NewSession(machine, creator, hookEscrow, group, clients, verifier)
	if err != nil {
		t.Fatalf("NewSession() = %v, want nil", err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session
}

// Test flow:
//  1. Bind a runner on a manual clock whose send spends ten seconds before the session's own send, then stamps the receipt and streams a role-only event a millisecond after it and the first content event a millisecond later.
//  2. Probe a nonce prepared on a real session whose host refuses the request, so the session's send fires the OnSend hook the runner registered.
//  3. Assert the ledger attempt is acknowledged and its receipt is not slow, because the probe's send time was restamped at the hook after the ten seconds.
//  4. Assert the reported total, first token and the ledger's first content delay all run from the hook too, so they leave out the ten seconds, and the first content delay skips the role-only event.
func TestShadowProbeSendTimeIsStampedByTheSendHook(t *testing.T) {
	defer leakcheck.VerifyNoneStarted(t)()
	clock := newManualClock()
	fixture := newFixture(t, func(ctx context.Context, session registry.EscrowSession, prepared *user.PreparedInference, answer io.Writer, onReceipt func()) (*host.HostResponse, error) {
		clock.advance(hookCatchUpTime)
		response, err := dispatch(ctx, session, prepared, answer, func() {})
		clock.advance(hookReceiptTime)
		onReceipt()
		if _, writeErr := answer.Write([]byte(roleEvent)); writeErr != nil {
			return nil, writeErr
		}
		clock.advance(hookContentGap)
		if _, writeErr := answer.Write([]byte(contentEvent)); writeErr != nil {
			return nil, writeErr
		}
		return response, err
	})
	fixture.runner.now = clock.now
	realSession := newHookSession(t)
	fixture.escrows.session = userBackedSession{stubSession: fixture.session, session: realSession}
	request := fixture.request()
	prepared, err := realSession.PrepareInferenceFn(func(user.HostBinding) (user.InferenceParams, bool, context.Context, error) {
		return request.Params.(user.InferenceParams), false, nil, nil
	})
	if err != nil {
		t.Fatalf("PrepareInferenceFn() = %v, want nil", err)
	}
	request.Assignment.Nonce = prepared

	if !fixture.runner.Probe(request) {
		t.Fatal("Probe = false, want the probe started")
	}
	result := fixture.await(t)

	if !errors.Is(result.Err, errHookHostRefused) {
		t.Fatalf("result = %+v, want the host's refusal reached through the session's send", result)
	}
	if len(fixture.probes.attempts) != 1 || !fixture.probes.attempts[0].Acknowledged {
		t.Fatalf("ledger attempts = %+v, want one acknowledged attempt", fixture.probes.attempts)
	}
	if result.Total != hookReceiptTime+hookContentGap || result.FirstToken != hookReceiptTime {
		t.Errorf("result total = %s, first token = %s, want %s and %s counted from the send hook, without the %s before it", result.Total, result.FirstToken, hookReceiptTime+hookContentGap, hookReceiptTime, hookCatchUpTime)
	}
	if got := fixture.probes.attempts[0].FirstContentDelay; got != hookReceiptTime+hookContentGap {
		t.Errorf("ledger first content delay = %s, want %s from the send hook to the first content event, as a client line's", got, hookReceiptTime+hookContentGap)
	}
	if fixture.probes.attempts[0].SlowReceipt {
		t.Errorf("ledger attempt = %+v, want a receipt timed from the send hook, not from before the catch-up (%s)", fixture.probes.attempts[0], accounting.SlowReceipt)
	}
}
