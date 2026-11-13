package validation

import (
	"bytes"
	"encoding/json"
	"io"
	"math"
	"math/rand"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"testing"

	"common/completionapi"

	"github.com/stretchr/testify/require"
)

const (
	tapeTestModel = "Qwen/Qwen3-0.6B"
	tapeTestSteps = 300
)

// A small model: every position has a random distribution over sixteen tokens.
func tapeTestLogprobs() []map[int]float64 {
	rng := rand.New(rand.NewSource(7))
	out := make([]map[int]float64, tapeTestSteps)
	for t := range out {
		logits := make([]float64, 16)
		z := 0.0
		for i := range logits {
			logits[i] = rng.NormFloat64() * 2
			z += math.Exp(logits[i])
		}
		out[t] = make(map[int]float64, len(logits))
		for i, l := range logits {
			out[t][i] = l - math.Log(z)
		}
	}
	return out
}

// What vLLM outputs under a seed: the argmax of logprob + noise at every position.
func tapeAnswer(logprobs []map[int]float64, seed uint64) []int {
	out := make([]int, len(logprobs))
	for t, lp := range logprobs {
		best := math.Inf(-1)
		for tok, l := range lp {
			if s := l + TapeNoise(seed, uint32(t), modelVocab[tapeTestModel], tok); s > best {
				best, out[t] = s, tok
			}
		}
	}
	return out
}

func greedyAnswer(logprobs []map[int]float64) []int {
	out := make([]int, len(logprobs))
	for t, lp := range logprobs {
		best := math.Inf(-1)
		for tok, l := range lp {
			if l > best {
				best, out[t] = l, tok
			}
		}
	}
	return out
}

// topOf keeps the answer's token and the k most likely tokens of every
// position, which is what a replay with top_logprobs k returns.
func topOf(logprobs []map[int]float64, out []int, k int) []map[int]float64 {
	cut := make([]map[int]float64, len(logprobs))
	for t, lp := range logprobs {
		tokens := make([]int, 0, len(lp))
		for tok := range lp {
			tokens = append(tokens, tok)
		}
		sort.Slice(tokens, func(a, b int) bool { return lp[tokens[a]] > lp[tokens[b]] })
		cut[t] = map[int]float64{out[t]: lp[out[t]]}
		for _, tok := range tokens[:k] {
			cut[t][tok] = lp[tok]
		}
	}
	return cut
}

func TestScoreTape_AnAnswerOnItsTapeIsNeverOffTheWinner(t *testing.T) {
	lp := tapeTestLogprobs()
	vocab := modelVocab[tapeTestModel]
	th, _ := TapeThresholdsFor(tapeTestModel)
	out := tapeAnswer(lp, 1234)
	// Fewer candidates cannot turn an honest position into one off the winner.
	for _, seen := range [][]map[int]float64{lp, topOf(lp, out, 5), topOf(lp, out, 1)} {
		s := ScoreTape(seen, out, 1234, vocab, false)
		require.Equal(t, tapeTestSteps, s.Steps)
		require.Zero(t, s.Count)
		require.Less(t, math.Abs(s.Z), 3.0)
		require.True(t, th.Pass(s))
	}
}

func TestScoreTape_AnotherSeedIsAnotherTape(t *testing.T) {
	lp := tapeTestLogprobs()
	vocab := modelVocab[tapeTestModel]
	th, _ := TapeThresholdsFor(tapeTestModel)
	out := tapeAnswer(lp, 1234)
	all := ScoreTape(lp, out, 1235, vocab, false)
	top5 := ScoreTape(topOf(lp, out, 5), out, 1235, vocab, false)
	require.False(t, th.Pass(all))
	require.False(t, th.Pass(top5))
	require.Less(t, all.Z, -4.0)
	// Five candidates see most of the positions all sixteen do.
	require.Greater(t, top5.Count, all.Count*9/10)
	// An answer sampled without the seed's noise is off its tape too.
	require.False(t, th.Pass(ScoreTape(topOf(lp, greedyAnswer(lp), 5), greedyAnswer(lp), 1234, vocab, false)))
}

func TestScoreTape_AGreedyAnswerIsTheMostLikelyToken(t *testing.T) {
	lp := tapeTestLogprobs()
	vocab := modelVocab[tapeTestModel]
	greedy := ScoreTape(lp, greedyAnswer(lp), 1234, vocab, true)
	require.Zero(t, greedy.Count)
	require.Zero(t, greedy.Z)
	sampled := ScoreTape(lp, tapeAnswer(lp, 1234), 1234, vocab, true)
	require.Greater(t, sampled.Count, tapeTestSteps/4)
}

