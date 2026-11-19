package filters

import "testing"

var (
	benchBodyMinimal = []byte(`{"model":"moonshotai/Kimi-K2.6","messages":[{"role":"user","content":"hi"}]}`)
	benchBodyTypical = []byte(`{"model":"moonshotai/Kimi-K2.6","messages":[{"role":"user","content":"hello"}],"temperature":0.7,"top_p":0.95,"max_tokens":512}`)
)

func BenchmarkNormalizeRequest(b *testing.B) {
	for _, testCase := range []struct {
		name string
		body []byte
	}{
		{name: "Minimal", body: benchBodyMinimal},
		{name: "Typical", body: benchBodyTypical},
	} {
		b.Run(testCase.name, func(b *testing.B) {
			b.ReportAllocs()
			b.SetBytes(int64(len(testCase.body)))
			for range b.N {
				_, _ = NormalizeRequest(testCase.body, Options{RoutedModel: "moonshotai/Kimi-K2.6"})
			}
		})
	}
}

// conversationBody builds the shape a chat client actually resends: a long multi-turn history plus tools.
func conversationBody(turns int) []byte {
	const paragraph = `The quick brown fox jumps over the lazy dog, and then explains at some length ` +
		`why it did so, quoting a \"source\" and a path like /usr/local/share for good measure. `
	body := []byte(`{"model":"moonshotai/Kimi-K2.6","temperature":0.7,"top_p":0.95,"max_tokens":512,` +
		`"tools":[{"type":"function","function":{"name":"lookup","description":"look something up",` +
		`"parameters":{"type":"object","properties":{"query":{"type":"string"}},"required":["query"]}}}],` +
		`"messages":[{"role":"system","content":"You are a helpful assistant."}`)
	for turn := range turns {
		role := "user"
		if turn%2 == 1 {
			role = "assistant"
		}
		body = append(body, `,{"role":"`+role+`","content":"`...)
		for range 4 {
			body = append(body, paragraph...)
		}
		body = append(body, `"}`...)
	}
	return append(body, `]}`...)
}

func BenchmarkNormalizeRequestConversation(b *testing.B) {
	body := conversationBody(20)
	b.ReportAllocs()
	b.SetBytes(int64(len(body)))
	for b.Loop() {
		if _, err := NormalizeRequest(body, Options{RoutedModel: "moonshotai/Kimi-K2.6"}); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkParseDocumentConversation(b *testing.B) {
	body := conversationBody(20)
	b.ReportAllocs()
	b.SetBytes(int64(len(body)))
	for b.Loop() {
		if _, err := ParseDocument(body); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkEnsureStructuralBounds(b *testing.B) {
	body := conversationBody(20)
	b.ReportAllocs()
	b.SetBytes(int64(len(body)))
	for b.Loop() {
		if err := ensureStructuralBounds(body, MaxNestingDepth, MaxStructuralNodes); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkNormalizeMessages(b *testing.B) {
	document, err := ParseDocument(conversationBody(20))
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	for b.Loop() {
		if err := normalizeMessages(document); err != nil {
			b.Fatal(err)
		}
	}
}
