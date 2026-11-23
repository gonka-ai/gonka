package jsonshape

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

type member struct {
	key   string
	value []byte
}

func referenceMembers(t testing.TB, raw []byte) ([]member, bool) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	tok, err := dec.Token()
	if err != nil || tok != json.Delim('{') {
		return nil, false
	}
	var out []member
	for dec.More() {
		keyTok, err := dec.Token()
		require.NoError(t, err)
		var value json.RawMessage
		require.NoError(t, dec.Decode(&value))
		out = append(out, member{key: keyTok.(string), value: value})
	}
	return out, true
}

func referenceElements(t testing.TB, raw []byte) ([][]byte, bool) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	tok, err := dec.Token()
	if err != nil || tok != json.Delim('[') {
		return nil, false
	}
	var out [][]byte
	for dec.More() {
		var value json.RawMessage
		require.NoError(t, dec.Decode(&value))
		out = append(out, value)
	}
	return out, true
}

func checkObjectAndField(t *testing.T, raw []byte) {
	if !json.Valid(raw) {
		return
	}
	want, ok := referenceMembers(t, raw)
	if !ok {
		return
	}
	var got []member
	Object(raw, func(key, value []byte) bool {
		got = append(got, member{key: string(key), value: value})
		return true
	})
	require.Len(t, got, len(want), "%q", raw)
	for i := range want {
		require.Truef(t, KeyEqual([]byte(got[i].key), want[i].key), "key %q does not equal %q", got[i].key, want[i].key)
		require.Equal(t, string(want[i].value), string(got[i].value))
	}
	var asMap map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(raw, &asMap))
	for name, value := range asMap {
		require.Equalf(t, string(value), string(Field(raw, name)), "Field(%q) in %q", name, raw)
	}
	var folded struct {
		Choices json.RawMessage `json:"choices"`
		Usage   json.RawMessage `json:"usage"`
	}
	require.NoError(t, json.Unmarshal(raw, &folded))
	require.Equal(t, string(folded.Choices), string(FieldFold(raw, "choices")), "%q", raw)
	require.Equal(t, string(folded.Usage), string(FieldFold(raw, "usage")), "%q", raw)
}

func checkArray(t *testing.T, raw []byte) {
	if !json.Valid(raw) {
		return
	}
	want, ok := referenceElements(t, raw)
	if !ok {
		return
	}
	var got [][]byte
	Array(raw, func(value []byte) bool {
		got = append(got, value)
		return true
	})
	require.Len(t, got, len(want), "%q", raw)
	for i := range want {
		require.Equal(t, string(want[i]), string(got[i]))
	}
}

var rewriteStripped = []string{"token_ids", "a"}

func testTransform(key []byte, depth int) (string, bool) {
	if depth == 1 && KeyEqual(key, "usage") {
		return "", true
	}
	for _, name := range rewriteStripped {
		if KeyEqual(key, name) {
			return "", true
		}
	}
	if KeyEqual(key, "top_logprobs") {
		return "[]", true
	}
	return "", false
}

func referenceTransform(v any, depth int) any {
	switch t := v.(type) {
	case map[string]any:
		for k, child := range t {
			switch {
			case depth == 1 && k == "usage":
				delete(t, k)
			case k == "token_ids" || k == "a":
				delete(t, k)
			case k == "top_logprobs":
				t[k] = []any{}
			default:
				t[k] = referenceTransform(child, depth+1)
			}
		}
		return t
	case []any:
		for i, child := range t {
			t[i] = referenceTransform(child, depth+1)
		}
		return t
	}
	return v
}

func decodeNumbers(t testing.TB, raw []byte) any {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	require.NoError(t, dec.Decode(&v))
	_, err := dec.Token()
	require.ErrorIs(t, err, io.EOF)
	return v
}

func checkRewrite(t *testing.T, raw []byte) {
	out, changed, err := Rewrite(raw, testTransform)
	if !json.Valid(raw) {
		require.ErrorIs(t, err, ErrInvalid)
		require.False(t, changed)
		return
	}
	if st, mErr := Measure(raw); mErr != nil {
		require.ErrorIs(t, err, ErrTooDeep)
		require.Greater(t, st.Depth, MaxDepth-1)
		return
	}
	require.NoError(t, err)
	require.Truef(t, json.Valid(out), "rewrite of %q produced invalid %q", raw, out)
	want := referenceTransform(decodeNumbers(t, raw), 1)
	require.Equalf(t, want, decodeNumbers(t, out), "rewrite of %q gave %q", raw, out)
	if !changed {
		require.Equal(t, string(raw), string(out))
	}
}

var rawSeeds = []string{
	`{}`,
	`[]`,
	`{"a":1}`,
	`{"a":1,"a":2}`,
	`{"\u0061":1,"b":[{"a":2,"token_ids":[1]}]}`,
	`{"usage":{"usage":1},"x":{"usage":2}}`,
	`{"top_logprobs":[{"token":"x"}],"y":{"top_logprobs":null}}`,
	`{ "choices" : [ { "index" : 0 } ] , "Choices" : 1 }`,
	`{"CHOICES":[1],"choices":[2]}`,
	`{"\u0063hoices":[3]}`,
	`{"\u0055SAGE":[4]}`,
	`{"k\"":"v\\","e":"\u0022,\u005c"}`,
	`{"\ud83d\ude00":1,"\ud83d":2}`,
	`{"a":"x","b":"\u2028"}`,
	`{"t\u006fken_ids":[1],"keep":1}`,
	`{"\u212Aey":1,"choi\u017Fes":2}`,
	`[1,"two",{"three":[3]},[],null,true]`,
	` [ {"a" : [ ] } , "]" ] `,
	`{"token_ids":1,"keep":{"a":1,"b":2},"usage":null}`,
	`"string"`,
	`12`,
	`{"a":[{"a":{"a":1}}]}`,
}

func TestRawHelpersMatchEncodingJSON(t *testing.T) {
	for _, s := range rawSeeds {
		raw := []byte(s)
		checkObjectAndField(t, raw)
		checkArray(t, raw)
		checkRewrite(t, raw)
	}
}

func TestRewriteFailsClosed(t *testing.T) {
	_, _, err := Rewrite([]byte(`{"token_ids":[1],`), testTransform)
	require.ErrorIs(t, err, ErrInvalid)
	deep := strings.Repeat(`{"x":`, MaxDepth+1) + `{"token_ids":1}` + strings.Repeat(`}`, MaxDepth+1)
	out, changed, err := Rewrite([]byte(deep), testTransform)
	require.ErrorIs(t, err, ErrTooDeep)
	require.True(t, errors.Is(err, ErrLimit))
	require.False(t, changed)
	require.Equal(t, deep, string(out))
}

func FuzzRawHelpersMatchEncodingJSON(f *testing.F) {
	for _, s := range rawSeeds {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, raw []byte) {
		checkObjectAndField(t, raw)
		checkArray(t, raw)
		checkRewrite(t, raw)
	})
}

func TestKeyEqualFoldLongKeyDoesNotAllocate(t *testing.T) {
	key := bytes.Repeat([]byte("k"), 1<<20)
	raw := []byte(`{"` + string(key) + `":1,"choices":[]}`)
	allocs := testing.AllocsPerRun(10, func() {
		if KeyEqualFold(key, "choices") {
			t.Fatal("unexpected match")
		}
		if FieldFold(raw, "choices") == nil {
			t.Fatal("choices not found")
		}
	})
	require.Zero(t, allocs)
}
