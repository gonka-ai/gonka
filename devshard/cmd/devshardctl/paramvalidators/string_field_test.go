package paramvalidators

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestStringFieldValidatorAcceptsForUser(t *testing.T) {
	v := StringFieldValidator{FieldName: "user", DefaultMaxLen: 512}
	tests := []struct {
		name string
		body string
	}{
		{name: "absent", body: `{"messages":[]}`},
		{name: "empty string", body: `{"user":""}`},
		{name: "typical openai shape", body: `{"user":"user_abc123"}`},
		{name: "uuid", body: `{"user":"550e8400-e29b-41d4-a716-446655440000"}`},
		{name: "email shape", body: `{"user":"user@example.com"}`},
		{name: "base64-ish", body: `{"user":"YWJjZA+/=="}`},
		{name: "session id with colons", body: `{"user":"langchain:session:42"}`},
		{name: "exactly at length", body: `{"user":"` + strings.Repeat("x", 512) + `"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.NoError(t, v.Validate(ValidatorContext{Document: parseDocument(t, tt.body)}))
		})
	}
}

func TestStringFieldValidatorRejectsForUser(t *testing.T) {
	v := StringFieldValidator{FieldName: "user", DefaultMaxLen: 512}
	tests := []struct {
		name    string
		body    string
		wantErr error
	}{
		{name: "number", body: `{"user":42}`, wantErr: ErrStringFieldShape},
		{name: "boolean", body: `{"user":true}`, wantErr: ErrStringFieldShape},
		{name: "object", body: `{"user":{}}`, wantErr: ErrStringFieldShape},
		{name: "array", body: `{"user":[]}`, wantErr: ErrStringFieldShape},
		{name: "null", body: `{"user":null}`, wantErr: ErrStringFieldShape},
		{name: "length over limit", body: `{"user":"` + strings.Repeat("x", 513) + `"}`, wantErr: ErrStringFieldLength},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := v.Validate(ValidatorContext{Document: parseDocument(t, tt.body)})
			require.Error(t, err)
			require.ErrorIs(t, err, tt.wantErr)
			require.Contains(t, err.Error(), "user:")
		})
	}
}

func TestStringFieldValidatorAcceptsForSafetyIdentifier(t *testing.T) {
	v := StringFieldValidator{FieldName: "safety_identifier", DefaultMaxLen: 512}
	tests := []struct {
		name string
		body string
	}{
		{name: "absent", body: `{"messages":[]}`},
		{name: "empty string", body: `{"safety_identifier":""}`},
		{name: "hashed user id", body: `{"safety_identifier":"sha256:abc123"}`},
		{name: "uuid", body: `{"safety_identifier":"550e8400-e29b-41d4-a716-446655440000"}`},
		{name: "exactly at length", body: `{"safety_identifier":"` + strings.Repeat("x", 512) + `"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.NoError(t, v.Validate(ValidatorContext{Document: parseDocument(t, tt.body)}))
		})
	}
}

func TestStringFieldValidatorRejectsForSafetyIdentifier(t *testing.T) {
	v := StringFieldValidator{FieldName: "safety_identifier", DefaultMaxLen: 512}
	tests := []struct {
		name    string
		body    string
		wantErr error
	}{
		{name: "number", body: `{"safety_identifier":42}`, wantErr: ErrStringFieldShape},
		{name: "boolean", body: `{"safety_identifier":true}`, wantErr: ErrStringFieldShape},
		{name: "object", body: `{"safety_identifier":{}}`, wantErr: ErrStringFieldShape},
		{name: "array", body: `{"safety_identifier":[]}`, wantErr: ErrStringFieldShape},
		{name: "null", body: `{"safety_identifier":null}`, wantErr: ErrStringFieldShape},
		{name: "length over limit", body: `{"safety_identifier":"` + strings.Repeat("x", 513) + `"}`, wantErr: ErrStringFieldLength},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := v.Validate(ValidatorContext{Document: parseDocument(t, tt.body)})
			require.Error(t, err)
			require.ErrorIs(t, err, tt.wantErr)
			require.Contains(t, err.Error(), "safety_identifier:")
		})
	}
}

func TestStringFieldValidatorRespectsCustomLimit(t *testing.T) {
	v := StringFieldValidator{FieldName: "user", MaxLen: 8, DefaultMaxLen: 512}
	require.NoError(t, v.Validate(ValidatorContext{Document: parseDocument(t, `{"user":"abcdefgh"}`)}))
	err := v.Validate(ValidatorContext{Document: parseDocument(t, `{"user":"abcdefghi"}`)})
	require.ErrorIs(t, err, ErrStringFieldLength)
}

func TestStringFieldValidatorZeroCapDisablesLengthCheck(t *testing.T) {
	v := StringFieldValidator{FieldName: "user"}
	huge := strings.Repeat("x", 1024*1024)
	require.NoError(t, v.Validate(ValidatorContext{Document: map[string]any{"user": huge}}))
}
