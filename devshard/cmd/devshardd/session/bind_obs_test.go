package session

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"devshard/bridge"
	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/storage"
	"devshard/stub"
	"devshard/transport"
	"devshard/transport/rpcpb"
	"devshard/transport/rpcpb/rpcpbconnect"
	"devshard/transport/rpcserver"
	"devshard/types"
)

func setupBindTestManager(t *testing.T, escrowID string) (*HostManager, *storage.SQLite, *signing.Secp256k1Signer, *signing.Secp256k1Signer) {
	t.Helper()
	mgr, store, user, hosts := setupBindTestGroup(t, escrowID)
	return mgr, store, user, hosts[0]
}

func setupBindTestGroup(t *testing.T, escrowID string) (*HostManager, *storage.SQLite, *signing.Secp256k1Signer, []*signing.Secp256k1Signer) {
	t.Helper()
	return setupBindTestGroupSignedBy(t, escrowID, 0)
}

func setupBindTestGroupSignedBy(t *testing.T, escrowID string, localSlot int) (*HostManager, *storage.SQLite, *signing.Secp256k1Signer, []*signing.Secp256k1Signer) {
	t.Helper()
	store := newManagerTestStore(t)
	hosts := make([]*signing.Secp256k1Signer, 3)
	for i := range hosts {
		hosts[i] = mustGenerateKey(t)
	}
	require.GreaterOrEqual(t, localSlot, 0)
	require.Less(t, localSlot, len(hosts))
	user := mustGenerateKey(t)
	addresses := make([]string, len(hosts))
	for i, h := range hosts {
		addresses[i] = h.Address()
	}
	br := &mockBridge{
		escrow: &bridge.EscrowInfo{
			EscrowID:       escrowID,
			EpochID:        7,
			Amount:         100000,
			CreatorAddress: user.Address(),
			Slots:          addresses,
			TokenPrice:     1,
		},
	}
	mgr := waitRecoveryRepairsOnCleanup(t, NewHostManager(store, hosts[localSlot], stub.NewInferenceEngine(), stub.NewValidationEngine(), nil, testutil.RuntimeTestVersion, br, nil, nil))
	return mgr, store, user, hosts
}

func signedPOST(t *testing.T, e *echo.Echo, signer *signing.Secp256k1Signer, path, escrowID string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	ts := time.Now().Unix()
	sig, err := transport.SignRequest(signer, escrowID, body, ts)
	require.NoError(t, err)
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
	req.Header.Set(echo.HeaderContentType, "application/json")
	req.Header.Set(transport.HeaderSignature, hex.EncodeToString(sig))
	req.Header.Set(transport.HeaderTimestamp, strconv.FormatInt(ts, 10))
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	return rec
}

// bindRPC is the Connect mount HostManager.Register serves when the RPC
// server is on: /sessions/{id}/rpc/. Peer bind tests go through it.
type bindRPC struct {
	t    *testing.T
	mgr  *HostManager
	http *httptest.Server
}

var bindAttachSeq atomic.Uint64

func newBindRPC(t *testing.T, mgr *HostManager) *bindRPC {
	t.Helper()
	mgr.SetRPCServerEnabled(true)
	e := echo.New()
	mgr.Register(e.Group(""))
	srv := httptest.NewServer(e)
	t.Cleanup(srv.Close)
	return &bindRPC{t: t, mgr: mgr, http: srv}
}

func (b *bindRPC) base(escrowID string) string {
	return b.http.URL + "/sessions/" + escrowID + "/rpc"
}

