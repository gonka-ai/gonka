package transport

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http/httptest"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"

	"devshard/host"
	"devshard/internal/testutil"
	"devshard/transport/rpcpb"
	"devshard/transport/rpcpb/rpcpbconnect"
)

type bodyAdmission struct {
	allowErr error
	results  []string
	faults   []string
}

func (a *bodyAdmission) AllowRequest(string, string) error { return a.allowErr }

func (a *bodyAdmission) ObserveResult(string, string, int) {}

func (a *bodyAdmission) ObserveResultWithBody(_, _ string, status int, body, devshardCode, _ string) {
	a.results = append(a.results, fmt.Sprintf("%d %s %s", status, devshardCode, body))
}

func (a *bodyAdmission) ObserveTransportFailure(string, string, error) {
	a.faults = append(a.faults, "transport")
}

func TestObserveChat_ApplicationStatusNotTransportFault(t *testing.T) {
	signer := testutil.MustGenerateKey(t)
	disabled := connect.NewError(connect.CodeUnavailable, errors.New("requests disabled"))
	disabled.Meta().Set(HeaderDevshardError, DevshardErrorRequestsDisabled)
	dial := connect.NewError(connect.CodeUnavailable, &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("refused")})

	cases := []struct {
		name      string
		err       error
		want      string
		transport bool
		silent    bool
	}{
		{
			name: "internal keeps the body",
			err:  connect.NewError(connect.CodeInternal, errors.New("apply diff nonce 1: post_state_root does not match computed state root")),
			want: "500  apply diff nonce 1: post_state_root does not match computed state root",
		},
		{
			name: "unavailable keeps the devshard code",
			err:  disabled,
			want: "503 " + DevshardErrorRequestsDisabled + " requests disabled",
		},
		{
			name: "resource exhausted is 429",
			err:  connect.NewError(connect.CodeResourceExhausted, errors.New("rate")),
			want: "429  rate",
		},
		{
			name: "permission denied is 403",
			err:  connect.NewError(connect.CodePermissionDenied, errors.New("restricted")),
			want: "403  restricted",
		},
		{
			name: "invalid argument is 400",
			err:  connect.NewError(connect.CodeInvalidArgument, errors.New("bad json")),
			want: "400  bad json",
		},
		{
			name: "unauthenticated is 401",
			err:  connect.NewError(connect.CodeUnauthenticated, errors.New("handshake required")),
			want: "401  handshake required",
		},
		{
			name: "not found is 404",
			err:  connect.NewError(connect.CodeNotFound, errors.New("session not found")),
			want: "404  session not found",
		},
		{
			name: "failed precondition is 412",
			err:  connect.NewError(connect.CodeFailedPrecondition, errors.New("escrow is not open on this host")),
			want: "412  escrow is not open on this host",
		},
		{
			name: "already exists is 409",
			err:  connect.NewError(connect.CodeAlreadyExists, errors.New("version conflict")),
			want: "409  version conflict",
		},
		{
			name:   "deadline is not a host fault",
			err:    connect.NewError(connect.CodeDeadlineExceeded, context.DeadlineExceeded),
			silent: true,
		},
		{
			name:      "dial stays a transport failure",
			err:       dial,
			transport: true,
		},
		{
			name:      "eof stays a transport failure",
			err:       io.ErrUnexpectedEOF,
			transport: true,
		},
	}

	path := "/sessions/escrow-1/chat/completions"
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			admission := &bodyAdmission{}
			cfg := DefaultClientConfig()
			cfg.ParticipantKey = "shared-host"
			cfg.Admission = admission
			rpc := &RPCClient{HTTPClient: NewHTTPClient("http://127.0.0.1", "escrow-1", signer, cfg)}
			rpc.observeConnect(path, tc.err)
			if tc.silent {
				require.Empty(t, admission.results)
				require.Empty(t, admission.faults)
				return
			}
			if tc.transport {
				require.Empty(t, admission.results)
				require.Equal(t, []string{"transport"}, admission.faults)
				return
			}
			require.Empty(t, admission.faults)
			require.Equal(t, []string{tc.want}, admission.results)
		})
	}
}

type chatStatusHandler struct {
	rpcpbconnect.UnimplementedSessionServiceHandler
	err   error
	calls int
}

func (h *chatStatusHandler) Chat(context.Context, *connect.Request[rpcpb.SignedEnvelope], *connect.ServerStream[rpcpb.ChatFrame]) error {
	h.calls++
	return h.err
}

