package filters

import (
	"bytes"
	stdjson "encoding/json"

	json "github.com/goccy/go-json"

	"devshard"
)

// The bounds are in README.md, "Structural bounds before the decode"; PromptBytesPerToken is in capacity.md, "The balance floor"; ChatTemplateMinimumTokens is the fewest tokens a chat template adds to a prompt, in request.md, "Step 6 in detail".
const (
	MaxNestingDepth = 32

	MaxBodyBytes = 10 << 20

	PromptBytesPerToken = 4

	ChatTemplateMinimumTokens = 4

	MaxStructuralNodes = 250_000

	MaxContainerMembers = 65536
)

// Document wraps a decoded request body. No mutex: the pipeline runs single-goroutine per request.
type Document struct {
	raw      map[string]any
	patterns *patternBudget
}

func (d *Document) patternBudget(limit int) *patternBudget {
	if d.patterns == nil {
		d.patterns = &patternBudget{limit: limit}
	}
	return d.patterns
}

// ParseDocument bounds body's size, nesting depth, node count and members per container, then decodes it into a Document.
func ParseDocument(body []byte) (*Document, error) {
	if len(body) > MaxBodyBytes {
		return nil, Reject("request body size %d exceeds limit %d", len(body), MaxBodyBytes)
	}
	if err := ensureStructuralBounds(body, MaxNestingDepth, MaxStructuralNodes, MaxContainerMembers); err != nil {
		return nil, err
	}
	// Standard library on purpose: its error text reaches the client verbatim and the goldens pin that wording.
	var raw map[string]any
	decoder := stdjson.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	if err := decoder.Decode(&raw); err != nil {
		return nil, Reject("parse request: %v", err)
	}
	return &Document{raw: raw}, nil
}

func (d *Document) Get(key string) (any, bool) {
	value, ok := d.raw[key]
	return value, ok
}

func (d *Document) Set(key string, value any) {
	d.raw[key] = value
}

func (d *Document) Delete(key string) {
	delete(d.raw, key)
}

func (d *Document) Has(key string) bool {
	_, ok := d.raw[key]
	return ok
}

func (d *Document) Object(key string) (map[string]any, bool) {
	value, ok := d.raw[key].(map[string]any)
	return value, ok
}

func (d *Document) Array(key string) ([]any, bool) {
	value, ok := d.raw[key].([]any)
	return value, ok
}

func (d *Document) Uint(key string) (uint64, bool) {
	value, ok := d.raw[key]
	if !ok {
		return 0, false
	}
	return devshard.JSONNumericUint64(value)
}

// ObjectField reports absent (present=false), wrong-type (present && !isObject), or the object.
func (d *Document) ObjectField(key string) (obj map[string]any, present, isObject bool) {
	raw, exists := d.raw[key]
	if !exists {
		return nil, false, false
	}
	obj, ok := raw.(map[string]any)
	return obj, true, ok
}

func (d *Document) Raw() map[string]any {
	return d.raw
}

// Marshal encodes the document without HTML escaping; map keys are sorted, so output is deterministic.
func (d *Document) Marshal() ([]byte, error) {
	body, err := json.MarshalWithOption(d.raw, json.DisableHTMLEscape())
	if err != nil {
		return nil, Reject("marshal request: %v", err)
	}
	return body, nil
}

// presentField reports a decoded field that exists and was not sent as an explicit null. See README.md, "Parameter rules".
func presentField(value any, exists bool) bool {
	return exists && value != nil
}

func ensureStructuralBounds(body []byte, maxDepth, maxNodes, maxMembers int) error {
	depth := 0
	nodes := 0
	separators := make([]int, maxDepth+1)
	for index := 0; index < len(body); index++ {
		switch body[index] {
		case '"':
			index += stringLiteralEnd(body[index+1:])
			continue
		case '{', '[':
			depth++
			if depth > maxDepth {
				return Reject("request nesting depth exceeds limit %d", maxDepth)
			}
			separators[depth] = 0
		case '}', ']':
			depth--
			if depth < 0 {
				depth = 0
			}
			continue
		case ',':
			separators[depth]++
			if separators[depth] >= maxMembers {
				return Reject("request container member count exceeds limit %d", maxMembers)
			}
		default:
			continue
		}
		nodes++
		if nodes > maxNodes {
			return Reject("request node count exceeds limit %d", maxNodes)
		}
	}
	return nil
}

// stringLiteralEnd returns how far past body's start the literal's closing quote sits, or its length when unterminated.
func stringLiteralEnd(body []byte) int {
	for offset := 0; offset < len(body); {
		quote := bytes.IndexByte(body[offset:], '"')
		if quote < 0 {
			return len(body)
		}
		closing := offset + quote
		backslashes := 0
		for closing-backslashes > offset && body[closing-backslashes-1] == '\\' {
			backslashes++
		}
		if backslashes%2 == 0 {
			return closing + 1
		}
		offset = closing + 1
	}
	return len(body)
}

// EstimatedPromptTokens is an input size, not a tokenizer call: the one definition of the gateway's own
// prompt estimate. See README.md, "Output token limits".
func EstimatedPromptTokens(bodyBytes int) uint64 {
	if bodyBytes <= 0 {
		return 1
	}
	return uint64((bodyBytes + PromptBytesPerToken - 1) / PromptBytesPerToken)
}
