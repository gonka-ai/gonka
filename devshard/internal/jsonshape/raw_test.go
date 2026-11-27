package jsonshape

import (
	"bytes"
	"encoding/json"
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

// Test flow:
//  1. Walk each seed with Object, and read every member with Field and two with FieldFold.
//  2. Assert the members, and each field's raw value, equal what encoding/json reads from the same bytes.
func TestRawHelpersMatchEncodingJSON(t *testing.T) {
	for _, s := range rawSeeds {
		raw := []byte(s)
		checkObjectAndField(t, raw)
	}
}

// Test flow:
//  1. Fuzz the same comparison from the seeds.
//  2. Assert Object, Field and FieldFold agree with encoding/json on every valid input.
func FuzzRawHelpersMatchEncodingJSON(f *testing.F) {
	for _, s := range rawSeeds {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, raw []byte) {
		checkObjectAndField(t, raw)
	})
}

// Test flow:
//  1. Compare a one-megabyte key with `choices` without regard to case, and find `choices` after it with FieldFold.
//  2. Assert neither allocates.
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
