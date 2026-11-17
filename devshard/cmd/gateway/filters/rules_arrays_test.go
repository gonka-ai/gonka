package filters

import (
	"encoding/json"
	"strings"
	"testing"

	"devshard"
)

// Test flow:
//  1. Run table cases of `validListLength(16, 256)` against a `stop` body, varying array length and element length around the entry and per-element length caps, plus a non-string element and a non-array value.
//  2. Assert each case's error message matches, or is nil, and that an error carries ErrorStatus 400.
//  3. Assert an absent field is a no-op.
//  4. Assert a zero entry cap disables the entry-count check.
func TestArraysValidListLength(t *testing.T) {
	const param = "stop"
	tests := []struct {
		name    string
		body    string
		wantErr string
	}{
		{"within entry cap kept", `{"stop":["a","b"]}`, ""},
		{"exactly at entry cap kept", `{"stop":["a","a","a","a","a","a","a","a","a","a","a","a","a","a","a","a"]}`, ""},
		{"one over entry cap rejected", `{"stop":["a","a","a","a","a","a","a","a","a","a","a","a","a","a","a","a","a"]}`, "stop: array length 17 exceeds limit 16"},
		{"element exactly at length cap kept", `{"stop":["` + strings.Repeat("x", 256) + `"]}`, ""},
		{"element one over length cap rejected", `{"stop":["` + strings.Repeat("x", 257) + `"]}`, "stop[0]: string length 257 exceeds limit 256"},
		{"non-string element skipped by length check", `{"stop":[42]}`, ""},
		{"non-array value passes through untouched", `{"stop":"not-an-array"}`, ""},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			document := parseTestDocument(t, testCase.body)
			err := validListLength(16, 256)(RuleContext{Document: document, Param: param})
			if testCase.wantErr == "" {
				if err != nil {
					t.Fatalf("validListLength() = %v, want nil", err)
				}
				return
			}
			if err == nil {
				t.Fatal("validListLength() = nil, want error")
			}
			if err.Error() != testCase.wantErr {
				t.Errorf("validListLength() error = %q, want %q", err.Error(), testCase.wantErr)
			}
			if got := ErrorStatus(err, 0); got != 400 {
				t.Errorf("ErrorStatus() = %d, want 400", got)
			}
		})
	}
	t.Run("absent is a no-op", func(t *testing.T) {
		document := parseTestDocument(t, `{}`)
		if err := validListLength(16, 256)(RuleContext{Document: document, Param: param}); err != nil {
			t.Fatalf("validListLength() = %v, want nil", err)
		}
	})
	t.Run("entry cap disabled by zero", func(t *testing.T) {
		document := parseTestDocument(t, `{"stop":["a","a","a"]}`)
		if err := validListLength(0, 0)(RuleContext{Document: document, Param: param}); err != nil {
			t.Fatalf("validListLength() = %v, want nil", err)
		}
	})
}

// Test flow:
//  1. Run table cases of `requireStringElements()` against a `stop` body, varying string vs non-string elements, an absent field, and a non-array value.
//  2. Assert each case's error message matches, or is nil.
func TestArraysRequireStringElements(t *testing.T) {
	const param = "stop"
	tests := []struct {
		name    string
		body    string
		wantErr string
	}{
		{"all string elements accepted", `{"stop":["a","b"]}`, ""},
		{"non-string element rejected with index", `{"stop":["a",42,"c"]}`, "stop[1]: must be a string"},
		{"absent passes through", `{}`, ""},
		{"non-array passes through", `{"stop":"hello"}`, ""},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			document := parseTestDocument(t, testCase.body)
			err := requireStringElements()(RuleContext{Document: document, Param: param})
			if testCase.wantErr == "" {
				if err != nil {
					t.Fatalf("requireStringElements() = %v, want nil", err)
				}
				return
			}
			if err == nil {
				t.Fatal("requireStringElements() = nil, want error")
			}
			if err.Error() != testCase.wantErr {
				t.Errorf("requireStringElements() error = %q, want %q", err.Error(), testCase.wantErr)
			}
		})
	}
}

