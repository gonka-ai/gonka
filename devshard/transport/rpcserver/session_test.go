package rpcserver

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"devshard/bridge"
	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/storage"
	"devshard/transport"
	"devshard/transport/rpcpb"
	"devshard/transport/rpcpb/rpcpbconnect"
	"devshard/types"
)

type stubCore struct {
	sigs     map[uint32][]byte
	diffs    []types.DiffRecord
	mempool  []*types.DevshardTx
	err      error
	deny     bool
	sawAllow *string
	owner    bool
	member   bool
}

func (s stubCore) ServeGetSignatures(uint64) (map[uint32][]byte, error) {
	return s.sigs, s.err
}

func (s stubCore) AllowsSender(addr string) bool {
	if s.sawAllow != nil {
		*s.sawAllow = addr
	}
	return !s.deny
}

func (s stubCore) IsOwner(string) bool       { return s.owner }
func (s stubCore) IsGroupMember(string) bool { return s.member }

func (s stubCore) ServeGetDiffs(uint64, uint64) ([]types.DiffRecord, error) {
	return s.diffs, s.err
}

func (s stubCore) ServeGetMempool(context.Context) ([]*types.DevshardTx, error) {
	return s.mempool, s.err
}

func (s stubCore) ServeChallengeReceipt(context.Context, transport.ChallengeReceiptRequest) (*transport.ChallengeReceiptResponse, error) {
	return &transport.ChallengeReceiptResponse{}, s.err
}

type stubLookup struct {
	core SessionCore
	err  error
}

func (s stubLookup) SessionServerExisting(string) (SessionCore, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.core, nil
}

func (s stubLookup) SessionForParticipant(id, addr string) (SessionCore, error) {
	_ = addr
	return s.SessionServerExisting(id)
}

type countingLookup struct {
	core SessionCore
	n    *int
}

func (s countingLookup) SessionServerExisting(string) (SessionCore, error) {
	*s.n++
	return s.core, nil
}

func (s countingLookup) SessionForParticipant(id, addr string) (SessionCore, error) {
	_ = addr
	return s.SessionServerExisting(id)
}

type countingBindLookup struct {
	core           SessionCore
	existing, bind int
}

func (s *countingBindLookup) SessionServerExisting(string) (SessionCore, error) {
	s.existing++
	return s.core, nil
}

func (s *countingBindLookup) SessionForParticipant(string, string) (SessionCore, error) {
	s.bind++
	return s.core, nil
}

func withEscrow(h http.Handler, escrowID string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.ServeHTTP(w, r.WithContext(WithEscrowID(r.Context(), escrowID)))
	})
}

type sessionEnv struct {
	session rpcpbconnect.SessionServiceClient
	gossip  rpcpbconnect.GossipServiceClient
	payload rpcpbconnect.PayloadServiceClient
	token   []byte
	peer    string
	signer  signing.Signer
}

func newSessionEnv(t *testing.T, lookup SessionLookup, escrowID string) sessionEnv {
	return newSessionEnvWith(t, lookup, escrowID, nil)
}

func newSessionEnvMux(t *testing.T, lookup SessionLookup, escrowID string, withGossip bool) sessionEnv {
	var opts []MuxOption
	if withGossip {
		opts = append(opts, WithGossipService(NewGossipHandler(lookup)))
	}
	return newSessionEnvWith(t, lookup, escrowID, nil, opts...)
}

