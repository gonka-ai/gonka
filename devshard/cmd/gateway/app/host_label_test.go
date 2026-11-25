package app

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"common/httpguard"
	"github.com/stretchr/testify/require"

	"devshard/bridge"
	"devshard/cmd/gateway/config"
	"devshard/cmd/gateway/internal/logcapture"
	"devshard/host"
	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/transport"
	"devshard/transport/rpcserver"
	"devshard/types"
)

const (
	attachWait      = 5 * time.Second
	attachPoll      = 25 * time.Millisecond
	labelledEscrow  = "1"
	receiptNonce    = 293
	hostLabelLength = 8
	sendWait        = time.Second
	rpcRoute        = "/devshard/v2/sessions/" + labelledEscrow + "/rpc"
)

const receiptEvent = "data: {\"devshard_receipt\":{\"nonce\":%d}}\n\n"

type receiptCore struct{}

func (receiptCore) ServeGetSignatures(uint64) (map[uint32][]byte, error) {
	return map[uint32][]byte{}, nil
}

func (receiptCore) AllowsSender(string) bool { return true }

func (receiptCore) IsOwner(string) bool { return true }

func (receiptCore) ServeInference(_ context.Context, call transport.InferenceCall) error {
	_, err := fmt.Fprintf(call.Sink, receiptEvent, receiptNonce)
	call.Sink.Flush()
	return err
}

type receiptLookup struct{}

func (receiptLookup) SessionServerExisting(string) (rpcserver.SessionCore, error) {
	return receiptCore{}, nil
}

func (receiptLookup) SessionForParticipant(string, string) (rpcserver.SessionCore, error) {
	return receiptCore{}, nil
}

func (receiptLookup) SessionForOwner(string, string) (rpcserver.SessionCore, error) {
	return receiptCore{}, nil
}

func (receiptLookup) SessionForStartProof(string, string, func() ([]types.Diff, error), string) (rpcserver.SessionCore, error) {
	return receiptCore{}, nil
}

type hostedChain struct {
	bridge.MainnetBridge
	hostURL string
	slots   []string
}

func (chain hostedChain) GetEscrow(string) (*bridge.EscrowInfo, error) {
	return &bridge.EscrowInfo{EscrowID: labelledEscrow, Slots: chain.slots, Amount: 5_000_000_000, TokenPrice: 1}, nil
}

func (chain hostedChain) GetHostInfo(address string) (*bridge.HostInfo, error) {
	return &bridge.HostInfo{Address: address, URL: chain.hostURL}, nil
}

func (hostedChain) VerifyWarmKey(string, string) (bool, error) { return true, nil }

func receiptHost(t *testing.T, hostSigner *signing.Secp256k1Signer) *httptest.Server {
	t.Helper()
	auth := rpcserver.NewPeerAuthHandler(signing.NewSecp256k1Verifier(), hostSigner.Address(), rpcserver.PeerAuthConfig{Heartbeat: time.Hour})
	mux := rpcserver.NewMux(auth, rpcserver.NewSessionHandler(receiptLookup{}))
	served := http.StripPrefix(rpcRoute, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mux.ServeHTTP(w, r.WithContext(rpcserver.WithEscrowID(r.Context(), labelledEscrow)))
	}))
	server := httptest.NewServer(served)
	t.Cleanup(server.Close)
	t.Cleanup(auth.Close)
	return server
}

// Test flow:
//  1. Allow dials to the loopback test host and open a serving session on an escrow whose only host streams a devshard receipt event.
//  2. Send inference requests to the session's host client until the peer has attached and its receipt is logged.
//  3. Assert the receipt line carries the last eight characters of the host's participant address as its host label.
func TestTheReceiptLineOfAServingSessionCarriesTheHostLabel(t *testing.T) {
	logged := logcapture.Install(t)
	httpguard.SetAllowPrivate(true)
	t.Cleanup(func() { httpguard.SetAllowPrivate(false) })
	t.Setenv("LABELLED_ESCROW_KEY", strings.Repeat("11", 32))
	hostSigner := testutil.MustGenerateKey(t)
	hostServer := receiptHost(t, hostSigner)
	rows := rowLister{{EscrowID: labelledEscrow, PrivateKeyEnv: "LABELLED_ESCROW_KEY", Model: "model-a", Active: true}}
	chain := hostedChain{hostURL: hostServer.URL, slots: []string{hostSigner.Address()}}
	open := servingSessions(rows, t.TempDir(), chain, config.HeightSync{}, nil, nil, "/devshard/v2", nil, nil)
	session, err := open(context.Background(), labelledEscrow)
	if err != nil {
		t.Fatalf("open(%s) = %v", labelledEscrow, err)
	}
	t.Cleanup(func() { _ = session.Close() })
	request := host.HostRequest{Nonce: receiptNonce, Payload: &host.InferencePayload{Prompt: []byte("x"), Model: "model-a", MaxTokens: 1, StartedAt: 1}}

	var receipt logcapture.Entry
	require.Eventually(t, func() bool {
		sendContext, cancel := context.WithTimeout(context.Background(), sendWait)
		defer cancel()
		_, _ = session.UserSession().Clients()[0].Send(sendContext, request, nil, nil)
		var found bool
		receipt, found = logged.Find("sse_devshard_receipt")
		return found
	}, attachWait, attachPoll, "the host client never streamed a receipt event once the peer attached")
	if got, want := logcapture.Field(receipt, "host"), hostSigner.Address()[len(hostSigner.Address())-hostLabelLength:]; got != want {
		t.Fatalf("receipt host label = %q, want %q", got, want)
	}
}
