package rpcserver

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"

	"devshard/internal/testutil"
	"devshard/transport/rpcpb/rpcpbconnect"

	_ "devshard/transport/rpcpb"
)

// AllProcedurePaths is hand-maintained and feeds the route-shape guard. If a
// new RPC lands in the protos without being listed there, the guard silently
// stops covering it — so derive the truth from the descriptors instead.
func TestAllProcedurePathsMatchesProtos(t *testing.T) {
	want := map[string]struct{}{}
	protoregistry.GlobalFiles.RangeFilesByPackage("devshard.transport.v1", func(fd protoreflect.FileDescriptor) bool {
		services := fd.Services()
		for i := 0; i < services.Len(); i++ {
			svc := services.Get(i)
			methods := svc.Methods()
			for j := 0; j < methods.Len(); j++ {
				want["/"+string(svc.FullName())+"/"+string(methods.Get(j).Name())] = struct{}{}
			}
		}
		return true
	})
	require.NotEmpty(t, want, "no transport services found; proto registration is broken")

	got := map[string]struct{}{}
	for _, proc := range AllProcedurePaths() {
		got[proc] = struct{}{}
	}
	require.Equal(t, want, got)
}

func TestImplementedRPC_AttachWatchSignatures(t *testing.T) {
	known := make(map[string]struct{})
	for _, proc := range AllProcedurePaths() {
		known[proc] = struct{}{}
	}
	for _, proc := range []string{
		rpcpbconnect.PeerAuthServiceAttachProcedure,
		rpcpbconnect.PeerAuthServiceWatchProcedure,
		rpcpbconnect.SessionServiceGetSignaturesProcedure,
		rpcpbconnect.SessionServiceGetDiffsProcedure,
		rpcpbconnect.SessionServiceGetMempoolProcedure,
	} {
		_, ok := known[proc]
		require.True(t, ok, proc)
	}

	auth := newTestAuth(PeerAuthConfig{})
	ts := httptest.NewServer(withTestEscrow(NewMux(auth, NewSessionHandler(stubLookup{core: stubCore{}}))))
	t.Cleanup(ts.Close)
	signer := testutil.MustGenerateKey(t)
	attached := attach(t, rpcpbconnect.NewPeerAuthServiceClient(ts.Client(), ts.URL), signer, []byte("impl-gate-attach-nonce-0123"))

	code := func(proc string) int {
		t.Helper()
		req, err := http.NewRequest(http.MethodPost, ts.URL+proc, nil)
		require.NoError(t, err)
		req.Header.Set("Content-Type", "application/proto")
		SetSessionHeader(req.Header, attached.SessionToken)
		resp, err := ts.Client().Do(req)
		require.NoError(t, err)
		t.Cleanup(func() { _ = resp.Body.Close() })
		return resp.StatusCode
	}
	require.NotEqual(t, http.StatusNotImplemented, code(rpcpbconnect.SessionServiceGetSignaturesProcedure))
	require.Equal(t, http.StatusNotImplemented, code(rpcpbconnect.SessionServiceChatProcedure))
	require.Equal(t, http.StatusNotImplemented, code(rpcpbconnect.GossipServiceNonceProcedure))
	require.Equal(t, http.StatusNotImplemented, code(rpcpbconnect.PayloadServiceGetPayloadProcedure))
}

func TestNewMux_NilAuthPanics(t *testing.T) {
	require.PanicsWithValue(t, "rpcserver.NewMux: PeerAuthHandler is required", func() {
		NewMux(nil, nil)
	})
}
