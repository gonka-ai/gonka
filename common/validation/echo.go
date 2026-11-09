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
}

// TapeParams are the sampling parameters the executor's tape ran under.
type TapeParams struct {
	Temperature float64
	TopP        float64
	TopK        int
}

func tapeParams(requestMap map[string]interface{}) TapeParams {
	p := TapeParams{Temperature: 1, TopP: 1}
	if v, ok := requestMap["temperature"].(float64); ok {
		p.Temperature = v
	}
	if v, ok := requestMap["top_p"].(float64); ok {
		p.TopP = v
	}
	if v, ok := requestMap["top_k"].(float64); ok {
		p.TopK = int(v)
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
// MiniMax-M2.7 FP8 the same way (4.1-4.6 % off the winner, long answers 4.6 %)
// and honest Qwen3-0.6B on one RTX 3090 (2026-11-04, 64 answers). FlipRate is the
// honest share of positions off the winner with a margin for the length bound,
// MaxGap the largest honest gap seen with a margin.
var echoThresholds = map[string]EchoThresholds{
	"deepseek-ai/DeepSeek-V4-Flash-0731": {ZMin: -3, FlipRate: 0.05, MaxGap: 5},
	"MiniMaxAI/MiniMax-M2.7":             {ZMin: -3, FlipRate: 0.06, MaxGap: 5},
	"Qwen/Qwen3-0.6B":                    {ZMin: -3, FlipRate: 0.006, MaxGap: 6.4},
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
	if t.FlipRate > 0 {
		n := float64(s.Steps)
		// The honest count is binomial; this is its 99.5% bound.
		bound := t.FlipRate*n + 2.576*math.Sqrt(n*t.FlipRate*(1-t.FlipRate)) + 0.5
		if float64(s.Count) > bound {
			return false
		}
	}
	return t.MaxGap == 0 || s.MaxGap <= t.MaxGap
}

// EchoValidationResult holds the signals of a scored answer.
type EchoValidationResult struct {
	BaseValidationResult
	Model   string
	Signals EchoSignals
}

func (r EchoValidationResult) IsSuccessful() bool {
	return EchoThresholdsFor(r.Model).Pass(r.Signals)
}

// ScoreEcho scores an answer from the validator's logprobs of its positions:
// logprobs[t] maps a token to its raw logprob at position t and includes the
// answer's own token out[t].
func ScoreEcho(logprobs []map[int]float64, out []int, seed uint64, vocab int, p TapeParams) EchoSignals {
	var s EchoSignals
	var sumR float64
	for t, x := range out {
		kept, sx, logPTilde := keptSet(logprobs[t], x, p)
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
	if s.Steps > 0 {
		s.MeanR = sumR / float64(s.Steps)
		s.Z = (sumR - eulerGamma*float64(s.Steps)) / (gumbelSigma * math.Sqrt(float64(s.Steps)))
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
func keptSet(toks map[int]float64, x int, p TapeParams) ([]candidate, float64, float64) {
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
	cand, tail := head, 0.0
	if p.TopK > 0 && p.TopK <= EchoTopLogprobs {
		if len(cand) > p.TopK {
			cand = cand[:p.TopK]
		}
	} else if T == 1 {
		mass := 0.0
		for _, c := range head {
			mass += math.Exp(c.score)
		}
		tail = math.Max(0, 1-mass)
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
func ExecuteEchoValidation(
	ctx context.Context,
	inferenceID string,
	promptPayload []byte,
	responsePayload []byte,
	execute func(ctx context.Context, body []byte) (*http.Response, error),
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

	body, err := EchoRequestBody(model, prompt, out)
	if err != nil {
		return nil, err
	}
	resp, err := execute(ctx, body)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusBadRequest || resp.StatusCode == http.StatusUnprocessableEntity {
		// The echo holds nothing but the executor's own token ids.
		return &InvalidInferenceResult{InferenceId: inferenceID, Reason: "Validator cannot embed the stored token ids."}, nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("echo validation: status %d: %s", resp.StatusCode, bytes.TrimSpace(respBody))
	}
	logprobs, err := ParseEcho(respBody, len(prompt), len(out))
	if err != nil {
		return nil, err
	}
	signals := ScoreEcho(logprobs, out, seed, vocab, tapeParams(requestMap))
	return &EchoValidationResult{
		BaseValidationResult: BaseValidationResult{InferenceId: inferenceID, ResponseBytes: respBody},
		Model:                model,
		Signals:              signals,
	}, nil
}
