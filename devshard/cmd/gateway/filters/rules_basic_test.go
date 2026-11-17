package filters

import (
	"strings"
	"testing"
)

func parseTestDocument(t *testing.T, body string) *Document {
	t.Helper()
	document, err := ParseDocument([]byte(body))
	if err != nil {
		t.Fatalf("ParseDocument(%s): %v", body, err)
	}
	return document
}

func getFloat64(t *testing.T, document *Document, param string) float64 {
	t.Helper()
	raw, ok := document.Get(param)
	if !ok {
		t.Fatalf("Get(%s): field missing, want a float64", param)
	}
	value, ok := raw.(float64)
	if !ok {
		t.Fatalf("Get(%s) = %T(%v), want float64", param, raw, raw)
	}
	return value
}

// Test flow:
//  1. Run table cases of `requireUint()` against a `seed` body, varying absent, null, zero, positive, negative, string-encoded, boolean, and fractional values.
//  2. Assert each case's error message matches, or is nil, and that an error carries ErrorStatus 400.
func TestBasicRequireUint(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		wantErr string
	}{
		{"absent passes through", `{}`, ""},
		{"null passes through", `{"seed":null}`, ""},
		{"zero accepted", `{"seed":0}`, ""},
		{"positive integer accepted", `{"seed":42}`, ""},
		{"negative rejected", `{"seed":-5}`, "seed: must be a non-negative integer"},
		{"string encoded rejected", `{"seed":"42"}`, "seed: must be a non-negative integer"},
		{"boolean rejected", `{"seed":true}`, "seed: must be a non-negative integer"},
		{"fractional number rejected", `{"seed":1.5}`, "seed: must be a non-negative integer"},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			document := parseTestDocument(t, testCase.body)
			err := requireUint()(RuleContext{Document: document, Param: "seed"})
			if testCase.wantErr == "" {
				if err != nil {
					t.Fatalf("requireUint() = %v, want nil", err)
				}
				return
			}
			if err == nil {
				t.Fatal("requireUint() = nil, want error")
			}
			if err.Error() != testCase.wantErr {
				t.Errorf("requireUint() error = %q, want %q", err.Error(), testCase.wantErr)
			}
			if got := ErrorStatus(err, 0); got != 400 {
				t.Errorf("ErrorStatus() = %d, want 400", got)
			}
		})
	}
}

// Test flow:
//  1. Run table cases of `requireBool()` against varying fields and bodies: absent, null, true, false, a string, and a number.
//  2. Assert each case's error message matches, or is nil.
func TestBasicRequireBool(t *testing.T) {
	tests := []struct {
		name    string
		field   string
		body    string
		wantErr string
	}{
		{"absent passes through", "skip_special_tokens", `{}`, ""},
		{"null passes through", "skip_special_tokens", `{"skip_special_tokens":null}`, ""},
		{"true accepted", "skip_special_tokens", `{"skip_special_tokens":true}`, ""},
		{"false accepted", "skip_special_tokens", `{"skip_special_tokens":false}`, ""},
		{"string rejected", "skip_special_tokens", `{"skip_special_tokens":"yes"}`, "skip_special_tokens: must be a boolean"},
		{"number rejected", "detokenize", `{"detokenize":1}`, "detokenize: must be a boolean"},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			document := parseTestDocument(t, testCase.body)
			err := requireBool()(RuleContext{Document: document, Param: testCase.field})
			if testCase.wantErr == "" {
				if err != nil {
					t.Fatalf("requireBool() = %v, want nil", err)
				}
				return
			}
			if err == nil {
				t.Fatal("requireBool() = nil, want error")
			}
			if err.Error() != testCase.wantErr {
				t.Errorf("requireBool() error = %q, want %q", err.Error(), testCase.wantErr)
			}
		})
	}
}

