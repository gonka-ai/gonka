package validation

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"common/completionapi"
	"common/logging"

	"github.com/productscience/inference/x/inference/types"
)

// EchoPath is where the validator scores a stored answer: one prefill of the
// prompt and the answer with the logprobs of every position.
const EchoPath = "/v1/completions"

// EchoTopLogprobs is the width of the validator's logprobs per position; the
// kept set is reconstructed inside it, so it must cover the request's top_k.
const EchoTopLogprobs = 20

const (
	eulerGamma  = 0.5772156649015329
	gumbelSigma = 1.2825498301618641 // pi / sqrt(6)
)

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

// modelDefaults are the sampling parameters vLLM applies when a request
// leaves them out: generation_config.json of the model (vLLM's
// --generation-config auto, which the node configs do not override). The
// executor sampled under them, so the validator's kept set must use them too.
var modelDefaults = map[string]TapeParams{
	"deepseek-ai/DeepSeek-V4-Flash-0731": {Temperature: 1, TopP: 1},
	"zai-org/GLM-5.3-Flash":              {Temperature: 1, TopP: 0.95},
	"MiniMaxAI/MiniMax-M2.7":             {Temperature: 1, TopP: 0.95, TopK: 40},
	"Qwen/Qwen3-0.6B":                    {Temperature: 0.6, TopP: 0.95, TopK: 20},
	"Qwen/Qwen3-4B-Instruct-2507":        {Temperature: 0.7, TopP: 0.8, TopK: 20},
	"Qwen/Qwen2.5-1.5B-Instruct":         {Temperature: 0.7, TopP: 0.8, TopK: 20, RepetitionPenalty: 1.1},
	"Qwen/Qwen2.5-7B-Instruct":           {Temperature: 0.7, TopP: 0.8, TopK: 20, RepetitionPenalty: 1.05},
}

// modelEOS are the generation_config eos_token_id(s) of each served model:
// vLLM masks them while min_tokens is not reached, and an answer that
// finishes with "stop" must end on one of them.
var modelEOS = map[string][]int{
	"deepseek-ai/DeepSeek-V4-Flash-0731": {1},
	"zai-org/GLM-5.3-Flash":              {154820, 154827, 154829},
	"MiniMaxAI/MiniMax-M2.7":             {200020},
	"Qwen/Qwen3-0.6B":                    {151645, 151643},
	"Qwen/Qwen3-4B-Instruct-2507":        {151645, 151643},
	"Qwen/Qwen2.5-1.5B-Instruct":         {151645, 151643},
	"Qwen/Qwen2.5-7B-Instruct":           {151645, 151643},
}

// ChatPath renders the stored request on the validator's engine: one token
// with return_token_ids gives the prompt ids the validator itself derives.
const ChatPath = "/v1/chat/completions"

// ErrEchoUnsupported marks an answer the validator cannot judge: its engine
// returned the replay without token ids, or not the forced answer.
var ErrEchoUnsupported = errors.New("echo validation: unsupported request")

// replayLogprobsMode asks the fork's engine for the logprobs its sampler drew
// from: after penalties, temperature, top_k/top_p and any grammar mask.
const replayLogprobsMode = "processed_logprobs"

// maskedLogprob is where the engine reports a token its sampler masked out
// (vLLM clamps -inf to -9999): in a replay the answer's token can sit there
// when it fell outside the validator's own top_p cut.
const maskedLogprob = -9000.0

// lengthRuleContextCap is the smallest max_model_len a node is expected to
// serve; an answer cut by the context can only be longer than that, so the
// length rule is applied below it.
const lengthRuleContextCap = 32768

// TapeParams are the sampling parameters the executor's tape ran under.
type TapeParams struct {
	Temperature float64
	TopP        float64
	TopK        int
	// MinTokens and MaxTokens are the budget devshard pins on the request;
	// end tokens are masked before MinTokens and the answer stops at MaxTokens.
	MinTokens int
	MaxTokens int
	// Penalties rescale the logits of tokens already seen. The validator's
	// prompt logprobs are taken before them, so a penalised tape has no echo.
	RepetitionPenalty float64
	FrequencyPenalty  float64
	PresencePenalty   float64
}

