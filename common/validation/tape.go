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
// for an answer sampled on its tape. Z and MeanR are reported, not judged:
// honest answers sit below the Gumbel mean by a drift that depends on the
// model and the hardware (0.01-0.05), which the square root in Z turns into
// false failures as the answer grows.
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
// the model's honest share allows for its length, or with a gap above the
// model's honest largest.
type TapeThresholds struct {
	FlipRate float64
	MaxGap   float64
}

// tapeThresholds. Honest answers, executor and validator the same checkpoint:
// DeepSeek-V4-Flash-0731 FP8 on B300 and 2xH200 3.4-4.1 % of positions off the
// winner, largest gap 4.9 (512 answers); its NVFP4 checkpoint of the B300 and
// B200 node configs 5.4-5.9 %, largest gap 6.8 (690 answers on one B300, top 5
// and top 20 candidates alike); MiniMax-M2.7 FP8 4.1-4.6 %; GLM-5.3-Flash FP8
// 2.4-2.7 %, largest gap 5.4; the testnet Qwen models on one RTX 3090
// 0.3-1.2 %, largest gap 2.5. FlipRate is the honest share with two points for
// nodes that were not measured, MaxGap the largest honest gap with a margin.
//
// At DeepSeek's 0.08 / 7 on the logprob comparison's replay: 5 of 306 honest
// NVFP4 answers are flagged; an answer under another seed is 20 % off the
// winner and is flagged 73 % of the time alone, 99 % by the sum of four; an
// answer of a checkpoint with half the experts pruned 86 % alone at 256 tokens
// and 97 % at 1024.
var tapeThresholds = map[string]TapeThresholds{
	"deepseek-ai/DeepSeek-V4-Flash-0731": {FlipRate: 0.08, MaxGap: 7},
	"MiniMaxAI/MiniMax-M2.7":             {FlipRate: 0.08, MaxGap: 5},
	"zai-org/GLM-5.3-Flash":              {FlipRate: 0.06, MaxGap: 5.5},
	"Qwen/Qwen3-0.6B":                    {FlipRate: 0.02, MaxGap: 6.4},
	"Qwen/Qwen3-4B-Instruct-2507":        {FlipRate: 0.02, MaxGap: 5},
	"Qwen/Qwen2.5-1.5B-Instruct":         {FlipRate: 0.02, MaxGap: 4},
	"Qwen/Qwen2.5-7B-Instruct":           {FlipRate: 0.02, MaxGap: 4},
}

// TapeThresholdsFor returns the model's thresholds and whether it has any.
func TapeThresholdsFor(model string) (TapeThresholds, bool) {
	t, ok := tapeThresholds[model]
	return t, ok
}

func (t TapeThresholds) Pass(s TapeSignals) bool {
	return s.Steps > 0 && t.CountPasses(s.Steps, s.Count, 2.576) && s.MaxGap <= t.MaxGap
}

// CountPasses is the binomial bound on the positions off the winner: the
// honest count over steps positions is Binomial(steps, FlipRate), and quantile
// is the normal quantile of the bound (2.576 for 99.5 %). It applies to one
// answer and to the pooled positions of a participant alike.
func (t TapeThresholds) CountPasses(steps, count int, quantile float64) bool {
	n := float64(steps)
	bound := t.FlipRate*n + quantile*math.Sqrt(n*t.FlipRate*(1-t.FlipRate)) + 0.5
	return float64(count) <= bound
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

// TapeResult holds the signals of an answer scored on its tape.
type TapeResult struct {
	Model   string
	Signals TapeSignals
}

// Pass reports whether the answer stays within the model's thresholds.
func (r TapeResult) Pass() bool {
	t, _ := TapeThresholdsFor(r.Model)
	return t.Pass(r.Signals)
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
		UseBeamSearch  bool     `json:"use_beam_search"`
		EnforcedTokens any      `json:"enforced_tokens"`
		EnforcedStr    any      `json:"enforced_str"`
	}
	if err := json.Unmarshal(modified.NewBody, &request); err != nil {
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
	positions := replay.ExtractLogits()
	if len(positions) != len(enforced.Tokens) {
		return nil, unjudged("the replay returned %d of %d positions", len(positions), len(enforced.Tokens))
	}
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
	return &TapeResult{
		Model:   request.Model,
		Signals: ScoreTape(logprobs, out, uint64(*request.Seed), vocab, greedy),
	}, nil
}

func unjudged(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrTapeUnjudged, fmt.Sprintf(format, args...))
}

// tokenID reads a token the engine wrote as its id, bare or as "token_id:N".
func tokenID(s string) (int, bool) {
	id, err := strconv.Atoi(strings.TrimPrefix(s, "token_id:"))
	return id, err == nil && id >= 0
}
