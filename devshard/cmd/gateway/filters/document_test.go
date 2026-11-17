package filters

import (
	"fmt"
	"strings"
	"testing"
)

// nestedObjectBody builds `{"a":{"a":...1...}}` with exactly depth opening braces.
func nestedObjectBody(depth int) []byte {
	var builder strings.Builder
	for range depth {
		builder.WriteString(`{"a":`)
	}
	builder.WriteString("1")
	for range depth {
		builder.WriteString("}")
	}
	return []byte(builder.String())
}

// Test flow:
//  1. Build a JSON body nested one level past the parser's depth limit via `nestedObjectBody`.
//  2. Parse the body.
//  3. Assert parsing fails with the exact "nesting depth exceeds limit" message.
//  4. Assert the error maps to HTTP 400.
func TestDocumentParseRejectsNestingDepthAboveLimit(t *testing.T) {
	_, err := ParseDocument(nestedObjectBody(33))
	if err == nil {
		t.Fatal("ParseDocument() at depth 33: want error, got nil")
	}
	want := "request nesting depth exceeds limit 32"
	if err.Error() != want {
		t.Errorf("ParseDocument() error = %q, want %q", err.Error(), want)
	}
	if got := ErrorStatus(err, 0); got != 400 {
		t.Errorf("ErrorStatus() = %d, want 400", got)
	}
}

// Test flow:
//  1. Build a JSON body nested exactly at the parser's depth limit via `nestedObjectBody`.
//  2. Parse the body.
//  3. Assert parsing succeeds and returns a non-nil document.
func TestDocumentParseAcceptsNestingDepthAtLimit(t *testing.T) {
	document, err := ParseDocument(nestedObjectBody(32))
	if err != nil {
		t.Fatalf("ParseDocument() at depth 32: want nil error, got %v", err)
	}
	if document == nil {
		t.Fatal("ParseDocument() returned nil document with nil error")
	}
}

// paddedBody builds `{"user":"aaa…"}` whose total length is exactly totalBytes.
func paddedBody(totalBytes int) []byte {
	const envelope = `{"user":""}`
	return []byte(`{"user":"` + strings.Repeat("a", totalBytes-len(envelope)) + `"}`)
}

// structuralNodeBody builds `{"stop":[0,0,…]}` holding exactly nodes structural tokens.
func structuralNodeBody(nodes int) []byte {
	var builder strings.Builder
	builder.WriteString(`{"stop":[0`)
	for i := 2; i < nodes; i++ {
		builder.WriteString(",0")
	}
	builder.WriteString("]}")
	return []byte(builder.String())
}

// Test flow:
//  1. Build a JSON body one byte past `MaxBodyBytes` via `paddedBody`.
//  2. Parse the body.
//  3. Assert parsing fails with the exact "body size exceeds limit" message.
//  4. Assert the error maps to HTTP 400.
func TestDocumentParseRejectsBodySizeAboveLimit(t *testing.T) {
	oversize := MaxBodyBytes + 1
	_, err := ParseDocument(paddedBody(oversize))
	if err == nil {
		t.Fatalf("ParseDocument() at %d bytes: want error, got nil", oversize)
	}
	want := fmt.Sprintf("request body size %d exceeds limit %d", oversize, MaxBodyBytes)
	if err.Error() != want {
		t.Errorf("ParseDocument() error = %q, want %q", err.Error(), want)
	}
	if got := ErrorStatus(err, 0); got != 400 {
		t.Errorf("ErrorStatus() = %d, want 400", got)
	}
}

// Test flow:
//  1. Build a JSON body exactly at `MaxBodyBytes` via `paddedBody`.
//  2. Parse the body.
//  3. Assert parsing succeeds with no error.
func TestDocumentParseAcceptsBodySizeAtLimit(t *testing.T) {
	if _, err := ParseDocument(paddedBody(MaxBodyBytes)); err != nil {
		t.Fatalf("ParseDocument() at %d bytes: want nil error, got %v", MaxBodyBytes, err)
	}
}

// Test flow:
//  1. Build a JSON array body one structural node past the parser's node-count limit via `structuralNodeBody`.
//  2. Parse the body.
//  3. Assert parsing fails with the exact "node count exceeds limit" message.
//  4. Assert the error maps to HTTP 400.
func TestDocumentParseRejectsNodeCountAboveLimit(t *testing.T) {
	_, err := ParseDocument(structuralNodeBody(250001))
	if err == nil {
		t.Fatal("ParseDocument() at 250001 nodes: want error, got nil")
	}
	want := "request node count exceeds limit 250000"
	if err.Error() != want {
		t.Errorf("ParseDocument() error = %q, want %q", err.Error(), want)
	}
	if got := ErrorStatus(err, 0); got != 400 {
		t.Errorf("ErrorStatus() = %d, want 400", got)
	}
}

// Test flow:
//  1. Build a JSON array body exactly at the parser's node-count limit via `structuralNodeBody`.
//  2. Parse the body.
//  3. Assert parsing succeeds with no error.
func TestDocumentParseAcceptsNodeCountAtLimit(t *testing.T) {
	if _, err := ParseDocument(structuralNodeBody(250000)); err != nil {
		t.Fatalf("ParseDocument() at 250000 nodes: want nil error, got %v", err)
	}
}

