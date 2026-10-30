// Package replay loads captured chat requests and their expected ML responses.
package replay

import (
	"bufio"
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
type Sample struct {
	Model       string    `json:"model"`
	Stream      bool      `json:"stream"`
	Temperature *float64  `json:"temperature"`
	Logprobs    bool      `json:"logprobs"`
	TopLogprobs int       `json:"top_logprobs"`
	MaxTokens   int64     `json:"max_tokens"`
	Messages    []Message `json:"messages"`
	Response    Response  `json:"response"`
}

type Dataset struct {
	samples       []Sample
	byModel       map[string][]int
	byFingerprint map[string]int
	byMessages    map[string]int
}

func LoadFile(path string) (*Dataset, error) {
	return loadFile(path, true)
}

// LoadRequestsFile loads requests for an external gateway; captured responses
// are optional and are never used to validate the remote model's output.
func LoadRequestsFile(path string) (*Dataset, error) {
	return loadFile(path, false)
}

func loadFile(path string, requireResponse bool) (*Dataset, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open replay file: %w", err)
	}
	defer file.Close()

	dataset := &Dataset{
		byModel:       make(map[string][]int),
		byFingerprint: make(map[string]int),
		byMessages:    make(map[string]int),
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
		if err := validateSample(sample, requireResponse); err != nil {
			return nil, fmt.Errorf("replay file line %d: %w", line, err)
		}
		fingerprint := Fingerprint(sample.Model, sample.Messages)
		if _, exists := dataset.byFingerprint[fingerprint]; exists {
			return nil, fmt.Errorf("replay file line %d: duplicate request fingerprint %s", line, fingerprint)
		}
		messageFingerprint := MessagesFingerprint(sample.Messages)
		if _, exists := dataset.byMessages[messageFingerprint]; exists {
			dataset.byMessages[messageFingerprint] = -1 // Ambiguous across models; disable the alias fallback.
		} else {
			dataset.byMessages[messageFingerprint] = len(dataset.samples)
		}
		dataset.byFingerprint[fingerprint] = len(dataset.samples)
		dataset.byModel[sample.Model] = append(dataset.byModel[sample.Model], len(dataset.samples))
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
	if !ok || index < 0 {
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

// HasModel reports whether the dataset has at least one sample for a model.
func (d *Dataset) HasModel(model string) bool { return d != nil && len(d.byModel[model]) > 0 }

// SelectModel samples only the requested model, keeping the original dataset index.
func (s *Selector) SelectModel(model string, requestIndex uint64) (Sample, int, bool) {
	if s == nil || s.dataset == nil {
		return Sample{}, 0, false
	}
	indices := s.dataset.byModel[model]
	if len(indices) == 0 {
		return Sample{}, 0, false
	}
	index := indices[s.selectionIndex(requestIndex, len(indices))]
	sample, ok := s.dataset.Sample(index)
	return sample, index, ok
}

func (s *Selector) Select(requestIndex uint64) (Sample, int, bool) {
	if s == nil || s.dataset == nil || s.dataset.Len() == 0 {
		return Sample{}, 0, false
	}
	index := s.selectionIndex(requestIndex, s.dataset.Len())
	sample, ok := s.dataset.Sample(index)
	return sample, index, ok
}

func (s *Selector) selectionIndex(requestIndex uint64, count int) int {
	var seed [16]byte
	binary.BigEndian.PutUint64(seed[:8], uint64(s.seed))
	binary.BigEndian.PutUint64(seed[8:], requestIndex)
	digest := sha256.Sum256(seed[:])
	return int(binary.BigEndian.Uint64(digest[:8]) % uint64(count))
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

func validateSample(sample Sample, requireResponse bool) error {
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
	if requireResponse && strings.TrimSpace(sample.Response.Role) == "" {
		return fmt.Errorf("response.role is required")
	}
	if sample.MaxTokens < 0 {
		return fmt.Errorf("max_tokens must not be negative")
	}
	return nil
}

func cloneSample(sample Sample) Sample {
	sample.Messages = append([]Message(nil), sample.Messages...)
	if sample.Temperature != nil {
		value := *sample.Temperature
		sample.Temperature = &value
	}
	return sample
}
