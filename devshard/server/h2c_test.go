package server

import (
	"context"
	"crypto/tls"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"
	"golang.org/x/net/http2"

	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/transport"
	"devshard/transport/rpcpb"
	"devshard/transport/rpcpb/rpcpbconnect"
	"devshard/transport/rpcserver"
)

func TestH2CServerAdvertisesStreamCap(t *testing.T) {
	s := H2CServer()
	require.Equal(t, transport.DefaultRPCMaxStreams, s.MaxConcurrentStreams)
	require.NotZero(t, s.MaxConcurrentStreams, "zero would hide SETTINGS_MAX_CONCURRENT_STREAMS")
}

func TestEnableH2CWrapsEchoListen(t *testing.T) {
	e := echo.New()
	EnableH2C(e)
	require.NotNil(t, e.Server.Handler)
	require.NotEqual(t, http.Handler(e), e.Server.Handler)
}

func TestH2C_HTTP1JSONStillWorks(t *testing.T) {
	e := echo.New()
	e.HideBanner = true
	e.GET("/healthz", func(c echo.Context) error { return c.String(http.StatusOK, "ok") })
	e.POST("/sessions/:id/chat/completions", func(c echo.Context) error {
		return c.JSON(http.StatusOK, map[string]string{"object": "chat.completion"})
	})
	srv := httptest.NewServer(H2CHandler(e))
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL + "/healthz")
	require.NoError(t, err)
	t.Cleanup(func() { _ = resp.Body.Close() })
	require.Equal(t, 1, resp.ProtoMajor)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, "ok", string(body))

	post, err := http.Post(srv.URL+"/sessions/1/chat/completions", "application/json", nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = post.Body.Close() })
	require.Equal(t, 1, post.ProtoMajor)
	require.Equal(t, http.StatusOK, post.StatusCode)
}

func TestH2C_WithoutH2CFailsClosed(t *testing.T) {
	e := echo.New()
	e.GET("/rpc/", func(c echo.Context) error { return c.NoContent(http.StatusOK) })
	srv := httptest.NewServer(e) // Echo only — no h2c.NewHandler
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL + "/rpc/")
	require.NoError(t, err)
	t.Cleanup(func() { _ = resp.Body.Close() })
	require.Equal(t, 1, resp.ProtoMajor, "default client is HTTP/1.1 fan-out, not h2")

	client := newH2CClient(t, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/rpc/", nil)
	require.NoError(t, err)
	h2resp, err := client.Do(req)
	if err == nil {
		defer h2resp.Body.Close()
		t.Fatalf("h2c client succeeded Proto=%s status=%d; h2 without h2c.NewHandler must fail closed", h2resp.Proto, h2resp.StatusCode)
	}
}

func TestH2C_MultiplexesConcurrentStreams(t *testing.T) {
	const n = 8
	var sawProto string
	var protoMu sync.Mutex
	started := make(chan struct{}, n)
	release := make(chan struct{})
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		protoMu.Lock()
		sawProto = r.Proto
		protoMu.Unlock()
		started <- struct{}{}
		<-release
		w.WriteHeader(http.StatusNoContent)
	})

	var dials atomic.Int32
	srv := httptest.NewServer(H2CHandler(h))
	t.Cleanup(srv.Close)

	client := newH2CClient(t, func() { dials.Add(1) })
	var wg sync.WaitGroup
	errCh := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			resp, err := client.Get(srv.URL + "/rpc/")
			if err != nil {
				errCh <- err
				return
			}
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
			if resp.StatusCode != http.StatusNoContent {
				errCh <- errStatus(resp.StatusCode)
			}
			if resp.ProtoMajor != 2 {
				errCh <- errProto(resp.Proto)
			}
		}()
	}
	for i := 0; i < n; i++ {
		select {
		case <-started:
		case <-time.After(3 * time.Second):
			t.Fatal("streams did not start; HTTP/2 multiplexing is off")
		}
	}
	require.Equal(t, int32(1), dials.Load(), "overlapping h2c streams must share one TCP connection")
	protoMu.Lock()
	require.Equal(t, "HTTP/2.0", sawProto)
	protoMu.Unlock()
	close(release)
	wg.Wait()
	close(errCh)
	for err := range errCh {
		require.NoError(t, err)
	}
}

func TestH2C_AttachChat(t *testing.T) {
	signer := testutil.MustGenerateKey(t)
	const host = "host-under-test"
	auth := rpcserver.NewPeerAuthHandler(signing.NewSecp256k1Verifier(), host, rpcserver.PeerAuthConfig{})
	t.Cleanup(auth.Close)
	e := echo.New()
	e.HideBanner = true
	e.GET("/healthz", func(c echo.Context) error { return c.String(http.StatusOK, "ok") })
	RegisterLazySessionRoutes(e.Group(""), payloadsOnlyResolver{resolves: "1"}, countingBinder{n: new(int)}, nil,
		WithPeerRPC(auth, rpcserver.NewSessionHandler(staticH2CLookup{core: h2cChatCore{}})))
	EnableH2C(e)
	srv := httptest.NewServer(e.Server.Handler)
	t.Cleanup(srv.Close)

	plain, err := http.Get(srv.URL + "/healthz")
	require.NoError(t, err)
	t.Cleanup(func() { _ = plain.Body.Close() })
	require.Equal(t, 1, plain.ProtoMajor)
	require.Equal(t, http.StatusOK, plain.StatusCode)

	h2 := newH2CClient(t, nil)
	rpcBase := srv.URL + "/sessions/1/rpc"
	authClient := rpcpbconnect.NewPeerAuthServiceClient(h2, rpcBase)
	nonce := []byte("h2c-attach-nonce-0123456789ab")
	ts := time.Now().Unix()
	sig, err := transport.SignAttach(signer, host, ts, signer.Address(), nonce, transport.AttachProtocolVersion, nil)
	require.NoError(t, err)
	attached, err := authClient.Attach(context.Background(), connect.NewRequest(&rpcpb.AttachRequest{
		PeerAddress:     signer.Address(),
		AttachNonce:     nonce,
		ProtocolVersion: transport.AttachProtocolVersion,
		HostAddress:     host,
		Timestamp:       ts,
		Signature:       sig,
	}))
	require.NoError(t, err)
	require.Equal(t, nonce, attached.Msg.SessionToken)

	session := rpcpbconnect.NewSessionServiceClient(h2, rpcBase)
	env, err := transport.SignEnvelope(signer, "1", []byte(`{"nonce":1}`), time.Now().Unix())
	require.NoError(t, err)
	stream, err := session.Chat(context.Background(), withH2CSession(connect.NewRequest(env), attached.Msg.SessionToken))
	require.NoError(t, err)
	var chunks [][]byte
	for stream.Receive() {
		chunks = append(chunks, append([]byte(nil), stream.Msg().GetChunk()...))
	}
	require.NoError(t, stream.Err())
	require.NotEmpty(t, chunks)
}

