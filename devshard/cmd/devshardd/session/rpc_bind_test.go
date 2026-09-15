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
	require.NoError(t, err, "first Attach on escrow A still CreateSession for the door")

	sessionB := rpcpbconnect.NewSessionServiceClient(ts.Client(), ts.URL+"/sessions/"+escrowB+"/rpc")
	diffsReq := connect.NewRequest(&rpcpb.GetDiffsRequest{})
	transport.SetSessionHeader(diffsReq.Header(), attached.Msg.SessionToken)
	_, err = sessionB.GetDiffs(context.Background(), diffsReq)
	require.Error(t, err)
	require.Equal(t, connect.CodeNotFound, connect.CodeOf(err), "GetDiffs must not bind a cold escrow")
	_, err = store.GetSessionMeta(escrowB)
	require.ErrorIs(t, err, storage.ErrSessionNotFound)

	const inferenceID uint64 = 1
	diff := testutil.SignDiff(t, user, escrowB, inferenceID, []*types.DevshardTx{testutil.StartTx(inferenceID)})
	dj, err := transport.DiffToJSON(diff)
	require.NoError(t, err)
	inner := transport.ChallengeReceiptRequestToProto(transport.ChallengeReceiptRequest{
		InferenceID: inferenceID,
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
