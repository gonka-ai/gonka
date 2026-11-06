package host

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"devshard"
	"devshard/testenv/replay"
	"devshard/types"
)

// recordedRenderer answers with the counts the build's render server gave
// for one recording. It checks the turn a voter rebuilt from the stored body
// carries the text the build served.
type recordedRenderer struct {
	t        *testing.T
	counts   replay.RenderCounts
	turns    int
	requests [][]byte
	// anyTurn takes a turn the executor changed from the recording.
	anyTurn bool
}

func (r *recordedRenderer) Serves(string) bool { return true }

func (r *recordedRenderer) CountPrompt(_ context.Context, _ string, request []byte) (uint64, string, error) {
	r.requests = append(r.requests, request)
	return r.counts.Input, "recorded", nil
}

func (r *recordedRenderer) CountTurn(_ context.Context, _ string, request []byte, turn map[string]any) (uint64, string, error) {
	r.turns++
	r.requests = append(r.requests, request)
	if !r.anyTurn {
		require.Equal(r.t, r.counts.TurnBytes, turnTextBytes(turn), "the rebuilt turn carries the served text")
	}
	return r.counts.Output, "recorded", nil
}

func turnTextBytes(turn map[string]any) uint64 {
	n := len(turn["content"].(string))
	if reasoning, ok := turn["reasoning"].(string); ok {
		n += len(reasoning)
	}
	calls, _ := turn["tool_calls"].([]any)
	for _, raw := range calls {
		function := raw.(map[string]any)["function"].(map[string]any)
		n += len(function["name"].(string)) + len(function["arguments"].(string))
	}
	return uint64(n)
}

// recordedChallenge is a recording as a voter fetches it: the stored body,
// the request the executor's ML node ran, and the claim it finished with.
type recordedChallenge struct {
	name   string
	sample replay.Sample
	fetch  devshard.ChallengeFetch
	rec    types.InferenceRecord
}

func loadRecordedChallenges(t *testing.T) []recordedChallenge {
	t.Helper()
	files, err := filepath.Glob("../testenv/mockopenai/testdata/vllm_recorded_*.jsonl.gz")
	require.NoError(t, err)
	require.Len(t, files, 3)
	var out []recordedChallenge
	for _, file := range files {
		dataset, err := replay.LoadFile(file)
		require.NoError(t, err)
		build := strings.TrimSuffix(filepath.Base(file), ".jsonl.gz")
		for i := 0; i < dataset.Len(); i++ {
			sample, ok := dataset.Sample(i)
			require.True(t, ok)
			require.NotNil(t, sample.Render, "%s %s: no render counts", build, sample.Name)
			events := make([]string, 0, len(sample.Recorded.Stream)+1)
			var usage struct {
				PromptTokens     uint64 `json:"prompt_tokens"`
				CompletionTokens uint64 `json:"completion_tokens"`
			}
			for _, chunk := range sample.Recorded.Stream {
				events = append(events, "data: "+string(chunk))
				var event struct {
					Usage *json.RawMessage `json:"usage"`
				}
				require.NoError(t, json.Unmarshal(chunk, &event))
				if event.Usage != nil && string(*event.Usage) != "null" {
					require.NoError(t, json.Unmarshal(*event.Usage, &usage))
				}
			}
			events = append(events, "data: [DONE]")
			body, err := json.Marshal(map[string]any{"events": events})
			require.NoError(t, err)
			out = append(out, recordedChallenge{
				name:   build + "/" + sample.Model + "/" + sample.Name,
				sample: sample,
				fetch:  devshard.ChallengeFetch{Outcome: devshard.ChallengeFetchFetched, Response: body, Request: sample.Request},
				rec:    types.InferenceRecord{Model: sample.Model, InputTokens: usage.PromptTokens, OutputTokens: usage.CompletionTokens},
			})
		}
	}
	return out
}

func (c recordedChallenge) vote(t *testing.T) (keptChallengeVote, *usageCount, *recordedRenderer) {
	t.Helper()
	return c.voteWith(t, &recordedRenderer{t: t, counts: *c.sample.Render})
}

func (c recordedChallenge) voteWith(t *testing.T, renderer *recordedRenderer) (keptChallengeVote, *usageCount, *recordedRenderer) {
	t.Helper()
	h := &Host{renderer: renderer}
	verdict, count, reject, cause := h.fetchedVerdict(context.Background(), c.fetch, c.rec)
	require.Empty(t, reject, cause)
	return verdict, count, renderer
}