func newSessionEnvWith(t *testing.T, lookup SessionLookup, escrowID string, signer *signing.Secp256k1Signer, opts ...MuxOption) sessionEnv {
	t.Helper()
	auth := newTestAuth(PeerAuthConfig{})
	mux := NewMux(auth, NewSessionHandler(lookup), opts...)
	srv := httptest.NewServer(withEscrow(mux, escrowID))
	t.Cleanup(srv.Close)

	if signer == nil {
		signer = testutil.MustGenerateKey(t)
	}
	authClient := rpcpbconnect.NewPeerAuthServiceClient(srv.Client(), srv.URL)
	nonce := []byte("session-handler-attach-012345")
	ts := time.Now().Unix()
	sig, err := transport.SignAttach(signer, testHostAddress, ts, signer.Address(), nonce, transport.AttachProtocolVersion, nil)
	require.NoError(t, err)
	attached, err := authClient.Attach(context.Background(), connect.NewRequest(&rpcpb.AttachRequest{
		PeerAddress:     signer.Address(),
		AttachNonce:     nonce,
		ProtocolVersion: transport.AttachProtocolVersion,
		HostAddress:     testHostAddress,
		Timestamp:       ts,
		Signature:       sig,
	}))
	require.NoError(t, err)
	return sessionEnv{
		session: rpcpbconnect.NewSessionServiceClient(srv.Client(), srv.URL),
		gossip:  rpcpbconnect.NewGossipServiceClient(srv.Client(), srv.URL),
		payload: rpcpbconnect.NewPayloadServiceClient(srv.Client(), srv.URL),
		token:   attached.Msg.SessionToken,
		peer:    signer.Address(),
		signer:  signer,
	}
}

func (e sessionEnv) signedEnvelope(t *testing.T, escrowID string, inner proto.Message) *rpcpb.SignedEnvelope {
	t.Helper()
	var payload []byte
	if inner != nil {
		var err error
		payload, err = proto.Marshal(inner)
		require.NoError(t, err)
	}
	env, err := transport.SignEnvelope(e.signer, escrowID, payload, time.Now().Unix())
	require.NoError(t, err)
	return env
}

func (e sessionEnv) getSignatures(nonce uint64) (*connect.Response[rpcpb.GetSignaturesResponse], error) {
	return e.session.GetSignatures(context.Background(), withSession(
		connect.NewRequest(&rpcpb.GetSignaturesRequest{Nonce: nonce}), e.token))
}

func TestSessionHandler_GetSignaturesWithoutHandshake(t *testing.T) {
	srv := httptest.NewServer(withEscrow(NewMux(
		newTestAuth(PeerAuthConfig{}),
		NewSessionHandler(stubLookup{core: stubCore{}}),
	), "1"))
	t.Cleanup(srv.Close)
	client := rpcpbconnect.NewSessionServiceClient(srv.Client(), srv.URL)
	_, err := client.GetSignatures(context.Background(), connect.NewRequest(&rpcpb.GetSignaturesRequest{Nonce: 1}))
	require.Error(t, err)
	require.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
}

