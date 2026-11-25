package validation

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"

	"common/completionapi"
)

// The tape of a request is the noise vLLM adds to the processed logprobs when
// it samples under the request's seed: the token of a position is the argmax
// of logprob + noise, and the noise is a public function of the seed, the
// position and the token (philox.go). The logprob comparison replays an answer
// with its tokens enforced and gets, for every position, the processed
// logprobs of the answer's token and of the most likely ones. On that same
// response the validator recomputes the noise and counts the positions whose
// token is not the winner: an answer sampled under another seed, without the
// seed or by another model is off the winner far more often than an honest one.

const (
	eulerGamma  = 0.5772156649015329
	gumbelSigma = 1.2825498301618641 // pi / sqrt(6)
)

// tapeLogprobsMode is the logprobs mode the tape is read in: the logprobs the
// engine sampled from, after the temperature, the cuts, the penalties and the
// masks of the request.
const tapeLogprobsMode = "processed_logprobs"

// maskedLogprob is at or above what the engine reports for a token its
// sampler masked out.
const maskedLogprob = -9000.0

// samplingEps is vLLM's bound below which a temperature is greedy.
const samplingEps = 1e-5

// ErrTapeUnjudged marks an answer whose tape the validator cannot judge. It is
// not a verdict on the executor.
var ErrTapeUnjudged = errors.New("tape not judged")

// modelVocab is the width of the logits of each served model, config.json
// vocab_size; torch's noise layout depends on it.
var modelVocab = map[string]int{
	"deepseek-ai/DeepSeek-V4-Flash-0731": 129280,
	"zai-org/GLM-5.3-Flash":              154880,
	"MiniMaxAI/MiniMax-M2.7":             200064,
	"Qwen/Qwen3-0.6B":                    151936,
	"Qwen/Qwen3-4B-Instruct-2507":        151936,
	"Qwen/Qwen2.5-1.5B-Instruct":         151936,
	"Qwen/Qwen2.5-7B-Instruct":           152064,
}

// TapeSignals summarise one answer. Count is the number of positions whose
// token is not the argmax of logprob + noise among the tokens the replay
// returned, MaxGap and SumGap the score gaps of those positions. A greedy
// answer has no noise: Count is the positions whose token is not the most
// likely one and the gaps are in logprob.
//
// Z sums the Gumbel residual noise(x) + logprob(x), which is standard Gumbel
// for an answer sampled on its tape. Z, MeanR and the gaps are reported, not
// judged: honest answers sit below the Gumbel mean by a drift that depends on
// the model, the hardware and the temperature, which the square root in Z
// turns into false failures as the answer grows; for the gaps see
// TapeThresholds.
type TapeSignals struct {
	Steps  int     `json:"steps"`
	MeanR  float64 `json:"mean_r"`
	Z      float64 `json:"z"`
	Count  int     `json:"count"`
	MaxGap float64 `json:"max_gap"`
	SumGap float64 `json:"sum_gap"`
	// Masked positions are counted off the winner but not scored: the
	// replaying engine masked the answer's token out of its own cut.
	Masked int `json:"masked,omitempty"`
}

// TapeThresholds challenge an answer with more positions off the winner than
// the model's honest share allows for its length. The share is what holds
// across requests and nodes: honest answers are 3-6 % off the winner at any
// temperature, under a grammar and between MLNode versions.
//
// The gap is reported, not judged. It is in the units of the processed
// logprobs, so it grows as 1/T: an honest answer at temperature 0.1 has a
// median largest gap of 12.7 and up to 48 against 1.3 and 4.5 at temperature
// 1. And between nodes that render a prompt or apply a grammar differently a
// few positions sit tens of units off while the share stays where it is. On a
// mainnet validator in shadow (1010 DeepSeek answers the logprob comparison
// passed, 13-16.11.2026) a bound of 7 on the gap failed 34 % of them; the
// share alone fails 2.9 %, a third of which is one participant at 13.6 % of
// positions.
type TapeThresholds struct {
	FlipRate float64
}

