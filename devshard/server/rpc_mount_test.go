package server

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"

	devshardpkg "devshard"
	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/transport"
	"devshard/transport/rpcpb"
	"devshard/transport/rpcpb/rpcpbconnect"
	"devshard/transport/rpcserver"
)

func TestRPCRouteShape(t *testing.T) {
	re := regexp.MustCompile(`^/devshard/[^/]+/sessions/[^/]+/rpc/`)
	for _, proc := range rpcserver.AllProcedurePaths() {
		url := devshardpkg.SessionRPCPath("", "1", proc)
		require.Regexp(t, re, url, proc)
	}
}

func TestRPCMount_FlagOffReturns404(t *testing.T) {
	e := echo.New()
	RegisterLazySessionRoutes(e.Group(""), payloadsOnlyResolver{resolves: "1"}, countingBinder{n: new(int)}, nil)

	for _, r := range e.Routes() {
		if strings.Contains(r.Path, "/rpc") {
			t.Fatalf("rpc route registered with flag off: %s %s", r.Method, r.Path)
		}
	}

	req := httptest.NewRequest(http.MethodPost, "/sessions/1/rpc"+rpcpbconnect.PeerAuthServiceAttachProcedure, nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	require.Equal(t, http.StatusNotFound, rec.Code)
}

func TestRPCMount_AttachWatch(t *testing.T) {
	signer := testutil.MustGenerateKey(t)
	const host = "host-under-test"
	auth := rpcserver.NewPeerAuthHandler(signing.NewSecp256k1Verifier(), host, rpcserver.PeerAuthConfig{})
	e := echo.New()
	RegisterLazySessionRoutes(e.Group(""), payloadsOnlyResolver{resolves: "1"}, countingBinder{n: new(int)}, nil,
		WithPeerRPC(auth, rpcserver.NewSessionHandler(nil)))

	httpSrv := httptest.NewServer(e)
	t.Cleanup(httpSrv.Close)
	client := rpcpbconnect.NewPeerAuthServiceClient(httpSrv.Client(), httpSrv.URL+"/sessions/1/rpc")

	attachNonce := []byte("echo-attach-nonce-0123456789ab")
	ts := time.Now().Unix()
	sig, err := transport.SignAttach(signer, host, ts, signer.Address(), attachNonce, transport.AttachProtocolVersion, nil)
	require.NoError(t, err)
	attached, err := client.Attach(context.Background(), connect.NewRequest(&rpcpb.AttachRequest{
		PeerAddress:     signer.Address(),
		AttachNonce:     attachNonce,
		ProtocolVersion: transport.AttachProtocolVersion,
		HostAddress:     host,
		Timestamp:       ts,
		Signature:       sig,
	}))
	require.NoError(t, err)
	require.Equal(t, attachNonce, attached.Msg.SessionToken)

	ctx, cancel := context.WithCancel(context.Background())
	watchReq := connect.NewRequest(&rpcpb.WatchRequest{SessionToken: attached.Msg.SessionToken})
	rpcserver.SetSessionHeader(watchReq.Header(), attached.Msg.SessionToken)
	stream, err := client.Watch(ctx, watchReq)
	require.NoError(t, err)
	require.True(t, stream.Receive(), stream.Err())
	cancel()
	_ = stream.Close()
	for stream.Receive() {
	}
}

func TestRPCMount_ConnectHandlesItsOwnRequestGzip(t *testing.T) {
	signer := testutil.MustGenerateKey(t)
	const host = "host-under-test"
	auth := rpcserver.NewPeerAuthHandler(signing.NewSecp256k1Verifier(), host, rpcserver.PeerAuthConfig{})
	e := echo.New()
	RegisterLazySessionRoutes(e.Group(""), payloadsOnlyResolver{resolves: "1"}, countingBinder{n: new(int)}, nil,
		WithPeerRPC(auth, rpcserver.NewSessionHandler(nil)))

	httpSrv := httptest.NewServer(e)
	t.Cleanup(httpSrv.Close)
	// WithSendGzip sets Content-Encoding: gzip, which the Echo group's
	// decompression middleware would otherwise consume before Connect sees it.
	client := rpcpbconnect.NewPeerAuthServiceClient(httpSrv.Client(), httpSrv.URL+"/sessions/1/rpc",
		connect.WithSendGzip())

	attachNonce := []byte("gzip-attach-nonce-0123456789ab")
	ts := time.Now().Unix()
	sig, err := transport.SignAttach(signer, host, ts, signer.Address(), attachNonce, transport.AttachProtocolVersion, nil)
	require.NoError(t, err)
	attached, err := client.Attach(context.Background(), connect.NewRequest(&rpcpb.AttachRequest{
		PeerAddress:     signer.Address(),
		AttachNonce:     attachNonce,
		ProtocolVersion: transport.AttachProtocolVersion,
		HostAddress:     host,
		Timestamp:       ts,
		Signature:       sig,
	}))
	require.NoError(t, err)
	require.Equal(t, attachNonce, attached.Msg.SessionToken)

	// A body that claims gzip but is not must be rejected by Connect, not by
	// Echo's decompression middleware — proof the middleware never ran here.
	bad, err := http.NewRequest(http.MethodPost,
		httpSrv.URL+"/sessions/1/rpc"+rpcpbconnect.PeerAuthServiceAttachProcedure,
		strings.NewReader("not gzip"))
	require.NoError(t, err)
	bad.Header.Set("Content-Type", "application/proto")
	bad.Header.Set("Content-Encoding", "gzip")
	resp, err := httpSrv.Client().Do(bad)
	require.NoError(t, err)
	t.Cleanup(func() { _ = resp.Body.Close() })
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.NotContains(t, string(body), "malformed gzip request body")
}

func TestSkipPeerRPC(t *testing.T) {
	e := echo.New()
	var ran bool
	mw := func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			ran = true
			return next(c)
		}
	}
	run := func(routePath string) bool {
		ran = false
		c := e.NewContext(httptest.NewRequest(http.MethodPost, "/", nil), httptest.NewRecorder())
		c.SetPath(routePath)
		require.NoError(t, skipPeerRPC(mw)(func(echo.Context) error { return nil })(c))
		return ran
	}

	require.False(t, run("/devshard/v5"+peerRPCRoute))
	require.True(t, run("/devshard/v5/sessions/:id/signatures"))
}

