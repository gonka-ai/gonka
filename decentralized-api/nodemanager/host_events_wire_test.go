package nodemanager

import (
	"testing"

	"devshard/nodemanager/gen"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

func TestNodeManager_GetHostEvents_WireFormat_FieldNumbersStable(t *testing.T) {
	// Existing RPCs / messages must keep their field numbers when GetHostEvents is added.
	assertFieldNum(t, (&gen.AcquireMLNodeRequest{}).ProtoReflect().Descriptor(), "model", 1)
	assertFieldNum(t, (&gen.AcquireMLNodeRequest{}).ProtoReflect().Descriptor(), "excluded_nodes", 2)
	assertFieldNum(t, (&gen.AcquireMLNodeRequest{}).ProtoReflect().Descriptor(), "escrow_id", 3)

	assertFieldNum(t, (&gen.GetRuntimeConfigRequest{}).ProtoReflect().Descriptor(), "client_params_block_height", 1)
	assertFieldNum(t, (&gen.GetRuntimeConfigRequest{}).ProtoReflect().Descriptor(), "max_wait_seconds", 2)

	assertFieldNum(t, (&gen.GetRuntimeConfigResponse{}).ProtoReflect().Descriptor(), "unchanged", 1)
	assertFieldNum(t, (&gen.GetRuntimeConfigResponse{}).ProtoReflect().Descriptor(), "config", 2)

	assertFieldNum(t, (&gen.RuntimeConfig{}).ProtoReflect().Descriptor(), "params_block_height", 1)
	assertFieldNum(t, (&gen.RuntimeConfig{}).ProtoReflect().Descriptor(), "vote_threshold_factor", 11)

	// New GetHostEvents messages: pin the contract from the plan.
	assertFieldNum(t, (&gen.GetHostEventsRequest{}).ProtoReflect().Descriptor(), "cursor", 1)
	assertFieldNum(t, (&gen.GetHostEventsRequest{}).ProtoReflect().Descriptor(), "max_wait_seconds", 2)
	assertFieldNum(t, (&gen.GetHostEventsRequest{}).ProtoReflect().Descriptor(), "subscribe", 3)
	assertFieldNum(t, (&gen.GetHostEventsRequest{}).ProtoReflect().Descriptor(), "generation", 4)

	assertFieldNum(t, (&gen.HostEvent{}).ProtoReflect().Descriptor(), "seq", 1)
	assertFieldNum(t, (&gen.HostEvent{}).ProtoReflect().Descriptor(), "kind", 2)
	assertFieldNum(t, (&gen.HostEvent{}).ProtoReflect().Descriptor(), "observed_at_unix", 3)
	assertFieldNum(t, (&gen.HostEvent{}).ProtoReflect().Descriptor(), "escrow", 12)
	assertFieldNum(t, (&gen.HostEvent{}).ProtoReflect().Descriptor(), "maintenance", 13)

	assertFieldNum(t, (&gen.GetHostEventsResponse{}).ProtoReflect().Descriptor(), "unchanged", 1)
	assertFieldNum(t, (&gen.GetHostEventsResponse{}).ProtoReflect().Descriptor(), "events", 2)
	assertFieldNum(t, (&gen.GetHostEventsResponse{}).ProtoReflect().Descriptor(), "next_cursor", 3)
	assertFieldNum(t, (&gen.GetHostEventsResponse{}).ProtoReflect().Descriptor(), "generation", 4)
	assertFieldNum(t, (&gen.GetHostEventsResponse{}).ProtoReflect().Descriptor(), "needs_reset", 5)
	assertFieldNum(t, (&gen.GetHostEventsResponse{}).ProtoReflect().Descriptor(), "open_escrow_count", 6)
	assertFieldNum(t, (&gen.GetHostEventsResponse{}).ProtoReflect().Descriptor(), "escrow_load", 7)

	assertFieldNum(t, (&gen.EscrowLoad{}).ProtoReflect().Descriptor(), "escrow_id", 1)
	assertFieldNum(t, (&gen.EscrowLoad{}).ProtoReflect().Descriptor(), "requests_per_min", 2)

	assertFieldNum(t, (&gen.NodeCapacityEntry{}).ProtoReflect().Descriptor(), "node_id", 1)
	assertFieldNum(t, (&gen.NodeCapacityEntry{}).ProtoReflect().Descriptor(), "model", 2)
	assertFieldNum(t, (&gen.NodeCapacityEntry{}).ProtoReflect().Descriptor(), "max_concurrent", 3)
	assertFieldNum(t, (&gen.NodeCapacityEntry{}).ProtoReflect().Descriptor(), "lock_count", 4)
	assertFieldNum(t, (&gen.NodeCapacityEntry{}).ProtoReflect().Descriptor(), "status", 5)

	assertFieldNum(t, (&gen.ListNodeCapacityResponse{}).ProtoReflect().Descriptor(), "nodes", 1)
	assertFieldNum(t, (&gen.ListNodeCapacityResponse{}).ProtoReflect().Descriptor(), "served_at_unix", 2)

	require.Equal(t, int32(0), int32(gen.HostEventKind_HOST_EVENT_KIND_UNSPECIFIED))
	require.Equal(t, int32(3), int32(gen.HostEventKind_HOST_EVENT_KIND_ESCROW_CREATED))
	require.Equal(t, int32(4), int32(gen.HostEventKind_HOST_EVENT_KIND_ESCROW_SETTLED))
	require.Equal(t, int32(5), int32(gen.HostEventKind_HOST_EVENT_KIND_MAINTENANCE_SCHEDULED))
	require.Equal(t, int32(6), int32(gen.HostEventKind_HOST_EVENT_KIND_MAINTENANCE_CANCELED))
}

func TestNodeManager_GetHostEvents_WireFormat_RoundTrip(t *testing.T) {
	req := &gen.GetHostEventsRequest{
		Cursor:         42,
		MaxWaitSeconds: 60,
		Subscribe: []gen.HostEventKind{
			gen.HostEventKind_HOST_EVENT_KIND_ESCROW_CREATED,
			gen.HostEventKind_HOST_EVENT_KIND_ESCROW_SETTLED,
		},
		Generation: 7,
	}
	raw, err := proto.Marshal(req)
	require.NoError(t, err)

	decoded := &gen.GetHostEventsRequest{}
	require.NoError(t, proto.Unmarshal(raw, decoded))
	require.True(t, proto.Equal(req, decoded))

	resp := &gen.GetHostEventsResponse{
		Unchanged: false,
		Events: []*gen.HostEvent{{
			Seq:            43,
			Kind:           gen.HostEventKind_HOST_EVENT_KIND_ESCROW_CREATED,
			ObservedAtUnix: 1_700_000_000,
			Escrow: &gen.EscrowPayload{
				EscrowId:   9,
				EpochIndex: 2,
				ModelId:    "m",
			},
		}},
		NextCursor:      43,
		Generation:      7,
		NeedsReset:      false,
		OpenEscrowCount: 1,
	}
	raw, err = proto.Marshal(resp)
	require.NoError(t, err)
	decodedResp := &gen.GetHostEventsResponse{}
	require.NoError(t, proto.Unmarshal(raw, decodedResp))
	require.True(t, proto.Equal(resp, decodedResp))
}

func TestNodeManager_GetHostEvents_WireFormat_LegacyRuntimeConfigBytesStillDecode(t *testing.T) {
	// Same legacy GetRuntimeConfigRequest bytes as the 3a back-compat test.
	legacyBytes := []byte{0x08, 0x64} // tag 1, varint 100
	req := &gen.GetRuntimeConfigRequest{}
	require.NoError(t, proto.Unmarshal(legacyBytes, req))
	require.Equal(t, int64(100), req.ClientParamsBlockHeight)
	require.Equal(t, int32(0), req.MaxWaitSeconds)
}

func assertFieldNum(t *testing.T, md protoreflect.MessageDescriptor, name string, want protoreflect.FieldNumber) {
	t.Helper()
	fd := md.Fields().ByName(protoreflect.Name(name))
	require.NotNil(t, fd, "missing field %s on %s", name, md.FullName())
	require.Equal(t, want, fd.Number(), "field %s on %s", name, md.FullName())
}