// Test flow:
//  1. Run table cases of `requireString(maxBytes)` against varying params and byte caps, with bodies around the length cap plus non-string and null values.
//  2. Assert each case's error message matches, or is nil.
func TestBasicRequireString(t *testing.T) {
	tests := []struct {
		name     string
		param    string
		maxBytes int
		body     string
		wantErr  string
	}{
		{"absent passes through", "note", 10, `{}`, ""},
		{"exactly at cap accepted", "note", 10, `{"note":"1234567890"}`, ""},
		{"one over cap rejected", "note", 10, `{"note":"12345678901"}`, "note: length exceeded: 11 > 10"},
		{"non string type rejected", "note", 10, `{"note":42}`, "note: invalid wrapper shape: must be a string"},
		{"null rejected, not treated as absent", "note", 10, `{"note":null}`, "note: invalid wrapper shape: must be a string"},
		{"user field pins the production bound", "user", 512, `{"user":"` + strings.Repeat("x", 513) + `"}`, "user: length exceeded: 513 > 512"},
		{"user field accepts a normal value", "user", 512, `{"user":"alice"}`, ""},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			document := parseTestDocument(t, testCase.body)
			err := requireString(testCase.maxBytes)(RuleContext{Document: document, Param: testCase.param})
			if testCase.wantErr == "" {
				if err != nil {
					t.Fatalf("requireString() = %v, want nil", err)
				}
				return
			}
			if err == nil {
				t.Fatal("requireString() = nil, want error")
			}
			if err.Error() != testCase.wantErr {
				t.Errorf("requireString() error = %q, want %q", err.Error(), testCase.wantErr)
			}
		})
	}
}

// Test flow:
//  1. Run table cases of `stripParameter()` against a present scalar field, a present object field, and an absent field.
//  2. Assert the parameter is always absent afterward.
func TestBasicStripParameter(t *testing.T) {
	tests := []struct {
		name  string
		param string
		body  string
	}{
		{"present scalar deleted", "service_tier", `{"service_tier":"flex"}`},
		{"present object deleted", "provider", `{"provider":{"order":["openai"]}}`},
		{"absent is a no-op", "store", `{}`},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			document := parseTestDocument(t, testCase.body)
			if err := stripParameter()(RuleContext{Document: document, Param: testCase.param}); err != nil {
				t.Fatalf("stripParameter() = %v, want nil", err)
			}
			if document.Has(testCase.param) {
				t.Errorf("Has(%s) = true after strip, want false", testCase.param)
			}
		})
	}
}

// Test flow:
//  1. Run table cases of `forceLiteral(value)` overwriting a present false value, setting the value on an absent field, and overwriting a present out-of-range int.
//  2. Assert Get returns the forced value in every case.
func TestBasicForceLiteral(t *testing.T) {
	tests := []struct {
		name  string
		param string
		value any
		body  string
	}{
		{"overwrites a present false value", "logprobs", true, `{"logprobs":false}`},
		{"sets on an absent field", "logprobs", true, `{}`},
		{"overwrites a present out-of-range int", "top_logprobs", 5, `{"top_logprobs":20}`},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			document := parseTestDocument(t, testCase.body)
			if err := forceLiteral(testCase.value)(RuleContext{Document: document, Param: testCase.param}); err != nil {
				t.Fatalf("forceLiteral() = %v, want nil", err)
			}
			got, ok := document.Get(testCase.param)
			if !ok {
				t.Fatalf("Get(%s) after forceLiteral: missing", testCase.param)
			}
			if got != testCase.value {
				t.Errorf("Get(%s) = %v, want %v", testCase.param, got, testCase.value)
			}
		})
	}
}

