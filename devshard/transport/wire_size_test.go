package transport

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"devshard/heightsync"
	"devshard/host"
	"devshard/types"
)

func diffWithPayloadSize(txBytes int) types.Diff {
	return types.Diff{
		Nonce: 4242,
		Txs: []*types.DevshardTx{{Tx: &types.DevshardTx_StartInference{StartInference: &types.MsgStartInference{
			InferenceId: 4242,
			PromptHash:  make([]byte, txBytes),
		}}}},
		UserSig:       make([]byte, 65),
		PostStateRoot: make([]byte, 32),
	}
}

func TestEncodedDiffSizeBoundsTheRealJSON(t *testing.T) {
	for _, txBytes := range []int{0, 1, 2, 3, 1024, 65536} {
		t.Run(fmt.Sprintf("tx_bytes=%d", txBytes), func(t *testing.T) {
			diff := diffWithPayloadSize(txBytes)
			wire, err := DiffToJSON(diff)
			require.NoError(t, err)
			actual, err := json.Marshal(wire)
			require.NoError(t, err)

			estimate := EncodedDiffSize(diff)
			require.GreaterOrEqual(t, estimate, len(actual),
				"the estimate must never undercount, or a chunk built on it overflows the host cap")
			require.LessOrEqual(t, estimate-len(actual), diffJSONOverheadBytes,
				"the estimate must stay tight, or the budget wastes most of the body")
		})
	}
}

func TestEncodedDiffSizeCountsAnEmptyDiff(t *testing.T) {
	estimate := EncodedDiffSize(types.Diff{Nonce: 1})
	require.Positive(t, estimate, "even an empty diff costs field names on the wire")
}

func TestEncodedPromptSizeBoundsTheRealJSON(t *testing.T) {
	for _, testCase := range []struct {
		name       string
		promptSize int
		model      string
	}{
		{name: "small prompt", promptSize: 16, model: "llama"},
		{name: "typical prompt", promptSize: 4096, model: "Qwen/Qwen3-235B-A22B-Instruct-2507"},
		{name: "model id longer than any fixed reserve", promptSize: 64, model: strings.Repeat("m", 4096)},
		{name: "model id that JSON has to escape", promptSize: 64, model: strings.Repeat("<", 2048)},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			prompt := make([]byte, testCase.promptSize)
			actual, err := json.Marshal(PayloadJSON{
				Prompt: prompt, Model: testCase.model,
				InputLength: uint64(testCase.promptSize), MaxTokens: 4096, StartedAt: 1 << 40,
			})
			require.NoError(t, err)
			require.GreaterOrEqual(t, EncodedPromptSize(prompt, testCase.model), len(actual),
				"a payload the gateway measured as fitting must actually fit")
		})
	}
}

func TestCatchUpBudgetLeavesRoomUnderTheHostCap(t *testing.T) {
	require.Positive(t, CatchUpBudgetBytes,
		"a non-positive budget refuses every request the gateway would ever send")
	require.Less(t, int64(CatchUpBudgetBytes), DefaultMaxBodySize,
		"the budget must reserve room for the envelope, signatures and the height-sync section")
}

func TestABodyFilledToTheBudgetStaysUnderTheHostCap(t *testing.T) {
	const model = "Qwen/Qwen3-235B-A22B-Instruct-2507"
	prompt := make([]byte, 64<<10)
	promptBytes := EncodedPromptSize(prompt, model)
	diff := diffWithPayloadSize(3 * (CatchUpBudgetBytes - promptBytes - 512) / 4)
	require.LessOrEqual(t, EncodedDiffSize(diff)+promptBytes, CatchUpBudgetBytes,
		"fixture must sit inside the budget, not below it")
	require.Greater(t, EncodedDiffSize(diff)+promptBytes, CatchUpBudgetBytes*9/10,
		"a fixture at half the budget would not test the ceiling")

	wire, err := DiffToJSON(diff)
	require.NoError(t, err)
	request := InferenceRequest{
		Diffs:   []DiffJSON{wire},
		Nonce:   diff.Nonce,
		Payload: PayloadToJSON(&host.InferencePayload{Prompt: prompt, Model: model, InputLength: uint64(len(prompt)), MaxTokens: 4096}),
	}

	body, err := json.Marshal(request)
	require.NoError(t, err)
	require.Less(t, int64(len(body)), DefaultMaxBodySize,
		"a body the budget admits has to pass the host's own reader")

	wrapped, err := MarshalWrappedInferenceRequest(CurrentInferenceEnvelopeSchemaVersion,
		&heightsync.HeightSyncSection{
			ChainID: "gonka-mainnet", ProofType: "anchor", Direction: "request",
			MainnetHeight: 1 << 40, MainnetBlockHashHex: strings.Repeat("a", 64), TimestampUnixMs: 1 << 40,
			OriginatorSenderID: strings.Repeat("g", 64),
		}, request)
	require.NoError(t, err)
	require.Less(t, int64(len(wrapped)), DefaultMaxBodySize,
		"the envelope reserve has to cover the envelope")

	envelopeOverhead := len(wrapped) - len(body)
	require.Positive(t, envelopeOverhead)
	require.LessOrEqual(t, int64(CatchUpBudgetBytes+envelopeOverhead), DefaultMaxBodySize,
		"a body filled to the budget plus its envelope must still fit under the host cap")
}
