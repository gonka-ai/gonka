package harness

import (
	"testing"

	"devshard/testenv/mockopenai"

	"github.com/stretchr/testify/require"
)

func execRec(model string, seed int, startMs int64, prompt, node string) mockopenai.RequestRecord {
	return mockopenai.RequestRecord{Phase: mockopenai.PhaseExecute, Model: model, Seed: &seed,
		StartUnixMs: startMs, PromptKey: prompt, Node: node, Outcome: "ok", Status: 200}
}

func TestAnExecutionOfTheSameIdWithAnotherMaxTokensIsNotThisInferences(t *testing.T) {
	// From a load run: escrow 7 inference 809 asked for 32 tokens; escrow 9 reused id 809 34 s later
	// with 512, inside the match window. The finish of 7/809 must be judged against its own execution.
	ref := InferenceRef{Escrow: "7", Inference: 809}
	chain := HostChain{Host: "primary", Inferences: map[InferenceRef]*InferenceFacts{
		ref: {Started: true, StartedAt: 1000, Model: "model-a", MaxTokens: 32, Finished: true, OutputTokens: 8},
	}}
	wire := func(maxTokens int) *mockopenai.WireView { return &mockopenai.WireView{MaxTokens: &maxTokens} }
	own := execRec("model-a", 809, 1_002_000, "p", "mock-openai-5")
	own.TokensOut, own.UsageCompletion, own.Wire = 32, 8, wire(32)
	other := execRec("model-a", 809, 1_034_000, "p", "mock-openai-5")
	other.TokensOut, other.UsageCompletion, other.Wire = 512, 128, wire(512)
	unlogged := execRec("model-a", 809, 1_040_000, "p", "mock-openai-5") // no wire view: kept
	unlogged.TokensOut, unlogged.UsageCompletion = 32, 8

	in := PromiseInput{Diffs: ExportDiffs{Hosts: []HostChain{chain}}, Mock: []mockopenai.RequestRecord{other, own, unlogged},
		Requests: []ClientRequest{{RequestID: "r-1", PromptKey: "p", HTTPStatus: 200, Executions: []Execution{{InferenceRef: ref}}}}}
	require.Empty(t, violations(CheckPromises(in), PaidOutputWithinProduced), "the other escrow's 128 is not compared with this finish")

	chain.Inferences[ref].OutputTokens = 16
	got := violations(CheckPromises(in), PaidOutputWithinProduced)
	require.Len(t, got, 1, "a finish its own executions do not explain is still a mismatch")
	require.Contains(t, got[0].Detail, "Finish output_tokens 16, the ML produced [8 8]")
}