// Test flow:
//  1. Run table cases of `dropBlankStringListElements()` against a `bad_words` body, varying blank, non-blank, and non-string entries.
//  2. For a case that empties the list, assert Has reports the field dropped; otherwise assert Get returns the surviving elements matching the case's expected result.
//  3. Assert an absent field is a no-op and stays absent.
func TestArraysDropBlankStringListElements(t *testing.T) {
	const param = "bad_words"
	tests := []struct {
		name       string
		body       string
		wantDrop   bool
		wantResult []any
	}{
		{"non-blank entries survive untouched", `{"bad_words":["keep","also"]}`, false, []any{"keep", "also"}},
		{"blank entries dropped, survivor kept", `{"bad_words":["","   ","keep"]}`, false, []any{"keep"}},
		{"all blank drops the field", `{"bad_words":["","\t","\n"]}`, true, nil},
		{"non-string entries pass through unchanged", `{"bad_words":[42,""]}`, false, []any{json.Number("42")}},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			document := parseTestDocument(t, testCase.body)
			if err := dropBlankStringListElements()(RuleContext{Document: document, Param: param}); err != nil {
				t.Fatalf("dropBlankStringListElements() = %v, want nil", err)
			}
			if testCase.wantDrop {
				if document.Has(param) {
					t.Errorf("Has(%s) = true, want dropped", param)
				}
				return
			}
			got, ok := document.Get(param)
			if !ok {
				t.Fatalf("Get(%s): missing, want %#v", param, testCase.wantResult)
			}
			gotList, ok := got.([]any)
			if !ok {
				t.Fatalf("Get(%s) = %T, want []any", param, got)
			}
			if len(gotList) != len(testCase.wantResult) {
				t.Fatalf("Get(%s) = %#v, want %#v", param, gotList, testCase.wantResult)
			}
			for i := range gotList {
				if gotList[i] != testCase.wantResult[i] {
					t.Errorf("Get(%s)[%d] = %v, want %v", param, i, gotList[i], testCase.wantResult[i])
				}
			}
		})
	}
	t.Run("absent is a no-op", func(t *testing.T) {
		document := parseTestDocument(t, `{}`)
		if err := dropBlankStringListElements()(RuleContext{Document: document, Param: param}); err != nil {
			t.Fatalf("dropBlankStringListElements() = %v, want nil", err)
		}
		if document.Has(param) {
			t.Error("Has(bad_words) = true, want absent to stay absent")
		}
	})
}

// Test flow:
//  1. Run table cases of `validFloatMap(-100, 100, 1024)` against a `logit_bias` body, varying values around the min and max range boundaries.
//  2. For a case that drops the field, assert Has reports it dropped; otherwise assert Object matches the expected map via `JSONNumericFloat64`.
//  3. Assert a map whose entry count exceeds the size cap is rejected, even when every entry is otherwise in range.
//  4. Assert an absent field is a no-op.
//  5. Assert a non-map value passes through untouched.
func TestArraysValidFloatMap(t *testing.T) {
	const param = "logit_bias"
	tests := []struct {
		name       string
		body       string
		wantDrop   bool
		wantResult map[string]float64
	}{
		{"within range kept", `{"logit_bias":{"1":10}}`, false, map[string]float64{"1": 10}},
		{"exactly at min boundary kept", `{"logit_bias":{"1":-100}}`, false, map[string]float64{"1": -100}},
		{"exactly at max boundary kept", `{"logit_bias":{"1":100}}`, false, map[string]float64{"1": 100}},
		{"below min dropped", `{"logit_bias":{"1":-101}}`, true, nil},
		{"above max dropped", `{"logit_bias":{"1":1e30}}`, true, nil},
		{"mixed entries: only in-range survive", `{"logit_bias":{"0":1e30,"1":10,"2":-101}}`, false, map[string]float64{"1": 10}},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			document := parseTestDocument(t, testCase.body)
			if err := validFloatMap(-100, 100, 1024)(RuleContext{Document: document, Param: param}); err != nil {
				t.Fatalf("validFloatMap() = %v, want nil", err)
			}
			if testCase.wantDrop {
				if document.Has(param) {
					t.Errorf("Has(%s) = true, want dropped", param)
				}
				return
			}
			got, ok := document.Object(param)
			if !ok {
				t.Fatalf("Object(%s): missing, want %#v", param, testCase.wantResult)
			}
			if len(got) != len(testCase.wantResult) {
				t.Fatalf("Object(%s) = %#v, want %#v", param, got, testCase.wantResult)
			}
			for key, wantValue := range testCase.wantResult {
				gotValue, ok := devshard.JSONNumericFloat64(got[key])
				if !ok || gotValue != wantValue {
					t.Errorf("Object(%s)[%s] = %v, want %v", param, key, got[key], wantValue)
				}
			}
		})
	}
	t.Run("map size cap rejects even all-valid entries", func(t *testing.T) {
		document := parseTestDocument(t, `{"logit_bias":{"1":10,"2":20,"3":30}}`)
		err := validFloatMap(-100, 100, 2)(RuleContext{Document: document, Param: param})
		if err == nil {
			t.Fatal("validFloatMap() = nil, want error")
		}
		wantErr := "logit_bias: map size 3 exceeds limit 2"
		if err.Error() != wantErr {
			t.Errorf("validFloatMap() error = %q, want %q", err.Error(), wantErr)
		}
	})
	t.Run("absent is a no-op", func(t *testing.T) {
		document := parseTestDocument(t, `{}`)
		if err := validFloatMap(-100, 100, 1024)(RuleContext{Document: document, Param: param}); err != nil {
			t.Fatalf("validFloatMap() = %v, want nil", err)
		}
	})
	t.Run("non-map value passes through untouched", func(t *testing.T) {
		document := parseTestDocument(t, `{"logit_bias":"nope"}`)
		if err := validFloatMap(-100, 100, 1024)(RuleContext{Document: document, Param: param}); err != nil {
			t.Fatalf("validFloatMap() = %v, want nil", err)
		}
		if got, _ := document.Get(param); got != "nope" {
			t.Errorf("Get(%s) = %v, want unchanged", param, got)
		}
	})
}