// The replaying engine masks a token outside its own cut: the position is off
// the winner, and there is no score to take a gap or a residual from.
func TestScoreTape_AMaskedAnswerTokenCountsButIsNotScored(t *testing.T) {
	lp := []map[int]float64{{1: -0.1, 2: -3}, {1: -9999, 2: -0.1}, {1: -0.1, 2: -9999}}
	s := ScoreTape(lp, []int{1, 1, 1}, 1234, modelVocab[tapeTestModel], true)
	require.Equal(t, 3, s.Steps)
	require.Equal(t, 1, s.Count)
	require.Equal(t, 1, s.Masked)
	require.Zero(t, s.MaxGap)
}

func TestTapeThresholds_TheBoundGrowsWithLength(t *testing.T) {
	th, ok := TapeThresholdsFor("deepseek-ai/DeepSeek-V4-Flash-0731")
	require.True(t, ok)
	// 500 positions at an honest 8 %: 40 expected, the 99.5 % bound at 56.
	require.True(t, th.Pass(TapeSignals{Steps: 500, Count: 56}))
	require.False(t, th.Pass(TapeSignals{Steps: 500, Count: 57}))
	// 50 positions: 4 expected, the bound at 9.
	require.True(t, th.Pass(TapeSignals{Steps: 50, Count: 9}))
	require.False(t, th.Pass(TapeSignals{Steps: 50, Count: 10}))
	require.False(t, th.Pass(TapeSignals{Steps: 500, Count: 0, MaxGap: 7.1}))
	// The residual is reported, not judged.
	require.True(t, th.Pass(TapeSignals{Steps: 500, Count: 0, Z: -9}))
	_, ok = TapeThresholdsFor("other/model")
	require.False(t, ok)
}

func tapeTestPosition(tok int, logprob float64, top map[int]float64) map[string]interface{} {
	alternatives := make([]interface{}, 0, len(top))
	for candidate, l := range top {
		alternatives = append(alternatives, map[string]interface{}{"token": strconv.Itoa(candidate), "logprob": l, "bytes": []int{}})
	}
	return map[string]interface{}{"token": strconv.Itoa(tok), "logprob": logprob, "bytes": []int{}, "top_logprobs": alternatives}
}

// storedTapeAnswer is an answer the way the executor stores it: the engine's
// response through the executor's response processor, as one JSON body or as a
// stream whose chunks carry up to three tokens each.
func storedTapeAnswer(t *testing.T, out []int, stream bool) []byte {
	t.Helper()
	positions := func(toks []int) []interface{} {
		content := make([]interface{}, 0, len(toks))
		for _, tok := range toks {
			content = append(content, tapeTestPosition(tok, -0.5, map[int]float64{tok: -0.5}))
		}
		return content
	}
	usage := map[string]interface{}{"prompt_tokens": 3, "completion_tokens": len(out), "total_tokens": 3 + len(out)}
	var body bytes.Buffer
	header := http.Header{}
	if stream {
		header.Set("Content-Type", "text/event-stream")
		write := func(chunk map[string]interface{}) {
			chunk["id"], chunk["object"], chunk["model"] = "x", "chat.completion.chunk", "m"
			encoded, _ := json.Marshal(chunk)
			body.WriteString("data: " + string(encoded) + "\n\n")
		}
		for i := 0; i < len(out); i += 3 {
			end := min(i+3, len(out))
			write(map[string]interface{}{"choices": []interface{}{map[string]interface{}{
				"index": 0, "delta": map[string]interface{}{"content": "a"}, "token_ids": out[i:end],
				"logprobs": map[string]interface{}{"content": positions(out[i:end])}, "finish_reason": nil}}})
		}
		write(map[string]interface{}{"choices": []interface{}{}, "usage": usage})
		body.WriteString("data: [DONE]\n\n")
	} else {
		header.Set("Content-Type", "application/json")
		encoded, _ := json.Marshal(map[string]interface{}{"id": "x", "object": "chat.completion", "model": "m", "usage": usage,
			"choices": []interface{}{map[string]interface{}{"index": 0, "finish_reason": "length", "token_ids": out,
				"message":  map[string]interface{}{"role": "assistant", "content": "a"},
				"logprobs": map[string]interface{}{"content": positions(out)}}}})
		body.Write(encoded)
	}
	processor := completionapi.NewExecutorResponseProcessor("devshard-1-42", false)
	require.NoError(t, completionapi.ProcessHTTPResponse(
		&http.Response{StatusCode: http.StatusOK, Header: header, Body: io.NopCloser(&body)}, processor))
	stored, err := processor.GetResponseBytes()
	require.NoError(t, err)
	return stored
}

