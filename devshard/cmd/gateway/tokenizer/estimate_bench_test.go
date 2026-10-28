package tokenizer

import (
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

const tokenizerDirVariable = "GATEWAY_TOKENIZER_DIR"

var seedTexts = map[string]string{
	"english": `The gateway reads the body, normalises the request and checks that it fits a host before any nonce is committed. ` +
		`A prompt that cannot fit the model's context is refused at the boundary, because a host would refuse it after the escrow had already paid for the attempt. ` +
		`Operators read the refusal in the route's status series, and clients read an error that names the limit they crossed.`,
	"russian": `Шлюз читает тело запроса, нормализует его и проверяет, что запрос поместится на хост, прежде чем будет выделен nonce. ` +
		`Промпт, который не помещается в контекст модели, отклоняется на границе, потому что хост отклонил бы его уже после оплаты попытки. ` +
		`Операторы видят отказ в статистике маршрута, а клиенты получают ошибку с названием превышенного предела.`,
	"chinese": `网关读取请求正文，对请求进行规范化，并在分配任何随机数之前检查它是否适合主机。` +
		`无法放入模型上下文的提示会在边界处被拒绝，因为主机会在托管账户已经为尝试付费之后才拒绝它。` +
		`运维人员在路由的状态序列中看到拒绝，客户端则收到一个指明所超出限制的错误。`,
	"code": "func (s *Server) fitsContext(configured config.Limits, normalized filters.Result) error {\n" +
		"\tif s.promptTokens == nil {\n\t\treturn nil\n\t}\n" +
		"\tcontextLength := configured.ContextLength(normalized.Model, s.snapshots.Snapshot().Models[normalized.Model].MaxModelLen)\n" +
		"\ttokens, known := s.promptTokens.Estimate(normalized.Model, normalized.Body)\n" +
		"\tfor index, part := range parts {\n\t\tcounted[index] = len(part) + 42\n\t}\n" +
		"\treturn &ContextLengthError{Model: normalized.Model, PromptTokens: tokens}\n}\n",
}

func shuffledText(words []string, separator string, size int, rng *rand.Rand) string {
	var builder strings.Builder
	for builder.Len() < size {
		builder.WriteString(words[rng.IntN(len(words))])
		if rng.IntN(40) == 0 {
			builder.WriteString("\n")
		} else {
			builder.WriteString(separator)
		}
	}
	return builder.String()
}

func codeText(size int, rng *rand.Rand) string {
	lines := strings.SplitAfter(seedTexts["code"], "\n")
	var builder strings.Builder
	for builder.Len() < size {
		builder.WriteString(strings.ReplaceAll(lines[rng.IntN(len(lines))], "index", fmt.Sprintf("v%d", rng.IntN(1000))))
	}
	return builder.String()
}

func jsonText(size int, rng *rand.Rand) string {
	var builder strings.Builder
	builder.WriteString("[")
	for builder.Len() < size {
		fmt.Fprintf(&builder, `{"id":%d,"name":"item-%x","price":%.2f,"tags":["a%d","b%d"],"active":%t},`,
			rng.IntN(1_000_000), rng.Uint32(), rng.Float64()*1000, rng.IntN(50), rng.IntN(50), rng.IntN(2) == 0)
	}
	builder.WriteString("{}]")
	return builder.String()
}

func noSpaceText(size int, rng *rand.Rand) string {
	const alphabet = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789!#$%&*+-./:;<=>?@^_|~éжд中文😀́"
	runes := []rune(alphabet)
	var builder strings.Builder
	for builder.Len() < size {
		builder.WriteRune(runes[rng.IntN(len(runes))])
	}
	return builder.String()
}

func benchText(kind string, size int) string {
	rng := rand.New(rand.NewPCG(uint64(size), uint64(len(kind))))
	var text string
	switch kind {
	case "code":
		text = codeText(size, rng)
	case "json":
		text = jsonText(size, rng)
	case "nospace":
		text = noSpaceText(size, rng)
	case "chinese":
		text = shuffledText(strings.Split(seedTexts[kind], ""), "", size, rng)
	case "mixed":
		var builder strings.Builder
		kinds := []string{"english", "russian", "chinese", "code", "json"}
		for block := 0; builder.Len() < size; block++ {
			builder.WriteString(benchText(kinds[block%len(kinds)], 4<<10))
		}
		text = builder.String()
	default:
		text = shuffledText(strings.Fields(seedTexts[kind]), " ", size, rng)
	}
	return text[:runeStartAtOrBefore(text, min(size, len(text)))]
}

func benchTokenizers(b *testing.B) map[string]*Counter {
	b.Helper()
	dir := os.Getenv(tokenizerDirVariable)
	if dir == "" {
		b.Skipf("%s names no directory of tokenizer.json files", tokenizerDirVariable)
	}
	paths, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil || len(paths) == 0 {
		b.Fatalf("no tokenizer.json files in %s: %v", dir, err)
	}
	counters := map[string]*Counter{}
	for _, path := range paths {
		file, err := os.Open(path)
		if err != nil {
			b.Fatal(err)
		}
		counter, err := Load(file)
		_ = file.Close()
		if err != nil {
			b.Fatalf("Load(%s): %v", path, err)
		}
		counters[strings.TrimSuffix(filepath.Base(path), ".json")] = counter
	}
	return counters
}

var (
	exactCounts sync.Map
	benchSizes  = []int{200, 4 << 10, 64 << 10, 256 << 10, 1 << 20, 4 << 20}
	benchKinds  = []string{"english", "russian", "chinese", "code", "json", "mixed", "nospace"}
)

func exactCount(b *testing.B, model string, counter *Counter, kind string, text string) int {
	key := fmt.Sprintf("%s/%s/%d", model, kind, len(text))
	if cached, found := exactCounts.Load(key); found {
		return cached.(int)
	}
	exact, err := counter.Count(text)
	if err != nil {
		b.Fatal(err)
	}
	exactCounts.Store(key, exact)
	return exact
}

// Test flow:
//  1. Load every tokenizer.json in GATEWAY_TOKENIZER_DIR, and skip when it names none.
//  2. For each tokenizer, text kind and size from 200 B to 4 MiB, build a deterministic prompt and count it exactly once.
//  3. Time Estimate on it, reporting allocations and the estimate's error against the exact count in percent.
func BenchmarkEstimate(b *testing.B) {
	for model, counter := range benchTokenizers(b) {
		for _, kind := range benchKinds {
			for _, size := range benchSizes {
				text := benchText(kind, size)
				b.Run(fmt.Sprintf("%s/%s/%dB", model, kind, size), func(b *testing.B) {
					exact := exactCount(b, model, counter, kind, text)
					var estimate uint64
					b.ReportAllocs()
					for b.Loop() {
						var err error
						if estimate, _, err = counter.estimate([]string{text}, uncappedPieceBytes); err != nil {
							b.Fatal(err)
						}
					}
					b.ReportMetric(100*(float64(estimate)-float64(exact))/float64(exact), "%err")
				})
			}
		}
	}
}

// Test flow:
//  1. Read every tokenizer.json in GATEWAY_TOKENIZER_DIR into memory, and skip when it names none.
//  2. Time Load on each, forcing a collection before and after.
//  3. Report the heap the loaded tokenizer keeps.
func BenchmarkLoad(b *testing.B) {
	dir := os.Getenv(tokenizerDirVariable)
	if dir == "" {
		b.Skipf("%s names no directory of tokenizer.json files", tokenizerDirVariable)
	}
	paths, _ := filepath.Glob(filepath.Join(dir, "*.json"))
	for _, path := range paths {
		raw, err := os.ReadFile(path)
		if err != nil {
			b.Fatal(err)
		}
		b.Run(strings.TrimSuffix(filepath.Base(path), ".json"), func(b *testing.B) {
			var kept uint64
			var elapsed time.Duration
			for b.Loop() {
				runtime.GC()
				var before, after runtime.MemStats
				runtime.ReadMemStats(&before)
				started := time.Now()
				counter, err := Load(strings.NewReader(string(raw)))
				elapsed += time.Since(started)
				if err != nil {
					b.Fatal(err)
				}
				runtime.GC()
				runtime.ReadMemStats(&after)
				kept = after.HeapAlloc - min(before.HeapAlloc, after.HeapAlloc)
				runtime.KeepAlive(counter)
			}
			b.ReportMetric(float64(kept)/(1<<20), "MiB-kept")
			b.ReportMetric(float64(elapsed.Milliseconds())/float64(b.N), "ms-load")
		})
	}
}