func (b *bindRPC) attach(escrowID string, signer *signing.Secp256k1Signer) ([]byte, error) {
	b.t.Helper()
	var nonce [16]byte
	binary.BigEndian.PutUint64(nonce[8:], bindAttachSeq.Add(1))
	client := rpcpbconnect.NewPeerAuthServiceClient(b.http.Client(), b.base(escrowID))
	ts := time.Now().Unix()
	host := b.mgr.signer.Address()
	sig, err := transport.SignAttach(signer, host, ts, signer.Address(), nonce[:], transport.AttachProtocolVersion, nil)
	if err != nil {
		return nil, err
	}
	attached, err := client.Attach(context.Background(), connect.NewRequest(&rpcpb.AttachRequest{
		PeerAddress:     signer.Address(),
		AttachNonce:     nonce[:],
		ProtocolVersion: transport.AttachProtocolVersion,
		HostAddress:     host,
		Timestamp:       ts,
		Signature:       sig,
	}))
	if err != nil {
		return nil, err
	}
	return attached.Msg.SessionToken, nil
}

func (b *bindRPC) mustAttach(escrowID string, signer *signing.Secp256k1Signer) []byte {
	b.t.Helper()
	token, err := b.attach(escrowID, signer)
	require.NoError(b.t, err)
	return token
}

func connectDevshardError(err error) string {
	var ce *connect.Error
	if !errors.As(err, &ce) {
		return ""
	}
	return ce.Meta().Get(transport.HeaderDevshardError)
}

func withBindSession[T any](req *connect.Request[T], token []byte) *connect.Request[T] {
	rpcserver.SetSessionHeader(req.Header(), token)
	return req
}

func signBindEnvelope(t *testing.T, signer signing.Signer, escrowID string, payload []byte) *rpcpb.SignedEnvelope {
	t.Helper()
	env, err := transport.SignEnvelope(signer, escrowID, payload, time.Now().Unix())
	require.NoError(t, err)
	return env
}

func marshalBindPayload(t *testing.T, msg proto.Message) []byte {
	t.Helper()
	payload, err := proto.Marshal(msg)
	require.NoError(t, err)
	return payload
}

func (b *bindRPC) chat(escrowID string, signer *signing.Secp256k1Signer, body []byte, opts ...connect.ClientOption) error {
	b.t.Helper()
	return b.chatToken(escrowID, signer, b.mustAttach(escrowID, signer), body, opts...)
}

func (b *bindRPC) chatToken(escrowID string, signer signing.Signer, token, body []byte, opts ...connect.ClientOption) error {
	b.t.Helper()
	env := signBindEnvelope(b.t, signer, escrowID, body)
	client := rpcpbconnect.NewSessionServiceClient(b.http.Client(), b.base(escrowID), opts...)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	stream, err := client.Chat(ctx, withBindSession(connect.NewRequest(env), token))
	if err != nil {
		return err
	}
	for stream.Receive() {
	}
	return stream.Err()
}

func (b *bindRPC) gossipNonce(escrowID string, signer *signing.Secp256k1Signer) error {
	b.t.Helper()
	token := b.mustAttach(escrowID, signer)
	env := signBindEnvelope(b.t, signer, escrowID, marshalBindPayload(b.t, &rpcpb.GossipNonceRequest{Nonce: 1}))
	client := rpcpbconnect.NewGossipServiceClient(b.http.Client(), b.base(escrowID))
	_, err := client.Nonce(context.Background(), withBindSession(connect.NewRequest(env), token))
	return err
}

func (b *bindRPC) seed(escrowID string, signer *signing.Secp256k1Signer) error {
	b.t.Helper()
	token := b.mustAttach(escrowID, signer)
	env := signBindEnvelope(b.t, signer, escrowID, nil)
	client := rpcpbconnect.NewSessionServiceClient(b.http.Client(), b.base(escrowID))
	_, err := client.SeedHeightSync(context.Background(), withBindSession(connect.NewRequest(env), token))
	return err
}

func (b *bindRPC) challenge(escrowID string, signer *signing.Secp256k1Signer, req transport.ChallengeReceiptRequest) (*transport.ChallengeReceiptResponse, error) {
	b.t.Helper()
	token, err := b.attach(escrowID, signer)
	if err != nil {
		return nil, err
	}
	env := signBindEnvelope(b.t, signer, escrowID, marshalBindPayload(b.t, transport.ChallengeReceiptRequestToProto(req)))
	client := rpcpbconnect.NewSessionServiceClient(b.http.Client(), b.base(escrowID))
	resp, err := client.ChallengeReceipt(context.Background(), withBindSession(connect.NewRequest(env), token))
	if err != nil {
		return nil, err
	}
	return transport.ChallengeReceiptResponseFromProto(resp.Msg), nil
}