// replayStream is the response of the logprob comparison's replay: a stream
// with the answer's tokens enforced and the five most likely tokens of every
// position, the usage in its last event.
func replayStream(lp []map[int]float64, out []int) []byte {
	var body strings.Builder
	write := func(chunk map[string]interface{}) {
		chunk["id"], chunk["object"], chunk["model"] = "x", "chat.completion.chunk", "m"
		encoded, _ := json.Marshal(chunk)
		body.WriteString("data: " + string(encoded) + "\n\n")
	}
	seen := topOf(lp, out, 5)
	for i, tok := range out {
		top := make(map[int]float64, 5)
		for candidate, l := range seen[i] {
			if len(top) < 5 && (candidate != tok || len(seen[i]) <= 5) {
				top[candidate] = l
			}
		}
		write(map[string]interface{}{"choices": []interface{}{map[string]interface{}{"index": 0, "delta": map[string]interface{}{"content": "a"},
			"logprobs": map[string]interface{}{"content": []interface{}{tapeTestPosition(tok, lp[i][tok], top)}}, "finish_reason": nil}}})
	}
	write(map[string]interface{}{"choices": []interface{}{},
		"usage": map[string]interface{}{"prompt_tokens": 3, "completion_tokens": len(out), "total_tokens": 3 + len(out)}})
	body.WriteString("data: [DONE]\n\n")
	return []byte(body.String())
}

const tapeTestPrompt = `{"model":"Qwen/Qwen3-0.6B","messages":[{"role":"user","content":"hi"}],"max_tokens":300}`

// The tape is read from the replay the logprob comparison already made: the
// stored answer as the executor's processor leaves it, the replay as the
// engine streams it.
func TestTapeOfReplay_ScoresTheStoredAnswerOnTheReplay(t *testing.T) {
	lp := tapeTestLogprobs()
	// Without a seed in the request the engine's is the inference id.
	for _, tc := range []struct {
		name string
		out  []int
		pass bool
	}{
		{"on the tape", tapeAnswer(lp, 42), true},
		{"under another seed", tapeAnswer(lp, 43), false},
	} {
		for _, stream := range []bool{false, true} {
			tape, err := TapeOfReplay("42", []byte(tapeTestPrompt), storedTapeAnswer(t, tc.out, stream), replayStream(lp, tc.out), tapeLogprobsMode)
			require.NoError(t, err, tc.name)
			require.Equal(t, tapeTestModel, tape.Model)
			require.Equal(t, tapeTestSteps, tape.Signals.Steps)
			require.Equal(t, tc.pass, tape.Pass(), tc.name)
		}
	}
	// The request's own seed is the tape's.
	seeded := strings.Replace(tapeTestPrompt, `"max_tokens"`, `"seed":43,"max_tokens"`, 1)
	out := tapeAnswer(lp, 43)
	tape, err := TapeOfReplay("42", []byte(seeded), storedTapeAnswer(t, out, false), replayStream(lp, out), tapeLogprobsMode)
	require.NoError(t, err)
	require.True(t, tape.Pass())
}

func TestTapeOfReplay_WhatItCannotJudge(t *testing.T) {
	lp := tapeTestLogprobs()
	out := tapeAnswer(lp, 42)
	stored := storedTapeAnswer(t, out, false)
	replay := replayStream(lp, out)
	with := func(field string) []byte {
		return []byte(strings.Replace(tapeTestPrompt, `"max_tokens"`, field+`,"max_tokens"`, 1))
	}
	other := append([]int{}, out...)
	other[7] = (other[7] + 1) % 16
	for name, tc := range map[string]struct {
		prompt, replay []byte
		mode           string
	}{
		"raw logprobs carry no sampling cut":      {[]byte(tapeTestPrompt), replay, "raw_logprobs"},
		"beam search":                             {with(`"use_beam_search":true`), replay, tapeLogprobsMode},
		"tokens the request forces":               {with(`"enforced_str":"x"`), replay, tapeLogprobsMode},
		"a seed the engine reads differently":     {with(`"seed":1.5`), replay, tapeLogprobsMode},
		"a model without a noise layout":          {[]byte(strings.Replace(tapeTestPrompt, tapeTestModel, "other/model", 1)), replay, tapeLogprobsMode},
		"a replay that lost a position":           {[]byte(tapeTestPrompt), replayStream(lp[:200], out[:200]), tapeLogprobsMode},
		"a replay that left the enforced answer":  {[]byte(tapeTestPrompt), replayStream(lp, other), tapeLogprobsMode},
		"no replay response, as after a 4xx pass": {[]byte(tapeTestPrompt), nil, tapeLogprobsMode},
	} {
		tape, err := TapeOfReplay("42", tc.prompt, stored, tc.replay, tc.mode)
		require.ErrorIs(t, err, ErrTapeUnjudged, name)
		require.Nil(t, tape, name)
	}
}