// withPositions appends n generated positions that carry no text to a stored
// body, as an executor that pads its positions to the claim would.
func withPositions(t *testing.T, stored []byte, n int) []byte {
	t.Helper()
	var envelope struct {
		Events []string `json:"events"`
	}
	require.NoError(t, json.Unmarshal(stored, &envelope))
	positions := make([]any, n)
	for i := range positions {
		positions[i] = map[string]any{"token": "1", "logprob": -0.1}
	}
	chunk, err := json.Marshal(map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{}, "logprobs": map[string]any{"content": positions}}}})
	require.NoError(t, err)
	last := len(envelope.Events) - 1
	events := append(append(append([]string(nil), envelope.Events[:last]...), "data: "+string(chunk)), envelope.Events[last:]...)
	body, err := json.Marshal(map[string]any{"events": events})
	require.NoError(t, err)
	return body
}

// withEvent inserts one SSE data payload before the closing [DONE] of a
// stored body.
func withEvent(t *testing.T, stored []byte, data string) []byte {
	t.Helper()
	var envelope struct {
		Events []string `json:"events"`
	}
	require.NoError(t, json.Unmarshal(stored, &envelope))
	last := len(envelope.Events) - 1
	events := append(append(append([]string(nil), envelope.Events[:last]...), "data: "+data), envelope.Events[last:]...)
	body, err := json.Marshal(map[string]any{"events": events})
	require.NoError(t, err)
	return body
}

func editedRequest(t *testing.T, request []byte, edit func(map[string]any)) []byte {
	t.Helper()
	var body map[string]any
	require.NoError(t, json.Unmarshal(request, &body))
	edit(body)
	out, err := json.Marshal(body)
	require.NoError(t, err)
	return out
}

// spelledByteByByte are the recordings whose sampler wrote half or more of
// the multi-byte tokens as single bytes. Their claim runs 2.5 to 4.5 times
// the render of the same text, past the voters' bound of twice it: the
// bound holds for answers close to the tokenizer's own spelling, and the
// gateway's byte bound, not the voters, is what covers these.
var spelledByteByByte = map[string]bool{"split_bytes_50": true, "split_bytes_75": true, "split_bytes_100": true, "split_bytes_100_mixed": true}

// forcedByTheClient are the recordings answered under a client logit_bias of
// +100 on a byte fragment: positions with no text. The gateway rejects
// logit_bias and the executor drops it, so no honest executor serves such an
// answer, and the voters count it as any other.
var forcedByTheClient = map[string]bool{"forced_fragment": true}

// Test flow:
//  1. Vote on every recording of every ML node build, with the counts its own render server gave.
//  2. Require NONE on every honest answer, the rendered checks to run wherever the request rule allows them, and
//     the turn rebuilt from the stored body to carry the served text.
//  3. The answers spelled byte by byte and the ones forced by a client logit_bias are the exception: voted
//     inflated on the rendered output.
func TestRecordedHonestAnswersPassTheVotersCounts(t *testing.T) {
	challenges := loadRecordedChallenges(t)
	outputCounted, inputCounted, spelled := 0, 0, 0
	for _, c := range challenges {
		t.Run(c.name, func(t *testing.T) {
			verdict, count, renderer := c.vote(t)
			require.Equal(t, c.rec.OutputTokens, count.positions, "an honest claim is its positions")
			if name := strings.SplitN(c.sample.Name, "@", 2)[0]; spelledByteByByte[name] || forcedByTheClient[name] {
				require.Equal(t, types.ChallengeCheck_CHALLENGE_CHECK_RENDERED_OUTPUT, verdict.Check, "%+v", count)
				spelled++
				return
			}
			require.Equal(t, types.ChallengeFault_CHALLENGE_FAULT_NONE, verdict.Fault, "%+v", count)
			if count.inputSkipped == "" {
				inputCounted++
				require.Equal(t, c.sample.Render.Input, count.input)
			}
			if renderer.turns > 0 && count.outputSkipped == "" {
				outputCounted++
			}
		})
	}
	require.Greater(t, outputCounted, 300)
	require.Equal(t, len(challenges)-spelled, inputCounted, "every recording is a text request")
}