func TestObsGET_DoesNotBindSession(t *testing.T) {
	const escrowID = "9701"
	mgr, store, _, _ := setupBindTestManager(t, escrowID)

	e := echo.New()
	mgr.Register(e.Group(""))

	req := httptest.NewRequest(http.MethodGet, "/sessions/"+escrowID+"/diffs", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	require.Equal(t, http.StatusNotFound, rec.Code, "body: %s", rec.Body.String())

	_, err := store.GetSessionMeta(escrowID)
	require.ErrorIs(t, err, storage.ErrSessionNotFound)

	active, err := store.ListActiveSessions()
	require.NoError(t, err)
	require.Empty(t, active)
}

func TestObsMempoolSignatures_DoNotBindSession(t *testing.T) {
	const escrowID = "9702"
	mgr, store, _, _ := setupBindTestManager(t, escrowID)
	e := echo.New()
	mgr.Register(e.Group(""))

	for _, path := range []string{
		"/sessions/" + escrowID + "/mempool",
		"/sessions/" + escrowID + "/signatures",
	} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		require.Equal(t, http.StatusNotFound, rec.Code, "path=%s body=%s", path, rec.Body.String())
	}

	_, err := store.GetSessionMeta(escrowID)
	require.ErrorIs(t, err, storage.ErrSessionNotFound)
}

func TestGossipUnbound_DoesNotBindSession(t *testing.T) {
	const escrowID = "9703"
	mgr, store, _, hostSigner := setupBindTestManager(t, escrowID)
	rpc := newBindRPC(t, mgr)

	err := rpc.gossipNonce(escrowID, hostSigner)
	require.Equal(t, connect.CodeNotFound, connect.CodeOf(err), "rpc=%v", err)

	_, err = store.GetSessionMeta(escrowID)
	require.ErrorIs(t, err, storage.ErrSessionNotFound, "gossip must not CreateSession; only gateway or start proof may bind")
}

func TestGossipUnbound_StrangerDoesNotBindSession(t *testing.T) {
	const escrowID = "9712"
	mgr, store, _, _ := setupBindTestManager(t, escrowID)
	rpc := newBindRPC(t, mgr)

	_, err := rpc.attach(escrowID, mustGenerateKey(t))
	require.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err), "rpc=%v", err)

	_, err = store.GetSessionMeta(escrowID)
	require.ErrorIs(t, err, storage.ErrSessionNotFound)
}