// Test flow:
//  1. Run table cases of `requireTokenIDKeys()` against a `logit_bias` body, varying valid token ids and invalid keys (negative, non-numeric, fractional, padded, hex, and one past the largest 32-bit id).
//  2. Assert each case's error message matches, or is nil, and that an error carries ErrorStatus 400.
//  3. Assert an absent field and a non-map value are both no-ops.
//  4. Repeat the check 64 times over a multi-key map to assert the reported invalid key is always the lexicographically first one, despite random map iteration order.
func TestArraysRequireTokenIDKeys(t *testing.T) {
	const param = "logit_bias"
	tests := []struct {
		name    string
		body    string
		wantErr string
	}{
		{"zero accepted", `{"logit_bias":{"0":10}}`, ""},
		{"typical token id accepted", `{"logit_bias":{"15043":10}}`, ""},
		{"largest 32-bit id accepted", `{"logit_bias":{"2147483647":10}}`, ""},
		{"one past largest 32-bit id rejected", `{"logit_bias":{"2147483648":10}}`, `logit_bias: key invalid: "2147483648" is not a non-negative integer token id`},
		{"negative id rejected", `{"logit_bias":{"-1":10}}`, `logit_bias: key invalid: "-1" is not a non-negative integer token id`},
		{"non-numeric key rejected", `{"logit_bias":{"hello":10}}`, `logit_bias: key invalid: "hello" is not a non-negative integer token id`},
		{"empty key rejected", `{"logit_bias":{"":10}}`, `logit_bias: key invalid: "" is not a non-negative integer token id`},
		{"fractional key rejected", `{"logit_bias":{"1.5":10}}`, `logit_bias: key invalid: "1.5" is not a non-negative integer token id`},
		{"padded key rejected", `{"logit_bias":{" 1":10}}`, `logit_bias: key invalid: " 1" is not a non-negative integer token id`},
		{"hex key rejected", `{"logit_bias":{"0x10":10}}`, `logit_bias: key invalid: "0x10" is not a non-negative integer token id`},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			document := parseTestDocument(t, testCase.body)
			err := requireTokenIDKeys()(RuleContext{Document: document, Param: param})
			if testCase.wantErr == "" {
				if err != nil {
					t.Fatalf("requireTokenIDKeys() = %v, want nil", err)
				}
				return
			}
			if err == nil {
				t.Fatal("requireTokenIDKeys() = nil, want error")
			}
			if err.Error() != testCase.wantErr {
				t.Errorf("requireTokenIDKeys() error = %q, want %q", err.Error(), testCase.wantErr)
			}
			if got := ErrorStatus(err, 0); got != 400 {
				t.Errorf("ErrorStatus() = %d, want 400", got)
			}
		})
	}
	t.Run("absent is a no-op", func(t *testing.T) {
		document := parseTestDocument(t, `{}`)
		if err := requireTokenIDKeys()(RuleContext{Document: document, Param: param}); err != nil {
			t.Fatalf("requireTokenIDKeys() = %v, want nil", err)
		}
	})
	t.Run("non-map value passes through untouched", func(t *testing.T) {
		document := parseTestDocument(t, `{"logit_bias":"nope"}`)
		if err := requireTokenIDKeys()(RuleContext{Document: document, Param: param}); err != nil {
			t.Fatalf("requireTokenIDKeys() = %v, want nil", err)
		}
	})
	t.Run("reports the lexicographically first invalid key across runs", func(t *testing.T) {
		want := `logit_bias: key invalid: "aaa" is not a non-negative integer token id`
		for range 64 {
			document := parseTestDocument(t, `{"logit_bias":{"1":1,"zzz":1,"mmm":1,"aaa":1,"2":1}}`)
			err := requireTokenIDKeys()(RuleContext{Document: document, Param: param})
			if err == nil || err.Error() != want {
				t.Fatalf("requireTokenIDKeys() error = %v, want %q", err, want)
			}
		}
	})
}