// Penalised reports a tape sampled under a repetition, frequency or presence
// penalty.
func (p TapeParams) Penalised() bool {
	return p.RepetitionPenalty != 1 || p.FrequencyPenalty != 0 || p.PresencePenalty != 0
}

func tapeParams(requestMap map[string]interface{}) TapeParams {
	p := TapeParams{Temperature: 1, TopP: 1, RepetitionPenalty: 1}
	if model, _ := requestMap["model"].(string); model != "" {
		if d, ok := modelDefaults[model]; ok {
			p = d
			if p.RepetitionPenalty == 0 {
				p.RepetitionPenalty = 1
			}
		}
	}
	if v, ok := requestMap["temperature"].(float64); ok {
		p.Temperature = v
	}
	if v, ok := requestMap["top_p"].(float64); ok {
		p.TopP = v
	}
	if v, ok := requestMap["top_k"].(float64); ok {
		p.TopK = int(v)
	}
	if v, ok := requestMap["min_tokens"].(float64); ok {
		p.MinTokens = int(v)
	}
	if v, ok := requestMap["max_tokens"].(float64); ok {
		p.MaxTokens = int(v)
	}
	if v, ok := requestMap["repetition_penalty"].(float64); ok {
		p.RepetitionPenalty = v
	}
	if v, ok := requestMap["frequency_penalty"].(float64); ok {
		p.FrequencyPenalty = v
	}
	if v, ok := requestMap["presence_penalty"].(float64); ok {
		p.PresencePenalty = v
	}
	return p
}

// EchoSignals summarise one answer. Z sums over positions the Gumbel residual
// r_t = noise(x_t) + log p~(x_t), which is standard Gumbel when the executor
// sampled on its tape from the validator's distribution. Count is the number
// of positions whose token is not the argmax of log p~ + noise within the
// validator's kept set, MaxGap and SumGap the score gaps of those positions.
type EchoSignals struct {
	Steps  int     `json:"steps"`
	MeanR  float64 `json:"mean_r"`
	Z      float64 `json:"z"`
	Count  int     `json:"count"`
	MaxGap float64 `json:"max_gap"`
	SumGap float64 `json:"sum_gap"`
	// Masked positions are counted off the winner but not scored: the
	// replaying engine masked the answer's token (maskedLogprob).
	Masked int `json:"masked,omitempty"`
}

// EchoThresholds challenge an answer with Z below ZMin, with more positions
// off the winner than the model's honest share allows for its length, or with
// a gap above the model's honest largest. ZMin is one bound for every model;
// FlipRate and MaxGap are calibrated per model and zero when they are not.
type EchoThresholds struct {
	ZMin     float64
	FlipRate float64
	MaxGap   float64
}

// echoThresholds: honest DeepSeek-V4-Flash-0731 FP8 with executor and
// validator on one B300, on 2xH200 and across the two (512 scored answers,
// 2026-11-09: 3.4-4.1 % of positions off the winner, largest gap 4.9), honest
// MiniMax-M2.7 FP8 the same way (4.1-4.6 % off the winner, long answers 4.6 %),
// honest GLM-5.3-Flash FP8 on 8xB300 (TP2) and 4xH200 (TP4) and across the two
// (2.4-2.7 % off the winner, short answers up to 7.8 %, largest gap 5.4) and
// honest Qwen3-0.6B on one RTX 3090 (2026-11-04, 64 answers) and the testnet
// Qwen2.5-1.5B / Qwen3-4B-2507 / Qwen2.5-7B on the same 3090 (2026-11-10, 80
// answers each, executor and validator one instance, the Qwen2.5 repetition
// penalty overridden: 0.3-1.2 % off the winner, largest gap 2.5). FlipRate is
// the honest share of positions off the winner with a margin for the length
// bound, MaxGap the largest honest gap seen with a margin.
var echoThresholds = map[string]EchoThresholds{
	"deepseek-ai/DeepSeek-V4-Flash-0731": {ZMin: -3, FlipRate: 0.05, MaxGap: 5},
	"MiniMaxAI/MiniMax-M2.7":             {ZMin: -3, FlipRate: 0.06, MaxGap: 5},
	"zai-org/GLM-5.3-Flash":              {ZMin: -3, FlipRate: 0.04, MaxGap: 5.5},
	"Qwen/Qwen3-0.6B":                    {ZMin: -3, FlipRate: 0.02, MaxGap: 6.4},
	"Qwen/Qwen3-4B-Instruct-2507":        {ZMin: -3, FlipRate: 0.02, MaxGap: 5},
	"Qwen/Qwen2.5-1.5B-Instruct":         {ZMin: -3, FlipRate: 0.02, MaxGap: 4},
	"Qwen/Qwen2.5-7B-Instruct":           {ZMin: -3, FlipRate: 0.02, MaxGap: 4},
}

