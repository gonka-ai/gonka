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

type refFrame struct {
	object  bool
	members int
	key     bool
	rootKey string
}

type rootKeyLen struct {
	key string
	n   int
}

func reference(t testing.TB, raw []byte) (Stats, bool) {
	st, _, ok := referenceWithKeys(t, raw)
	return st, ok
}

func referenceWithKeys(t testing.TB, raw []byte) (Stats, []rootKeyLen, bool) {
	if !json.Valid(raw) {
		return Stats{}, nil, false
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var (
		st      Stats
		stack   []*refFrame
		lastKey string
		keyLens []rootKeyLen
	)
	value := func() {
		st.Nodes++
		if len(stack) == 0 {
			return
		}
		f := stack[len(stack)-1]
		f.members++
		if len(stack) == 1 && f.members > st.RootMembers {
			st.RootMembers = f.members
		}
		if f.object {
			f.key = true
			if f.members > st.MaxObjectMembers {
				st.MaxObjectMembers = f.members
			}
		} else if f.members > st.MaxArrayElements {
			st.MaxArrayElements = f.members
		}
	}
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		require.NoError(t, err)
		if len(stack) > 0 {
			top := stack[len(stack)-1]
			if top.object && top.key {
				if k, isKey := tok.(string); isKey {
					top.key = false
					if len(stack) == 1 {
						lastKey = k
					}
					continue
				}
			}
		}
		switch d := tok.(type) {
		case json.Delim:
			switch d {
			case '{', '[':
				value()
				child := &refFrame{object: d == '{', key: d == '{'}
				if d == '[' && len(stack) == 1 && stack[0].object {
					child.rootKey = lastKey + "\x00"
				}
				stack = append(stack, child)
				if len(stack) > st.Depth {
					st.Depth = len(stack)
				}
			default:
				if closing := stack[len(stack)-1]; closing.rootKey != "" {
					keyLens = append(keyLens, rootKeyLen{key: strings.TrimSuffix(closing.rootKey, "\x00"), n: closing.members})
				}
				stack = stack[:len(stack)-1]
				if len(stack) > 0 && stack[len(stack)-1].object {
					stack[len(stack)-1].key = true
				}
			}
		default:
			value()
		}
	}
	return st, keyLens, true
}

func TestMeasureMatchesReference(t *testing.T) {
	cases := []string{
		`{}`,
		`[]`,
		`"s"`,
		`12.5e3`,
		`true`,
		`null`,
		`{"a":1}`,
		`{"a":1,"a":2}`,
		`{"a":"x,y]}{[","b":[1,2,[3,[]]],"c":{"d":{}}}`,
		`[{},{},[],[[]],"",0,false]`,
		`{"k\"":"v\\","e":"\u0022,\u005c"}`,
		` { "a" : [ 1 , 2 ] , "b" : { } } `,
		`{"choices":[{"index":0,"delta":{"k0":"","k1":"","k2":""}}]}`,
	}
	for _, c := range cases {
		want, ok := reference(t, []byte(c))
		require.True(t, ok, c)
		got, err := Measure([]byte(c))
		require.NoError(t, err, c)
		require.Equal(t, want, got, c)
	}
}

