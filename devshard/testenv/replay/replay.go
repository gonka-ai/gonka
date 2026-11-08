// Package replay loads captured chat requests and their expected ML responses.
package replay

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
)

type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type Response struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// Sample is one captured client request and the response that a replay ML
// node should produce for it.
//
// A sample with Recorded set also carries the exact Request the ML node got
// and is looked up by RequestFingerprint of that request instead of by
// model and messages, so recordings that share a prompt stay apart.
type Sample struct {
	Name        string    `json:"name,omitempty"`
	Model       string    `json:"model"`
	Stream      bool      `json:"stream"`
	Temperature float64   `json:"temperature"`
	MaxTokens   int64     `json:"max_tokens"`
	Messages    []Message `json:"messages"`
	Response    Response  `json:"response"`

	Request  json.RawMessage `json:"request,omitempty"`
	Recorded *Recorded       `json:"recorded,omitempty"`
	Build    *Build          `json:"build,omitempty"`
	// ClientRequest is the body the client sent before the gateway and the
	// executor rewrote it into Request, when the recording kept it.
	ClientRequest json.RawMessage `json:"client_request,omitempty"`
}

// Recorded is what an ML node returned for Sample.Request, kept as raw JSON so
// a replay serves the same bytes: Stream holds the SSE data payloads in order
// without the closing [DONE], NonStream the body of the same answer served
// without streaming.
type Recorded struct {
	Stream    []json.RawMessage `json:"stream"`
	NonStream json.RawMessage   `json:"nonstream"`
}

// Build names the serving build a recording came from.
type Build struct {
	Model    string `json:"model,omitempty"`
	Revision string `json:"revision,omitempty"`
	Variant  string `json:"variant,omitempty"`
	Commit   string `json:"commit,omitempty"`
}

type Dataset struct {
	samples       []Sample
	byFingerprint map[string]int
	byMessages    map[string]int
	byRequest     map[string]int
}

func LoadFile(path string) (*Dataset, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open replay file: %w", err)
	}
	defer file.Close()

	dataset := &Dataset{
		byFingerprint: make(map[string]int),
		byMessages:    make(map[string]int),
		byRequest:     make(map[string]int),
	}
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)
	line := 0
	for scanner.Scan() {
		line++
		body := strings.TrimSpace(scanner.Text())
		if body == "" {
			continue
		}
		var sample Sample
		if err := json.Unmarshal([]byte(body), &sample); err != nil {
			return nil, fmt.Errorf("decode replay file line %d: %w", line, err)
		}
		if err := validateSample(sample); err != nil {
			return nil, fmt.Errorf("replay file line %d: %w", line, err)
		}
		if sample.Recorded != nil {
			fingerprint, err := RequestFingerprint(sample.Request)
			if err != nil {
				return nil, fmt.Errorf("replay file line %d: request: %w", line, err)
			}
			if _, exists := dataset.byRequest[fingerprint]; exists {
				return nil, fmt.Errorf("replay file line %d: duplicate recorded request fingerprint %s", line, fingerprint)
			}
			dataset.byRequest[fingerprint] = len(dataset.samples)
			dataset.samples = append(dataset.samples, cloneSample(sample))
			continue
		}
		fingerprint := Fingerprint(sample.Model, sample.Messages)
		if _, exists := dataset.byFingerprint[fingerprint]; exists {
			return nil, fmt.Errorf("replay file line %d: duplicate request fingerprint %s", line, fingerprint)
		}
		messageFingerprint := MessagesFingerprint(sample.Messages)
		if _, exists := dataset.byMessages[messageFingerprint]; exists {
			return nil, fmt.Errorf("replay file line %d: duplicate messages fingerprint %s", line, messageFingerprint)
		}
		dataset.byFingerprint[fingerprint] = len(dataset.samples)
		dataset.byMessages[messageFingerprint] = len(dataset.samples)
		dataset.samples = append(dataset.samples, cloneSample(sample))
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read replay file: %w", err)
	}
	if len(dataset.samples) == 0 {
		return nil, fmt.Errorf("replay file contains no samples")
	}
	return dataset, nil
}