func TestMapAllowError(t *testing.T) {
	tests := []struct {
		name   string
		err    error
		code   connect.Code
		msg    string
		header string
	}{
		{
			name:   "initializing",
			err:    fmt.Errorf("wrapped: %w", storage.ErrStorageIndexRebuilding),
			code:   connect.CodeUnavailable,
			msg:    "host initializing",
			header: transport.DevshardErrorInitializing,
		},
		{
			name:   "chain unavailable",
			err:    fmt.Errorf("get escrow: %w", bridge.ErrChainUnavailable),
			code:   connect.CodeUnavailable,
			msg:    "chain unavailable",
			header: transport.DevshardErrorChainUnavailable,
		},
		{
			name:   "escrow lookup limited",
			err:    fmt.Errorf("get escrow: %w", bridge.ErrEscrowLookupLimited),
			code:   connect.CodeResourceExhausted,
			msg:    "too many escrow lookups",
			header: transport.DevshardErrorEscrowLookupLimited,
		},
		{
			name:   "escrow not found",
			err:    fmt.Errorf("get escrow: %w", bridge.ErrEscrowNotFound),
			code:   connect.CodeFailedPrecondition,
			msg:    "escrow is not open on this host",
			header: transport.DevshardErrorEscrowNotFound,
		},
		{
			name: "session not found",
			err:  storage.ErrSessionNotFound,
			code: connect.CodeNotFound,
			msg:  "session not found",
		},
		{
			name:   "settled",
			err:    fmt.Errorf("%w: escrow 1", storage.ErrSessionNotActive),
			code:   connect.CodeFailedPrecondition,
			msg:    "escrow settled",
			header: transport.DevshardErrorEscrowSettled,
		},
		{
			name:   "escrow settled",
			err:    bridge.ErrEscrowSettled,
			code:   connect.CodeFailedPrecondition,
			msg:    "escrow settled",
			header: transport.DevshardErrorEscrowSettled,
		},
		{
			name: "version conflict",
			err:  storage.ErrSessionVersionConflict,
			code: connect.CodeFailedPrecondition,
			msg:  "session version conflict",
		},
		{
			name: "epoch conflict",
			err:  storage.ErrSessionEpochConflict,
			code: connect.CodeFailedPrecondition,
			msg:  "session epoch conflict",
		},
		{
			name: "opaque unknown",
			err:  errors.New("storage: disk full at /var/lib/devshard"),
			code: connect.CodeFailedPrecondition,
			msg:  "escrow is not open on this host",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := mapAllowError(tt.err)
			require.Equal(t, tt.code, connect.CodeOf(err))
			require.Contains(t, err.Error(), tt.msg)
			require.NotContains(t, err.Error(), "disk")
			require.NotContains(t, err.Error(), "postgres")
			require.NotContains(t, err.Error(), "rebuilding")
			var ce *connect.Error
			require.ErrorAs(t, err, &ce)
			if tt.header == "" {
				require.Empty(t, ce.Meta().Get(transport.HeaderDevshardError))
				return
			}
			require.Equal(t, tt.header, ce.Meta().Get(transport.HeaderDevshardError))
		})
	}
}

func TestSessionHandler_GetSignaturesLookupClassified(t *testing.T) {
	cases := []struct {
		name string
		err  error
		code connect.Code
		msg  string
	}{
		{"not found", storage.ErrSessionNotFound, connect.CodeNotFound, "session not found"},
		{"chain unavailable", bridge.ErrChainUnavailable, connect.CodeUnavailable, "chain unavailable"},
		{"settled", storage.ErrSessionNotActive, connect.CodeFailedPrecondition, "escrow settled"},
		{"version", storage.ErrSessionVersionConflict, connect.CodeFailedPrecondition, "session version conflict"},
		{"epoch", storage.ErrSessionEpochConflict, connect.CodeFailedPrecondition, "session epoch conflict"},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			env := newSessionEnv(t, stubLookup{err: tt.err}, "1")
			_, err := env.getSignatures(1)
			require.Error(t, err)
			require.Equal(t, tt.code, connect.CodeOf(err))
			require.Contains(t, err.Error(), tt.msg)
		})
	}
}

func TestSessionHandler_GetSignaturesLookupMiss(t *testing.T) {
	env := newSessionEnv(t, stubLookup{err: errors.New("no session: storage: disk full at /var/lib/devshard")}, "1")
	_, err := env.getSignatures(1)
	require.Error(t, err)
	require.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
	require.Contains(t, err.Error(), "escrow is not open on this host")
	require.NotContains(t, err.Error(), "storage")
	require.NotContains(t, err.Error(), "disk")
}

func TestSessionHandler_GetSignaturesCoreError(t *testing.T) {
	env := newSessionEnv(t, stubLookup{core: stubCore{err: errors.New("postgres: relation signatures does not exist")}}, "1")
	_, err := env.getSignatures(1)
	require.Error(t, err)
	require.Equal(t, connect.CodeInternal, connect.CodeOf(err))
	require.Contains(t, err.Error(), "get signatures failed")
	require.NotContains(t, err.Error(), "postgres")
	require.NotContains(t, err.Error(), "relation")
}

func TestSessionHandler_GetSignaturesNilLookup(t *testing.T) {
	env := newSessionEnv(t, nil, "1")
	_, err := env.getSignatures(1)
	require.Error(t, err)
	require.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
}