func TestChallengeReceiptUnbound_BindsAndAppliesStart(t *testing.T) {
	const escrowID = "9713"
	// This process is slot 1 so inference 1 (nonce 1) assigns it as executor.
	mgr, store, user, hosts := setupBindTestGroupSignedBy(t, escrowID, 1)
	rpc := newBindRPC(t, mgr)

	const inferenceID uint64 = 1
	diff := testutil.SignDiff(t, user, escrowID, inferenceID, []*types.DevshardTx{testutil.StartTxVersioned(inferenceID, testutil.RuntimeTestVersion)})
	dj, err := transport.DiffToJSON(diff)
	require.NoError(t, err)

	challenger := hosts[2]
	require.NotEqual(t, hosts[1].Address(), challenger.Address())
	resp, err := rpc.challenge(escrowID, challenger, transport.ChallengeReceiptRequest{
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
	require.NoError(t, err)

	meta, err := store.GetSessionMeta(escrowID)
	require.NoError(t, err, "challenge-receipt on a cold host must CreateSession")
	require.Equal(t, user.Address(), meta.CreatorAddr)

	require.NotEmpty(t, resp.Receipt, "executor must sign a receipt after applying the missed StartInference")

	txs, err := transport.DevshardTxsFromBytes(resp.Mempool)
	require.NoError(t, err)
	found := false
	for _, tx := range txs {
		if cs := tx.GetConfirmStart(); cs != nil && cs.InferenceId == inferenceID {
			found = true
			break
		}
	}
	require.True(t, found, "recovery mempool must include MsgConfirmStart for the challenged inference")
}

func TestChallengeReceiptUnbound_WrongVersionDoesNotBind(t *testing.T) {
	const escrowID = "9715"
	mgr, store, user, hosts := setupBindTestGroupSignedBy(t, escrowID, 1)
	rpc := newBindRPC(t, mgr)

	start := testutil.StartTx(1)
	start.GetStartInference().ProtocolVersion = "other-version"
	diff := testutil.SignDiff(t, user, escrowID, 1, []*types.DevshardTx{start})
	dj, err := transport.DiffToJSON(diff)
	require.NoError(t, err)

	_, err = rpc.challenge(escrowID, hosts[2], transport.ChallengeReceiptRequest{
		InferenceID:     1,
		ProtocolVersion: "other-version",
		Payload:         &transport.PayloadJSON{Prompt: testutil.TestPrompt, Model: "llama", InputLength: 100, MaxTokens: testutil.TestMaxTokens, StartedAt: 1000},
		Diffs:           []transport.DiffJSON{dj},
	})
	require.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err), "rpc=%v", err)
	_, err = store.GetSessionMeta(escrowID)
	require.ErrorIs(t, err, storage.ErrSessionNotFound)
}

func TestChallengeReceiptUnbound_MissingVersionDoesNotBind(t *testing.T) {
	const escrowID = "9716"
	mgr, store, user, hosts := setupBindTestGroupSignedBy(t, escrowID, 1)
	rpc := newBindRPC(t, mgr)

	diff := testutil.SignDiff(t, user, escrowID, 1, []*types.DevshardTx{testutil.StartTx(1)})
	dj, err := transport.DiffToJSON(diff)
	require.NoError(t, err)

	_, err = rpc.challenge(escrowID, hosts[2], transport.ChallengeReceiptRequest{
		InferenceID: 1,
		Payload:     &transport.PayloadJSON{Prompt: testutil.TestPrompt, Model: "llama", InputLength: 100, MaxTokens: testutil.TestMaxTokens, StartedAt: 1000},
		Diffs:       []transport.DiffJSON{dj},
	})
	require.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err), "rpc=%v", err)
	_, err = store.GetSessionMeta(escrowID)
	require.ErrorIs(t, err, storage.ErrSessionNotFound)
}

func TestChallengeReceiptUnbound_ClaimedVersionMismatchDoesNotBind(t *testing.T) {
	const escrowID = "9717"
	mgr, store, user, hosts := setupBindTestGroupSignedBy(t, escrowID, 1)
	rpc := newBindRPC(t, mgr)

	diff := testutil.SignDiff(t, user, escrowID, 1, []*types.DevshardTx{testutil.StartTxVersioned(1, testutil.RuntimeTestVersion)})
	dj, err := transport.DiffToJSON(diff)
	require.NoError(t, err)

	_, err = rpc.challenge(escrowID, hosts[2], transport.ChallengeReceiptRequest{
		InferenceID:     1,
		ProtocolVersion: "other-version",
		Payload:         &transport.PayloadJSON{Prompt: testutil.TestPrompt, Model: "llama", InputLength: 100, MaxTokens: testutil.TestMaxTokens, StartedAt: 1000},
		Diffs:           []transport.DiffJSON{dj},
	})
	require.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err), "rpc=%v", err)
	_, err = store.GetSessionMeta(escrowID)
	require.ErrorIs(t, err, storage.ErrSessionNotFound)
}

