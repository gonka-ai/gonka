package rpcserver

import (
	"net/http"

	"connectrpc.com/connect"

	"devshard/transport"
	"devshard/transport/rpcpb/rpcpbconnect"
)

const maxRecvBytes = transport.DefaultRPCReadMaxBytes

type muxConfig struct {
	gossip  *GossipHandler
	payload *PayloadHandler
}

// MuxOption configures optional Connect services on NewMux.
type MuxOption func(*muxConfig)

// WithGossipService registers GossipService. Without it the path is
// unimplemented at handshakeGate so Connect never reads the body.
func WithGossipService(h *GossipHandler) MuxOption {
	return func(c *muxConfig) { c.gossip = h }
}

// WithPayloadService registers PayloadService. Requests use the 10 MiB
// JSON body cap; responses use DefaultRPCPayloadSendMaxBytes (512 MiB).
// Clients still apply PayloadReadLimit per inference.
func WithPayloadService(h *PayloadHandler) MuxOption {
	return func(c *muxConfig) { c.payload = h }
}

// NewMux serves all transport RPC services. auth is required; nil panics at
// construction so Attach cannot run on a nil receiver. Unimplemented
// services still occupy their Connect paths so the public URL shape is
// stable; handshakeGate answers those as unimplemented before Connect reads
// the body. Every RPC except Attach is dropped unless
// X-Devshard-Session names a live host-level handshake. Chat stays
// unimplemented until Phase 5.
func NewMux(auth *PeerAuthHandler, session *SessionHandler, opts ...MuxOption) http.Handler {
	if auth == nil {
		panic("rpcserver.NewMux: PeerAuthHandler is required")
	}
	var cfg muxConfig
	for _, o := range opts {
		if o != nil {
			o(&cfg)
		}
	}
	unaryOpts := []connect.HandlerOption{
		connect.WithReadMaxBytes(maxRecvBytes),
		connect.WithInterceptors(&sessionInterceptor{auth: auth}),
	}
	largeOpts := []connect.HandlerOption{
		connect.WithReadMaxBytes(transport.DefaultRPCLargeReadMaxBytes),
		connect.WithInterceptors(&sessionInterceptor{auth: auth}),
	}
	payloadOpts := []connect.HandlerOption{
		connect.WithReadMaxBytes(int(transport.DefaultMaxBodySize)),
		connect.WithSendMaxBytes(transport.DefaultRPCPayloadSendMaxBytes),
		connect.WithInterceptors(&sessionInterceptor{auth: auth}),
	}
	implemented := map[string]struct{}{
		rpcpbconnect.PeerAuthServiceAttachProcedure: {},
		rpcpbconnect.PeerAuthServiceWatchProcedure:  {},
	}
	mux := http.NewServeMux()
	mux.Handle(rpcpbconnect.NewPeerAuthServiceHandler(auth, unaryOpts...))
	if session != nil {
		mux.Handle(splitSessionService(session, unaryOpts, largeOpts))
		for _, proc := range sessionUnaryProcedures() {
			implemented[proc] = struct{}{}
		}
	} else {
		mux.Handle(rpcpbconnect.NewSessionServiceHandler(&rpcpbconnect.UnimplementedSessionServiceHandler{}, unaryOpts...))
	}
	if cfg.gossip != nil {
		mux.Handle(splitGossipService(cfg.gossip, unaryOpts, largeOpts))
		implemented[rpcpbconnect.GossipServiceNonceProcedure] = struct{}{}
		implemented[rpcpbconnect.GossipServiceTxsProcedure] = struct{}{}
	} else {
		mux.Handle(rpcpbconnect.NewGossipServiceHandler(&rpcpbconnect.UnimplementedGossipServiceHandler{}, unaryOpts...))
	}
	if cfg.payload != nil {
		mux.Handle(rpcpbconnect.NewPayloadServiceHandler(cfg.payload, payloadOpts...))
		implemented[rpcpbconnect.PayloadServiceGetPayloadProcedure] = struct{}{}
	} else {
		mux.Handle(rpcpbconnect.NewPayloadServiceHandler(&rpcpbconnect.UnimplementedPayloadServiceHandler{}, payloadOpts...))
	}
	return handshakeGate(auth, mux, implemented)
}

