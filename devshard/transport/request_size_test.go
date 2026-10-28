package transport

import (
	"testing"

	"devshard/types"

	"github.com/stretchr/testify/require"
)

// Test flow:
//  1. Price a 3 MiB prompt on the wire.
//  2. Assert it costs its raw bytes, a one-byte tag and a four-byte length, plus the envelope allowance.
func TestPromptWireBytesCountsTheRawBytesItTravelsAs(t *testing.T) {
	const raw = 3 << 20

	wire := PromptWireBytes(raw)

	require.Equal(t, raw+1+4+envelopeBytes, wire,
		"a prompt is envelope field 4, so it travels as raw bytes rather than base64")
}

// Test flow:
//  1. Ask whether a prompt just under the host cap less the envelope fits one body with no catch-up.
//  2. Assert it does, and that a prompt of the whole cap does not.
func TestAPromptFitsUpToTheHostCapLessTheEnvelope(t *testing.T) {
	require.True(t, InferenceRequestFits(MaxHostRequestBytes-envelopeBytes-5, nil))
	require.False(t, InferenceRequestFits(MaxHostRequestBytes, nil))
}

func TestCatchUpIsCountedAgainstTheSameBody(t *testing.T) {
	diffs := make([]types.Diff, 96)
	for index := range diffs {
		diffs[index] = types.Diff{UserSig: make([]byte, 100<<10), PostStateRoot: make([]byte, 32)}
	}

	require.True(t, InferenceRequestFits(1<<20, nil))
	require.False(t, InferenceRequestFits(1<<20, diffs),
		"a backlog the prompt leaves no room for is what turns a servable request into a 413")
}

func TestTheWireWalkStopsAtTheLimit(t *testing.T) {
	diffs := make([]types.Diff, 1_000)
	for index := range diffs {
		diffs[index] = types.Diff{UserSig: make([]byte, 1<<20)}
	}

	require.True(t, DiffsExceedWireBytes(diffs, 1<<20),
		"the walk answers from the first diffs rather than reading a history of any length")
	require.False(t, DiffsExceedWireBytes(nil, 1))
}

func TestChunkingStopsWhereTheBodyWouldNotFit(t *testing.T) {
	diffs := make([]types.Diff, 20)
	for index := range diffs {
		diffs[index] = types.Diff{UserSig: make([]byte, 1<<20)}
	}

	require.Equal(t, 7, DiffsWithinWireBytes(diffs, 10<<20),
		"seven diffs of a megabyte are what fits ten once each is base64")
	require.Equal(t, 20, DiffsWithinWireBytes(diffs, 100<<20))
}

// One diff past the limit still has to move, or the walk that carries it never advances again.
func TestASingleOversizedDiffIsStillTaken(t *testing.T) {
	require.Equal(t, 1, DiffsWithinWireBytes([]types.Diff{{UserSig: make([]byte, 20<<20)}}, 1<<20))
	require.Zero(t, DiffsWithinWireBytes(nil, 1<<20))
}

// Test flow:
//  1. For each table case, build a prompt and a catch-up of real diffs, and marshal the chat body a host is sent for them.
//  2. Assert the wire estimate is never below the body's real length and at most envelopeBytes above it.
//  3. Assert a 9 MiB prompt with no catch-up fits one host-bound body.
func TestTheWireEstimateTracksTheBodyAHostIsSent(t *testing.T) {
	startTx := func(id uint64) *types.DevshardTx {
		return &types.DevshardTx{Tx: &types.DevshardTx_StartInference{StartInference: &types.MsgStartInference{
			InferenceId: id, Model: "Qwen/Qwen3-235B-A22B-Instruct-2507-FP8", PromptHash: make([]byte, 32), InputLength: 1_000, MaxTokens: 4_096, StartedAt: 1_700_000_000,
		}}}
	}
	cases := []struct {
		name   string
		prompt int
		diffs  int
	}{
		{name: "a prompt alone", prompt: 9 << 20},
		{name: "a small prompt with a long catch-up", prompt: 4 << 10, diffs: 200},
		{name: "a large prompt with a short catch-up", prompt: 7 << 20, diffs: 16},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			diffs := make([]types.Diff, testCase.diffs)
			wireDiffs := make([]DiffJSON, testCase.diffs)
			for index := range diffs {
				nonce := uint64(1<<40) + uint64(index)
				diffs[index] = types.Diff{Nonce: nonce, Txs: []*types.DevshardTx{startTx(nonce), startTx(nonce + 1)}, UserSig: make([]byte, 65), PostStateRoot: make([]byte, 32)}
				wire, err := DiffToJSON(diffs[index])
				require.NoError(t, err)
				wireDiffs[index] = wire
			}
			body, _, err := marshalChatBody(nil, InferenceRequest{
				Diffs: wireDiffs, Nonce: 1 << 40, Stream: true,
				Payload: &PayloadJSON{Prompt: make([]byte, testCase.prompt), Model: "Qwen/Qwen3-235B-A22B-Instruct-2507-FP8", InputLength: 1_000, MaxTokens: 4_096, StartedAt: 1_700_000_000},
			})
			require.NoError(t, err)

			estimate := PromptWireBytes(testCase.prompt)
			for _, diff := range diffs {
				estimate += diffWireBytes(diff)
			}
			require.GreaterOrEqual(t, estimate, len(body), "the estimate is below the body a host is sent")
			require.LessOrEqual(t, estimate, len(body)+envelopeBytes, "the estimate refuses bodies a host would accept")
		})
	}
	require.True(t, InferenceRequestFits(9<<20, nil))
}