// Test flow:
//  1. Take honest recordings and forge their claims or bodies the ways an executor could.
//  2. Require each forgery to be voted USAGE_INFLATED by the check that sees it, with the deviation of the claim
//     over the count, and the bound itself to be the last honest claim.
func TestRecordedForgeriesAreVotedInflated(t *testing.T) {
	byName := map[string]recordedChallenge{}
	for _, c := range loadRecordedChallenges(t) {
		byName[c.name] = c
	}
	models := []string{types.DeepSeekV4FlashModelID, types.MiniMaxM27ModelID, types.GLM53FlashModelID}
	for _, build := range []string{"vllm_recorded_mlnode310", "vllm_recorded_mlnode400"} {
		for _, model := range models {
			plain, ok := byName[build+"/"+model+"/plain_short"]
			require.True(t, ok, build+" "+model)
			prefix := build + "/" + model + "/"

			t.Run(prefix+"claim past its positions", func(t *testing.T) {
				c := plain
				c.rec.OutputTokens += positionSlack + 1
				verdict, _, _ := c.vote(t)
				require.Equal(t, types.ChallengeFault_CHALLENGE_FAULT_USAGE_INFLATED, verdict.Fault)
				require.Equal(t, types.ChallengeCheck_CHALLENGE_CHECK_POSITIONS, verdict.Check)
				require.Equal(t, int64(positionSlack+1), verdict.Deviation)

				c.rec.OutputTokens--
				verdict, _, _ = c.vote(t)
				require.Equal(t, types.ChallengeFault_CHALLENGE_FAULT_NONE, verdict.Fault)
			})

			t.Run(prefix+"positions padded past the rendered turn", func(t *testing.T) {
				c := plain
				allowance, _ := types.UsageAllowanceFor(model)
				limit := 2*c.sample.Render.Output + allowance.ServiceTokens
				pad := limit - c.rec.OutputTokens
				c.fetch.Response = withPositions(t, plain.fetch.Response, int(pad))
				c.rec.OutputTokens = limit
				verdict, _, _ := c.vote(t)
				require.Equal(t, types.ChallengeFault_CHALLENGE_FAULT_NONE, verdict.Fault, "the bound itself is honest")

				c.fetch.Response = withPositions(t, plain.fetch.Response, int(pad+1))
				c.rec.OutputTokens = limit + 1
				verdict, _, _ = c.vote(t)
				require.Equal(t, types.ChallengeFault_CHALLENGE_FAULT_USAGE_INFLATED, verdict.Fault)
				require.Equal(t, types.ChallengeCheck_CHALLENGE_CHECK_RENDERED_OUTPUT, verdict.Check)
				require.Equal(t, int64(limit+1-c.sample.Render.Output), verdict.Deviation)
			})

			t.Run(prefix+"claim past the max_tokens of the executed request", func(t *testing.T) {
				c := plain
				c.fetch.Request = editedRequest(t, plain.fetch.Request, func(body map[string]any) {
					body["max_tokens"] = c.rec.OutputTokens - 1
					body["max_completion_tokens"] = c.rec.OutputTokens - 1
				})
				verdict, _, _ := c.vote(t)
				require.Equal(t, types.ChallengeFault_CHALLENGE_FAULT_USAGE_INFLATED, verdict.Fault)
				require.Equal(t, types.ChallengeCheck_CHALLENGE_CHECK_POSITIONS, verdict.Check)
				require.Equal(t, int64(1), verdict.Deviation)
			})

			t.Run(prefix+"a broken call on a request without tools is rendered", func(t *testing.T) {
				c := plain
				allowance, _ := types.UsageAllowanceFor(model)
				limit := 2*c.sample.Render.Output + allowance.ServiceTokens + allowance.ToolCallHeader
				call := `{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"c","type":"function","function":{"name":"f","arguments":"x"}}]}}]}`
				c.fetch.Response = withPositions(t, withEvent(t, plain.fetch.Response, call), int(limit-c.rec.OutputTokens+1))
				c.rec.OutputTokens = limit + 1
				verdict, count, _ := c.voteWith(t, &recordedRenderer{t: t, counts: *c.sample.Render, anyTurn: true})
				require.Empty(t, count.outputSkipped, "only a cut last call of a request with tools leaves the output out")
				require.Equal(t, types.ChallengeCheck_CHALLENGE_CHECK_RENDERED_OUTPUT, verdict.Check, "%+v", count)
			})

			t.Run(prefix+"a broken call before the last one", func(t *testing.T) {
				c := plain
				calls := `{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"a","type":"function","function":{"name":"f","arguments":"{\"x\": [1"}},` +
					`{"index":1,"id":"b","type":"function","function":{"name":"f","arguments":"{}"}}]}}]}`
				c.fetch.Response = withEvent(t, plain.fetch.Response, calls)
				verdict, count, _ := c.voteWith(t, &recordedRenderer{t: t, counts: *c.sample.Render, anyTurn: true})
				require.Equal(t, types.ChallengeFault_CHALLENGE_FAULT_USAGE_INFLATED, verdict.Fault, "%+v", count)
				require.Equal(t, types.ChallengeCheck_CHALLENGE_CHECK_RENDERED_OUTPUT, verdict.Check)
			})

			t.Run(prefix+"padding served as a message a streaming client does not read", func(t *testing.T) {
				c := plain
				allowance, _ := types.UsageAllowanceFor(model)
				limit := 2*c.sample.Render.Output + allowance.ServiceTokens
				pad := int(limit - c.rec.OutputTokens + 1)
				positions := make([]any, pad)
				for i := range positions {
					positions[i] = map[string]any{"token": "1", "logprob": -0.1}
				}
				chunk, err := json.Marshal(map[string]any{"choices": []any{map[string]any{"index": 0,
					"message": map[string]any{"content": strings.Repeat("x", 4*pad)}, "logprobs": map[string]any{"content": positions}}}})
				require.NoError(t, err)
				c.fetch.Response = withEvent(t, plain.fetch.Response, string(chunk))
				c.rec.OutputTokens = limit + 1
				verdict, count, _ := c.vote(t)
				require.Equal(t, types.ChallengeCheck_CHALLENGE_CHECK_RENDERED_OUTPUT, verdict.Check, "%+v", count)
			})

			t.Run(prefix+"an event the gateway skips leaves the answer as served", func(t *testing.T) {
				c := plain
				c.fetch.Response = withEvent(t, plain.fetch.Response, `{"choices":[{"index":"0","delta":{"content":5}}]}`)
				verdict, count, _ := c.vote(t)
				require.Equal(t, types.ChallengeFault_CHALLENGE_FAULT_NONE, verdict.Fault, "%+v", count)
				require.Equal(t, c.rec.OutputTokens, count.positions)
			})

			t.Run(prefix+"min_tokens of the executed request raises the bound", func(t *testing.T) {
				c := plain
				allowance, _ := types.UsageAllowanceFor(model)
				limit := 64 + 2*c.sample.Render.Output + allowance.ServiceTokens
				c.fetch.Response = withPositions(t, plain.fetch.Response, int(limit-c.rec.OutputTokens+1))
				c.rec.OutputTokens = limit + 1
				verdict, _, _ := c.vote(t)
				require.Equal(t, types.ChallengeCheck_CHALLENGE_CHECK_RENDERED_OUTPUT, verdict.Check, "without min_tokens the claim is inflated")

				c.fetch.Request = editedRequest(t, plain.fetch.Request, func(body map[string]any) { body["min_tokens"] = 64 })
				verdict, _, _ = c.vote(t)
				require.Equal(t, types.ChallengeCheck_CHALLENGE_CHECK_RENDERED_OUTPUT, verdict.Check, "one past the bound")
				c.rec.OutputTokens = limit
				c.fetch.Response = withPositions(t, plain.fetch.Response, int(limit-plain.rec.OutputTokens))
				verdict, _, _ = c.vote(t)
				require.Equal(t, types.ChallengeFault_CHALLENGE_FAULT_NONE, verdict.Fault)
			})

			t.Run(prefix+"input past the honest spread", func(t *testing.T) {
				c := plain
				allowance, _ := types.UsageAllowanceFor(model)
				c.rec.InputTokens = c.sample.Render.Input + allowance.InputSpread
				verdict, _, _ := c.vote(t)
				require.Equal(t, types.ChallengeFault_CHALLENGE_FAULT_NONE, verdict.Fault)

				c.rec.InputTokens++
				verdict, _, _ = c.vote(t)
				require.Equal(t, types.ChallengeFault_CHALLENGE_FAULT_USAGE_INFLATED, verdict.Fault)
				require.Equal(t, types.ChallengeCheck_CHALLENGE_CHECK_RENDERED_INPUT, verdict.Check)
				require.Equal(t, int64(allowance.InputSpread+1), verdict.Deviation)
			})

			// The voter reads no logit_bias: an executor that ran one is not the
			// honest executor, which drops it.
			t.Run(prefix+"forced fragments with or without the client's logit_bias", func(t *testing.T) {
				c, ok := byName[prefix+"forced_fragment"]
				require.True(t, ok)
				verdict, _, _ := c.vote(t)
				require.Equal(t, types.ChallengeFault_CHALLENGE_FAULT_USAGE_INFLATED, verdict.Fault)
				require.Equal(t, types.ChallengeCheck_CHALLENGE_CHECK_RENDERED_OUTPUT, verdict.Check)

				c.fetch.Request = editedRequest(t, c.fetch.Request, func(body map[string]any) { delete(body, "logit_bias") })
				verdict, _, _ = c.vote(t)
				require.Equal(t, types.ChallengeFault_CHALLENGE_FAULT_USAGE_INFLATED, verdict.Fault)
				require.Equal(t, types.ChallengeCheck_CHALLENGE_CHECK_RENDERED_OUTPUT, verdict.Check)
			})
		}
	}
}