func TestChallengeReceiptUnbound_StrangerDoesNotBind(t *testing.T) {
	const escrowID = "9714"
	mgr, store, _, _ := setupBindTestManager(t, escrowID)
	rpc := newBindRPC(t, mgr)

	_, err := rpc.attach(escrowID, mustGenerateKey(t))
	require.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err), "rpc=%v", err)
	_, err = store.GetSessionMeta(escrowID)
	require.ErrorIs(t, err, storage.ErrSessionNotFound)
}

func TestOwnerChat_BindsSession(t *testing.T) {
	const escrowID = "9704"
	mgr, store, user, _ := setupBindTestManager(t, escrowID)
	rpc := newBindRPC(t, mgr)

	body := []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`)
	chatErr := rpc.chat(escrowID, user, body)
	// Inference may fail downstream; bind must have happened regardless.
	meta, err := store.GetSessionMeta(escrowID)
	require.NoError(t, err, "owner chat must CreateSession; rpc=%v", chatErr)
	require.Equal(t, testutil.RuntimeTestVersion, meta.Version)
	require.Equal(t, user.Address(), meta.CreatorAddr)
}

func TestHeightSyncSeed_BindsSession(t *testing.T) {
	const escrowID = "9711"
	mgr, store, user, _ := setupBindTestManager(t, escrowID)
	rpc := newBindRPC(t, mgr)

	seedErr := rpc.seed(escrowID, user)
	meta, err := store.GetSessionMeta(escrowID)
	require.NoError(t, err, "owner seed must CreateSession; rpc=%v", seedErr)
	require.Equal(t, testutil.RuntimeTestVersion, meta.Version)
	require.Equal(t, user.Address(), meta.CreatorAddr)
}

func TestOwnerChat_SettledEscrow_DoesNotBindSession(t *testing.T) {
	const escrowID = "9709"
	mgr, store, user, _ := setupBindTestManager(t, escrowID)
	mgr.bridge.(*mockBridge).escrow.Settled = true
	rpc := newBindRPC(t, mgr)

	_, err := rpc.attach(escrowID, user)
	require.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err), "rpc=%v", err)
	require.Equal(t, transport.DevshardErrorEscrowSettled, connectDevshardError(err))

	_, err = store.GetSessionMeta(escrowID)
	require.ErrorIs(t, err, storage.ErrSessionNotFound)

	active, err := store.ListActiveSessions()
	require.NoError(t, err)
	require.Empty(t, active)
}

func TestOwnerChat_SettledLocalRow_ReturnsConflict(t *testing.T) {
	const escrowID = "9710"
	mgr, store, user, _ := setupBindTestManager(t, escrowID)
	rpc := newBindRPC(t, mgr)

	body := []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`)
	token := rpc.mustAttach(escrowID, user)
	_ = rpc.chatToken(escrowID, user, token, body)
	meta, err := store.GetSessionMeta(escrowID)
	require.NoError(t, err, "precondition: first chat must bind the session")
	require.Equal(t, "active", meta.Status)

	require.NoError(t, mgr.HandleSettlementFinalized(escrowID))

	err = rpc.chatToken(escrowID, user, token, body)
	require.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err), "rpc=%v", err)
	require.Equal(t, transport.DevshardErrorEscrowSettled, connectDevshardError(err))
}

func TestOwnerChat_RejectsOversizedEnvelopeBeforeBinding(t *testing.T) {
	const escrowID = "9708"
	mgr, store, user, _ := setupBindTestManager(t, escrowID)
	rpc := newBindRPC(t, mgr)

	// Chat's Connect read cap is DefaultMaxBodySize. A gzipped envelope that
	// inflates past it is refused before SessionForOwner.
	body := bytes.Repeat([]byte("n"), int(transport.DefaultMaxBodySize)+1)
	err := rpc.chat(escrowID, user, body, connect.WithSendGzip())
	require.Equal(t, connect.CodeResourceExhausted, connect.CodeOf(err), "rpc=%v", err)
	_, err = store.GetSessionMeta(escrowID)
	require.ErrorIs(t, err, storage.ErrSessionNotFound)
}

type countingGetEscrowBridge struct {
	bridge.MainnetBridge
	calls int
}