func TestSessionHandler_GetSignaturesCallsCore(t *testing.T) {
	want := []byte{0xde, 0xad}
	env := newSessionEnv(t, stubLookup{core: stubCore{sigs: map[uint32][]byte{0: want}}}, "1")
	resp, err := env.getSignatures(3)
	require.NoError(t, err)
	require.Equal(t, want, resp.Msg.Signatures[0])
}

func TestSessionHandler_GetSignaturesRosterDenied(t *testing.T) {
	env := newSessionEnv(t, stubLookup{core: stubCore{sigs: map[uint32][]byte{0: {1}}, deny: true}}, "1")
	_, err := env.getSignatures(1)
	require.Error(t, err)
	require.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
}

func TestSessionHandler_GetSignaturesEscrowNotOpen(t *testing.T) {
	env := newSessionEnv(t, stubLookup{err: errors.New("storage: not found")}, "1")
	_, err := env.getSignatures(1)
	require.Error(t, err)
	require.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
	require.Contains(t, err.Error(), "escrow is not open on this host")
	require.NotContains(t, err.Error(), "storage")
}

func TestSessionHandler_GetSignaturesInitializing(t *testing.T) {
	env := newSessionEnv(t, stubLookup{err: fmt.Errorf("escrow 1 is not open on this host: %w", storage.ErrStorageIndexRebuilding)}, "1")
	_, err := env.getSignatures(1)
	require.Error(t, err)
	require.Equal(t, connect.CodeUnavailable, connect.CodeOf(err))
	require.Contains(t, err.Error(), "host initializing")
	require.NotContains(t, err.Error(), "rebuilding")
	require.NotContains(t, err.Error(), "postgres")
}

func TestSessionHandler_GetSignaturesLookupInitializing(t *testing.T) {
	env := newSessionEnv(t, stubLookup{err: storage.ErrStorageIndexRebuilding}, "1")
	_, err := env.getSignatures(1)
	require.Error(t, err)
	require.Equal(t, connect.CodeUnavailable, connect.CodeOf(err))
	require.Contains(t, err.Error(), "host initializing")
	require.NotContains(t, err.Error(), "rebuilding")
}

func TestSessionHandler_GetSignaturesNilServer(t *testing.T) {
	env := newSessionEnv(t, stubLookup{}, "1")
	_, err := env.getSignatures(1)
	require.Error(t, err)
	require.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
	require.Contains(t, err.Error(), "session not found")
}

func TestSessionHandler_GetSignaturesUsesHandshakePeer(t *testing.T) {
	var saw string
	env := newSessionEnv(t, stubLookup{core: stubCore{sigs: map[uint32][]byte{0: {1}}, sawAllow: &saw}}, "1")
	_, err := env.getSignatures(1)
	require.NoError(t, err)
	require.Equal(t, env.peer, saw, "allow must see the handshake peer, not a body field")
}

func TestSessionHandler_ResolvesEscrowOnce(t *testing.T) {
	n := 0
	env := newSessionEnv(t, countingLookup{core: stubCore{sigs: map[uint32][]byte{0: {1}}}, n: &n}, "1")
	_, err := env.getSignatures(1)
	require.NoError(t, err)
	require.Equal(t, 1, n, "roster and serve must share one SessionServerExisting call")
}

func TestSessionHandler_GetSignaturesResolutionMetrics(t *testing.T) {
	delta := func(status, reason string, fn func()) {
		t.Helper()
		labels := map[string]string{"route": rpcGetSignaturesRoute, "status": status, "reason": reason}
		before := metricCounter(t, "devshard_session_resolution_total", labels)
		fn()
		require.Equal(t, before+1, metricCounter(t, "devshard_session_resolution_total", labels), status+"/"+reason)
	}

	delta("ok", "ok", func() {
		env := newSessionEnv(t, stubLookup{core: stubCore{sigs: map[uint32][]byte{0: {1}}}}, "1")
		_, err := env.getSignatures(1)
		require.NoError(t, err)
	})
	delta("error", "initializing", func() {
		env := newSessionEnv(t, stubLookup{err: storage.ErrStorageIndexRebuilding}, "1")
		_, err := env.getSignatures(1)
		require.Error(t, err)
	})
	delta("error", "session_resolve_err", func() {
		env := newSessionEnv(t, stubLookup{}, "1")
		_, err := env.getSignatures(1)
		require.Error(t, err)
	})
	delta("error", "get_escrow_err", func() {
		env := newSessionEnv(t, stubLookup{err: bridge.ErrChainUnavailable}, "1")
		_, err := env.getSignatures(1)
		require.Error(t, err)
	})
	delta("error", "escrow_settled", func() {
		env := newSessionEnv(t, stubLookup{err: storage.ErrSessionNotActive}, "1")
		_, err := env.getSignatures(1)
		require.Error(t, err)
	})
}