func TestStripRPCPrefix(t *testing.T) {
	proc := rpcpbconnect.PeerAuthServiceAttachProcedure
	got := stripRPCPrefix("/sessions/1/rpc"+proc, "1")
	require.Equal(t, proc, got)

	got = stripRPCPrefix("/devshard/v5/sessions/1/rpc"+proc, "1")
	require.Equal(t, proc, got)

	got = stripRPCPrefix("/sessions/1/rpc/devshard/v5/sessions/1/rpc"+proc, "1")
	require.Equal(t, proc, got, "decoy /sessions/1/rpc earlier must not steal the procedure")

	got = stripRPCPrefix("/devshard/v5/sessions/1/rpc/sessions/1/rpc"+proc, "1")
	require.Equal(t, proc, got)
}

func TestRPCMount_RewritesProcedurePathAndEscrow(t *testing.T) {
	var gotPath, gotEscrow string
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotEscrow = rpcserver.EscrowIDFromContext(r.Context())
		w.WriteHeader(http.StatusNoContent)
	})
	e := echo.New()
	mountPeerRPC(e.Group("/devshard/v5"), inner)

	for _, proc := range rpcserver.AllProcedurePaths() {
		orig := "/devshard/v5/sessions/1/rpc" + proc
		req := httptest.NewRequest(http.MethodPost, orig, nil)
		echoPath := req.URL.Path
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		require.Equal(t, http.StatusNoContent, rec.Code, proc)
		require.Equal(t, proc, gotPath, proc)
		require.Equal(t, "1", gotEscrow, proc)
		require.Equal(t, echoPath, req.URL.Path, "Echo request URL must not be mutated in place")
	}
}
