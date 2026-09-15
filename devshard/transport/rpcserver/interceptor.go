package rpcserver

import (
	"context"
	"encoding/hex"
	"errors"
	"net/http"

	"connectrpc.com/connect"

	"devshard/observability"
	"devshard/transport"
	"devshard/transport/rpcpb/rpcpbconnect"
)

// SessionHeader is the bearer from Attach. Every RPC except Attach must carry
// it; requests without a completed handshake are dropped.
const SessionHeader = transport.SessionHeader

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
// this instead of WatchRequest.session_token. Unary RPCs do not stash it.
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
	return transport.EncodeSessionToken(token)
}

// SetSessionHeader puts the Attach token on a Connect request.
func SetSessionHeader(h http.Header, token []byte) {
	transport.SetSessionHeader(h, token)
}

func isAttachPath(path string) bool {
	return path == rpcpbconnect.PeerAuthServiceAttachProcedure
}

func isWatchPath(path string) bool {
	return path == rpcpbconnect.PeerAuthServiceWatchProcedure
}

func isImplementedRPC(implemented map[string]struct{}, path string) bool {
	_, ok := implemented[path]
	return ok
}

// handshakeGate admits non-Attach RPCs from the session header before Connect
// reads the body. Unary interceptors run after protobuf decode; this wrapper
// does not. The ResponseWriter and session token are stashed only on Watch.
// Unimplemented procedures (unmounted Gossip/Payload, junk paths) are
// answered here before the peer budget is charged, so Connect never reads
// the body. Chat is implemented; it is charged after the stream slot is
// acquired. Attach is bounded by the process floor (ECDSA) and a 4 KiB
// body cap, not by origin IP.
func handshakeGate(auth *PeerAuthHandler, next http.Handler, implemented map[string]struct{}) http.Handler {
	ew := connect.NewErrorWriter()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isAttachPath(r.URL.Path) {
			if r.ContentLength > maxAttachRecvBytes {
				if _, err := auth.chargeAttach(r.Context()); err != nil {
					observability.IncPeerRPCAttach(connect.CodeOf(err).String())
					auth.observeAttach(r.Context(), err)
					_ = ew.Write(w, r, err)
					return
				}
				observability.IncPeerRPCAttach(connect.CodeResourceExhausted.String())
				sizeErr := connect.NewError(connect.CodeResourceExhausted, errors.New("attach request too large"))
				auth.observeAttach(r.Context(), sizeErr)
				_ = ew.Write(w, r, sizeErr)
				return
			}
			r.Body = http.MaxBytesReader(w, r.Body, maxAttachRecvBytes)
			next.ServeHTTP(w, r)
			return
		}
		ctx := r.Context()
		watch := isWatchPath(r.URL.Path)
		if watch {
			ctx = withResponseWriter(ctx, w)
		}
		ctx, err := admitSession(auth, ctx, r.Header, watch)
		if err != nil {
			_ = ew.Write(w, r, err)
			return
		}
		ctx = withClientIP(ctx, r.Header.Get("X-Real-IP"))
		if !isImplementedRPC(implemented, r.URL.Path) {
			r.Body = http.MaxBytesReader(w, r.Body, 0)
			_ = ew.Write(w, r, connect.NewError(connect.CodeUnimplemented, errors.New("method is not implemented")))
			return
		}
		// Chat is charged after acquireStream (finding 6). Watch is
		// weight 0 so charging here is a no-op.
		if !isChatPath(r.URL.Path) && !isWatchPath(r.URL.Path) {
			if err := auth.limiter.charge(ctx, PeerFromContext(ctx), r.URL.Path); err != nil {
				auth.observeRPC(ctx, r.URL.Path, PeerFromContext(ctx), true, false, false, false)
				_ = ew.Write(w, r, err)
				return
			}
			ctx = withRateLimitCharged(ctx)
			auth.observeRPC(ctx, r.URL.Path, PeerFromContext(ctx), false, false, false, false)
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
	// would double the hex decode and the read lock on every RPC. The fallback
	// keeps this interceptor a complete gate on its own for any mux built
	// without the wrapper.
	if PeerFromContext(ctx) != "" {
		return ctx, nil
	}
	return admitSession(s.auth, ctx, header, isWatchPath(procedure))
}

type rateLimitInterceptor struct {
	auth *PeerAuthHandler
}

func (s *rateLimitInterceptor) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		ctx, err := s.charge(ctx, req.Spec().Procedure)
		if err != nil {
			return nil, err
		}
		return next(ctx, req)
	}
}

func (s *rateLimitInterceptor) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return next
}

func (s *rateLimitInterceptor) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return func(ctx context.Context, conn connect.StreamingHandlerConn) error {
		procedure := conn.Spec().Procedure
		peer := PeerFromContext(ctx)
		if isStreamPath(procedure) {
			if err := s.auth.limiter.acquireStream(ctx, peer, procedure); err != nil {
				s.auth.observeRPC(ctx, procedure, peer, true, true, false, false)
				return err
			}
			defer s.auth.limiter.releaseStream(peer)
		}
		if !rateLimitCharged(ctx) && !isAttachPath(procedure) {
			if err := s.auth.limiter.charge(ctx, PeerFromContext(ctx), procedure); err != nil {
				s.auth.observeRPC(ctx, procedure, peer, true, false, false, false)
				return err
			}
		}
		if isStreamPath(procedure) {
			s.auth.observeRPC(ctx, procedure, peer, false, false, false, false)
		}
		return next(ctx, conn)
	}
}

func (s *rateLimitInterceptor) charge(ctx context.Context, procedure string) (context.Context, error) {
	if isAttachPath(procedure) || rateLimitCharged(ctx) {
		return ctx, nil
	}
	if err := s.auth.limiter.charge(ctx, PeerFromContext(ctx), procedure); err != nil {
		return ctx, err
	}
	return withRateLimitCharged(ctx), nil
}

func admitSession(auth *PeerAuthHandler, ctx context.Context, header http.Header, stashToken bool) (context.Context, error) {
	if auth == nil {
		observability.IncPeerRPCGate(gateReasonMissing)
		return ctx, handshakeRequired()
	}
	if auth.Closed() {
		return ctx, hostShuttingDown()
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
	ctx = withPeer(ctx, peer)
	if stashToken {
		// Watch is the only reader of TokenFromContext.
		ctx = withToken(ctx, append([]byte(nil), raw...))
	}
	return ctx, nil
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
	// Debug, not warn: this path needs no credentials.
	// gate_total{reason="forged"} is the operator signal.
	observability.Log(ctx, observability.LevelDebug, "peer RPC handshake forged",
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