// Test flow:
//  1. Run table cases of `clampFloat(0, 2)` against a `temperature` body, varying in-range, below-min, above-max, boundary, and non-finite/unparseable values.
//  2. For a stripped case, assert Has reports the field removed; otherwise assert the clamped float matches the expected result.
//  3. Assert an absent field is a no-op.
func TestBasicClampFloat(t *testing.T) {
	const param = "temperature"
	tests := []struct {
		name       string
		body       string
		wantStrip  bool
		wantResult float64
	}{
		{"within range integer-shaped", `{"temperature":1}`, false, 1},
		{"within range string encoded", `{"temperature":"1.2"}`, false, 1.2},
		{"below min clamps to min", `{"temperature":-1}`, false, 0},
		{"above max clamps to max", `{"temperature":999999}`, false, 2},
		{"exactly at min boundary kept", `{"temperature":0}`, false, 0},
		{"exactly at max boundary kept", `{"temperature":2}`, false, 2},
		{"non-finite nan stripped", `{"temperature":"nan"}`, true, 0},
		{"non-finite infinity stripped", `{"temperature":"infinity"}`, true, 0},
		{"unparseable type stripped", `{"temperature":true}`, true, 0},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			document := parseTestDocument(t, testCase.body)
			if err := clampFloat(0, 2)(RuleContext{Document: document, Param: param}); err != nil {
				t.Fatalf("clampFloat() = %v, want nil", err)
			}
			if testCase.wantStrip {
				if document.Has(param) {
					t.Errorf("Has(%s) = true, want stripped", param)
				}
				return
			}
			if got := getFloat64(t, document, param); got != testCase.wantResult {
				t.Errorf("Get(%s) = %v, want %v", param, got, testCase.wantResult)
			}
		})
	}
	t.Run("absent is a no-op", func(t *testing.T) {
		document := parseTestDocument(t, `{}`)
		if err := clampFloat(0, 2)(RuleContext{Document: document, Param: param}); err != nil {
			t.Fatalf("clampFloat() = %v, want nil", err)
		}
		if document.Has(param) {
			t.Error("Has(temperature) = true, want absent to stay absent")
		}
	})
}

// Test flow:
//  1. Run table cases of `rejectNonPositiveThenClamp(2)` against a `repetition_penalty` body, varying in-range, at-max, above-max, zero, negative, and non-finite or unparseable values.
//  2. Assert a non-positive value is rejected with an error, a stripped case leaves the field absent, and an otherwise valid value is clamped to the expected result.
//  3. Assert an absent field is a no-op.
func TestBasicRejectNonPositiveThenClamp(t *testing.T) {
	const param = "repetition_penalty"
	tests := []struct {
		name       string
		body       string
		wantErr    string
		wantStrip  bool
		wantResult float64
	}{
		{"within range kept", `{"repetition_penalty":1.5}`, "", false, 1.5},
		{"exactly at max kept", `{"repetition_penalty":2}`, "", false, 2},
		{"above max string encoded clamps to max", `{"repetition_penalty":"5.0"}`, "", false, 2},
		{"zero rejected", `{"repetition_penalty":0}`, "repetition_penalty: must be greater than 0", false, 0},
		{"negative rejected", `{"repetition_penalty":-1}`, "repetition_penalty: must be greater than 0", false, 0},
		{"non-finite nan stripped not rejected", `{"repetition_penalty":"nan"}`, "", true, 0},
		{"unparseable type stripped not rejected", `{"repetition_penalty":[1]}`, "", true, 0},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			document := parseTestDocument(t, testCase.body)
			err := rejectNonPositiveThenClamp(2)(RuleContext{Document: document, Param: param})
			if testCase.wantErr != "" {
				if err == nil {
					t.Fatal("rejectNonPositiveThenClamp() = nil, want error")
				}
				if err.Error() != testCase.wantErr {
					t.Errorf("error = %q, want %q", err.Error(), testCase.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("rejectNonPositiveThenClamp() = %v, want nil", err)
			}
			if testCase.wantStrip {
				if document.Has(param) {
					t.Errorf("Has(%s) = true, want stripped", param)
				}
				return
			}
			if got := getFloat64(t, document, param); got != testCase.wantResult {
				t.Errorf("Get(%s) = %v, want %v", param, got, testCase.wantResult)
			}
		})
	}
	t.Run("absent is a no-op", func(t *testing.T) {
		document := parseTestDocument(t, `{}`)
		if err := rejectNonPositiveThenClamp(2)(RuleContext{Document: document, Param: param}); err != nil {
			t.Fatalf("rejectNonPositiveThenClamp() = %v, want nil", err)
		}
		if document.Has(param) {
			t.Error("Has(repetition_penalty) = true, want absent to stay absent")
		}
	})
}

