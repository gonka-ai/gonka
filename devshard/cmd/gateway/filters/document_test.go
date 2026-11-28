package filters

import (
	"fmt"
	"strings"
	"testing"
)

const (
	htmlRepetitions       = 2000
	forcedFieldsAllowance = 512
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

func structuralNodeBody(nodes int) []byte {
	const wrapperNodes = 2
	remaining := nodes - wrapperNodes
	groups := 1
	for remaining-(groups-1) > groups*MaxContainerMembers {
		groups++
	}
	remaining -= groups - 1
	var builder strings.Builder
	builder.WriteString(`{"stop":[`)
	for group := range groups {
		if group > 0 {
			builder.WriteString(",")
		}
		members := min(remaining, MaxContainerMembers)
		remaining -= members
		builder.WriteString("[0")
		builder.WriteString(strings.Repeat(",0", members-1))
		builder.WriteString("]")
	}
	builder.WriteString("]}")
	return []byte(builder.String())
}

func wideContainerBody(members int, object bool) []byte {
	var builder strings.Builder
	if object {
		builder.WriteString(`{"x":{"0":0`)
		for index := 1; index < members; index++ {
			fmt.Fprintf(&builder, `,"%d":0`, index)
		}
		builder.WriteString("}}")
	} else {
		builder.WriteString(`{"x":[0`)
		builder.WriteString(strings.Repeat(",0", members-1))
		builder.WriteString("]}")
	}
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
//  1. For an array and an object, build a body whose one container holds one member past `MaxContainerMembers` via `wideContainerBody`.
//  2. Parse each body.
//  3. Assert parsing fails with the exact "container member count exceeds limit" message and maps to HTTP 400.
func TestDocumentParseRejectsContainerMembersAboveLimit(t *testing.T) {
	for _, object := range []bool{false, true} {
		_, err := ParseDocument(wideContainerBody(MaxContainerMembers+1, object))
		if err == nil {
			t.Fatalf("ParseDocument() with %d members, object=%v: want error, got nil", MaxContainerMembers+1, object)
		}
		want := fmt.Sprintf("request container member count exceeds limit %d", MaxContainerMembers)
		if err.Error() != want {
			t.Errorf("ParseDocument() object=%v error = %q, want %q", object, err.Error(), want)
		}
		if got := ErrorStatus(err, 0); got != 400 {
			t.Errorf("ErrorStatus() object=%v = %d, want 400", object, got)
		}
	}
}

// Test flow:
//  1. For an array and an object, build a body whose one container holds exactly `MaxContainerMembers` members.
//  2. Parse each body.
//  3. Assert parsing succeeds.
func TestDocumentParseAcceptsContainerMembersAtLimit(t *testing.T) {
	for _, object := range []bool{false, true} {
		if _, err := ParseDocument(wideContainerBody(MaxContainerMembers, object)); err != nil {
			t.Errorf("ParseDocument() with %d members, object=%v: want nil error, got %v", MaxContainerMembers, object, err)
		}
	}
}

// Test flow:
//  1. Build a body of three sibling arrays and one nested array, each holding exactly `MaxContainerMembers` members, so the whole body holds more than that in total.
//  2. Parse the body.
//  3. Assert parsing succeeds, because each container is counted on its own and a nested container does not add to its parent.
func TestDocumentParseCountsMembersPerContainer(t *testing.T) {
	array := string(wideContainerBody(MaxContainerMembers, false))
	array = array[len(`{"x":`) : len(array)-1]
	body := []byte(`{"a":` + array + `,"b":` + array + `,"c":[` + array + `]}`)
	if _, err := ParseDocument(body); err != nil {
		t.Errorf("ParseDocument() with full sibling and nested containers: want nil error, got %v", err)
	}
}

// Test flow:
//  1. Build a body whose container has members past the limit, only inside string literals.
//  2. Parse the body.
//  3. Assert parsing succeeds, because commas inside a string are not members.
func TestDocumentParseIgnoresCommasInsideStringsForMemberCount(t *testing.T) {
	body := []byte(`{"user":"` + strings.Repeat(",", MaxContainerMembers*2) + `"}`)
	if _, err := ParseDocument(body); err != nil {
		t.Errorf("ParseDocument() with commas inside a string: want nil error, got %v", err)
	}
}

// Test flow:
//  1. Build an ordinary request with the largest containers the other rules allow: 2048 messages, 1024 logit_bias entries, 128 tools.
//  2. Assert `MaxContainerMembers` is at least twice the message limit.
//  3. Run the body through `ensureStructuralBounds` at the production limits and assert it is accepted, so the member limit leaves headroom over every legitimate container.
func TestMemberLimitLeavesHeadroomOverTheLargestLegitimateContainers(t *testing.T) {
	messages := make([]string, 0, messagesMaxEntries)
	tools := make([]string, 0, defaultToolsMaxCount)
	bias := make([]string, 0, logitBiasMaxEntries)
	for range messagesMaxEntries {
		messages = append(messages, `{"role":"user","content":"hi"}`)
	}
	for range defaultToolsMaxCount {
		tools = append(tools, `{"type":"function","function":{"name":"f","parameters":{"type":"object"}}}`)
	}
	for index := range logitBiasMaxEntries {
		bias = append(bias, fmt.Sprintf(`"%d":1`, index))
	}
	body := `{"model":"m","messages":[` + strings.Join(messages, ",") + `],"tools":[` + strings.Join(tools, ",") + `],"logit_bias":{` + strings.Join(bias, ",") + `}}`
	if MaxContainerMembers < 2*messagesMaxEntries {
		t.Errorf("MaxContainerMembers = %d, want at least twice the %d-message limit", MaxContainerMembers, messagesMaxEntries)
	}
	if err := ensureStructuralBounds([]byte(body), MaxNestingDepth, MaxStructuralNodes, MaxContainerMembers); err != nil {
		t.Errorf("structural bounds on the largest legitimate containers: want nil error, got %v", err)
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

// Test flow:
//  1. Parse a document whose strings carry <, > and & as the client wrote them.
//  2. Marshal the document.
//  3. Assert the output keeps the three characters literal and is exactly as long as the compact input.
func TestDocumentMarshalDoesNotEscapeHTML(t *testing.T) {
	input := `{"content":"if a < b && c > d { return }"}`
	document, err := ParseDocument([]byte(input))
	if err != nil {
		t.Fatalf("ParseDocument: %v", err)
	}

	got, err := document.Marshal()
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if string(got) != input {
		t.Errorf("Marshal() = %s, want %s", got, input)
	}
}

// Test flow:
//  1. Build a chat body whose user message is 2000 repetitions of "<a> && <b>".
//  2. Call NormalizeRequest with default options.
//  3. Assert the normalized body carries none of the unicode escapes a default encoder writes for those three characters, and is shorter than the input plus 512 bytes of forced fields.
func TestNormalizeRequestKeepsHTMLCharactersOneByteEach(t *testing.T) {
	content := strings.Repeat("<a> && <b>", htmlRepetitions)
	body := `{"model":"qwen","messages":[{"role":"user","content":"` + content + `"}]}`

	result, err := NormalizeRequest([]byte(body), Options{DefaultMaxTokens: 3072, MaxTokensCap: 4096})
	if err != nil {
		t.Fatalf("NormalizeRequest: %v", err)
	}
	for _, code := range []string{"u003c", "u003e", "u0026"} {
		if strings.Contains(string(result.Body), `\`+code) {
			t.Fatalf("normalized body contains an escaped %s", code)
		}
	}
	if len(result.Body) > len(body)+forcedFieldsAllowance {
		t.Errorf("normalized body is %d bytes for a %d-byte input", len(result.Body), len(body))
	}
}
