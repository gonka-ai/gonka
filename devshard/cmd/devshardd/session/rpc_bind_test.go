package session

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/storage"
	"devshard/transport"
	"devshard/transport/rpcpb"
	"devshard/transport/rpcpb/rpcpbconnect"
	"devshard/types"
)

func TestRPCChallenge_BindsColdEscrowOnLiveHandshake(t *testing.T) {
	const escrowA = "9720"
	const escrowB = "9721"
	mgr, store, user, hosts := setupBindTestGroupSignedBy(t, escrowA, 1)
	mgr.SetRPCServerEnabled(true)
	e := echo.New()
	mgr.Register(e.Group(""))
	ts := httptest.NewServer(e)
	t.Cleanup(ts.Close)

	challenger := hosts[2]
	require.NotEqual(t, hosts[1].Address(), challenger.Address())
	hostAddr := hosts[1].Address()

	authA := rpcpbconnect.NewPeerAuthServiceClient(ts.Client(), ts.URL+"/sessions/"+escrowA+"/rpc")
	nonce := []byte("rpc-bind-attach-nonce-aaaa")
	now := time.Now().Unix()
	sig, err := transport.SignAttach(challenger, hostAddr, now, challenger.Address(), nonce, transport.AttachProtocolVersion, nil)
	require.NoError(t, err)
	attached, err := authA.Attach(context.Background(), connect.NewRequest(&rpcpb.AttachRequest{
		PeerAddress:     challenger.Address(),
		AttachNonce:     nonce,
		ProtocolVersion: transport.AttachProtocolVersion,
		HostAddress:     hostAddr,
		Timestamp:       now,
		Signature:       sig,
	}))
	require.NoError(t, err)
	_, err = store.GetSessionMeta(escrowA)
	require.ErrorIs(t, err, storage.ErrSessionNotFound, "first Attach must not CreateSession")

	sessionB := rpcpbconnect.NewSessionServiceClient(ts.Client(), ts.URL+"/sessions/"+escrowB+"/rpc")
	diffsReq := connect.NewRequest(&rpcpb.GetDiffsRequest{})
	transport.SetSessionHeader(diffsReq.Header(), attached.Msg.SessionToken)
	_, err = sessionB.GetDiffs(context.Background(), diffsReq)
	require.Error(t, err)
	require.Equal(t, connect.CodeNotFound, connect.CodeOf(err), "GetDiffs must not bind a cold escrow")
	_, err = store.GetSessionMeta(escrowB)
	require.ErrorIs(t, err, storage.ErrSessionNotFound)

	const inferenceID uint64 = 1
	diff := testutil.SignDiff(t, user, escrowB, inferenceID, []*types.DevshardTx{testutil.StartTxVersioned(inferenceID, testutil.RuntimeTestVersion)})
	dj, err := transport.DiffToJSON(diff)
	require.NoError(t, err)
	inner := transport.ChallengeReceiptRequestToProto(transport.ChallengeReceiptRequest{
		InferenceID:     inferenceID,
		ProtocolVersion: testutil.RuntimeTestVersion,
		Payload: &transport.PayloadJSON{
			Prompt:      testutil.TestPrompt,
			Model:       "llama",
			InputLength: 100,
			MaxTokens:   testutil.TestMaxTokens,
			StartedAt:   1000,
		},
		Diffs: []transport.DiffJSON{dj},
	})
	payload, err := proto.Marshal(inner)
	require.NoError(t, err)
	env, err := transport.SignEnvelope(challenger, escrowB, payload, time.Now().Unix())
	require.NoError(t, err)
	creq := connect.NewRequest(env)
	transport.SetSessionHeader(creq.Header(), attached.Msg.SessionToken)
	resp, err := sessionB.ChallengeReceipt(context.Background(), creq)
	require.NoError(t, err, "ChallengeReceipt on a live host handshake must CreateSession for a cold escrow")
	require.NotEmpty(t, resp.Msg.GetReceipt())

	meta, err := store.GetSessionMeta(escrowB)
	require.NoError(t, err)
	require.Equal(t, user.Address(), meta.CreatorAddr)
}

