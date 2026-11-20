package transport

import (
	"testing"
	"time"

	"devshard/host"
	"devshard/transport/rpcpb"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

func TestVerifyErrorMissFitsExactlyAtConnectLimit(t *testing.T) {
	const escrowID = "verify-escrow"
	const inferenceID = 7
	payload := make([]byte, DefaultRPCLargeReadMaxBytes+1)
	envelopeSize := func(response []byte) int {
		inner, err := proto.Marshal(VerifyErrorMissRequestToProto(VerifyErrorMissRequest{
			InferenceID: inferenceID, ResponsePayload: response,
		}))
		require.NoError(t, err)
		return proto.Size(&rpcpb.SignedEnvelope{
			Payload: inner, Signature: make([]byte, 65), Timestamp: time.Now().Unix(), EscrowId: escrowID,
		})
	}
	exact := 2*DefaultRPCLargeReadMaxBytes - envelopeSize(payload[:DefaultRPCLargeReadMaxBytes])
	require.Equal(t, DefaultRPCLargeReadMaxBytes, envelopeSize(payload[:exact]))
	for _, tc := range []struct {
		size int
		fits bool
	}{{exact, true}, {exact + 1, false}} {
		fits, err := VerifyErrorMissFits(escrowID, inferenceID, host.TimeoutArtifacts{
			ResponsePayload: payload[:tc.size],
		}, nil)
		require.NoError(t, err)
		require.Equal(t, tc.fits, fits)
	}
}