// Test flow:
//  1. Run table cases of `validTopK(topKMax)` against a `top_k` body, varying the disabled sentinel, fractional truncation, values around the max cap, invalid values, and a non-finite value.
//  2. Assert a rejected case's error message matches, a stripped case leaves the field absent, and an accepted case's int64 result matches the expected value.
//  3. Assert an absent field is a no-op.
func TestBasicValidTopK(t *testing.T) {
	const param = "top_k"
	tests := []struct {
		name       string
		body       string
		wantErr    string
		wantStrip  bool
		wantResult int64
	}{
		{"negative one disabled sentinel accepted", `{"top_k":-1}`, "", false, -1},
		{"exactly one accepted", `{"top_k":1}`, "", false, 1},
		{"large positive accepted", `{"top_k":50}`, "", false, 50},
		{"fractional above one truncated toward zero", `{"top_k":1.5}`, "", false, 1},
		{"fractional near an integer truncated not rounded", `{"top_k":40.7}`, "", false, 40},
		{"one below max accepted unclamped", `{"top_k":262143}`, "", false, 262143},
		{"exactly at max accepted unclamped", `{"top_k":262144}`, "", false, 262144},
		{"one above max clamped down to max", `{"top_k":262145}`, "", false, 262144},
		{"astronomically large clamped down to max", `{"top_k":1e300}`, "", false, 262144},
		{"zero rejected", `{"top_k":0}`, "top_k: must be -1 or a positive integer", false, 0},
		{"fraction between zero and one rejected", `{"top_k":0.5}`, "top_k: must be -1 or a positive integer", false, 0},
		{"negative two rejected", `{"top_k":-2}`, "top_k: must be -1 or a positive integer", false, 0},
		{"non-finite nan stripped not rejected", `{"top_k":"nan"}`, "", true, 0},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			document := parseTestDocument(t, testCase.body)
			err := validTopK(topKMax)(RuleContext{Document: document, Param: param})
			if testCase.wantErr != "" {
				if err == nil {
					t.Fatal("validTopK() = nil, want error")
				}
				if err.Error() != testCase.wantErr {
					t.Errorf("error = %q, want %q", err.Error(), testCase.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("validTopK() = %v, want nil", err)
			}
			if testCase.wantStrip {
				if document.Has(param) {
					t.Errorf("Has(%s) = true, want stripped", param)
				}
				return
			}
			raw, _ := document.Get(param)
			got, ok := raw.(int64)
			if !ok {
				t.Fatalf("Get(%s) = %T(%v), want int64", param, raw, raw)
			}
			if got != testCase.wantResult {
				t.Errorf("Get(%s) = %v, want %v", param, got, testCase.wantResult)
			}
		})
	}
	t.Run("absent is a no-op", func(t *testing.T) {
		document := parseTestDocument(t, `{}`)
		if err := validTopK(topKMax)(RuleContext{Document: document, Param: param}); err != nil {
			t.Fatalf("validTopK() = %v, want nil", err)
		}
		if document.Has(param) {
			t.Error("Has(top_k) = true, want absent to stay absent")
		}
	})
}

