package paramvalidators

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMetadataValidatorAccepts(t *testing.T) {
	v := MetadataValidator{}
	tests := []struct {
		name string
		body string
	}{
		{name: "absent", body: `{"messages":[]}`},
		{name: "empty object", body: `{"metadata":{}}`},
		{name: "single entry", body: `{"metadata":{"trace_id":"abc"}}`},
		{name: "tracing payload", body: `{"metadata":{"trace_id":"abc","span_id":"def","user_segment":"beta"}}`},
		{name: "exactly at key limit", body: `{"metadata":` + buildKeys(t, 16) + `}`},
		{name: "exactly at key length", body: `{"metadata":{"` + strings.Repeat("k", 64) + `":"v"}}`},
		{name: "exactly at value length", body: `{"metadata":{"k":"` + strings.Repeat("v", 512) + `"}}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.NoError(t, v.Validate(ValidatorContext{Document: parseDocument(t, tt.body)}))
		})
	}
}

func TestMetadataValidatorRejects(t *testing.T) {
	v := MetadataValidator{}
	tests := []struct {
		name    string
		body    string
		wantErr error
	}{
		{name: "wrapper is string", body: `{"metadata":"v"}`, wantErr: ErrMetadataShape},
		{name: "wrapper is array", body: `{"metadata":[]}`, wantErr: ErrMetadataShape},
		{name: "wrapper is bool", body: `{"metadata":true}`, wantErr: ErrMetadataShape},
		{name: "key count over limit", body: `{"metadata":` + buildKeys(t, 17) + `}`, wantErr: ErrMetadataKeyCount},
		{name: "key length over limit", body: `{"metadata":{"` + strings.Repeat("k", 65) + `":"v"}}`, wantErr: ErrMetadataKey},
		{name: "value not a string (number)", body: `{"metadata":{"k":42}}`, wantErr: ErrMetadataValue},
		{name: "value not a string (object)", body: `{"metadata":{"k":{}}}`, wantErr: ErrMetadataValue},
		{name: "value not a string (array)", body: `{"metadata":{"k":["v"]}}`, wantErr: ErrMetadataValue},
		{name: "value length over limit", body: `{"metadata":{"k":"` + strings.Repeat("v", 513) + `"}}`, wantErr: ErrMetadataValue},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := v.Validate(ValidatorContext{Document: parseDocument(t, tt.body)})
			require.Error(t, err)
			require.ErrorIs(t, err, tt.wantErr)
		})
	}
}

// Custom limits override the OpenAI-documented defaults.
func TestMetadataValidatorRespectsCustomLimits(t *testing.T) {
	v := MetadataValidator{MaxKeys: 1, MaxKeyLen: 4, MaxValueLen: 4}

	require.NoError(t, v.Validate(ValidatorContext{Document: parseDocument(t, `{"metadata":{"key":"val"}}`)}))

	err := v.Validate(ValidatorContext{Document: parseDocument(t, `{"metadata":{"k1":"v","k2":"v"}}`)})
	require.ErrorIs(t, err, ErrMetadataKeyCount)

	err = v.Validate(ValidatorContext{Document: parseDocument(t, `{"metadata":{"keyy":"v"}}`)})
	require.NoError(t, err) // 4 chars exactly
	err = v.Validate(ValidatorContext{Document: parseDocument(t, `{"metadata":{"keyyy":"v"}}`)})
	require.ErrorIs(t, err, ErrMetadataKey)
}

// buildKeys returns a JSON object literal with n distinct string-valued entries.
func buildKeys(tb testing.TB, n int) string {
	tb.Helper()
	var b strings.Builder
	b.WriteByte('{')
	for i := 0; i < n; i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt := `"k` + strings.Repeat("0", 0)
		_ = fmt
		b.WriteString(`"k`)
		// distinct keys k0..kN-1
		for _, c := range []byte{byte('0' + (i / 10)), byte('0' + (i % 10))} {
			b.WriteByte(c)
		}
		b.WriteString(`":"v"`)
	}
	b.WriteByte('}')
	return b.String()
}