// EchoThresholdsFor returns the model's thresholds; a model without a
// calibration is judged by Z alone.
func EchoThresholdsFor(model string) EchoThresholds {
	if t, ok := echoThresholds[model]; ok {
		return t
	}
	return EchoThresholds{ZMin: -3}
}

func (t EchoThresholds) Pass(s EchoSignals) bool {
	if s.Steps == 0 || s.Z < t.ZMin {
		return false
	}
	if !t.CountPasses(s.Steps, s.Count, 2.576) {
		return false
	}
	return t.MaxGap == 0 || s.MaxGap <= t.MaxGap
}

// CountPasses is the binomial bound on the positions off the winner: the
// honest count over steps positions is Binomial(steps, FlipRate), and quantile
// is the normal quantile of the bound (2.576 for 99.5 %). It applies to one
// answer and to the pooled positions of a participant alike. Without a
// calibration it always passes.
func (t EchoThresholds) CountPasses(steps, count int, quantile float64) bool {
	if t.FlipRate <= 0 || steps <= 0 {
		return true
	}
	n := float64(steps)
	bound := t.FlipRate*n + quantile*math.Sqrt(n*t.FlipRate*(1-t.FlipRate)) + 0.5
	return float64(count) <= bound
}

// EchoValidationResult holds the signals of a scored answer.
type EchoValidationResult struct {
	BaseValidationResult
	Model   string
	Signals EchoSignals
	// Replay names what kept the echo from judging the answer, which the
	// validator replayed through its engine's sampler instead; empty for an echo.
	Replay string
}

func (r EchoValidationResult) IsSuccessful() bool {
	return EchoThresholdsFor(r.Model).Pass(r.Signals)
}

// ScoreEcho scores an answer from the validator's logprobs of its positions:
// logprobs[t] maps a token to its raw logprob at position t and includes the
// answer's own token out[t].
//
// eos are the model's end tokens: before p.MinTokens positions the engine
// masks them, so they leave the kept set there.
func ScoreEcho(logprobs []map[int]float64, out []int, seed uint64, vocab int, p TapeParams, eos []int) EchoSignals {
	var s EchoSignals
	var sumR float64
	for t, x := range out {
		if lp, ok := logprobs[t][x]; !ok || lp <= maskedLogprob {
			// Outside the validator's own cut: a position off the winner whose
			// score the engine did not report, so it counts and is not scored.
			s.Count++
			s.Masked++
			continue
		}
		var masked map[int]bool
		if t < p.MinTokens && len(eos) > 0 {
			masked = make(map[int]bool, len(eos))
			for _, e := range eos {
				if e != x {
					masked[e] = true
				}
			}
		}
		kept, sx, logPTilde := keptSet(logprobs[t], x, p, masked)
		noise := make(map[int]float64, len(kept)+1)
		for _, c := range kept {
			noise[c.token] = TapeNoise(seed, uint32(t), vocab, c.token)
		}
		if _, ok := noise[x]; !ok {
			noise[x] = TapeNoise(seed, uint32(t), vocab, x)
		}
		sumR += noise[x] + logPTilde
		scoreX := sx + noise[x]
		best := math.Inf(-1)
		for _, c := range kept {
			if c.token != x && c.score+noise[c.token] > best {
				best = c.score + noise[c.token]
			}
		}
		if best > scoreX {
			s.Count++
			s.SumGap += best - scoreX
			s.MaxGap = math.Max(s.MaxGap, best-scoreX)
		}
	}
	s.Steps = len(out)
	if n := s.Steps - s.Masked; n > 0 {
		s.MeanR = sumR / float64(n)
		s.Z = (sumR - eulerGamma*float64(n)) / (gumbelSigma * math.Sqrt(float64(n)))
	}
	return s
}

