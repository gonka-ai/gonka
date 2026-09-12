package rpcserver

import (
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"

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
	} {
		_, ok := known[proc]
		require.True(t, ok, proc)
		require.True(t, isImplementedRPC(proc), proc)
	}
	require.False(t, isImplementedRPC(rpcpbconnect.GossipServiceNonceProcedure))
	require.False(t, isImplementedRPC(rpcpbconnect.SessionServiceChatProcedure))
}

func TestNewMux_NilAuthPanics(t *testing.T) {
	require.PanicsWithValue(t, "rpcserver.NewMux: PeerAuthHandler is required", func() {
		NewMux(nil, nil)
	})
}