func readyChatClient(t *testing.T, base string, admission RequestAdmissionController) *RPCClient {
	t.Helper()
	signer := testutil.MustGenerateKey(t)
	cfg := DefaultClientConfig()
	cfg.InferenceTimeout = 3 * time.Second
	cfg.ParticipantKey = "shared-host"
	cfg.Admission = admission
	pc := NewPeerConn(PeerConnConfig{
		BaseURL:     base,
		HostAddress: "gonka1chatgrade",
		Signer:      signer,
		DirectMux:   true,
	})
	t.Cleanup(pc.Close)
	pc.streams.apply(&rpcpb.RateLimits{MaxStreams: 8})
	pc.budget.apply(&rpcpb.RateLimits{MessagesPerMin: 6000, MessagesBurst: 10}, time.Now())
	pc.setState(stateReady)
	pc.publishToken([]byte("tok-chat-grade"), time.Now().Add(time.Hour))
	return NewRPCClient(NewHTTPClient(base, "escrow-1", signer, cfg), pc, ParseRPCEndpoints(EndpointChat))
}

func chatRequest() host.HostRequest {
	return host.HostRequest{
		Nonce: 1,
		Payload: &host.InferencePayload{
			Prompt:    []byte("x"),
			Model:     "llama",
			MaxTokens: 1,
			StartedAt: 1,
		},
	}
}

func TestRPCClient_Send_RequestsDisabledIsUpstreamStatus(t *testing.T) {
	handler := &chatStatusHandler{}
	handler.err = connect.NewError(connect.CodeUnavailable, errors.New("requests disabled"))
	handler.err.(*connect.Error).Meta().Set(HeaderDevshardError, DevshardErrorRequestsDisabled)
	_, h := rpcpbconnect.NewSessionServiceHandler(handler)
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	admission := &bodyAdmission{}
	rpc := readyChatClient(t, srv.URL, admission)
	_, err := rpc.Send(context.Background(), chatRequest(), nil, nil)
	require.Equal(t, connect.CodeUnavailable, connect.CodeOf(err))
	require.Equal(t, 1, handler.calls)
	require.Empty(t, admission.faults)
	require.Equal(t, []string{"503 " + DevshardErrorRequestsDisabled + " requests disabled"}, admission.results)
}

func escrowLookupLimitedError() *connect.Error {
	err := connect.NewError(connect.CodeResourceExhausted, errors.New("too many escrow lookups"))
	err.Meta().Set(HeaderDevshardError, DevshardErrorEscrowLookupLimited)
	return err
}

func TestRPCClient_Send_EscrowLookupLimitedReachesLimiterOnce(t *testing.T) {
	handler := &chatStatusHandler{err: escrowLookupLimitedError()}
	_, h := rpcpbconnect.NewSessionServiceHandler(handler)
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	admission := &bodyAdmission{}
	rpc := readyChatClient(t, srv.URL, admission)
	_, err := rpc.Send(context.Background(), chatRequest(), nil, nil)
	require.Equal(t, connect.CodeResourceExhausted, connect.CodeOf(err))
	require.Equal(t, 1, handler.calls, "chat is not retried on the same host")
	require.Empty(t, admission.faults)
	require.Equal(t, []string{"429 " + DevshardErrorEscrowLookupLimited + " too many escrow lookups"}, admission.results,
		"the limiter needs the devshard code to tell this apart from a host 429")
}

func TestRPCAttempt_EscrowLookupLimitedIsNotRetried(t *testing.T) {
	signer := testutil.MustGenerateKey(t)
	cfg := DefaultClientConfig()
	cfg.ParticipantKey = "shared-host"
	admission := &bodyAdmission{}
	cfg.Admission = admission
	rpc := &RPCClient{HTTPClient: NewHTTPClient("http://127.0.0.1", "escrow-1", signer, cfg)}

	calls := 0
	err := rpc.rpcAttempt(context.Background(), rpcpbconnect.SessionServiceGetDiffsProcedure, func() error {
		calls++
		return escrowLookupLimitedError()
	})
	require.Equal(t, connect.CodeResourceExhausted, connect.CodeOf(err))
	require.Equal(t, 1, calls)
	require.Equal(t, []string{"429 " + DevshardErrorEscrowLookupLimited + " too many escrow lookups"}, admission.results)

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	calls = 0
	_ = rpc.rpcAttempt(ctx, rpcpbconnect.SessionServiceGetDiffsProcedure, func() error {
		calls++
		return connect.NewError(connect.CodeResourceExhausted, errors.New("too many diffs requests"))
	})
	require.Greater(t, calls, 1, "a plain rate limit still retries")
}

