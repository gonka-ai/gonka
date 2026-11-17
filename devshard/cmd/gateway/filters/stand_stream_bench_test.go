package filters

import (
	"fmt"
	"testing"
)

// BenchmarkBodyFolderStandStream folds a stream shaped like the stand's hosts send: one character per chunk under five top logprobs. It runs once per logprob intent and reports the cost per chunk, so a cost that grows with the answer's length shows.
func BenchmarkBodyFolderStandStream(b *testing.B) {
	for name, intent := range logprobIntents {
		for _, tokens := range standStreamLengths {
			events := standStream(tokens)
			b.Run(fmt.Sprintf("asked=%s/tokens=%d", name, tokens), func(b *testing.B) {
				b.ReportAllocs()
				for b.Loop() {
					folder := NewBodyFolder(intent)
					for _, event := range events {
						if _, err := folder.Write(event); err != nil {
							b.Fatal(err)
						}
					}
					if len(folder.Body()) == 0 {
						b.Fatal("empty body")
					}
				}
				b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N*len(events)), "ns/chunk")
			})
		}
	}
}

// BenchmarkStreamRewriterStandStream rewrites the same stand-shaped stream once per logprob intent a client can state.
func BenchmarkStreamRewriterStandStream(b *testing.B) {
	for name, intent := range logprobIntents {
		for _, tokens := range standStreamLengths {
			events := standStream(tokens)
			b.Run(fmt.Sprintf("asked=%s/tokens=%d", name, tokens), func(b *testing.B) {
				b.ReportAllocs()
				for b.Loop() {
					rewriter := NewStreamRewriter(intent, false)
					for _, event := range events {
						if _, err := rewriter.Write(event); err != nil {
							b.Fatal(err)
						}
					}
					if _, err := rewriter.Close(); err != nil {
						b.Fatal(err)
					}
				}
				b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N*len(events)), "ns/chunk")
			})
		}
	}
}