func TestCheckLimits(t *testing.T) {
	keys := func(n int) string {
		parts := make([]string, n)
		for i := range parts {
			parts[i] = `"k` + strings.Repeat("x", i%3) + string(rune('a'+i%26)) + `":""`
		}
		return "{" + strings.Join(parts, ",") + "}"
	}
	_, err := Check([]byte(keys(5)), Limits{RootMembers: 5})
	require.NoError(t, err)
	_, err = Check([]byte(keys(6)), Limits{RootMembers: 5})
	require.ErrorIs(t, err, ErrRootMembers)
	require.ErrorIs(t, err, ErrLimit)

	_, err = Check([]byte(`{"d":`+keys(6)+`}`), Limits{RootMembers: 5})
	require.NoError(t, err)
	_, err = Check([]byte(`{"d":`+keys(6)+`}`), Limits{ObjectMembers: 5})
	require.ErrorIs(t, err, ErrObjectMembers)

	_, err = Check([]byte(`{"a":[1,2,3,4]}`), Limits{ArrayElements: 3})
	require.ErrorIs(t, err, ErrArrayElements)
	_, err = Check([]byte(`{"a":[1,2,3],"b":[{}]}`), Limits{ArrayElements: 3, ObjectMembers: 2})
	require.NoError(t, err)

	st, err := Check([]byte(`[{},{},[]]`), Limits{Nodes: 4})
	require.NoError(t, err)
	require.Equal(t, 4, st.Nodes)
	_, err = Check([]byte(`[{},{},[],0]`), Limits{Nodes: 4})
	require.ErrorIs(t, err, ErrNodes)

	_, err = Check([]byte(strings.Repeat("[", MaxDepth)+strings.Repeat("]", MaxDepth)), Limits{})
	require.NoError(t, err)
	_, err = Check([]byte(strings.Repeat("[", MaxDepth+1)+strings.Repeat("]", MaxDepth+1)), Limits{})
	require.ErrorIs(t, err, ErrTooDeep)
}

func TestCheckRootKeyElements(t *testing.T) {
	limits := Limits{RootKeyElements: map[string]int{"choices": 2}}
	_, err := Check([]byte(`{"choices":[{},{}],"other":[1,2,3]}`), limits)
	require.NoError(t, err)
	_, err = Check([]byte(`{"choices":[{},{},{}]}`), limits)
	require.ErrorIs(t, err, ErrRootKeyElements)
	_, err = Check([]byte(`{"choices":[{}],"choices":[1,2,3]}`), limits)
	require.ErrorIs(t, err, ErrRootKeyElements)
	_, err = Check([]byte(`{"choic\u0065s":[1,2,3]}`), limits)
	require.ErrorIs(t, err, ErrRootKeyElements)
	_, err = Check([]byte(`{"x":{"choices":[1,2,3]}}`), limits)
	require.NoError(t, err)
	_, err = Check([]byte(`[{"choices":[1,2,3]}]`), limits)
	require.NoError(t, err)
	_, err = Check([]byte(`{"\u0061lternate":[1,2,3],"choices":{"a":1,"b":2,"c":3}}`), limits)
	require.NoError(t, err)
}

func TestCheckStopsEarly(t *testing.T) {
	var b bytes.Buffer
	b.WriteString(`{"delta":{`)
	for i := 0; i < 200000; i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(`"k":""`)
	}
	b.WriteString(`}}`)
	allocs := testing.AllocsPerRun(5, func() {
		_, err := Check(b.Bytes(), Limits{ObjectMembers: 256})
		require.ErrorIs(t, err, ErrObjectMembers)
	})
	require.Zero(t, allocs)
}

func FuzzMeasureMatchesReference(f *testing.F) {
	for _, s := range []string{`{"choic\u0065s":[1,2]}`, `{"choices":[{},{}]}`, `{}`, `[1,{"a":[]}]`, `{"a":"\"]"}`, `"x"`, `[[[]]]`, `{"a":1,"a":2}`} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, raw []byte) {
		want, keyLens, ok := referenceWithKeys(t, raw)
		if !ok {
			_, _ = Measure(raw)
			return
		}
		got, err := Measure(raw)
		if errors.Is(err, ErrTooDeep) {
			require.Greater(t, want.Depth, MaxDepth)
			return
		}
		require.NoError(t, err)
		require.Equal(t, want, got)
		over := false
		for _, kl := range keyLens {
			if kl.key == "choices" && kl.n > 1 {
				over = true
			}
		}
		_, err = Check(raw, Limits{RootKeyElements: map[string]int{"choices": 1}})
		if over {
			require.ErrorIs(t, err, ErrRootKeyElements)
		} else {
			require.NoError(t, err)
		}
	})
}