// Test flow:
//  1. Run table cases of `validModelName(modelMaxLen)` against a `model` body, varying accepted name shapes, length boundaries, empty/whitespace values, non-string types, and characters outside the allowed set.
//  2. Assert each case's error message matches, or is nil, and that an error carries ErrorStatus 400.
func TestBasicValidModelName(t *testing.T) {
	const param = "model"
	tests := []struct {
		name    string
		body    string
		wantErr string
	}{
		{"absent passes through", `{}`, ""},
		{"vendor slash name accepted", `{"model":"model-a"}`, ""},
		{"full published identifier accepted", `{"model":"Qwen/Qwen3-235B-A22B-Instruct-2507-FP8"}`, ""},
		{"dotted version accepted", `{"model":"vendor-b/model-b.7"}`, ""},
		{"underscore accepted", `{"model":"vendor_c/model_c"}`, ""},
		{"exactly at length limit accepted", `{"model":"` + strings.Repeat("a", 256) + `"}`, ""},
		{"one byte over length limit rejected", `{"model":"` + strings.Repeat("a", 257) + `"}`, "model: length exceeded: 257 > 256"},
		{"empty string rejected", `{"model":""}`, "model: must not be empty"},
		{"whitespace only rejected", `{"model":"   "}`, "model: must not be empty"},
		{"null rejected as non-string", `{"model":null}`, "model: invalid wrapper shape: must be a string"},
		{"number rejected as non-string", `{"model":7}`, "model: invalid wrapper shape: must be a string"},
		{"object rejected as non-string", `{"model":{"name":"model-a"}}`, "model: invalid wrapper shape: must be a string"},
		{"embedded space rejected", `{"model":"model a"}`, "model: invalid: must contain only letters, digits, and the characters ._-/"},
		{"surrounding whitespace rejected", `{"model":" model-a "}`, "model: invalid: must contain only letters, digits, and the characters ._-/"},
		{"newline rejected", `{"model":"model-a\nmodel-b"}`, "model: invalid: must contain only letters, digits, and the characters ._-/"},
		{"colon rejected", `{"model":"model-a:latest"}`, "model: invalid: must contain only letters, digits, and the characters ._-/"},
		{"non-ascii rejected", `{"model":"modèle-à"}`, "model: invalid: must contain only letters, digits, and the characters ._-/"},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			document := parseTestDocument(t, testCase.body)
			err := validModelName(modelMaxLen)(RuleContext{Document: document, Param: param})
			if testCase.wantErr == "" {
				if err != nil {
					t.Fatalf("validModelName() = %v, want nil", err)
				}
				return
			}
			if err == nil {
				t.Fatal("validModelName() = nil, want error")
			}
			if err.Error() != testCase.wantErr {
				t.Errorf("validModelName() error = %q, want %q", err.Error(), testCase.wantErr)
			}
			if got := ErrorStatus(err, 0); got != 400 {
				t.Errorf("ErrorStatus() = %d, want 400", got)
			}
		})
	}
}

// Test flow:
//  1. Call `validModelName(modelMaxLen)` on a model name one megabyte long.
//  2. Assert it is rejected with the exact length-exceeded error.
func TestBasicValidModelNameRejectsMegabyteNameOnLength(t *testing.T) {
	document := parseTestDocument(t, `{"model":"`+strings.Repeat("a", 1<<20)+`"}`)
	err := validModelName(modelMaxLen)(RuleContext{Document: document, Param: "model"})
	if err == nil {
		t.Fatal("validModelName() = nil, want error")
	}
	want := "model: length exceeded: 1048576 > 256"
	if err.Error() != want {
		t.Errorf("validModelName() error = %q, want %q", err.Error(), want)
	}
}

// Test flow:
//  1. Run table cases of `replaceIfPresent(uint64(1))` against a present numeric, zero, huge, and non-numeric value.
//  2. Assert Get returns the replacement value in every case.
//  3. Assert an absent field stays absent.
func TestBasicReplaceIfPresent(t *testing.T) {
	const param = "n"
	tests := []struct {
		name string
		body string
		want any
	}{
		{"numeric value is replaced", `{"n":5}`, uint64(1)},
		{"zero is replaced", `{"n":0}`, uint64(1)},
		{"huge value is replaced", `{"n":1638400}`, uint64(1)},
		{"non-numeric type is replaced too", `{"n":"abc"}`, uint64(1)},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			document := parseTestDocument(t, testCase.body)
			if err := replaceIfPresent(uint64(1))(RuleContext{Document: document, Param: param}); err != nil {
				t.Fatalf("replaceIfPresent() = %v, want nil", err)
			}
			got, ok := document.Get(param)
			if !ok {
				t.Fatalf("Get(%s): missing", param)
			}
			if got != testCase.want {
				t.Errorf("Get(%s) = %#v, want %#v", param, got, testCase.want)
			}
		})
	}
	t.Run("absent stays absent", func(t *testing.T) {
		document := parseTestDocument(t, `{}`)
		if err := replaceIfPresent(uint64(1))(RuleContext{Document: document, Param: param}); err != nil {
			t.Fatalf("replaceIfPresent() = %v, want nil", err)
		}
		if document.Has(param) {
			t.Error("Has(n) = true, want an omitted parameter to stay omitted")
		}
	})
}
