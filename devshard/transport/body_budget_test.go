package transport

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"devshard/heightsync"
	"devshard/host"
	"devshard/types"
)

func worstCaseInferenceRequestBody(t *testing.T, promptBytes int64, diffs []DiffJSON) []byte {
	t.Helper()

	request := InferenceRequest{
		Diffs: diffs,
		Nonce: ^uint64(0),
		Payload: PayloadToJSON(&host.InferencePayload{
			Prompt:      make([]byte, promptBytes),
			Model:       strings.Repeat("m", 128),
			InputLength: ^uint64(0),
			MaxTokens:   ^uint64(0),
			StartedAt:   ^int64(0) >> 1,
		}),
		Stream:                true,
		ForceHeightSyncAnchor: true,
	}
	section := &heightsync.HeightSyncSection{
		ProofType:             heightsync.AnchorProofType,
		MainnetHeight:         ^int64(0) >> 1,
		MainnetBlockHashHex:   strings.Repeat("f", heightsync.MaxMainnetBlockHashHexChars),
		TimestampUnixMs:       ^int64(0) >> 1,
		Direction:             "response",
		OriginatorSenderID:    strings.Repeat("o", 64),
		OriginatorTimestampMs: ^int64(0) >> 1,
		SenderSignature:       make([]byte, heightsync.MaxOriginSignatureBytes),
	}

	body, err := MarshalWrappedInferenceRequest(CurrentInferenceEnvelopeSchemaVersion, section, request)
	require.NoError(t, err)
	return body
}

func TestMaxRawPromptBytesFitsHostBodyCap(t *testing.T) {
	body := worstCaseInferenceRequestBody(t, MaxRawPromptBytes, nil)

	budget := DefaultMaxBodySize - hostCatchUpReserveBytes
	require.LessOrEqual(t, int64(len(body)), budget,
		"a prompt at MaxRawPromptBytes must leave the whole catch-up reserve free")
}

func TestInferenceRequestEnvelopeOverheadIsDeclaredHonestly(t *testing.T) {
	measured := int64(len(worstCaseInferenceRequestBody(t, 0, nil)))

	require.LessOrEqual(t, measured, inferenceRequestEnvelopeOverheadBytes,
		"the declared overhead must cover what a payload-only request really carries besides the prompt")
	// The lower bound is driven by worstCaseInferenceRequestBody: shrinking the fixture's
	// model or height-sync section below what production sends would fail this honestly.
	require.Greater(t, measured*2, inferenceRequestEnvelopeOverheadBytes,
		"an overhead more than twice the measured one spends prompt budget on nothing")
}

// chunkSizedCatchUp mirrors user.catchUpChunkSize: the largest catch-up the finalize
// path ever sends in one request. Deliberately an absolute count, not derived from
// hostCatchUpReserveBytes, so shrinking the reserve fails this test.
const chunkSizedCatchUp = 200

func TestMaxRawPromptBytesLeavesRoomForAChunkSizedCatchUp(t *testing.T) {
	oneDiff, err := DiffToJSON(types.Diff{
		Nonce:         ^uint64(0),
		UserSig:       make([]byte, 65),
		PostStateRoot: make([]byte, 32),
	})
	require.NoError(t, err)
	diffs := make([]DiffJSON, chunkSizedCatchUp)
	for i := range diffs {
		diffs[i] = oneDiff
	}

	body := worstCaseInferenceRequestBody(t, MaxRawPromptBytes, diffs)

	require.LessOrEqual(t, int64(len(body)), DefaultMaxBodySize,
		"a max prompt plus a chunk-sized catch-up must still fit the host cap")
}

func TestMeasureInferenceRequestSplitsPromptFromDiffs(t *testing.T) {
	request := InferenceRequest{
		Payload: &PayloadJSON{Prompt: make([]byte, 3000)},
		Diffs: []DiffJSON{
			{Txs: make([]byte, 200), UserSig: make([]byte, 65), PostStateRoot: make([]byte, 32)},
			{Txs: make([]byte, 100), UserSig: make([]byte, 65)},
		},
	}

	got := measureInferenceRequest(request)

	require.EqualValues(t, 3000, got.PromptBytes)
	require.EqualValues(t, 462, got.DiffsBytes)
	require.Equal(t, 2, got.DiffCount)
}

func TestMeasureInferenceRequestHandlesMissingPayload(t *testing.T) {
	got := measureInferenceRequest(InferenceRequest{Diffs: []DiffJSON{{Txs: make([]byte, 10)}}})

	require.Zero(t, got.PromptBytes, "a payload-free catch-up request has no prompt")
	require.EqualValues(t, 10, got.DiffsBytes)
}
