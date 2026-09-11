package rpcserver

import (
	"context"
	"encoding/hex"
	"errors"
	"net/http"

	"connectrpc.com/connect"

	"devshard/observability"
	"devshard/transport/rpcpb/rpcpbconnect"
)

// SessionHeader is the bearer from Attach. Every RPC except Attach must carry
// it; requests without a completed handshake are dropped.
const SessionHeader = "X-Devshard-Session"

type peerKey struct{}
type tokenKey struct{}
type responseWriterKey struct{}

// PeerFromContext is the address bound by a successful handshake.
func PeerFromContext(ctx context.Context) string {
	v, _ := ctx.Value(peerKey{}).(string)
	return v
}

func withPeer(ctx context.Context, addr string) context.Context {
	return context.WithValue(ctx, peerKey{}, addr)
}

// TokenFromContext is the Attach token the interceptor admitted. Watch uses
// this instead of WatchRequest.session_token.
func TokenFromContext(ctx context.Context) []byte {
	v, _ := ctx.Value(tokenKey{}).([]byte)
	return v
}

func withToken(ctx context.Context, token []byte) context.Context {
	return context.WithValue(ctx, tokenKey{}, token)
}

func withResponseWriter(ctx context.Context, w http.ResponseWriter) context.Context {
	return context.WithValue(ctx, responseWriterKey{}, w)
}

func responseWriterFromContext(ctx context.Context) http.ResponseWriter {
	w, _ := ctx.Value(responseWriterKey{}).(http.ResponseWriter)
	return w
}

// EncodeSessionToken is the on-wire form of AttachResponse.session_token.
func EncodeSessionToken(token []byte) string {
	return hex.EncodeToString(token)
}

// SetSessionHeader puts the Attach token on a Connect request.
func SetSessionHeader(h http.Header, token []byte) {
	h.Set(SessionHeader, EncodeSessionToken(token))
}

func isAttachPath(path string) bool {
	return path == rpcpbconnect.PeerAuthServiceAttachProcedure
}

func isWatchPath(path string) bool {
	return path == rpcpbconnect.PeerAuthServiceWatchProcedure
}

// handshakeGate admits non-Attach RPCs from the session header before Connect
// reads the body. Unary interceptors run after protobuf decode; this wrapper
// does not. The ResponseWriter is stashed only on Watch (write deadline).
func handshakeGate(auth *PeerAuthHandler, next http.Handler) http.Handler {
	ew := connect.NewErrorWriter()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isAttachPath(r.URL.Path) {
			if r.ContentLength > maxAttachRecvBytes {
				observability.IncPeerRPCAttach(connect.CodeResourceExhausted.String())
				_ = ew.Write(w, r, connect.NewError(connect.CodeResourceExhausted, errors.New("attach request too large")))
				return
			}
			r.Body = http.MaxBytesReader(w, r.Body, maxAttachRecvBytes)
			next.ServeHTTP(w, r)
			return
		}
		ctx := r.Context()
		if isWatchPath(r.URL.Path) {
			ctx = withResponseWriter(ctx, w)
		}
		ctx, err := admitSession(auth, ctx, r.Header)
		if err != nil {
			_ = ew.Write(w, r, err)
			return
		}
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

type sessionInterceptor struct {
	auth *PeerAuthHandler
}

func (s *sessionInterceptor) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		ctx, err := s.admit(ctx, req.Spec().Procedure, req.Header())
		if err != nil {
			return nil, err
		}
		return next(ctx, req)
	}
}

func (s *sessionInterceptor) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return next
}

func (s *sessionInterceptor) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return func(ctx context.Context, conn connect.StreamingHandlerConn) error {
		ctx, err := s.admit(ctx, conn.Spec().Procedure, conn.RequestHeader())
		if err != nil {
			return err
		}
		return next(ctx, conn)
	}
}

func (s *sessionInterceptor) admit(ctx context.Context, procedure string, header http.Header) (context.Context, error) {
	if isAttachPath(procedure) {
		return ctx, nil
	}
	// handshakeGate already admitted this request before Connect read the
	// body, and nothing off the wire can set a context value, so a bound peer
	// means the token was decoded and looked up one layer out. Admitting again
	// would double the hex decode, the read lock, and the token copy on every
	// RPC. The fallback keeps this interceptor a complete gate on its own for
	// any mux built without the wrapper.
	if PeerFromContext(ctx) != "" {
		return ctx, nil
	}
	return admitSession(s.auth, ctx, header)
}

func admitSession(auth *PeerAuthHandler, ctx context.Context, header http.Header) (context.Context, error) {
	if auth == nil {
		observability.IncPeerRPCGate(gateReasonMissing)
		return ctx, handshakeRequired()
	}
	enc := header.Get(SessionHeader)
	if len(enc) == 0 {
		observability.IncPeerRPCGate(gateReasonMissing)
		return ctx, handshakeRequired()
	}
	if len(enc) > maxAttachNonceBytes*2 {
		observability.IncPeerRPCGate(gateReasonOversized)
		return ctx, handshakeRequired()
	}
	raw, err := hex.DecodeString(enc)
	if err != nil || len(raw) == 0 {
		observeGateForged(ctx, err)
		return ctx, handshakeRequired()
	}
	peer, ok, expired := auth.inspectToken(raw)
	if expired {
		observability.IncPeerRPCGate(gateReasonExpired)
		return ctx, handshakeRequired()
	}
	if !ok {
		observeGateForged(ctx, errInvalidSessionToken)
		return ctx, handshakeRequired()
	}
	observability.IncPeerRPCGate(gateReasonAdmitted)
	observability.Log(ctx, observability.LevelDebug, "peer RPC handshake admitted",
		observability.StageRequest, observability.WherePeerRPCGate, EscrowIDFromContext(ctx), observability.ReasonOK, nil,
		"peer", peer)
	tok := append([]byte(nil), raw...)
	return withToken(withPeer(ctx, peer), tok), nil
}

const (
	gateReasonAdmitted  = "admitted"
	gateReasonMissing   = "missing"
	gateReasonForged    = "forged"
	gateReasonExpired   = "expired"
	gateReasonOversized = "oversized"
)

var errInvalidSessionToken = errors.New("invalid session token")

func observeGateForged(ctx context.Context, err error) {
	observability.IncPeerRPCGate(gateReasonForged)
	observability.Log(ctx, observability.LevelWarn, "peer RPC handshake forged",
		observability.StageRequest, observability.WherePeerRPCGate, EscrowIDFromContext(ctx), observability.ReasonInvalidSignature, err)
}

// requirePeer is the data-RPC identity: handshake peer + URL escrow.
// Handlers must not read a sender from the request body.
func requirePeer(ctx context.Context) (peer, escrow string, err error) {
	peer = PeerFromContext(ctx)
	if peer == "" {
		return "", "", handshakeRequired()
	}
	escrow = EscrowIDFromContext(ctx)
	if escrow == "" {
		return "", "", connect.NewError(connect.CodeInvalidArgument, errors.New("missing escrow id"))
	}
	return peer, escrow, nil
}

func handshakeRequired() error {
	return connect.NewError(connect.CodeUnauthenticated, errors.New("handshake required"))
}