func (b *countingGetEscrowBridge) GetEscrow(escrowID string) (*bridge.EscrowInfo, error) {
	b.calls++
	return b.MainnetBridge.GetEscrow(escrowID)
}

func TestOwnerChat_FirstBindSingleGetEscrow(t *testing.T) {
	const escrowID = "9705"
	store := newManagerTestStore(t)
	hosts := make([]*signing.Secp256k1Signer, 3)
	for i := range hosts {
		hosts[i] = mustGenerateKey(t)
	}
	user := mustGenerateKey(t)
	addresses := make([]string, len(hosts))
	for i, h := range hosts {
		addresses[i] = h.Address()
	}
	inner := &mockBridge{
		escrow: &bridge.EscrowInfo{
			EscrowID:       escrowID,
			EpochID:        7,
			Amount:         100000,
			CreatorAddress: user.Address(),
			Slots:          addresses,
			TokenPrice:     1,
		},
	}
	br := &countingGetEscrowBridge{MainnetBridge: inner}
	mgr := waitRecoveryRepairsOnCleanup(t, NewHostManager(store, hosts[0], stub.NewInferenceEngine(), stub.NewValidationEngine(), nil, testutil.RuntimeTestVersion, br, nil, nil))

	rpc := newBindRPC(t, mgr)
	body := []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`)
	_ = rpc.chat(escrowID, user, body)

	meta, err := store.GetSessionMeta(escrowID)
	require.NoError(t, err)
	require.Equal(t, testutil.RuntimeTestVersion, meta.Version)
	require.Equal(t, 1, br.calls, "first-bind path must GetEscrow once (reuse for BuildGroup + CreateSession)")
}

func TestOwnerChat_WarmedCacheStillGetEscrow(t *testing.T) {
	const escrowID = "9711"
	store := newManagerTestStore(t)
	hosts := make([]*signing.Secp256k1Signer, 3)
	for i := range hosts {
		hosts[i] = mustGenerateKey(t)
	}
	user := mustGenerateKey(t)
	addresses := make([]string, len(hosts))
	urls := make(map[string]string, len(hosts))
	for i, h := range hosts {
		addresses[i] = h.Address()
		urls[h.Address()] = "http://localhost"
	}
	require.NoError(t, store.PutEscrowCache(storage.EscrowCacheInfo{
		EscrowID: escrowID, EpochID: 7, Amount: 100000, CreatorAddress: user.Address(),
		Slots: addresses, TokenPrice: 1, SlotURLs: urls,
	}))
	inner := &mockBridge{
		escrow: &bridge.EscrowInfo{
			EscrowID: escrowID, EpochID: 7, Amount: 100000,
			CreatorAddress: user.Address(), Slots: addresses, TokenPrice: 1,
		},
	}
	br := &countingGetEscrowBridge{MainnetBridge: inner}
	mgr := waitRecoveryRepairsOnCleanup(t, NewHostManager(store, hosts[0], stub.NewInferenceEngine(), stub.NewValidationEngine(), nil, testutil.RuntimeTestVersion, br, nil, nil))

	rpc := newBindRPC(t, mgr)
	body := []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`)
	_ = rpc.chat(escrowID, user, body)

	require.Equal(t, 1, br.calls, "owner first chat still GetEscrow live even when escrow_cache is warm")
}