type candidate struct {
	token int
	score float64 // logprob / temperature
}

// keptSet is vLLM's sampling set as far as the validator's top logprobs show
// it: temperature, then top_k, then top_p, as a list of candidates with their
// scaled logits; with the scaled logit of x and log of its processed
// probability. Beyond the list the mass of the distribution is known only at
// temperature 1, where the logprobs are normalised.
//
// masked tokens (end tokens before min_tokens) are dropped from the set the
// way the engine drops them, after the tail mass is taken from the full head.
func keptSet(toks map[int]float64, x int, p TapeParams, masked map[int]bool) ([]candidate, float64, float64) {
	T := p.Temperature
	head := make([]candidate, 0, len(toks))
	for tok, lp := range toks {
		head = append(head, candidate{tok, lp})
	}
	sort.Slice(head, func(i, j int) bool {
		if head[i].score != head[j].score {
			return head[i].score > head[j].score
		}
		return head[i].token < head[j].token
	})
	if len(head) > EchoTopLogprobs {
		head = head[:EchoTopLogprobs]
	}
	tail := 0.0
	if !(p.TopK > 0 && p.TopK <= EchoTopLogprobs) && T == 1 {
		mass := 0.0
		for _, c := range head {
			mass += math.Exp(c.score)
		}
		tail = math.Max(0, 1-mass)
	}
	cand := head
	if len(masked) > 0 {
		cand = make([]candidate, 0, len(head))
		for _, c := range head {
			if !masked[c.token] {
				cand = append(cand, c)
			}
		}
	}
	if p.TopK > 0 && p.TopK <= EchoTopLogprobs && len(cand) > p.TopK {
		cand = cand[:p.TopK]
	}
	m := cand[0].score / T
	z := 0.0
	for i := range cand {
		cand[i].score /= T
		z += math.Exp(cand[i].score - m)
	}
	if tail > 0 {
		z += tail * math.Exp(-m)
	}
	kept := cand[:0:0]
	cum, inSet := 0.0, false
	for _, c := range cand {
		kept = append(kept, c)
		cum += math.Exp(c.score-m) / z
		if c.token == x {
			inSet = true
		}
		if p.TopP < 1 && cum >= p.TopP {
			break
		}
	}
	mass := cum
	if p.TopP >= 1 {
		mass, inSet = 1, true
	} else if cum < p.TopP {
		mass = p.TopP
	}
	sx := toks[x] / T
	px := math.Exp(sx-m) / z
	if !inSet {
		mass += px
	}
	return kept, sx, math.Log(px) - math.Log(mass)
}

// EchoRequestBody asks the validator's engine for the logprobs of every token
// of prompt + out, in one prefill.
func EchoRequestBody(model string, prompt, out []int) ([]byte, error) {
	ids := make([]int, 0, len(prompt)+len(out))
	ids = append(ids, prompt...)
	ids = append(ids, out...)
	return json.Marshal(map[string]interface{}{
		"model": model, "prompt": ids, "max_tokens": 1, "temperature": 0,
		"prompt_logprobs": EchoTopLogprobs,
	})
}