func (d *Dataset) Len() int {
	if d == nil {
		return 0
	}
	return len(d.samples)
}

// Models returns the unique model IDs present in the dataset in stable order.
// The load-test bootstrap uses this list to register every replay runtime
// before the Gateway starts accepting requests.
func (d *Dataset) Models() []string {
	if d == nil {
		return nil
	}
	seen := make(map[string]struct{}, len(d.samples))
	models := make([]string, 0, len(d.samples))
	for _, sample := range d.samples {
		if _, ok := seen[sample.Model]; ok {
			continue
		}
		seen[sample.Model] = struct{}{}
		models = append(models, sample.Model)
	}
	sort.Strings(models)
	return models
}

func (d *Dataset) Sample(index int) (Sample, bool) {
	if d == nil || index < 0 || index >= len(d.samples) {
		return Sample{}, false
	}
	return cloneSample(d.samples[index]), true
}

func (d *Dataset) Lookup(model string, messages []Message) (Sample, int, bool) {
	if d == nil {
		return Sample{}, 0, false
	}
	index, ok := d.byFingerprint[Fingerprint(model, messages)]
	if !ok {
		index, ok = d.byMessages[MessagesFingerprint(messages)]
	}
	if !ok {
		return Sample{}, 0, false
	}
	return cloneSample(d.samples[index]), index, true
}

// LookupRequest finds the recorded sample for a chat request body by its
// RequestFingerprint. Samples without a recording are not searched.
func (d *Dataset) LookupRequest(body []byte) (Sample, int, bool) {
	if d == nil || len(d.byRequest) == 0 {
		return Sample{}, 0, false
	}
	fingerprint, err := RequestFingerprint(body)
	if err != nil {
		return Sample{}, 0, false
	}
	index, ok := d.byRequest[fingerprint]
	if !ok {
		return Sample{}, 0, false
	}
	return cloneSample(d.samples[index]), index, true
}

// Selector makes a deterministic pseudo-random selection with replacement.
// The request index makes selection independent of goroutine scheduling.
type Selector struct {
	dataset *Dataset
	seed    int64
}

func NewSelector(dataset *Dataset, seed int64) *Selector {
	return &Selector{dataset: dataset, seed: seed}
}

func (s *Selector) Select(requestIndex uint64) (Sample, int, bool) {
	if s == nil || s.dataset == nil || s.dataset.Len() == 0 {
		return Sample{}, 0, false
	}
	var seed [16]byte
	binary.BigEndian.PutUint64(seed[:8], uint64(s.seed))
	binary.BigEndian.PutUint64(seed[8:], requestIndex)
	digest := sha256.Sum256(seed[:])
	index := int(binary.BigEndian.Uint64(digest[:8]) % uint64(s.dataset.Len()))
	sample, ok := s.dataset.Sample(index)
	return sample, index, ok
}

func Fingerprint(model string, messages []Message) string {
	body, _ := json.Marshal(struct {
		Model    string    `json:"model"`
		Messages []Message `json:"messages"`
	}{Model: model, Messages: messages})
	digest := sha256.Sum256(body)
	return hex.EncodeToString(digest[:])
}

func MessagesFingerprint(messages []Message) string {
	body, _ := json.Marshal(messages)
	digest := sha256.Sum256(body)
	return hex.EncodeToString(digest[:])
}

// requestKeyFields are the chat request fields that decide a recorded answer:
// the rendered prompt (messages, tools, tool_choice, chat_template_kwargs) and
// what may end or constrain the output. Everything else is left out on
// purpose. A recorded answer is a fixed token sequence rather than a sample,
// so sampling knobs and seed do not pick it, and the validator replays with
// its own seed anyway. stream, stream_options, logprobs, top_logprobs,
// return_token_ids, skip_special_tokens and n are forced on the way to the ML
// node, and the validator replay forces stream on and adds enforced_tokens
// and logprobs_mode, so none of them may split an executor request from its
// validator replay.
var requestKeyFields = []string{
	"model",
	"messages",
	"tools",
	"tool_choice",
	"chat_template_kwargs",
	"max_tokens",
	"min_tokens",
	"stop",
	"stop_token_ids",
	"bad_words",
	"logit_bias",
	"response_format",
	"structured_outputs",
}