// Test flow:
//  1. Call NormalizeRequest with a `logit_bias` entry whose key is invalid and whose value is also out of range (so it would otherwise be dropped by validFloatMap).
//  2. Assert normalization rejects the request with the bad-key error rather than silently dropping the value and passing.
func TestArraysLogitBiasBadKeyRejectsDespiteDroppableValue(t *testing.T) {
	result, err := NormalizeRequest([]byte(`{"model":"model-a","messages":[{"role":"user","content":"hi"}],"logit_bias":{"nope":1e30}}`), Options{
		DefaultMaxTokens: 3072,
		MaxTokensCap:     4096,
		RoutedModel:      "model-a",
	})
	if err == nil {
		t.Fatalf("NormalizeRequest() = nil error, want rejection; body = %s", result.Body)
	}
	want := `logit_bias: key invalid: "nope" is not a non-negative integer token id`
	if err.Error() != want {
		t.Errorf("NormalizeRequest() error = %q, want %q", err.Error(), want)
	}
}

// Test flow:
//  1. Run table cases of `validMetadata(16, 64, 512)` against a `metadata` body, varying key count, key length, value type, and value length around their caps.
//  2. Assert each case's error message matches, or is nil, and that an error carries ErrorStatus 400.
func TestArraysValidMetadata(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		wantErr string
	}{
		{"absent passes through", `{}`, ""},
		{"within bounds accepted", `{"metadata":{"trace":"abc"}}`, ""},
		{"non-object shape rejected", `{"metadata":"nope"}`, "metadata: invalid wrapper shape: must be an object"},
		{"exactly at key count cap accepted", `{"metadata":{"k0":"v","k1":"v","k2":"v","k3":"v","k4":"v","k5":"v","k6":"v","k7":"v","k8":"v","k9":"v","k10":"v","k11":"v","k12":"v","k13":"v","k14":"v","k15":"v"}}`, ""},
		{"key count one over cap rejected", `{"metadata":{"k0":"v","k1":"v","k2":"v","k3":"v","k4":"v","k5":"v","k6":"v","k7":"v","k8":"v","k9":"v","k10":"v","k11":"v","k12":"v","k13":"v","k14":"v","k15":"v","k16":"v"}}`, "metadata: key count exceeded: 17 > 16"},
		{"key exactly at length cap accepted", `{"metadata":{"` + strings.Repeat("k", 64) + `":"v"}}`, ""},
		{"key one over length cap rejected", `{"metadata":{"` + strings.Repeat("k", 65) + `":"v"}}`, "metadata: key invalid: key length 65 > 64"},
		{"non-string value rejected", `{"metadata":{"k":123}}`, `metadata: value invalid: value for "k" must be a string`},
		{"value exactly at length cap accepted", `{"metadata":{"k":"` + strings.Repeat("v", 512) + `"}}`, ""},
		{"value one over length cap rejected", `{"metadata":{"k":"` + strings.Repeat("v", 513) + `"}}`, `metadata: value invalid: value for "k" length 513 > 512`},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			document := parseTestDocument(t, testCase.body)
			err := validMetadata(16, 64, 512)(RuleContext{Document: document, Param: "metadata"})
			if testCase.wantErr == "" {
				if err != nil {
					t.Fatalf("validMetadata() = %v, want nil", err)
				}
				return
			}
			if err == nil {
				t.Fatal("validMetadata() = nil, want error")
			}
			if err.Error() != testCase.wantErr {
				t.Errorf("validMetadata() error = %q, want %q", err.Error(), testCase.wantErr)
			}
			if got := ErrorStatus(err, 0); got != 400 {
				t.Errorf("ErrorStatus() = %d, want 400", got)
			}
		})
	}
}

