package paramvalidators

import (
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func defaultChatTemplateKwargsValidator() ChatTemplateKwargsValidator {
	return ChatTemplateKwargsValidator{
		MaxDepth: 16,
		MaxSize:  16 * 1024,
		MaxNodes: 128,
	}
}

func TestChatTemplateKwargsValidatorAccepts(t *testing.T) {
	v := defaultChatTemplateKwargsValidator()
	tests := []struct {
		name string
		body string
	}{
		{name: "absent", body: `{"messages":[]}`},
		{name: "empty object", body: `{"chat_template_kwargs":{}}`},
		{name: "kimi thinking shape", body: `{"chat_template_kwargs":{"thinking":true}}`},
		{name: "qwen enable_thinking + preserve", body: `{"chat_template_kwargs":{"enable_thinking":true,"preserve_thinking":false}}`},
		{name: "add_generation_prompt is allowed", body: `{"chat_template_kwargs":{"add_generation_prompt":true}}`},
		{name: "nested at depth limit", body: `{"chat_template_kwargs":` + nestedObjectChain(16) + `}`},
		{name: "array of strings", body: `{"chat_template_kwargs":{"tags":["a","b","c"]}}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			doc := parseDocument(t, tt.body)
			require.NoError(t, v.Validate(ValidatorContext{Document: doc}))
		})
	}
}

func TestChatTemplateKwargsValidatorRejects(t *testing.T) {
	v := defaultChatTemplateKwargsValidator()
	tests := []struct {
		name    string
		body    string
		wantErr error
	}{
		{name: "wrapper not object", body: `{"chat_template_kwargs":"hi"}`, wantErr: ErrChatTemplateKwargsShape},
		{name: "wrapper is array", body: `{"chat_template_kwargs":[1,2]}`, wantErr: ErrChatTemplateKwargsShape},
		{name: "depth exceeds limit", body: `{"chat_template_kwargs":` + nestedObjectChain(17) + `}`, wantErr: ErrSchemaDepth},
		{name: "deep recursion attack", body: `{"chat_template_kwargs":` + nestedObjectChain(200) + `}`, wantErr: ErrSchemaDepth},
		{name: "node count exceeds limit", body: `{"chat_template_kwargs":` + flatPropertiesObject(200) + `}`, wantErr: ErrSchemaNodes},
		{name: "size exceeds limit", body: `{"chat_template_kwargs":{"x":"` + strings.Repeat("a", 17*1024) + `"}}`, wantErr: ErrSchemaSize},
		// CVE-2025-61620: Jinja template smuggling via chat_template key
		{name: "forbidden chat_template key", body: `{"chat_template_kwargs":{"chat_template":"{% for x in range(99999) %}{% endfor %}"}}`, wantErr: ErrChatTemplateKwargsForbiddenKey},
		// CVE-2025-62426: synchronous tokenization stalls handler
		{name: "forbidden tokenize key", body: `{"chat_template_kwargs":{"tokenize":true}}`, wantErr: ErrChatTemplateKwargsForbiddenKey},
		// Other positional-arg overrides
		{name: "forbidden tools key", body: `{"chat_template_kwargs":{"tools":[]}}`, wantErr: ErrChatTemplateKwargsForbiddenKey},
		{name: "forbidden documents key", body: `{"chat_template_kwargs":{"documents":[]}}`, wantErr: ErrChatTemplateKwargsForbiddenKey},
		{name: "forbidden conversation key", body: `{"chat_template_kwargs":{"conversation":[]}}`, wantErr: ErrChatTemplateKwargsForbiddenKey},
		{name: "forbidden continue_final_message", body: `{"chat_template_kwargs":{"continue_final_message":true}}`, wantErr: ErrChatTemplateKwargsForbiddenKey},
		{name: "forbidden max_length", body: `{"chat_template_kwargs":{"max_length":1024}}`, wantErr: ErrChatTemplateKwargsForbiddenKey},
		{name: "forbidden alongside legit", body: `{"chat_template_kwargs":{"thinking":true,"chat_template":"bomb"}}`, wantErr: ErrChatTemplateKwargsForbiddenKey},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			doc := parseDocument(t, tt.body)
			err := v.Validate(ValidatorContext{Document: doc})
			require.Error(t, err)
			require.ErrorIs(t, err, tt.wantErr)
		})
	}
}

// nestedObjectChain produces {"x": {"x": ... {} }} -- a plain-object chain (no JSON Schema).
func nestedObjectChain(depth int) string {
	if depth <= 1 {
		return `{}`
	}
	return `{"x":` + nestedObjectChain(depth-1) + `}`
}

// flatPropertiesObject is a single object with `count` keys, each mapped to `{}`.
// Used to exhaust the node-count budget without hitting the depth cap.
func flatPropertiesObject(count int) string {
	var b strings.Builder
	b.WriteByte('{')
	for i := 0; i < count; i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(`"k`)
		b.WriteString(strconv.Itoa(i))
		b.WriteString(`":{}`)
	}
	b.WriteByte('}')
	return b.String()
}