// Test flow:
//  1. Build a body whose string value repeats structural characters ({[,) 250000 times.
//  2. Parse the body.
//  3. Assert parsing succeeds because characters inside a string literal are not counted as structural nodes.
func TestDocumentParseIgnoresStructuralCharactersInsideStrings(t *testing.T) {
	body := []byte(`{"user":"` + strings.Repeat(`{[,`, 250000) + `"}`)
	if _, err := ParseDocument(body); err != nil {
		t.Fatalf("ParseDocument() with structural characters inside a string: want nil error, got %v", err)
	}
}

// Test flow:
//  1. Run each case's raw byte string through `stringLiteralEnd`, varying plain, escaped-quote, escaped-backslash, unterminated and empty literals.
//  2. Assert the returned end offset matches the case's expected count.
func TestStringLiteralEndCountsEscapes(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		body string
		want int
	}{
		{name: "plain literal", body: `hi"rest`, want: 3},
		{name: "escaped quote inside", body: `a\"b"rest`, want: 5},
		{name: "escaped backslash ends the literal", body: `a\\"rest`, want: 4},
		{name: "unterminated literal", body: `a\"b`, want: 4},
		{name: "empty literal", body: `"rest`, want: 1},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			if got := stringLiteralEnd([]byte(testCase.body)); got != testCase.want {
				t.Errorf("stringLiteralEnd(%q) = %d, want %d", testCase.body, got, testCase.want)
			}
		})
	}
}

// Test flow:
//  1. Parse a document whose string value contains escaped quotes, brackets and a trailing escaped backslash alongside a nested object.
//  2. Assert parsing succeeds with no error.
//  3. Assert the escaped content survives the parse under the "user" key.
func TestDocumentParseCountsDepthAroundEscapedQuotes(t *testing.T) {
	t.Parallel()
	body := []byte(`{"user":"a\"{[,\\","nested":{"deep":[1,2]}}`)
	document, err := ParseDocument(body)
	if err != nil {
		t.Fatalf("ParseDocument() with escapes in a string: want nil error, got %v", err)
	}
	if value, held := document.Get("user"); !held {
		t.Fatalf("the escaped content did not survive the parse: %v", value)
	}
}

// Test flow:
//  1. Parse a nil body.
//  2. Assert parsing fails.
//  3. Assert the error message is exactly "parse request: EOF".
func TestDocumentParseEmptyBodyRejectsWithEOF(t *testing.T) {
	_, err := ParseDocument(nil)
	if err == nil {
		t.Fatal("ParseDocument(nil): want error, got nil")
	}
	want := "parse request: EOF"
	if err.Error() != want {
		t.Errorf("ParseDocument(nil) error = %q, want %q", err.Error(), want)
	}
}

// Test flow:
//  1. Parse a JSON string literal instead of an object.
//  2. Assert parsing fails.
//  3. Assert the error message reports the JSON string could not unmarshal into the expected map type.
func TestDocumentParseNonObjectBodyRejects(t *testing.T) {
	_, err := ParseDocument([]byte(`"not a json object"`))
	if err == nil {
		t.Fatal("ParseDocument() on a JSON string: want error, got nil")
	}
	want := "parse request: json: cannot unmarshal string into Go value of type map[string]interface {}"
	if err.Error() != want {
		t.Errorf("ParseDocument() error = %q, want %q", err.Error(), want)
	}
}

// Test flow:
//  1. Parse a document with a single "model" field.
//  2. Assert Get returns the parsed value.
//  3. Set a new "stream" field and assert Get returns it.
//  4. Assert Has reports "model" present, then Delete it.
//  5. Assert Has and Get both report "model" absent afterward.
func TestDocumentGetSetDeleteRoundTrip(t *testing.T) {
	document, err := ParseDocument([]byte(`{"model":"qwen"}`))
	if err != nil {
		t.Fatalf("ParseDocument: %v", err)
	}

	value, ok := document.Get("model")
	if !ok || value != "qwen" {
		t.Fatalf("Get(model) = (%v, %v), want (qwen, true)", value, ok)
	}

	document.Set("stream", true)
	value, ok = document.Get("stream")
	if !ok || value != true {
		t.Fatalf("Get(stream) after Set = (%v, %v), want (true, true)", value, ok)
	}

	if !document.Has("model") {
		t.Fatal("Has(model) = false before Delete, want true")
	}
	document.Delete("model")
	if document.Has("model") {
		t.Fatal("Has(model) = true after Delete, want false")
	}
	if _, ok := document.Get("model"); ok {
		t.Fatal("Get(model) after Delete: ok = true, want false")
	}
}