// RequestFingerprint hashes the requestKeyFields of a chat request body after
// the normalization the request gets before it reaches the ML node: one output
// limit (max_tokens, else max_completion_tokens, as the executor resolves it),
// tool_choice "auto" when tools are given without one and none without tools,
// a single stop string as a one-element list, and empty values and a zero
// min_tokens as absent. Object keys are sorted, so key order never matters.
func RequestFingerprint(body []byte) (string, error) {
	var request map[string]any
	if err := json.Unmarshal(body, &request); err != nil {
		return "", err
	}
	if request == nil {
		return "", fmt.Errorf("request must be a JSON object")
	}
	key := make(map[string]any, len(requestKeyFields))
	for _, name := range requestKeyFields {
		if value, ok := request[name]; ok && !emptyJSONValue(value) {
			key[name] = value
		}
	}
	if _, ok := key["max_tokens"]; !ok {
		if value, ok := request["max_completion_tokens"]; ok && !emptyJSONValue(value) {
			key["max_tokens"] = value
		}
	}
	if _, ok := key["tools"]; !ok {
		delete(key, "tool_choice")
	} else if _, ok := key["tool_choice"]; !ok {
		key["tool_choice"] = "auto"
	}
	if stop, ok := key["stop"].(string); ok {
		key["stop"] = []any{stop}
	}
	if minTokens, ok := key["min_tokens"].(float64); ok && minTokens == 0 {
		delete(key, "min_tokens")
	}
	encoded, err := json.Marshal(key)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func emptyJSONValue(value any) bool {
	switch typed := value.(type) {
	case nil:
		return true
	case string:
		return typed == ""
	case []any:
		return len(typed) == 0
	case map[string]any:
		return len(typed) == 0
	default:
		return false
	}
}

func validateSample(sample Sample) error {
	if strings.TrimSpace(sample.Model) == "" {
		return fmt.Errorf("model is required")
	}
	if len(sample.Messages) == 0 {
		return fmt.Errorf("messages are required")
	}
	for i, message := range sample.Messages {
		if strings.TrimSpace(message.Role) == "" {
			return fmt.Errorf("messages[%d].role is required", i)
		}
	}
	if strings.TrimSpace(sample.Response.Role) == "" {
		return fmt.Errorf("response.role is required")
	}
	if sample.MaxTokens < 0 {
		return fmt.Errorf("max_tokens must not be negative")
	}
	if sample.Recorded != nil {
		return validateRecorded(sample)
	}
	return nil
}

func validateRecorded(sample Sample) error {
	if len(sample.Request) == 0 {
		return fmt.Errorf("a recorded sample needs its request")
	}
	var request struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(sample.Request, &request); err != nil {
		return fmt.Errorf("request: %w", err)
	}
	if request.Model != sample.Model {
		return fmt.Errorf("request model %q differs from sample model %q", request.Model, sample.Model)
	}
	if len(sample.Recorded.Stream) == 0 {
		return fmt.Errorf("recorded.stream is required")
	}
	for i, chunk := range sample.Recorded.Stream {
		if !isJSONObject(chunk) {
			return fmt.Errorf("recorded.stream[%d] must be a JSON object", i)
		}
	}
	if !isJSONObject(sample.Recorded.NonStream) {
		return fmt.Errorf("recorded.nonstream must be a JSON object")
	}
	return nil
}

func isJSONObject(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	return len(trimmed) > 0 && trimmed[0] == '{'
}

func cloneSample(sample Sample) Sample {
	sample.Messages = append([]Message(nil), sample.Messages...)
	return sample
}