func TestAdaptLookup_NilServer(t *testing.T) {
	h := NewSessionHandler(AdaptLookup(func(string) (*transport.Server, error) {
		return nil, nil
	}))
	_, err := h.GetSignatures(
		WithEscrowID(withPeer(context.Background(), "gonka1peer"), "1"),
		connect.NewRequest(&rpcpb.GetSignaturesRequest{Nonce: 1}),
	)
	require.Error(t, err)
	require.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
	require.Contains(t, err.Error(), "session not found")
}

func TestRequirePeer(t *testing.T) {
	_, _, err := requirePeer(context.Background())
	require.Error(t, err)
	require.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
	require.Contains(t, err.Error(), "handshake required")

	_, _, err = requirePeer(withPeer(context.Background(), "gonka1peer"))
	require.Error(t, err)
	require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	require.Contains(t, err.Error(), "missing escrow id")

	peer, escrow, err := requirePeer(WithEscrowID(withPeer(context.Background(), "gonka1peer"), "escrow-1"))
	require.NoError(t, err)
	require.Equal(t, "gonka1peer", peer)
	require.Equal(t, "escrow-1", escrow)
}

func TestSessionHandler_GetSignaturesMissingEscrow(t *testing.T) {
	h := NewSessionHandler(stubLookup{core: stubCore{}})
	_, err := h.GetSignatures(withPeer(context.Background(), "gonka1peer"),
		connect.NewRequest(&rpcpb.GetSignaturesRequest{Nonce: 1}))
	require.Error(t, err)
	require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	require.Contains(t, err.Error(), "missing escrow id")
}

func TestSessionHandler_GetDiffsAndMempool(t *testing.T) {
	core := stubCore{
		diffs:   []types.DiffRecord{{Diff: types.Diff{Nonce: 2, Txs: nil, UserSig: []byte{1}}}},
		mempool: []*types.DevshardTx{{}},
	}
	env := newSessionEnv(t, stubLookup{core: core}, "escrow-1")
	diffs, err := env.session.GetDiffs(context.Background(), withSession(
		connect.NewRequest(&rpcpb.GetDiffsRequest{From: 1, To: 2}), env.token))
	require.NoError(t, err)
	require.Len(t, diffs.Msg.GetRecords(), 1)
	require.Equal(t, uint64(2), diffs.Msg.GetRecords()[0].GetDiff().GetNonce())

	mp, err := env.session.GetMempool(context.Background(), withSession(
		connect.NewRequest(&rpcpb.GetMempoolRequest{}), env.token))
	require.NoError(t, err)
	require.Len(t, mp.Msg.GetTxs(), 1)
}

func TestSessionHandler_EscrowMismatch(t *testing.T) {
	core := stubCore{owner: true, member: true}
	env := newSessionEnv(t, stubLookup{core: core}, "escrow-1")
	bad, err := transport.SignEnvelope(env.signer, "other-escrow", nil, time.Now().Unix())
	require.NoError(t, err)
	_, err = env.session.SeedHeightSync(context.Background(), withSession(connect.NewRequest(bad), env.token))
	require.Error(t, err)
	require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	require.Contains(t, err.Error(), "escrow mismatch")
}