func TestNonOwnerChat_DoesNotBindSession(t *testing.T) {
	const escrowID = "9706"
	mgr, store, _, hostSigner := setupBindTestManager(t, escrowID)
	rpc := newBindRPC(t, mgr)

	body := []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`)
	err := rpc.chat(escrowID, hostSigner, body)
	require.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err), "rpc=%v", err)

	_, err = store.GetSessionMeta(escrowID)
	require.ErrorIs(t, err, storage.ErrSessionNotFound)
}

func TestSessionServerExisting_NoCreate(t *testing.T) {
	const escrowID = "9707"
	mgr, store, _, _ := setupBindTestManager(t, escrowID)

	_, err := mgr.SessionServerExisting(escrowID)
	require.ErrorIs(t, err, storage.ErrSessionNotFound)

	_, err = store.GetSessionMeta(escrowID)
	require.ErrorIs(t, err, storage.ErrSessionNotFound)
}

type countingGetSessionMetaStore struct {
	storage.Storage
	gets atomic.Int32
}

func (s *countingGetSessionMetaStore) GetSessionMeta(escrowID string) (*storage.SessionMeta, error) {
	s.gets.Add(1)
	return s.Storage.GetSessionMeta(escrowID)
}

func TestSessionServerExisting_NegativeCachesMiss(t *testing.T) {
	counted := &countingGetSessionMetaStore{Storage: storage.NewMemory()}
	mgr := NewHostManager(counted, mustGenerateKey(t), nil, nil, nil, "v5", nil, nil, nil)
	t.Cleanup(func() { _ = mgr.Close() })

	const escrowID = "9708"
	_, err := mgr.SessionServerExisting(escrowID)
	require.ErrorIs(t, err, storage.ErrSessionNotFound)
	first := counted.gets.Load()
	require.Greater(t, first, int32(0))

	_, err = mgr.SessionServerExisting(escrowID)
	require.ErrorIs(t, err, storage.ErrSessionNotFound)
	require.Equal(t, first, counted.gets.Load(), "cached miss must not recover again")
}

func TestSessionServerExisting_MissDoesNotOccupyRecoveryGate(t *testing.T) {
	mgr := NewHostManager(storage.NewMemory(), mustGenerateKey(t), nil, nil, nil, "v5", nil, nil, nil)
	t.Cleanup(func() { _ = mgr.Close() })

	const escrowID = "9709"
	_, err := mgr.SessionServerExisting(escrowID)
	require.ErrorIs(t, err, storage.ErrSessionNotFound)

	mgr.recoveryGate.mu.Lock()
	defer mgr.recoveryGate.mu.Unlock()
	require.Empty(t, mgr.recoveryGate.requested, "a miss must not occupy the demand set")
	require.Zero(t, mgr.recoveryGate.inFlight)
}

func TestOwnerChat_BindsAfterExistingMiss(t *testing.T) {
	const escrowID = "9712"
	mgr, store, user, _ := setupBindTestManager(t, escrowID)

	_, err := mgr.SessionServerExisting(escrowID)
	require.ErrorIs(t, err, storage.ErrSessionNotFound)

	rpc := newBindRPC(t, mgr)
	body := []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`)
	chatErr := rpc.chat(escrowID, user, body)
	meta, err := store.GetSessionMeta(escrowID)
	require.NoError(t, err, "cached Existing miss must not block first bind; rpc=%v", chatErr)
	require.Equal(t, testutil.RuntimeTestVersion, meta.Version)
}

func TestGetOrCreate_RecoversBeforeCreate(t *testing.T) {
	store := newManagerTestStore(t)
	_, user, hostSigner := populateStore(t, store, 2)

	addresses := []string{hostSigner.Address()}
	// populateStore uses 3 hosts; rebuild addresses from meta.
	meta, err := store.GetSessionMeta("1")
	require.NoError(t, err)
	addresses = make([]string, len(meta.Group))
	for i, s := range meta.Group {
		addresses[i] = s.ValidatorAddress
	}

	br := &mockBridge{
		escrow: &bridge.EscrowInfo{
			EscrowID:       "1",
			EpochID:        7,
			Amount:         100000,
			CreatorAddress: user.Address(),
			Slots:          addresses,
		},
	}
	mgr := waitRecoveryRepairsOnCleanup(t, NewHostManager(store, hostSigner, stub.NewInferenceEngine(), stub.NewValidationEngine(), nil, testutil.RuntimeTestVersion, br, nil, nil))

	srv, err := mgr.getOrCreate("1", nil)
	require.NoError(t, err)
	require.Equal(t, uint64(2), srv.Host().SnapshotState().LatestNonce)
}