func TestRPCClient_Send_ReadyTimeoutIsTransportFault(t *testing.T) {
	prev := chatReadyTimeout
	chatReadyTimeout = 40 * time.Millisecond
	t.Cleanup(func() { chatReadyTimeout = prev })

	signer := testutil.MustGenerateKey(t)
	admission := &bodyAdmission{}
	cfg := DefaultClientConfig()
	cfg.InferenceTimeout = 5 * time.Second
	cfg.ParticipantKey = "shared-host"
	cfg.Admission = admission
	pc := NewPeerConn(PeerConnConfig{
		BaseURL:     "http://127.0.0.1:1",
		HostAddress: "gonka1chatready",
		Signer:      signer,
		DirectMux:   true,
	})
	t.Cleanup(pc.Close)
	rpc := NewRPCClient(NewHTTPClient("http://127.0.0.1:1", "escrow-1", signer, cfg), pc, ParseRPCEndpoints(EndpointChat))

	start := time.Now()
	_, err := rpc.Send(context.Background(), chatRequest(), nil, nil)
	require.ErrorIs(t, err, ErrPeerNotReady)
	require.Less(t, time.Since(start), time.Second)
	require.Equal(t, []string{"transport"}, admission.faults)
	require.Empty(t, admission.results)

	cancelled := &bodyAdmission{}
	cfg.Admission = cancelled
	rpc = NewRPCClient(NewHTTPClient("http://127.0.0.1:1", "escrow-1", signer, cfg), pc, ParseRPCEndpoints(EndpointChat))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = rpc.Send(ctx, chatRequest(), nil, nil)
	require.ErrorIs(t, err, ErrPeerNotReady)
	require.Empty(t, cancelled.faults)
}

func TestRPCClient_Send_DeadListenerIsTransportFault(t *testing.T) {
	admission := &bodyAdmission{}
	rpc := readyChatClient(t, "http://127.0.0.1:1", admission)
	_, err := rpc.Send(context.Background(), chatRequest(), nil, nil)
	require.Error(t, err)
	require.Empty(t, admission.results)
	require.NotEmpty(t, admission.faults)
}

func TestRPCClient_Send_UnauthenticatedRetriesOnce(t *testing.T) {
	handler := &chatStatusHandler{}
	handler.err = connect.NewError(connect.CodeUnauthenticated, errors.New("handshake required"))
	_, h := rpcpbconnect.NewSessionServiceHandler(handler)
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	admission := &bodyAdmission{}
	rpc := readyChatClient(t, srv.URL, admission)
	_, err := rpc.Send(context.Background(), chatRequest(), nil, nil)
	require.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
	require.Equal(t, 2, handler.calls)
	require.Empty(t, admission.faults)
	require.Len(t, admission.results, 2)
}

func TestRPCAttempt_AdmitsOnceAndObservesTheFinalStatus(t *testing.T) {
	signer := testutil.MustGenerateKey(t)
	cfg := DefaultClientConfig()
	cfg.ParticipantKey = "shared-host"
	admission := &bodyAdmission{}
	cfg.Admission = admission
	rpc := &RPCClient{HTTPClient: NewHTTPClient("http://127.0.0.1", "escrow-1", signer, cfg)}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	calls := 0
	err := rpc.rpcAttempt(ctx, rpcpbconnect.SessionServiceGetDiffsProcedure, func() error {
		calls++
		disabled := connect.NewError(connect.CodeUnavailable, errors.New("requests disabled"))
		disabled.Meta().Set(HeaderDevshardError, DevshardErrorRequestsDisabled)
		return disabled
	})
	require.Error(t, err)
	require.GreaterOrEqual(t, calls, 1)
	require.Empty(t, admission.faults)
	require.Equal(t, []string{"503 " + DevshardErrorRequestsDisabled + " requests disabled"}, admission.results)

	blocked := &bodyAdmission{allowErr: errors.New("participant request budget exhausted")}
	cfg.Admission = blocked
	rpc = &RPCClient{HTTPClient: NewHTTPClient("http://127.0.0.1", "escrow-1", signer, cfg)}
	calls = 0
	err = rpc.rpcAttempt(context.Background(), rpcpbconnect.GossipServiceNonceProcedure, func() error {
		calls++
		return nil
	})
	require.ErrorContains(t, err, "participant request budget exhausted")
	require.Equal(t, 0, calls)
	require.Empty(t, blocked.results)
	require.Empty(t, blocked.faults)
}

func TestRPCClient_Send_AllowRequestSkipsTheHost(t *testing.T) {
	handler := &chatStatusHandler{err: connect.NewError(connect.CodeInternal, errors.New("should not run"))}
	_, h := rpcpbconnect.NewSessionServiceHandler(handler)
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	admission := &bodyAdmission{allowErr: errors.New("participant request budget exhausted")}
	rpc := readyChatClient(t, srv.URL, admission)
	_, err := rpc.Send(context.Background(), chatRequest(), nil, nil)
	require.ErrorContains(t, err, "participant request budget exhausted")
	require.Equal(t, 0, handler.calls)
	require.Empty(t, admission.results)
	require.Empty(t, admission.faults)
}
