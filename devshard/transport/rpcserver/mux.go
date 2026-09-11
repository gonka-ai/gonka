package rpcserver

import (
	"net/http"

	"connectrpc.com/connect"

	"devshard/transport/rpcpb/rpcpbconnect"
)

const maxRecvBytes = 10 << 20

// NewMux serves all transport RPC services. auth is required; nil panics at
// construction so Attach cannot run on a nil receiver. Unimplemented
// services still occupy their Connect paths so the public URL shape is
// stable. Every RPC except Attach is dropped unless X-Devshard-Session names a
// live host-level handshake. The outer handshakeGate runs before Connect reads
// the body.
func NewMux(auth *PeerAuthHandler, session *SessionHandler) http.Handler {
	if auth == nil {
		panic("rpcserver.NewMux: PeerAuthHandler is required")
	}
	opts := []connect.HandlerOption{
		connect.WithReadMaxBytes(maxRecvBytes),
		connect.WithInterceptors(&sessionInterceptor{auth: auth}),
	}
	mux := http.NewServeMux()
	mux.Handle(rpcpbconnect.NewPeerAuthServiceHandler(auth, opts...))
	if session != nil {
		mux.Handle(rpcpbconnect.NewSessionServiceHandler(session, opts...))
	} else {
		mux.Handle(rpcpbconnect.NewSessionServiceHandler(&rpcpbconnect.UnimplementedSessionServiceHandler{}, opts...))
	}
	mux.Handle(rpcpbconnect.NewGossipServiceHandler(&rpcpbconnect.UnimplementedGossipServiceHandler{}, opts...))
	mux.Handle(rpcpbconnect.NewPayloadServiceHandler(&rpcpbconnect.UnimplementedPayloadServiceHandler{}, opts...))
	return handshakeGate(auth, mux)
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
