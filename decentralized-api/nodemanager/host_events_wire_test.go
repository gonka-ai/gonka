package nodemanager

import (
	"bytes"
	"testing"
	"time"

	"common/nodemanager/gen"

	"github.com/cometbft/cometbft/crypto/tmhash"
	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	cmtversion "github.com/cometbft/cometbft/proto/tendermint/version"
	cmttypes "github.com/cometbft/cometbft/types"
	gogoproto "github.com/cosmos/gogoproto/proto"
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
	assertFieldNum(t, (&gen.RuntimeConfig{}).ProtoReflect().Descriptor(), "validation_thresholds", 12)
	assertFieldNum(t, (&gen.RuntimeConfig{}).ProtoReflect().Descriptor(), "height_sync_interval_ms", 13)
	assertFieldNum(t, (&gen.RuntimeConfig{}).ProtoReflect().Descriptor(), "height_sync_ack_deadline_blocks", 14)
	assertFieldNum(t, (&gen.RuntimeConfig{}).ProtoReflect().Descriptor(), "height_sync_idle_timeout_ms", 15)
	assertFieldNum(t, (&gen.RuntimeConfig{}).ProtoReflect().Descriptor(), "height_sync_probe_stagger_ms", 16)
	assertFieldNum(t, (&gen.RuntimeConfig{}).ProtoReflect().Descriptor(), "height_sync_max_probes_per_window", 17)

	// New GetHostEvents messages: pin the contract from the plan.
	assertFieldNum(t, (&gen.GetHostEventsRequest{}).ProtoReflect().Descriptor(), "cursor", 1)
	assertFieldNum(t, (&gen.GetHostEventsRequest{}).ProtoReflect().Descriptor(), "max_wait_seconds", 2)
	assertFieldNum(t, (&gen.GetHostEventsRequest{}).ProtoReflect().Descriptor(), "subscribe", 3)
	assertFieldNum(t, (&gen.GetHostEventsRequest{}).ProtoReflect().Descriptor(), "generation", 4)
	assertFieldNum(t, (&gen.GetHostEventsRequest{}).ProtoReflect().Descriptor(), "after_height", 5)

	assertFieldNum(t, (&gen.HostEvent{}).ProtoReflect().Descriptor(), "seq", 1)
	assertFieldNum(t, (&gen.HostEvent{}).ProtoReflect().Descriptor(), "kind", 2)
	assertFieldNum(t, (&gen.HostEvent{}).ProtoReflect().Descriptor(), "observed_at_unix", 3)
	assertFieldNum(t, (&gen.HostEvent{}).ProtoReflect().Descriptor(), "escrow", 12)
	assertFieldNum(t, (&gen.HostEvent{}).ProtoReflect().Descriptor(), "maintenance", 13)
	assertFieldNum(t, (&gen.HostEvent{}).ProtoReflect().Descriptor(), "warm_key", 14)
	assertFieldNum(t, (&gen.HostEvent{}).ProtoReflect().Descriptor(), "block", 15)

	assertFieldNum(t, (&gen.BlockPayload{}).ProtoReflect().Descriptor(), "height", 1)
	assertFieldNum(t, (&gen.BlockPayload{}).ProtoReflect().Descriptor(), "hash", 2)
	assertFieldNum(t, (&gen.BlockPayload{}).ProtoReflect().Descriptor(), "header_time_unix_ms", 3)
	assertFieldNum(t, (&gen.BlockPayload{}).ProtoReflect().Descriptor(), "epoch_index", 4)
	assertFieldNum(t, (&gen.BlockPayload{}).ProtoReflect().Descriptor(), "header", 5)

	assertFieldNum(t, (&gen.GetHostEventsResponse{}).ProtoReflect().Descriptor(), "unchanged", 1)
	assertFieldNum(t, (&gen.GetHostEventsResponse{}).ProtoReflect().Descriptor(), "events", 2)
	assertFieldNum(t, (&gen.GetHostEventsResponse{}).ProtoReflect().Descriptor(), "next_cursor", 3)
	assertFieldNum(t, (&gen.GetHostEventsResponse{}).ProtoReflect().Descriptor(), "generation", 4)
	assertFieldNum(t, (&gen.GetHostEventsResponse{}).ProtoReflect().Descriptor(), "needs_reset", 5)
	assertFieldNum(t, (&gen.GetHostEventsResponse{}).ProtoReflect().Descriptor(), "open_escrow_count", 6)
	assertFieldNum(t, (&gen.GetHostEventsResponse{}).ProtoReflect().Descriptor(), "escrow_load", 7)
	assertFieldNum(t, (&gen.GetHostEventsResponse{}).ProtoReflect().Descriptor(), "supported_kinds", 8)

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
	require.Equal(t, int32(7), int32(gen.HostEventKind_HOST_EVENT_KIND_WARM_KEY_GRANTED))
	require.Equal(t, int32(8), int32(gen.HostEventKind_HOST_EVENT_KIND_WARM_KEY_REVOKED))
	require.Equal(t, int32(9), int32(gen.HostEventKind_HOST_EVENT_KIND_BLOCK))
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

func TestNodeManager_GetHostEvents_WireFormat_OldPeerOmitsNewFields(t *testing.T) {
	// A peer built before after_height and supported_kinds omits those fields.
	// proto3 zero values encode the same way: the tags are absent, and decoding
	// them yields zero.
	req := &gen.GetHostEventsRequest{Cursor: 1, Generation: 2}
	raw, err := proto.Marshal(req)
	require.NoError(t, err)
	require.NotContains(t, raw, []byte{0x28}) // field 5, varint

	decoded := &gen.GetHostEventsRequest{}
	require.NoError(t, proto.Unmarshal(raw, decoded))
	require.Equal(t, uint64(0), decoded.GetAfterHeight())
	require.Equal(t, uint64(1), decoded.GetCursor())

	resp := &gen.GetHostEventsResponse{NextCursor: 9, Generation: 2}
	raw, err = proto.Marshal(resp)
	require.NoError(t, err)
	require.NotContains(t, raw, []byte{0x42}) // field 8, length-delimited; packed enum is wire type 2

	decodedResp := &gen.GetHostEventsResponse{}
	require.NoError(t, proto.Unmarshal(raw, decodedResp))
	require.Empty(t, decodedResp.GetSupportedKinds())
	require.Equal(t, uint64(9), decodedResp.GetNextCursor())
}

func TestNodeManager_GetHostEvents_BlockPayload_HeaderRoundTrip(t *testing.T) {
	at := time.UnixMilli(1_700_000_000_123).UTC()
	hdr := cmttypes.Header{
		Version: cmtversion.Consensus{Block: 11},
		ChainID: "gonka-test",
		Height:  42,
		Time:    at,
		LastBlockID: cmttypes.BlockID{
			Hash: bytes.Repeat([]byte{1}, tmhash.Size),
			PartSetHeader: cmttypes.PartSetHeader{
				Total: 1,
				Hash:  bytes.Repeat([]byte{2}, tmhash.Size),
			},
		},
		LastCommitHash:     bytes.Repeat([]byte{3}, tmhash.Size),
		DataHash:           bytes.Repeat([]byte{4}, tmhash.Size),
		ValidatorsHash:     bytes.Repeat([]byte{5}, tmhash.Size),
		NextValidatorsHash: bytes.Repeat([]byte{6}, tmhash.Size),
		ConsensusHash:      bytes.Repeat([]byte{7}, tmhash.Size),
		AppHash:            []byte{9, 9, 9},
		LastResultsHash:    bytes.Repeat([]byte{8}, tmhash.Size),
		EvidenceHash:       bytes.Repeat([]byte{10}, tmhash.Size),
		ProposerAddress:    bytes.Repeat([]byte{11}, 20),
	}
	rawHeader, err := gogoproto.Marshal(hdr.ToProto())
	require.NoError(t, err)

	ev := &gen.HostEvent{
		Seq:  1,
		Kind: gen.HostEventKind_HOST_EVENT_KIND_BLOCK,
		Block: &gen.BlockPayload{
			Height:           hdr.Height,
			Hash:             append([]byte(nil), hdr.Hash()...),
			HeaderTimeUnixMs: hdr.Time.UnixMilli(),
			EpochIndex:       3,
			Header:           rawHeader,
		},
	}
	raw, err := proto.Marshal(ev)
	require.NoError(t, err)
	decoded := &gen.HostEvent{}
	require.NoError(t, proto.Unmarshal(raw, decoded))
	require.True(t, proto.Equal(ev, decoded))

	var gotPB cmtproto.Header
	require.NoError(t, gogoproto.Unmarshal(decoded.GetBlock().GetHeader(), &gotPB))
	got, err := cmttypes.HeaderFromProto(&gotPB)
	require.NoError(t, err)
	require.Equal(t, decoded.GetBlock().GetHash(), []byte(got.Hash()))
	require.Equal(t, decoded.GetBlock().GetHeaderTimeUnixMs(), got.Time.UnixMilli())
	require.Len(t, decoded.GetBlock().GetHash(), tmhash.Size)
}

func assertFieldNum(t *testing.T, md protoreflect.MessageDescriptor, name string, want protoreflect.FieldNumber) {
	t.Helper()
	fd := md.Fields().ByName(protoreflect.Name(name))
	require.NotNil(t, fd, "missing field %s on %s", name, md.FullName())
	require.Equal(t, want, fd.Number(), "field %s on %s", name, md.FullName())
}