// ReplayRequestBody replays the stored answer on the validator's engine with
// the fork's enforced_tokens: one decode step per token, the engine's own
// sampler applying what the echo cannot (penalties, a grammar, a thinking
// budget) and its processed logprobs at every step. Stop strings only end an
// answer and are dropped so that every position is replayed.
func ReplayRequestBody(requestMap map[string]interface{}, out []int) ([]byte, error) {
	req := make(map[string]interface{}, len(requestMap)+6)
	for k, v := range requestMap {
		req[k] = v
	}
	tokens := make([]map[string]interface{}, len(out))
	for i, tok := range out {
		tokens[i] = map[string]interface{}{"token": strconv.Itoa(tok), "top_tokens": []string{}}
	}
	req["enforced_tokens"] = map[string]interface{}{"tokens": tokens}
	req["logprobs"] = true
	req["top_logprobs"] = EchoTopLogprobs
	req["logprobs_mode"] = replayLogprobsMode
	req["return_token_ids"] = true
	req["max_tokens"] = len(out)
	req["stream"] = false
	for _, key := range []string{"stream_options", "stop", "stop_token_ids", "enforced_str"} {
		delete(req, key)
	}
	return json.Marshal(req)
}

// ParseReplay returns the processed logprobs of every replayed position from
// a chat completion with enforced_tokens. The engine must return the whole
// answer and its tokens as ids (--return-tokens-as-token-ids); decoded text
// leaves the answer unsupported.
func ParseReplay(body []byte, out []int) ([]map[int]float64, error) {
	var resp struct {
		Choices []struct {
			TokenIDs []int `json:"token_ids"`
			Logprobs struct {
				Content []struct {
					Token       string  `json:"token"`
					Logprob     float64 `json:"logprob"`
					TopLogprobs []struct {
						Token   string  `json:"token"`
						Logprob float64 `json:"logprob"`
					} `json:"top_logprobs"`
				} `json:"content"`
			} `json:"logprobs"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, err
	}
	// The fork's replay can lose its place in the forced answer when the
	// batch around it changes (seen under load on vLLM 0.30 with async
	// scheduling); such an answer is left unjudged, not failed.
	if len(resp.Choices) == 0 || !equalIDs(resp.Choices[0].TokenIDs, out) {
		return nil, fmt.Errorf("%w: the engine did not return the forced answer", ErrEchoUnsupported)
	}
	content := resp.Choices[0].Logprobs.Content
	if len(content) != len(out) {
		return nil, fmt.Errorf("%w: the replay has %d positions for %d tokens", ErrEchoUnsupported, len(content), len(out))
	}
	logprobs := make([]map[int]float64, len(out))
	for t, step := range content {
		entry := make(map[int]float64, len(step.TopLogprobs)+1)
		for _, c := range step.TopLogprobs {
			tok, err := replayTokenID(c.Token)
			if err != nil {
				return nil, err
			}
			entry[tok] = c.Logprob
		}
		tok, err := replayTokenID(step.Token)
		if err != nil {
			return nil, err
		}
		if tok != out[t] {
			return nil, fmt.Errorf("replay position %d: token %d where %d was forced", t, tok, out[t])
		}
		entry[tok] = step.Logprob
		logprobs[t] = entry
	}
	return logprobs, nil
}

func replayTokenID(s string) (int, error) {
	tok, err := strconv.Atoi(strings.TrimPrefix(s, "token_id:"))
	if err != nil {
		return 0, fmt.Errorf("%w: the engine returned a decoded token %q, not an id", ErrEchoUnsupported, s)
	}
	return tok, nil
}

// ParseEcho returns the logprobs of the positions after promptLen from a
// completions response with prompt_logprobs.
func ParseEcho(body []byte, promptLen, outLen int) ([]map[int]float64, error) {
	var resp struct {
		Choices []struct {
			PromptLogprobs []map[string]struct {
				Logprob float64 `json:"logprob"`
			} `json:"prompt_logprobs"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, err
	}
	if len(resp.Choices) == 0 || len(resp.Choices[0].PromptLogprobs) < promptLen+outLen {
		return nil, errors.New("echo response has fewer positions than the answer")
	}
	out := make([]map[int]float64, outLen)
	for i := range out {
		entry := resp.Choices[0].PromptLogprobs[promptLen+i]
		out[i] = make(map[int]float64, len(entry))
		for key, v := range entry {
			tok, err := strconv.Atoi(key)
			if err != nil {
				return nil, fmt.Errorf("echo position %d: token %q", i, key)
			}
			out[i][tok] = v.Logprob
		}
	}
	return out, nil
}

// StoredTokenIDs reads the prompt and answer token ids of a stored executor
// response, which devshard asks for with return_token_ids, from a JSON body or
// from the events of a stream.
func StoredTokenIDs(responsePayload []byte) (prompt, out []int, err error) {
	var streamed completionapi.SerializedStreamedResponse
	if json.Unmarshal(responsePayload, &streamed) == nil && len(streamed.Events) > 0 {
		for _, line := range streamed.Events {
			data, ok := strings.CutPrefix(line, "data: ")
			if !ok || strings.TrimSpace(data) == "[DONE]" {
				continue
			}
			p, o, chunkErr := tokenIDsOf([]byte(data))
			if chunkErr != nil {
				continue
			}
			if len(p) > 0 {
				prompt = p
			}
			out = append(out, o...)
		}
	} else {
		prompt, out, err = tokenIDsOf(responsePayload)
		if err != nil {
			return nil, nil, err
		}
	}
	if len(prompt) == 0 || len(out) == 0 {
		return nil, nil, errors.New("stored response carries no token ids")
	}
	return prompt, out, nil
}

func tokenIDsOf(body []byte) (prompt, out []int, err error) {
	var resp struct {
		PromptTokenIDs []int `json:"prompt_token_ids"`
		Choices        []struct {
			TokenIDs []int `json:"token_ids"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, nil, err
	}
	if len(resp.Choices) > 0 {
		out = resp.Choices[0].TokenIDs
	}
	return resp.PromptTokenIDs, out, nil
}

// ExecuteEchoValidation validates a stored inference on the executor's tape: the
// answer's token ids are scored against the validator's own logprobs and the
// noise the request's seed fixes. execute must POST the body to EchoPath.
//
// Before the echo the validator renders the request itself (ChatPath, one
// token) and requires the executor's prompt ids to equal its own, and checks
// the answer's end against finish_reason and the pinned token budget. A
// request the echo cannot judge returns ErrEchoUnsupported.
func ExecuteEchoValidation(
	ctx context.Context,
	inferenceID string,
	promptPayload []byte,
	responsePayload []byte,
	execute func(ctx context.Context, path string, body []byte) (*http.Response, error),
	claimedInputTokens, claimedOutputTokens uint64,
) (ValidationResult, error) {
	modified, err := completionapi.ModifyRequestBody(promptPayload, validationReplaySeed(inferenceID))
	if err != nil {
		return &InvalidInferenceResult{inferenceID, "Failed to modify promptPayload.", err}, nil
	}
	var requestMap map[string]interface{}
	if err := json.Unmarshal(modified.NewBody, &requestMap); err != nil {
		return &InvalidInferenceResult{inferenceID, "Failed to unmarshal promptPayload.", err}, nil
	}
	replay := replayReason(requestMap)
	model, _ := requestMap["model"].(string)
	vocab, ok := modelVocab[model]
	if !ok {
		return nil, fmt.Errorf("echo validation: unknown vocabulary of model %q", model)
	}
	seedValue, _ := requestMap["seed"].(float64)
	seed := uint64(int64(seedValue))
	prompt, out, err := StoredTokenIDs(responsePayload)
	if err != nil {
		return &InvalidInferenceResult{InferenceId: inferenceID, Reason: "Stored response carries no token ids.", Error: err}, nil
	}
	if TokenCountInflated(claimedInputTokens, uint64(len(prompt))) ||
		TokenCountInflated(claimedOutputTokens, uint64(len(out))) {
		logging.Warn("validation failed: inflated token counts", types.Validation,
			"inferenceId", inferenceID, "claimedInput", claimedInputTokens, "prompt", len(prompt),
			"claimedOutput", claimedOutputTokens, "answer", len(out))
		return &InvalidInferenceResult{InferenceId: inferenceID, Reason: "Inflated token counts."}, nil
	}

	renderBody, err := promptIDsRequestBody(requestMap)
	if err != nil {
		return nil, err
	}
	status, rendered, err := post(ctx, execute, ChatPath, renderBody)
	if err != nil {
		return nil, err
	}
	if status == http.StatusBadRequest || status == http.StatusUnprocessableEntity {
		return &InvalidInferenceResult{InferenceId: inferenceID, Reason: "Validator cannot render the stored request."}, nil
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("echo validation: render status %d: %s", status, bytes.TrimSpace(rendered))
	}
	ownPrompt, _, err := tokenIDsOf(rendered)
	if err != nil || len(ownPrompt) == 0 {
		return nil, fmt.Errorf("echo validation: rendered request carries no prompt ids: %v", err)
	}
	if !equalIDs(ownPrompt, prompt) {
		logging.Warn("validation failed: prompt ids differ from the request", types.Validation,
			"inferenceId", inferenceID, "executor", len(prompt), "validator", len(ownPrompt))
		return &InvalidInferenceResult{InferenceId: inferenceID, Reason: "Prompt token ids differ from the request."}, nil
	}

	params := tapeParams(requestMap)
	if reason := answerShapeReason(model, prompt, out, StoredFinishReason(responsePayload), params, hasStopStrings(requestMap)); reason != "" {
		logging.Warn("validation failed: "+reason, types.Validation, "inferenceId", inferenceID, "answer", len(out))
		return &InvalidInferenceResult{InferenceId: inferenceID, Reason: reason}, nil
	}

	var signals EchoSignals
	var respBody []byte
	if replay != "" {
		body, err := ReplayRequestBody(requestMap, out)
		if err != nil {
			return nil, err
		}
		var status int
		status, respBody, err = post(ctx, execute, ChatPath, body)
		if err != nil {
			return nil, err
		}
		if status == http.StatusBadRequest || status == http.StatusUnprocessableEntity {
			return &InvalidInferenceResult{InferenceId: inferenceID, Reason: "Validator cannot replay the stored answer."}, nil
		}
		if status != http.StatusOK {
			return nil, fmt.Errorf("replay validation: status %d: %s", status, bytes.TrimSpace(respBody))
		}
		logprobs, err := ParseReplay(respBody, out)
		if err != nil {
			return nil, err
		}
		// Processed logprobs already carry the temperature, the cut and the
		// masks the engine sampled under.
		signals = ScoreEcho(logprobs, out, seed, vocab, TapeParams{Temperature: 1, TopP: 1, RepetitionPenalty: 1}, nil)
	} else {
		body, err := EchoRequestBody(model, prompt, out)
		if err != nil {
			return nil, err
		}
		var status int
		status, respBody, err = post(ctx, execute, EchoPath, body)
		if err != nil {
			return nil, err
		}
		if status == http.StatusBadRequest || status == http.StatusUnprocessableEntity {
			// The echo holds nothing but the executor's own token ids.
			return &InvalidInferenceResult{InferenceId: inferenceID, Reason: "Validator cannot embed the stored token ids."}, nil
		}
		if status != http.StatusOK {
			return nil, fmt.Errorf("echo validation: status %d: %s", status, bytes.TrimSpace(respBody))
		}
		logprobs, err := ParseEcho(respBody, len(prompt), len(out))
		if err != nil {
			return nil, err
		}
		signals = ScoreEcho(logprobs, out, seed, vocab, params, modelEOS[model])
	}
	return &EchoValidationResult{
		BaseValidationResult: BaseValidationResult{InferenceId: inferenceID, ResponseBytes: respBody},
		Model:                model,
		Signals:              signals,
		Replay:               replay,
	}, nil
}

func post(ctx context.Context, execute func(context.Context, string, []byte) (*http.Response, error), path string, body []byte) (int, []byte, error) {
	resp, err := execute(ctx, path, body)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, nil, err
	}
	return resp.StatusCode, respBody, nil
}

func equalIDs(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// replayReason names what keeps the echo from judging a request, so that the
// answer is replayed through the engine's sampler instead: a grammar (the
// validator's prefill does not see the mask the executor sampled under), a
// thinking budget (it forces a token the tape never chose) or a sampling
// penalty, requested or the model's default (vLLM applies generation_config's
// repetition_penalty unless the node overrides it); the prompt logprobs of a
// prefill skip all of them.
func replayReason(requestMap map[string]interface{}) string {
	if GrammarConstrained(requestMap) {
		return "grammar-constrained request"
	}
	if tapeParams(requestMap).Penalised() {
		return "sampling penalty the echo cannot replay"
	}
	if _, ok := requestMap["thinking_token_budget"]; ok {
		return "thinking budget forces a token"
	}
	if kw, ok := requestMap["chat_template_kwargs"].(map[string]interface{}); ok {
		if _, ok := kw["thinking_budget"]; ok {
			return "thinking budget forces a token"
		}
	}
	return ""
}

// GrammarConstrained reports a request whose sampling the engine restricts
// with a grammar: a structured response_format, a forced tool choice, or a
// guided decoding field.
func GrammarConstrained(requestMap map[string]interface{}) bool {
	if rf, ok := requestMap["response_format"].(map[string]interface{}); ok {
		if t, _ := rf["type"].(string); t != "" && t != "text" {
			return true
		}
	}
	switch tc := requestMap["tool_choice"].(type) {
	case string:
		if tc == "required" {
			return true
		}
	case map[string]interface{}:
		return true
	}
	for _, key := range []string{"guided_json", "guided_regex", "guided_grammar", "guided_choice", "structured_outputs"} {
		if _, ok := requestMap[key]; ok {
			return true
		}
	}
	return false
}

func hasStopStrings(requestMap map[string]interface{}) bool {
	switch stop := requestMap["stop"].(type) {
	case string:
		return stop != ""
	case []interface{}:
		return len(stop) > 0
	}
	return false
}

// promptIDsRequestBody is the stored request as a one-token chat completion
// with return_token_ids: its prompt_token_ids are the validator's own rendering
// of the messages, tools and template arguments.
func promptIDsRequestBody(requestMap map[string]interface{}) ([]byte, error) {
	render := make(map[string]interface{}, len(requestMap))
	for k, v := range requestMap {
		render[k] = v
	}
	render["stream"] = false
	delete(render, "stream_options")
	render["max_tokens"] = 1
	render["max_completion_tokens"] = 1
	render["min_tokens"] = 1
	render["logprobs"] = false
	delete(render, "top_logprobs")
	delete(render, "logprobs_mode")
	render["return_token_ids"] = true
	if _, ok := render["n"]; ok {
		render["n"] = 1
	}
	return json.Marshal(render)
}

// StoredFinishReason is the finish_reason of the stored answer: the choice's
// in a JSON body, the last non-empty one in the events of a stream.
func StoredFinishReason(responsePayload []byte) string {
	var streamed completionapi.SerializedStreamedResponse
	if json.Unmarshal(responsePayload, &streamed) == nil && len(streamed.Events) > 0 {
		finish := ""
		for _, line := range streamed.Events {
			data, ok := strings.CutPrefix(line, "data: ")
			if !ok || strings.TrimSpace(data) == "[DONE]" {
				continue
			}
			if f := finishReasonOf([]byte(data)); f != "" {
				finish = f
			}
		}
		return finish
	}
	return finishReasonOf(responsePayload)
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

// answerShapeReason checks the end of the answer against how it finished: an
// answer that stopped must end on an end token (unless the request carried
// stop strings) and not before min_tokens; an answer cut by length must hold
// max_tokens unless the context itself could have cut it.
func answerShapeReason(model string, prompt, out []int, finish string, p TapeParams, stopStrings bool) string {
	if len(out) == 0 {
		return "Empty answer."
	}
	eos := modelEOS[model]
	switch finish {
	case "stop", "tool_calls":
		if p.MinTokens > 0 && len(out) < p.MinTokens {
			return "Answer stopped before min_tokens."
		}
		if !stopStrings && len(eos) > 0 && !contains(eos, out[len(out)-1]) {
			return "Answer stopped without an end token."
		}
	case "length":
		if p.MaxTokens > 0 && len(out) < p.MaxTokens && len(prompt)+len(out) < lengthRuleContextCap {
			return "Answer cut before max_tokens."
		}
	}
	return ""
}

func contains(ids []int, x int) bool {
	for _, id := range ids {
		if id == x {
			return true
		}
	}
	return false
}