// splitSessionService is two SessionService handlers with different
// WithReadMaxBytes. Connect applies that option per service, not per RPC, so
// dispute unaries (prompt + catch-up diffs) cannot share the 16 KiB cap of
// GetSignatures / seed / repair. GetDiffs and GetMempool requests stay 16 KiB
// (tiny); their responses use DefaultRPCQueryReadMaxBytes (10 MiB) on the client.
func splitSessionService(session *SessionHandler, unaryOpts, largeOpts []connect.HandlerOption) (string, http.Handler) {
	pattern, small := rpcpbconnect.NewSessionServiceHandler(session, unaryOpts...)
	_, large := rpcpbconnect.NewSessionServiceHandler(session, largeOpts...)
	return splitByProcedure(pattern, small, large, sessionLargeProcedures()...)
}

func splitGossipService(gossip *GossipHandler, unaryOpts, largeOpts []connect.HandlerOption) (string, http.Handler) {
	pattern, small := rpcpbconnect.NewGossipServiceHandler(gossip, unaryOpts...)
	_, large := rpcpbconnect.NewGossipServiceHandler(gossip, largeOpts...)
	return splitByProcedure(pattern, small, large, rpcpbconnect.GossipServiceTxsProcedure)
}

func splitByProcedure(pattern string, small, large http.Handler, largeProcs ...string) (string, http.Handler) {
	largeSet := make(map[string]struct{}, len(largeProcs))
	for _, p := range largeProcs {
		largeSet[p] = struct{}{}
	}
	return pattern, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := largeSet[r.URL.Path]; ok {
			large.ServeHTTP(w, r)
			return
		}
		small.ServeHTTP(w, r)
	})
}

func sessionLargeProcedures() []string {
	return []string{
		rpcpbconnect.SessionServiceVerifyTimeoutProcedure,
		rpcpbconnect.SessionServiceVerifyErrorMissProcedure,
		rpcpbconnect.SessionServiceChallengeReceiptProcedure,
	}
}

func sessionUnaryProcedures() []string {
	return []string{
		rpcpbconnect.SessionServiceSeedHeightSyncProcedure,
		rpcpbconnect.SessionServiceRepairHeightSyncProcedure,
		rpcpbconnect.SessionServiceVerifyTimeoutProcedure,
		rpcpbconnect.SessionServiceVerifyErrorMissProcedure,
		rpcpbconnect.SessionServiceChallengeReceiptProcedure,
		rpcpbconnect.SessionServiceGetDiffsProcedure,
		rpcpbconnect.SessionServiceGetMempoolProcedure,
		rpcpbconnect.SessionServiceGetSignaturesProcedure,
	}
}

// AllProcedurePaths is every Connect procedure the peer RPC surface will serve.
func AllProcedurePaths() []string {
	return []string{
		rpcpbconnect.PeerAuthServiceAttachProcedure,
		rpcpbconnect.PeerAuthServiceWatchProcedure,
		rpcpbconnect.SessionServiceChatProcedure,
		rpcpbconnect.SessionServiceSeedHeightSyncProcedure,
		rpcpbconnect.SessionServiceRepairHeightSyncProcedure,
		rpcpbconnect.SessionServiceVerifyTimeoutProcedure,
		rpcpbconnect.SessionServiceVerifyErrorMissProcedure,
		rpcpbconnect.SessionServiceChallengeReceiptProcedure,
		rpcpbconnect.SessionServiceGetDiffsProcedure,
		rpcpbconnect.SessionServiceGetMempoolProcedure,
		rpcpbconnect.SessionServiceGetSignaturesProcedure,
		rpcpbconnect.GossipServiceNonceProcedure,
		rpcpbconnect.GossipServiceTxsProcedure,
		rpcpbconnect.PayloadServiceGetPayloadProcedure,
	}
}