// Test flow:
//  1. Parse a document with a nested "metadata" object and a "model" string field.
//  2. Assert Object("metadata") returns the nested map.
//  3. Assert Object on a missing key returns not-ok.
//  4. Assert Object on a non-object field ("model") returns not-ok.
func TestDocumentObject(t *testing.T) {
	document, err := ParseDocument([]byte(`{"metadata":{"key":"value"},"model":"qwen"}`))
	if err != nil {
		t.Fatalf("ParseDocument: %v", err)
	}

	object, ok := document.Object("metadata")
	if !ok || object["key"] != "value" {
		t.Fatalf("Object(metadata) = (%v, %v), want a map with key=value", object, ok)
	}
	if _, ok := document.Object("missing"); ok {
		t.Error("Object(missing) = ok true, want false")
	}
	if _, ok := document.Object("model"); ok {
		t.Error("Object(model) on a string field = ok true, want false")
	}
}

// Test flow:
//  1. Parse a document with a "messages" array and a "model" string field.
//  2. Assert Array("messages") returns the parsed slice.
//  3. Assert Array on a missing key returns not-ok.
//  4. Assert Array on a non-array field ("model") returns not-ok.
func TestDocumentArray(t *testing.T) {
	document, err := ParseDocument([]byte(`{"messages":["a","b"],"model":"qwen"}`))
	if err != nil {
		t.Fatalf("ParseDocument: %v", err)
	}

	array, ok := document.Array("messages")
	if !ok || len(array) != 2 || array[0] != "a" {
		t.Fatalf("Array(messages) = (%v, %v), want ([a b], true)", array, ok)
	}
	if _, ok := document.Array("missing"); ok {
		t.Error("Array(missing) = ok true, want false")
	}
	if _, ok := document.Array("model"); ok {
		t.Error("Array(model) on a string field = ok true, want false")
	}
}

// Test flow:
//  1. Parse a document with a positive "seed", a "model" string, and a negative "negative" field.
//  2. Assert Uint("seed") returns the parsed value.
//  3. Assert Uint on a missing key returns not-ok.
//  4. Assert Uint on a non-numeric field ("model") returns not-ok.
//  5. Assert Uint on a negative number returns not-ok.
func TestDocumentUint(t *testing.T) {
	document, err := ParseDocument([]byte(`{"seed":42,"model":"qwen","negative":-5}`))
	if err != nil {
		t.Fatalf("ParseDocument: %v", err)
	}

	value, ok := document.Uint("seed")
	if !ok || value != 42 {
		t.Fatalf("Uint(seed) = (%v, %v), want (42, true)", value, ok)
	}
	if _, ok := document.Uint("missing"); ok {
		t.Error("Uint(missing) = ok true, want false")
	}
	if _, ok := document.Uint("model"); ok {
		t.Error("Uint(model) on a string field = ok true, want false")
	}
	if _, ok := document.Uint("negative"); ok {
		t.Error("Uint(negative) on a negative number = ok true, want false")
	}
}

// Test flow:
//  1. Parse a document with a nested "metadata" object and a "model" string field.
//  2. Assert ObjectField("metadata") reports present and isObject with the nested map.
//  3. Assert ObjectField on a missing key reports absent and not an object.
//  4. Assert ObjectField on a non-object field ("model") reports present but not an object.
func TestDocumentObjectField(t *testing.T) {
	document, err := ParseDocument([]byte(`{"metadata":{"key":"value"},"model":"qwen"}`))
	if err != nil {
		t.Fatalf("ParseDocument: %v", err)
	}

	object, present, isObject := document.ObjectField("metadata")
	if !present || !isObject || object["key"] != "value" {
		t.Fatalf("ObjectField(metadata) = (%v, %v, %v), want a map with key=value, true, true", object, present, isObject)
	}
	if _, present, isObject := document.ObjectField("missing"); present || isObject {
		t.Errorf("ObjectField(missing) = (present=%v, isObject=%v), want (false, false)", present, isObject)
	}
	if _, present, isObject := document.ObjectField("model"); !present || isObject {
		t.Errorf("ObjectField(model) on a string field = (present=%v, isObject=%v), want (true, false)", present, isObject)
	}
}

// Test flow:
//  1. Parse an empty document.
//  2. Set a "model" field.
//  3. Assert Raw() reflects the mutation.
func TestDocumentRawReflectsMutations(t *testing.T) {
	document, err := ParseDocument([]byte(`{}`))
	if err != nil {
		t.Fatalf("ParseDocument: %v", err)
	}
	document.Set("model", "qwen")
	if document.Raw()["model"] != "qwen" {
		t.Errorf("Raw()[model] = %v, want qwen", document.Raw()["model"])
	}
}

// Test flow:
//  1. Parse an empty document and set three keys out of alphabetical order.
//  2. Marshal the document.
//  3. Assert the output bytes list the keys sorted alphabetically.
func TestDocumentMarshalSortsKeys(t *testing.T) {
	document, err := ParseDocument([]byte(`{}`))
	if err != nil {
		t.Fatalf("ParseDocument: %v", err)
	}
	document.Set("zebra", "last")
	document.Set("apple", "first")
	document.Set("middle", "second")

	got, err := document.Marshal()
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	want := `{"apple":"first","middle":"second","zebra":"last"}`
	if string(got) != want {
		t.Errorf("Marshal() = %s, want %s", got, want)
	}
}