func TestRPCChat_SlotMemberDoesNotBindColdEscrow(t *testing.T) {
	const escrowA = "9722"
	const escrowB = "9723"
	mgr, store, _, hosts := setupBindTestGroupSignedBy(t, escrowA, 1)
	mgr.SetRPCServerEnabled(true)
	e := echo.New()
	mgr.Register(e.Group(""))
	ts := httptest.NewServer(e)
	t.Cleanup(ts.Close)

	member := hosts[2]
	require.NotEqual(t, hosts[1].Address(), member.Address())
	token := attachRPCOnEscrow(t, ts, escrowA, hosts[1].Address(), member, []byte("rpc-chat-member-attach-aaaa"))
	_, err := store.GetSessionMeta(escrowA)
	require.ErrorIs(t, err, storage.ErrSessionNotFound, "first Attach must not CreateSession")

	err = rpcChat(t, ts, escrowB, token, member, nil)
	require.Error(t, err)
	require.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
	require.Contains(t, err.Error(), "restricted to escrow owner")
	_, err = store.GetSessionMeta(escrowB)
	require.ErrorIs(t, err, storage.ErrSessionNotFound)
}

func TestRPCChat_OwnerBindsColdEscrowOnLiveHandshake(t *testing.T) {
	const escrowA = "9724"
	const escrowB = "9725"
	mgr, store, user, hosts := setupBindTestGroupSignedBy(t, escrowA, 1)
	mgr.SetRPCServerEnabled(true)
	e := echo.New()
	mgr.Register(e.Group(""))
	ts := httptest.NewServer(e)
	t.Cleanup(ts.Close)

	token := attachRPCOnEscrow(t, ts, escrowA, hosts[1].Address(), user, []byte("rpc-chat-owner-attach-aaaa"))
	_, err := store.GetSessionMeta(escrowA)
	require.ErrorIs(t, err, storage.ErrSessionNotFound, "first Attach must not CreateSession")

	body := []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`)
	_ = rpcChat(t, ts, escrowB, token, user, body)
	meta, err := store.GetSessionMeta(escrowB)
	require.NoError(t, err, "owner Chat on a live host handshake must CreateSession for a cold escrow")
	require.Equal(t, user.Address(), meta.CreatorAddr)
}

func attachRPCOnEscrow(t *testing.T, ts *httptest.Server, escrowID, hostAddr string, peer *signing.Secp256k1Signer, nonce []byte) []byte {
	t.Helper()
	auth := rpcpbconnect.NewPeerAuthServiceClient(ts.Client(), ts.URL+"/sessions/"+escrowID+"/rpc")
	now := time.Now().Unix()
	sig, err := transport.SignAttach(peer, hostAddr, now, peer.Address(), nonce, transport.AttachProtocolVersion, nil)
	require.NoError(t, err)
	attached, err := auth.Attach(context.Background(), connect.NewRequest(&rpcpb.AttachRequest{
		PeerAddress:     peer.Address(),
		AttachNonce:     nonce,
		ProtocolVersion: transport.AttachProtocolVersion,
		HostAddress:     hostAddr,
		Timestamp:       now,
		Signature:       sig,
	}))
	require.NoError(t, err)
	return attached.Msg.SessionToken
}

func rpcChat(t *testing.T, ts *httptest.Server, escrowID string, token []byte, signer *signing.Secp256k1Signer, payload []byte) error {
	t.Helper()
	session := rpcpbconnect.NewSessionServiceClient(ts.Client(), ts.URL+"/sessions/"+escrowID+"/rpc")
	env, err := transport.SignEnvelope(signer, escrowID, payload, time.Now().Unix())
	require.NoError(t, err)
	req := connect.NewRequest(env)
	transport.SetSessionHeader(req.Header(), token)
	stream, err := session.Chat(context.Background(), req)
	require.NoError(t, err)
	for stream.Receive() {
	}
	return stream.Err()
}