func TestH2C_NativeGRPCAttachChatAndGetSignatures(t *testing.T) {
	signer := testutil.MustGenerateKey(t)
	const host = "host-under-test"
	auth := rpcserver.NewPeerAuthHandler(signing.NewSecp256k1Verifier(), host, rpcserver.PeerAuthConfig{})
	t.Cleanup(auth.Close)
	e := echo.New()
	e.HideBanner = true
	RegisterLazySessionRoutes(e.Group(""), payloadsOnlyResolver{resolves: "1"}, countingBinder{n: new(int)}, nil,
		WithPeerRPC(auth, rpcserver.NewSessionHandler(staticH2CLookup{core: h2cChatCore{}})))
	EnableH2C(e)
	srv := httptest.NewServer(e.Server.Handler)
	t.Cleanup(srv.Close)

	h2 := newH2CClient(t, nil)
	rpcBase := srv.URL + "/sessions/1/rpc"
	authClient := rpcpbconnect.NewPeerAuthServiceClient(h2, rpcBase, connect.WithGRPC())
	nonce := []byte("h2c-grpc-attach-nonce-012345")
	ts := time.Now().Unix()
	sig, err := transport.SignAttach(signer, host, ts, signer.Address(), nonce, transport.AttachProtocolVersion, nil)
	require.NoError(t, err)
	attached, err := authClient.Attach(context.Background(), connect.NewRequest(&rpcpb.AttachRequest{
		PeerAddress:     signer.Address(),
		AttachNonce:     nonce,
		ProtocolVersion: transport.AttachProtocolVersion,
		HostAddress:     host,
		Timestamp:       ts,
		Signature:       sig,
	}))
	require.NoError(t, err)
	require.Equal(t, nonce, attached.Msg.SessionToken)

	session := rpcpbconnect.NewSessionServiceClient(h2, rpcBase, connect.WithGRPC())
	sigs, err := session.GetSignatures(context.Background(), withH2CSession(
		connect.NewRequest(&rpcpb.GetSignaturesRequest{Nonce: 1}), attached.Msg.SessionToken))
	require.NoError(t, err)
	require.Empty(t, sigs.Msg.GetSignatures())

	env, err := transport.SignEnvelope(signer, "1", []byte(`{"nonce":1}`), time.Now().Unix())
	require.NoError(t, err)
	stream, err := session.Chat(context.Background(), withH2CSession(connect.NewRequest(env), attached.Msg.SessionToken))
	require.NoError(t, err)
	var chunks [][]byte
	for stream.Receive() {
		chunks = append(chunks, append([]byte(nil), stream.Msg().GetChunk()...))
	}
	require.NoError(t, stream.Err())
	require.NotEmpty(t, chunks)
}

type errStatus int

func (e errStatus) Error() string { return http.StatusText(int(e)) }

type errProto string

func (e errProto) Error() string { return "proto " + string(e) }

func newH2CClient(t *testing.T, onDial func()) *http.Client {
	t.Helper()
	tr := &http2.Transport{
		AllowHTTP: true,
		DialTLSContext: func(ctx context.Context, network, addr string, _ *tls.Config) (net.Conn, error) {
			if onDial != nil {
				onDial()
			}
			var d net.Dialer
			return d.DialContext(ctx, network, addr)
		},
	}
	t.Cleanup(tr.CloseIdleConnections)
	return &http.Client{Transport: tr, Timeout: 10 * time.Second}
}

func withH2CSession[T any](req *connect.Request[T], token []byte) *connect.Request[T] {
	rpcserver.SetSessionHeader(req.Header(), token)
	return req
}

type h2cChatCore struct{}

func (h2cChatCore) ServeGetSignatures(uint64) (map[uint32][]byte, error) {
	return map[uint32][]byte{}, nil
}

func (h2cChatCore) AllowsSender(string) bool { return true }

func (h2cChatCore) IsOwner(string) bool { return true }

func (h2cChatCore) ServeInference(_ context.Context, call transport.InferenceCall) error {
	if _, err := call.Sink.Write([]byte("h2c-chat")); err != nil {
		return err
	}
	call.Sink.Flush()
	return nil
}

type staticH2CLookup struct{ core rpcserver.SessionCore }

func (s staticH2CLookup) SessionServerExisting(string) (rpcserver.SessionCore, error) {
	return s.core, nil
}

func (s staticH2CLookup) SessionForParticipant(string, string) (rpcserver.SessionCore, error) {
	return s.core, nil
}

func (s staticH2CLookup) SessionForOwner(string, string) (rpcserver.SessionCore, error) {
	return s.core, nil
}