// tapeThresholds. Honest answers, executor and validator the same checkpoint:
// DeepSeek-V4-Flash-0731 FP8 on B300 and 2xH200 3.4-4.1 % of positions off the
// winner (512 answers); its NVFP4 checkpoint of the B300 and B200 node configs
// 4.3-6.0 % at temperatures 0 to 1 (1074 answers on one B300); MiniMax-M2.7
// FP8 1.7-4.6 %; GLM-5.3-Flash FP8 2.4-2.7 %; the testnet Qwen models on one
// RTX 3090 0.3-1.2 %. FlipRate is the honest share with two points for nodes
// that were not measured.
//
// GLM-5.3-Flash stands higher: on mainnet its 8xH200 executors are 7.5-8.3 %
// off the winner for a B300 validator on the same image (3977 answers), the
// B300 ones 2.1-2.3 % (11357 answers).
//
// At DeepSeek's 0.08 on the logprob comparison's replay an answer under
// another seed is 20 % off the winner, and three answers in four are flagged.
// An answer of a checkpoint with half the experts pruned is 18 % off the
// winner.
var tapeThresholds = map[string]TapeThresholds{
	"deepseek-ai/DeepSeek-V4-Flash-0731": {FlipRate: 0.08},
	"MiniMaxAI/MiniMax-M2.7":             {FlipRate: 0.08},
	"zai-org/GLM-5.3-Flash":              {FlipRate: 0.09},
	"Qwen/Qwen3-0.6B":                    {FlipRate: 0.02},
	"Qwen/Qwen3-4B-Instruct-2507":        {FlipRate: 0.02},
	"Qwen/Qwen2.5-1.5B-Instruct":         {FlipRate: 0.02},
	"Qwen/Qwen2.5-7B-Instruct":           {FlipRate: 0.02},
}

// TapeThresholdsFor returns the model's thresholds and whether it has any.
func TapeThresholdsFor(model string) (TapeThresholds, bool) {
	t, ok := tapeThresholds[model]
	return t, ok
}

// Pass is the binomial bound on the positions off the winner: the honest count
// over the answer's positions is Binomial(steps, FlipRate), and an answer
// passes within the 99.5 % bound of it (normal quantile 2.576).
func (t TapeThresholds) Pass(s TapeSignals) bool {
	n := float64(s.Steps)
	bound := t.FlipRate*n + 2.576*math.Sqrt(n*t.FlipRate*(1-t.FlipRate)) + 0.5
	return s.Steps > 0 && float64(s.Count) <= bound
}

// ScoreTape scores an answer on the processed logprobs of its positions:
// logprobs[t] maps a token to its logprob at position t and includes the
// answer's own token out[t]. A greedy answer has no tape: its token must be
// the most likely one.
func ScoreTape(logprobs []map[int]float64, out []int, seed uint64, vocab int, greedy bool) TapeSignals {
	var s TapeSignals
	var sumR float64
	scored := 0
	for t, x := range out {
		scoreX, ok := logprobs[t][x]
		if !ok || scoreX <= maskedLogprob {
			s.Count++
			s.Masked++
			continue
		}
		if !greedy {
			noise := TapeNoise(seed, uint32(t), vocab, x)
			sumR += noise + scoreX
			scoreX += noise
			scored++
		}
		best := math.Inf(-1)
		for token, score := range logprobs[t] {
			if token == x || score <= maskedLogprob {
				continue
			}
			if !greedy {
				score += TapeNoise(seed, uint32(t), vocab, token)
			}
			best = math.Max(best, score)
		}
		if best > scoreX {
			s.Count++
			s.SumGap += best - scoreX
			s.MaxGap = math.Max(s.MaxGap, best-scoreX)
		}
	}
	s.Steps = len(out)
	if scored > 0 {
		n := float64(scored)
		s.MeanR = sumR / n
		s.Z = (sumR - eulerGamma*n) / (gumbelSigma * math.Sqrt(n))
	}
	return s
}

// TapeResult holds the signals of an answer scored on its tape and what the
// request asked of the sampler: a share that drifts with one of these is seen
// in the log without the ML node's own.
type TapeResult struct {
	Model   string
	Signals TapeSignals
	// Temperature, TopP and TopK are the request's own, nil when it leaves
	// them to the model's defaults.
	Temperature, TopP, TopK *float64
	// Grammar is set when the request decodes under a structured-output grammar.
	Grammar bool
	// Finish is the finish_reason the executor stored, ReplayPast the number
	// of positions the replay ran past the answer: one when the answer carries
	// no end token and the engine added its own.
	Finish     string
	ReplayPast int
	// End says which token ended the answer for the validator's engine:
	// "token" when the replay stopped on the answer's own last token,
	// "appended" when the answer carried none and the engine closed it with
	// its own, empty when the replay ran to the token limit. EndGap is how far
	// that token sits behind the winner of its position, in logits.
	End    string
	EndGap float64
}

// Pass reports whether the answer stays within the model's thresholds.
func (r TapeResult) Pass() bool {
	t, _ := TapeThresholdsFor(r.Model)
	return t.Pass(r.Signals)
}