// Test flow:
//  1. Run table cases of `validStreamOptions()` against a `stream_options` body, varying whitelisted and unknown keys, `stream` set to true, false, absent, or a wrong type, and a non-object shape.
//  2. Assert each case's error message, dropped state, or surviving whitelisted keys matches expectations.
//  3. Assert an absent field is a no-op and stays absent.
func TestArraysValidStreamOptions(t *testing.T) {
	tests := []struct {
		name       string
		body       string
		wantErr    string
		wantDrop   bool
		wantResult map[string]any
	}{
		{"stream true, include_usage whitelisted kept", `{"stream":true,"stream_options":{"include_usage":true}}`, "", false, map[string]any{"include_usage": true}},
		{"stream true, unknown key dropped leaving survivor", `{"stream":true,"stream_options":{"include_usage":true,"continuous_usage_stats":true}}`, "", false, map[string]any{"include_usage": true}},
		{"stream true, only unknown key empties and drops field", `{"stream":true,"stream_options":{"continuous_usage_stats":true}}`, "", true, nil},
		{"stream false strips regardless of content", `{"stream":false,"stream_options":{"include_usage":true}}`, "", true, nil},
		{"stream absent strips regardless of content", `{"stream_options":{"include_usage":true}}`, "", true, nil},
		{"stream true, non-object shape rejected", `{"stream":true,"stream_options":"nope"}`, "stream_options: invalid wrapper shape: must be an object", false, nil},
		{"stream false, non-object shape silently stripped", `{"stream":false,"stream_options":"nope"}`, "", true, nil},
		{"stream wrong type treated as not-true, strips", `{"stream":"true","stream_options":{"include_usage":true}}`, "", true, nil},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			document := parseTestDocument(t, testCase.body)
			err := validStreamOptions()(RuleContext{Document: document, Param: "stream_options"})
			if testCase.wantErr != "" {
				if err == nil {
					t.Fatal("validStreamOptions() = nil, want error")
				}
				if err.Error() != testCase.wantErr {
					t.Errorf("validStreamOptions() error = %q, want %q", err.Error(), testCase.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("validStreamOptions() = %v, want nil", err)
			}
			if testCase.wantDrop {
				if document.Has("stream_options") {
					t.Error("Has(stream_options) = true, want dropped")
				}
				return
			}
			got, ok := document.Object("stream_options")
			if !ok {
				t.Fatalf("Object(stream_options): missing, want %#v", testCase.wantResult)
			}
			if len(got) != len(testCase.wantResult) {
				t.Fatalf("Object(stream_options) = %#v, want %#v", got, testCase.wantResult)
			}
			for key, want := range testCase.wantResult {
				if got[key] != want {
					t.Errorf("Object(stream_options)[%s] = %v, want %v", key, got[key], want)
				}
			}
		})
	}
	t.Run("absent is a no-op", func(t *testing.T) {
		document := parseTestDocument(t, `{"stream":true}`)
		if err := validStreamOptions()(RuleContext{Document: document, Param: "stream_options"}); err != nil {
			t.Fatalf("validStreamOptions() = %v, want nil", err)
		}
		if document.Has("stream_options") {
			t.Error("Has(stream_options) = true, want absent to stay absent")
		}
	})
}