func TestSessionHandler_OwnerVsGroup(t *testing.T) {
	t.Run("group member cannot seed", func(t *testing.T) {
		env := newSessionEnv(t, stubLookup{core: stubCore{member: true}}, "escrow-1")
		_, err := env.session.SeedHeightSync(context.Background(), withSession(connect.NewRequest(env.signedEnvelope(t, "escrow-1", nil)), env.token))
		require.Error(t, err)
		require.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
		require.Contains(t, err.Error(), "restricted to escrow owner")
	})
	t.Run("group member cannot verify timeout", func(t *testing.T) {
		env := newSessionEnv(t, stubLookup{core: stubCore{member: true}}, "escrow-1")
		_, err := env.session.VerifyTimeout(context.Background(), withSession(
			connect.NewRequest(env.signedEnvelope(t, "escrow-1", &rpcpb.VerifyTimeoutRequest{})), env.token))
		require.Error(t, err)
		require.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
		require.Contains(t, err.Error(), "restricted to escrow owner")
	})
	t.Run("group member can challenge receipt", func(t *testing.T) {
		env := newSessionEnv(t, stubLookup{core: stubCore{member: true}}, "escrow-1")
		_, err := env.session.ChallengeReceipt(context.Background(), withSession(
			connect.NewRequest(env.signedEnvelope(t, "escrow-1", &rpcpb.ChallengeReceiptRequest{InferenceId: 999})), env.token))
		require.NoError(t, err)
	})
	t.Run("owner can challenge receipt", func(t *testing.T) {
		env := newSessionEnv(t, stubLookup{core: stubCore{owner: true}}, "escrow-1")
		_, err := env.session.ChallengeReceipt(context.Background(), withSession(
			connect.NewRequest(env.signedEnvelope(t, "escrow-1", &rpcpb.ChallengeReceiptRequest{InferenceId: 999})), env.token))
		require.NoError(t, err)
	})
	t.Run("outsider cannot challenge receipt", func(t *testing.T) {
		env := newSessionEnv(t, stubLookup{core: stubCore{}}, "escrow-1")
		_, err := env.session.ChallengeReceipt(context.Background(), withSession(
			connect.NewRequest(env.signedEnvelope(t, "escrow-1", &rpcpb.ChallengeReceiptRequest{})), env.token))
		require.Error(t, err)
		require.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
		require.Contains(t, err.Error(), "restricted to escrow owner or group member")
	})
	t.Run("owner cannot gossip", func(t *testing.T) {
		env := newSessionEnvMux(t, stubLookup{core: stubCore{owner: true}}, "escrow-1", true)
		_, err := env.gossip.Nonce(context.Background(), withSession(
			connect.NewRequest(env.signedEnvelope(t, "escrow-1", &rpcpb.GossipNonceRequest{Nonce: 1, StateSig: []byte{1}})), env.token))
		require.Error(t, err)
		require.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
		require.Contains(t, err.Error(), "restricted to group members")
	})
}

func TestSessionHandler_ParticipantBindVsExisting(t *testing.T) {
	lookup := &countingBindLookup{core: stubCore{member: true, owner: true}}
	env := newSessionEnvMux(t, lookup, "escrow-1", true)

	_, err := env.session.GetDiffs(context.Background(), withSession(
		connect.NewRequest(&rpcpb.GetDiffsRequest{}), env.token))
	require.NoError(t, err)
	_, err = env.session.GetMempool(context.Background(), withSession(
		connect.NewRequest(&rpcpb.GetMempoolRequest{}), env.token))
	require.NoError(t, err)
	require.Equal(t, 2, lookup.existing, "GetDiffs/GetMempool must not CreateSession")
	require.Equal(t, 0, lookup.bind)

	_, err = env.session.ChallengeReceipt(context.Background(), withSession(
		connect.NewRequest(env.signedEnvelope(t, "escrow-1", &rpcpb.ChallengeReceiptRequest{InferenceId: 1})), env.token))
	require.NoError(t, err)
	require.Equal(t, 1, lookup.bind, "ChallengeReceipt must bind a participant session")
	require.Equal(t, 2, lookup.existing)
}
