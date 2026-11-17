package tokenizer

import (
	"os"
	"path/filepath"
	"testing"
)

const countBenchTokenizer = "qwen.json"

var countBenchCases = []struct {
	name string
	kind string
	size int
}{
	{"english-200B", "english", 200},
	{"mixed-64KiB", "mixed", 64 << 10},
	{"english-1MiB", "english", 1 << 20},
	{"go-1MiB", "code", 1 << 20},
	{"russian-1MiB", "russian", 1 << 20},
	{"cjk-1MiB", "chinese", 1 << 20},
}

// Test flow:
//  1. Load qwen.json from GATEWAY_TOKENIZER_DIR, and skip when the variable is unset.
//  2. For each case, build the deterministic text of that kind and size.
//  3. Time Count on it, reporting bytes per second and allocations.
func BenchmarkCount(b *testing.B) {
	dir := os.Getenv(tokenizerDirVariable)
	if dir == "" {
		b.Skipf("%s names no directory of tokenizer.json files", tokenizerDirVariable)
	}
	file, err := os.Open(filepath.Join(dir, countBenchTokenizer))
	if err != nil {
		b.Fatal(err)
	}
	counter, err := Load(file)
	_ = file.Close()
	if err != nil {
		b.Fatal(err)
	}
	for _, c := range countBenchCases {
		text := benchText(c.kind, c.size)
		b.Run(c.name, func(b *testing.B) {
			b.SetBytes(int64(len(text)))
			b.ReportAllocs()
			for b.Loop() {
				if _, err := counter.Count(text); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