// Test flow:
//  1. Vote on every DeepSeek recording, with the counts its own render server gave.
//  2. Require the requests the voter renders to name the thinking mode when the answer carried reasoning and the
//     request named no mode, and to be the executed request otherwise.
func TestAVoterNamesTheThinkingModeOfAReasonedAnswer(t *testing.T) {
	named := 0
	for _, c := range loadRecordedChallenges(t) {
		if c.sample.Model != types.DeepSeekV4FlashModelID {
			continue
		}
		t.Run(c.name, func(t *testing.T) {
			_, count, renderer := c.vote(t)
			answer, err := parseServedAnswer(c.fetch.Response)
			require.NoError(t, err)
			var request map[string]any
			require.NoError(t, json.Unmarshal(c.sample.Request, &request))
			kwargs, _ := request["chat_template_kwargs"].(map[string]any)
			_, mode := kwargs["thinking"]
			require.Equal(t, answer.reasoning != "" && !mode, count.thinkingNamed)
			require.NotEmpty(t, renderer.requests)
			for _, sent := range renderer.requests {
				if !count.thinkingNamed {
					require.Equal(t, []byte(c.sample.Request), sent)
					continue
				}
				want := editedRequest(t, c.sample.Request, func(body map[string]any) {
					kwargs, _ := body["chat_template_kwargs"].(map[string]any)
					if kwargs == nil {
						kwargs = map[string]any{}
					}
					kwargs["thinking"] = true
					body["chat_template_kwargs"] = kwargs
				})
				require.JSONEq(t, string(want), string(sent))
			}
			if count.thinkingNamed {
				named++
			}
		})
	}
	require.Greater(t, named, 40, "the recordings of builds that reason by default")
}