// endGapLimit bounds, in logits, how far behind the winner of its position the
// token that ends an answer may sit. The share of positions cannot see an
// answer cut short: every token it keeps is on the tape, and the cut is one
// position. An honest answer ends where the end token wins its position, or
// loses a near-tie to the noise of another node; an answer cut where the model
// was going on ends on a token tens of logits behind.
//
// Qwen3-0.6B on one RTX 3090, executor and validator one engine: over 515
// honest answers the end token is at most 0.10 behind; over 648 answers cut at
// 64 tokens under a sampler with no top_k or top_p it is 9 to 48 behind, three
// of them under the limit. Under a top_k or top_p the sampler does not hold the
// end token at the cut at all (432 answers of 432).
const endGapLimit = 10.0

// endGapCeiling stands for an end token the validator's sampler masked out.
const endGapCeiling = 9000.0

// EndPasses reports whether the answer ends where its tape ends it.
func (r TapeResult) EndPasses() bool {
	return r.End == "" || r.EndGap <= endGapLimit
}

// endGap is how far token x sits behind the best other candidate of its
// position, zero when it is the winner.
func endGap(candidates map[int]float64, x int, seed uint64, step uint32, vocab int, greedy bool) float64 {
	scoreX, ok := candidates[x]
	if !ok || scoreX <= maskedLogprob {
		return endGapCeiling
	}
	if !greedy {
		scoreX += TapeNoise(seed, step, vocab, x)
	}
	gap := 0.0
	for token, score := range candidates {
		if token == x || score <= maskedLogprob {
			continue
		}
		if !greedy {
			score += TapeNoise(seed, step, vocab, token)
		}
		gap = math.Max(gap, score-scoreX)
	}
	return gap
}

