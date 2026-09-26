package transport

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"

	"common/validation"
	devtest "devshard/internal/testutil"
	"devshard/transport/rpcpb"
	"devshard/transport/rpcpb/rpcpbconnect"
)

func TestPayloadReadBucket(t *testing.T) {
	require.Equal(t, DefaultRPCPayloadMaxBytes, payloadReadBucket(0), "unknown tokens default to 64 MiB")
	require.Equal(t, rpcPayloadRead32, payloadReadBucket(512))
	require.Equal(t, rpcPayloadRead32, payloadReadBucket(int64(rpcPayloadRead32)))
	require.Equal(t, DefaultRPCPayloadMaxBytes, payloadReadBucket(int64(rpcPayloadRead32)+1))
	require.Equal(t, DefaultRPCPayloadMaxBytes, payloadReadBucket(int64(DefaultRPCPayloadMaxBytes)))
	require.Equal(t, rpcPayloadRead256, payloadReadBucket(int64(DefaultRPCPayloadMaxBytes)+1))
	require.Equal(t, rpcPayloadRead256, payloadReadBucket(int64(rpcPayloadRead256)))
	require.Equal(t, DefaultRPCPayloadSendMaxBytes, payloadReadBucket(int64(rpcPayloadRead256)+1))
	require.Equal(t, DefaultRPCPayloadSendMaxBytes, payloadReadBucket(int64(DefaultRPCPayloadSendMaxBytes)))
	require.Equal(t, DefaultRPCPayloadSendMaxBytes, payloadReadBucket(int64(DefaultRPCPayloadSendMaxBytes)+1))
	require.Equal(t, DefaultRPCPayloadMaxBytes, payloadReadBucket(validation.PayloadResponseByteLimit(4096)))
}

func TestNewRPCClientSkipsPayloadStubsWithoutEndpoint(t *testing.T) {
	signer := devtest.MustGenerateKey(t)
	pc := NewPeerConn(PeerConnConfig{
		BaseURL:      "http://127.0.0.1:1",
		HostAddress:  "gonka1nopayload",
		DoorEscrowID: "42",
		Signer:       signer,
		DirectMux:    true,
	})
	t.Cleanup(pc.Close)
	c := NewRPCClient(NewHTTPClient("http://127.0.0.1:1", "42", signer), pc, ParseRPCEndpoints(EndpointSignatures))
	t.Cleanup(c.Close)
	for i := range c.payload {
		require.Nil(t, c.payload[i])
		require.Nil(t, c.payloadGRPC[i])
	}
	stub, err := c.payloadClient(512)
	require.NoError(t, err)
	require.NotNil(t, stub)
	again, err := c.payloadClient(512)
	require.NoError(t, err)
	require.Equal(t, stub, again)
	require.NotNil(t, c.payload[0])
	require.Nil(t, c.payload[1])
}

func TestPayloadClientReusesBucketStub(t *testing.T) {
	s32 := &reusePayloadStub{id: 32}
	s64 := &reusePayloadStub{id: 64}
	s256 := &reusePayloadStub{id: 256}
	s512 := &reusePayloadStub{id: 512}
	c := &RPCClient{
		payload: [rpcPayloadReadBucketCount]rpcpbconnect.PayloadServiceClient{s32, s64, s256, s512},
	}
	a, err := c.payloadClient(512)
	require.NoError(t, err)
	b, err := c.payloadClient(1 << 20)
	require.NoError(t, err)
	require.Same(t, s32, a)
	require.Same(t, a, b, "distinct needs in the 32 MiB band must share one stub")

	c64, err := c.payloadClient(0)
	require.NoError(t, err)
	require.Same(t, s64, c64)

	c256, err := c.payloadClient(int64(DefaultRPCPayloadMaxBytes) + 1)
	require.NoError(t, err)
	require.Same(t, s256, c256)

	c512, err := c.payloadClient(int64(DefaultRPCPayloadSendMaxBytes))
	require.NoError(t, err)
	require.Same(t, s512, c512)
}

func TestPayloadDecodedSize(t *testing.T) {
	require.Equal(t, int64(0), payloadDecodedSize(nil))
	require.Equal(t, int64(0), payloadDecodedSize(&rpcpb.GetPayloadResponse{}))
	require.Equal(t, int64(7), payloadDecodedSize(&rpcpb.GetPayloadResponse{
		PromptPayload:   []byte("abc"),
		ResponsePayload: []byte("defg"),
	}))
	limit := validation.PayloadReadLimit(512)
	require.Equal(t, int64(512), limit)
	under := &rpcpb.GetPayloadResponse{ResponsePayload: make([]byte, 512)}
	require.LessOrEqual(t, payloadDecodedSize(under), limit)
	over := &rpcpb.GetPayloadResponse{ResponsePayload: make([]byte, 2048)}
	require.Greater(t, payloadDecodedSize(over), limit)
	err := errPayloadDecodedTooLarge(payloadDecodedSize(over), limit)
	require.Equal(t, connect.CodeResourceExhausted, connect.CodeOf(err))
	require.False(t, IsRetryableNonInference(err), "decoded pin must not retry like a quota")
}

type reusePayloadStub struct{ id int }

func (s *reusePayloadStub) GetPayload(context.Context, *connect.Request[rpcpb.GetPayloadRequest]) (*connect.Response[rpcpb.GetPayloadResponse], error) {
	return nil, nil
}