// Test flow:
//  1. Name the thinking mode in requests that name none, name it otherwise, or carry template options of another
//     shape.
//  2. Require the mode added beside the other options, and a request that names it or whose options are not an
//     object sent as it was.
func TestWithThinkingKeepsTheModeARequestNamed(t *testing.T) {
	for _, tc := range []struct {
		request, want string
		named         bool
	}{
		{`{"messages":[]}`, `{"messages":[],"chat_template_kwargs":{"thinking":true}}`, true},
		{`{"chat_template_kwargs":null}`, `{"chat_template_kwargs":{"thinking":true}}`, true},
		{`{"chat_template_kwargs":{"x":1}}`, `{"chat_template_kwargs":{"x":1,"thinking":true}}`, true},
		{`{"chat_template_kwargs":{"thinking":false}}`, `{"chat_template_kwargs":{"thinking":false}}`, false},
		{`{"chat_template_kwargs":"thinking"}`, `{"chat_template_kwargs":"thinking"}`, false},
	} {
		got, named, err := withThinking([]byte(tc.request), "thinking")
		require.NoError(t, err, tc.request)
		require.Equal(t, tc.named, named, tc.request)
		require.JSONEq(t, tc.want, string(got), tc.request)
	}
	_, _, err := withThinking([]byte(`null`), "thinking")
	require.Error(t, err)
}