// TapeOfReplay scores a stored answer on its tape from the response of the
// replay the logprob comparison made for it: no further call to the engine.
// replayResponse is ValidationResult.GetValidationResponseBytes, logprobsMode
// the mode the replay was made in. An answer the validator cannot judge
// returns ErrTapeUnjudged with the cause.
func TapeOfReplay(inferenceID string, promptPayload, responsePayload, replayResponse []byte, logprobsMode string) (*TapeResult, error) {
	if logprobsMode != tapeLogprobsMode {
		return nil, unjudged("logprobs mode %q is not %s", logprobsMode, tapeLogprobsMode)
	}
	modified, err := completionapi.ModifyRequestBodyWithLogprobsMode(promptPayload, validationReplaySeed(inferenceID), logprobsMode)
	if err != nil {
		return nil, unjudged("request: %v", err)
	}
	var request struct {
		Model          string   `json:"model"`
		Seed           *float64 `json:"seed"`
		Temperature    *float64 `json:"temperature"`
		TopP           *float64 `json:"top_p"`
		TopK           *float64 `json:"top_k"`
		UseBeamSearch  bool     `json:"use_beam_search"`
		EnforcedTokens any      `json:"enforced_tokens"`
		EnforcedStr    any      `json:"enforced_str"`
	}
	var requestMap map[string]interface{}
	if err := json.Unmarshal(modified.NewBody, &request); err != nil {
		return nil, unjudged("request: %v", err)
	}
	if err := json.Unmarshal(modified.NewBody, &requestMap); err != nil {
		return nil, unjudged("request: %v", err)
	}
	// The output of these was not sampled on the tape.
	if request.UseBeamSearch || request.EnforcedTokens != nil || request.EnforcedStr != nil {
		return nil, unjudged("the request does not sample on the tape")
	}
	if request.Seed == nil || *request.Seed < 0 || *request.Seed >= 1<<63 || *request.Seed != math.Trunc(*request.Seed) {
		return nil, unjudged("seed is not a whole non-negative number")
	}
	vocab, known := modelVocab[request.Model]
	if _, calibrated := TapeThresholdsFor(request.Model); !known || !calibrated {
		return nil, unjudged("no noise layout or thresholds for model %q", request.Model)
	}

	original, err := UnmarshalResponsePayload(responsePayload)
	if err != nil {
		return nil, unjudged("stored response: %v", err)
	}
	enforced, err := original.GetEnforcedTokens()
	if err != nil || IsEmptySentinelTokens(enforced) || len(enforced.Tokens) == 0 {
		return nil, unjudged("stored response carries no answer tokens")
	}
	if len(replayResponse) == 0 {
		return nil, unjudged("the logprob comparison kept no replay response")
	}
	replay, err := parseReplayResponse(replayResponse)
	if err != nil {
		return nil, unjudged("replay response: %v", err)
	}
	// The engine ends a forced answer that carries no end token with one of
	// its own, so a replay may run past the answer; the answer's positions are
	// all there, as the logprob comparison reads them. A shorter replay lost
	// positions of the answer.
	replayed := replay.ExtractLogits()
	if len(replayed) < len(enforced.Tokens) {
		return nil, unjudged("the replay returned %d of %d positions", len(replayed), len(enforced.Tokens))
	}
	past := len(replayed) - len(enforced.Tokens)
	positions := replayed[:len(enforced.Tokens)]
	out := make([]int, len(positions))
	logprobs := make([]map[int]float64, len(positions))
	for i, position := range positions {
		id, ok := tokenID(position.Token)
		if answer, known := tokenID(enforced.Tokens[i].Token); !ok || !known || id != answer {
			return nil, unjudged("position %d of the replay is not the answer's token", i)
		}
		out[i] = id
		logprobs[i] = map[int]float64{id: position.Logprob}
		for _, top := range position.TopLogprobs {
			if candidate, ok := tokenID(top.Token); ok {
				logprobs[i][candidate] = top.Logprob
			}
		}
	}
	greedy := request.Temperature != nil && *request.Temperature < samplingEps
	result := &TapeResult{
		Model:       request.Model,
		Signals:     ScoreTape(logprobs, out, uint64(*request.Seed), vocab, greedy),
		Temperature: request.Temperature,
		TopP:        request.TopP,
		TopK:        request.TopK,
		Grammar:     completionapi.GrammarConstrained(requestMap),
		Finish:      storedFinishReason(responsePayload),
		ReplayPast:  past,
	}
	// Where the answer ends is read from the validator's own engine, not from
	// the finish_reason the executor stored: the engine stops on the answer's
	// last token, or closes an answer that carries no end token with its own,
	// or runs to the token limit.
	end, endCandidates := -1, map[int]float64(nil)
	switch {
	case past > 0:
		appended := replayed[len(enforced.Tokens)]
		if id, ok := tokenID(appended.Token); ok {
			result.End, end = "appended", id
			endCandidates = map[int]float64{id: appended.Logprob}
			for _, top := range appended.TopLogprobs {
				if candidate, ok := tokenID(top.Token); ok {
					endCandidates[candidate] = top.Logprob
				}
			}
		}
	case replayFinishReason(replayResponse) == "stop":
		result.End, end, endCandidates = "token", out[len(out)-1], logprobs[len(out)-1]
	}
	if result.End != "" {
		step := len(out) - 1
		if result.End == "appended" {
			step = len(out)
		}
		// Processed logprobs are logits over the temperature.
		scale := 1.0
		if request.Temperature != nil && !greedy {
			scale = *request.Temperature
		}
		result.EndGap = math.Min(endGap(endCandidates, end, uint64(*request.Seed), uint32(step), vocab, greedy)*scale, endGapCeiling)
	}
	return result, nil
}

func finishReasonOf(body []byte) string {
	var resp struct {
		Choices []struct {
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
	}
	if json.Unmarshal(body, &resp) != nil || len(resp.Choices) == 0 {
		return ""
	}
	return resp.Choices[0].FinishReason
}

// storedFinishReason is the finish_reason of the stored answer: the choice's
// in a JSON body, the last non-empty one in the events of a stream.
func storedFinishReason(responsePayload []byte) string {
	var streamed completionapi.SerializedStreamedResponse
	if json.Unmarshal(responsePayload, &streamed) != nil || len(streamed.Events) == 0 {
		return finishReasonOf(responsePayload)
	}
	return lastFinishReason(streamed.Events)
}

// replayFinishReason is the finish_reason the validator's engine gave the replay.
func replayFinishReason(replayResponse []byte) string {
	lines, isStream := replayDataLines(replayResponse)
	if !isStream {
		return finishReasonOf(replayResponse)
	}
	return lastFinishReason(lines)
}

func lastFinishReason(events []string) string {
	finish := ""
	for _, line := range events {
		data, ok := strings.CutPrefix(line, "data:")
		if !ok || strings.TrimSpace(data) == "[DONE]" {
			continue
		}
		if f := finishReasonOf([]byte(data)); f != "" {
			finish = f
		}
	}
	return finish
}

func unjudged(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrTapeUnjudged, fmt.Sprintf(format, args...))
}

// tokenID reads a token the engine wrote as its id, bare or as "token_id:N".
func tokenID(s string) (int, bool) {
	id, err := strconv.Atoi(strings.TrimPrefix(s, "token_id:"))
	return id, err == nil && id >= 0
}