// Test flow:
//  1. Vote on recordings whose request rule leaves the rendered output check out: a grammar, a call cut by max_tokens
//     inside an argument the schema does not type as a string. A DeepSeek call cut inside an object argument is
//     served with whole arguments, so it is counted; a call cut inside a string argument is rendered as served.
//  2. Require no turn render where the check is left out, NONE, and the positions still counted.
func TestRecordedRequestRuleLeavesTheRenderedOutputOut(t *testing.T) {
	for _, c := range loadRecordedChallenges(t) {
		name := c.sample.Name
		if !strings.HasPrefix(name, "guided_json") && !strings.HasPrefix(name, "cut_in_") {
			continue
		}
		t.Run(c.name, func(t *testing.T) {
			verdict, count, renderer := c.vote(t)
			require.Equal(t, types.ChallengeFault_CHALLENGE_FAULT_NONE, verdict.Fault)
			if strings.HasPrefix(name, "guided_json") {
				require.Equal(t, "grammar_constrained", count.outputSkipped)
			}
			// A string argument is served as generated: its cut is rendered as
			// served, and the honest claim stays within the bound.
			if strings.HasPrefix(name, "cut_in_string_arg") {
				require.Empty(t, count.outputSkipped)
				require.NotZero(t, renderer.turns)
			}
			if count.outputSkipped != "" {
				require.Zero(t, renderer.turns)
			}
			c.rec.OutputTokens += positionSlack + 1
			verdict, _, _ = c.vote(t)
			require.Equal(t, types.ChallengeCheck_CHALLENGE_CHECK_POSITIONS, verdict.Check)
		})
	}
}

// Test flow:
//  1. Vote on an honest recording of a model in the table with no renderer, and with one that does not serve it.
//  2. Require no vote with the no_render reject; a body short of its claim is still voted on its positions.
func TestAVoterWithoutARendererDoesNotVote(t *testing.T) {
	c := loadRecordedChallenges(t)[0]
	for name, renderer := range map[string]ChallengeRenderer{"no renderer": nil, "another model's renderer": NewRenderClient(map[string][]string{"other": {"http://render"}}, nil)} {
		t.Run(name, func(t *testing.T) {
			h := &Host{renderer: renderer}
			_, _, reject, _ := h.fetchedVerdict(context.Background(), c.fetch, c.rec)
			require.Equal(t, ChallengeRejectNoRender, reject)

			forged := c.rec
			forged.OutputTokens += positionSlack + 1
			verdict, _, reject, _ := h.fetchedVerdict(context.Background(), c.fetch, forged)
			require.Empty(t, reject)
			require.Equal(t, types.ChallengeCheck_CHALLENGE_CHECK_POSITIONS, verdict.Check)
		})
	}
}

// Test flow:
//  1. Read the open argument of cut tool call arguments, and the schema type of arguments in a request's tools.
//  2. Require the key being written at the cut, no key for a cut inside a key or outside an object, and a string
//     type only for "string" alone or with "null".
func TestACutCallIsPlacedByItsArgumentAndSchema(t *testing.T) {
	for arguments, want := range map[string]string{
		`{"city": "Par`:                "city",
		`{"city": "Paris", "loc": {"l`: "loc",
		`{"a": 1, "b": [1, 2`:          "b",
		`{"a": 1, "b":`:                "b",
	} {
		key, ok := openArgument(arguments)
		require.True(t, ok, arguments)
		require.Equal(t, want, key, arguments)
	}
	for _, arguments := range []string{`{"ci`, `[1, 2`, ``, `"x`} {
		_, ok := openArgument(arguments)
		require.False(t, ok, arguments)
	}

	terms, err := readExecutedTerms([]byte(`{"tools":[{"type":"function","function":{"name":"f","parameters":{"type":"object","properties":{
		"s":{"type":"string"},"n":{"type":["string","null"]},"o":{"type":"object"},"m":{"type":["string","integer"]},"u":{}}}}}]}`))
	require.NoError(t, err)
	require.True(t, terms.stringArgument("f", "s"))
	require.True(t, terms.stringArgument("f", "n"))
	for _, argument := range []string{"o", "m", "u", "missing"} {
		require.False(t, terms.stringArgument("f", argument), argument)
	}
	require.False(t, terms.stringArgument("g", "s"))
}
