package api

import (
	"encoding/json"
	"fmt"
	"go/build"
	"io"
	"math/rand/v2"
	"net/http"
	"os"
	"path/filepath"
	"runtime/metrics"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
	"unicode/utf8"

	"devshard/cmd/gateway/config"
	"devshard/cmd/gateway/env"
	"devshard/cmd/gateway/filters"
	"devshard/cmd/gateway/tokenizer"
	"devshard/transport"
)

const noTokenizerModel = "kimi"

var benchModelContexts = map[string]int64{"minimax": 180_000, "deepseek": 400_000, "glm": 400_000, noTokenizerModel: 240_000}

var (
	benchModelKinds = []string{"english", "code", "russian", "chinese", "json", "tools"}
	benchModelSizes = []int{200, 4 << 10, 32 << 10, 64 << 10, 256 << 10, 1 << 20}
	benchWorstCases = []string{"space", "newline", "letter", "letters", "base64", "han", "han-repeat", "emoji", "mark", "ds-prefix"}
)

type benchModel struct {
	name    string
	counter *tokenizer.Counter
	context int64
}

func (m benchModel) prompts() PromptTokens {
	if m.counter == nil {
		return nil
	}
	return counterPromptTokens{counter: m.counter}
}

var (
	benchModelsOnce   sync.Once
	benchModelsLoaded []benchModel
)

func benchModels(b *testing.B) []benchModel {
	b.Helper()
	if os.Getenv("GATEWAY_TOKENIZER_DIR") == "" {
		b.Skip("GATEWAY_TOKENIZER_DIR names no directory of tokenizer.json files")
	}
	benchModelsOnce.Do(func() {
		for name, counter := range benchCounters(b) {
			benchModelsLoaded = append(benchModelsLoaded, benchModel{name: name, counter: counter, context: benchModelContexts[name]})
		}
		slices.SortFunc(benchModelsLoaded, func(left, right benchModel) int { return strings.Compare(left.name, right.name) })
		benchModelsLoaded = append(benchModelsLoaded, benchModel{name: noTokenizerModel, context: benchModelContexts[noTokenizerModel]})
	})
	return benchModelsLoaded
}

var englishCommon = strings.Fields(`the of and to a in is that for it as was with be by on not he this are or his from at which but have an they you were her she there been one all we their has would when if so no will more can what out up other into about who them some could its than then time only now like my new over also any these may first well very after just should most through where before our much how even many those such two because back way here between both under never each while same last another being right still might every since around again year people work part world without however against life long take great place think three small used make made go see know good day water house word number system point state end order line set name group high fact case real public power value thing process example program question result reason model request server token prompt context limit host network chain`)

var russianCommon = strings.Fields(`и в не на что с по это как он она они мы вы я но из у за о от то же так для все его ее их был была были быть который которая которые если или когда уже только еще где там здесь потом после перед между через под над при без до к да нет очень может можно нужно должен сейчас сегодня время год день человек люди работа дело слово место жизнь мир страна город система вопрос ответ запрос сервер модель сеть токен контекст предел`)

var russianStems = strings.Fields(`работ говор дел сдел дума знач пис чита помн понима сказ спрос отвеч провер счита запуск обработ получ отправ ожида измер сравн оцен посчита разреш запрет огранич повтор сохран удал измен нача законч открыва закрыва постро разработ испыта исправ описа объясн показа увид услыш`)

var russianEndings = []string{"ать", "ает", "ают", "ал", "ала", "али", "ение", "ения", "ением", "ениями", "ный", "ная", "ное", "ные", "ного", "ному", "ным", "ными", "ость", "ости", "ик", "ика", "ики", "ов", "ами", "ах"}

const chineseCharacters = `的一是不了人我在有他这为之大来以个中上们到说国和地也子时道出而要于就下得可你年生自会那后能对着事其里所去行过家十用发天如然作方成者多日都三小军二无同么经法当起与好看学进种将还分此心前面又定见只主没公从知全工已被现明问力理本体长相点外所已无变由求信并管门已新口通计高实果样系立使动产量重西论些文处期最间联代合问原通定机器网关模型令牌请求上下文限制主机节点链路验证奖励计算服务数据结构函数参数返回错误时间响应编码字符训练推理输入输出`

const syllables = "ka ri on ex tion ment al pro de con ver ism ate ly ing er an st ble ous ter ma ni lo qu ph ch th sh im un re dis ab ic"

func benchVocabulary(rng *rand.Rand, common []string, generated func() string, extra int) []string {
	vocabulary := slices.Clone(common)
	seen := map[string]bool{}
	for _, word := range common {
		seen[word] = true
	}
	for len(vocabulary) < len(common)+extra {
		if word := generated(); !seen[word] {
			seen[word] = true
			vocabulary = append(vocabulary, word)
		}
	}
	tail := vocabulary[len(common):]
	rng.Shuffle(len(tail), func(i, j int) { tail[i], tail[j] = tail[j], tail[i] })
	return vocabulary
}

func englishVocabulary(rng *rand.Rand) []string {
	parts := strings.Fields(syllables)
	return benchVocabulary(rng, englishCommon, func() string {
		var word strings.Builder
		for range 2 + rng.IntN(3) {
			word.WriteString(parts[rng.IntN(len(parts))])
		}
		return word.String()
	}, 6000)
}

func russianVocabulary(rng *rand.Rand) []string {
	return benchVocabulary(rng, russianCommon, func() string {
		return russianStems[rng.IntN(len(russianStems))] + russianEndings[rng.IntN(len(russianEndings))]
	}, 1000)
}

func chineseVocabulary(rng *rand.Rand) []string {
	characters := []rune(chineseCharacters)
	return benchVocabulary(rng, nil, func() string {
		word := make([]rune, 1+rng.IntN(3))
		for index := range word {
			word[index] = characters[rng.IntN(len(characters))]
		}
		return string(word)
	}, 4000)
}

var englishSentences = []string{
	"The gateway reads the whole request before it decides anything, because a decision taken on half a body is a decision it may have to take back.",
	"Most of the traffic is short: a system prompt, a question, and an answer of a few hundred tokens.",
	"When a host falls behind, the scheduler stops sending it new work and waits for the next epoch to judge it again.",
	"Nobody on the operations team had expected the cache to matter so much, but on Monday morning it answered almost a third of the requests.",
	"She opened the logs, scrolled past the usual warnings about stale snapshots, and found the line she was looking for near the bottom.",
	"A validator replays a sample of the inferences and compares the probabilities it gets with the ones the host reported.",
	"If the two disagree by more than the threshold, the host loses part of its reward for the epoch, and the network remembers it.",
	"In practice, the hardest bugs were never in the arithmetic; they were in the order things happened when two requests arrived at once.",
	"He wrote the migration on a Friday, tested it against a copy of the production database, and still found a column he had forgotten.",
	"The report is due at the end of the quarter, and it should explain, in plain words, what each of the four models costs per million tokens.",
	"Weather permitting, the team will meet in the old library on the second floor, where the windows face the river.",
	"There is no single right answer here, only a set of trade-offs between latency, cost and the risk of refusing a request a host would have served.",
	"The first chapter describes the problem; the second shows why the obvious fix does not work; the third proposes something better.",
	"For every request it admits, the limiter holds back a share of the capacity until the answer is finished or the client goes away.",
	"Please summarise the attached document in five bullet points, keep the numbers exact, and list any open questions at the end.",
	"It was late in October, and the leaves in the courtyard had turned the colour of old copper.",
	"Each participant signs the nonce it was given, so a later dispute can be settled by anyone who holds the signatures.",
	"The benchmark ran ten times on a busy machine, and the spread between the fastest and the slowest run was larger than the effect we were looking for.",
	"Can you check whether the numbers in the second table add up, and tell me which row is wrong if they do not?",
	"Under load, the slowest requests are not the largest ones but the ones that wait longest for a free slot.",
	"A good commit message says what changed and why, in one line, and leaves the story for the pull request.",
	"The river froze early that year, and the children skated on it from December until the end of February.",
	"Our customers in Europe mostly send English and German, while the ones in Asia send Chinese, Japanese and a surprising amount of Russian.",
	"If you are not sure, say so; a confident wrong answer costs more than an honest question.",
}

var russianSentences = []string{
	"Шлюз читает запрос целиком, прежде чем что-либо решать, потому что решение по половине тела, возможно, придётся отменить.",
	"Большая часть трафика короткая: системная подсказка, вопрос и ответ на несколько сотен токенов.",
	"Когда хост отстаёт, планировщик перестаёт отправлять ему новую работу и ждёт следующей эпохи, чтобы оценить его снова.",
	"Никто в команде эксплуатации не ожидал, что кэш будет так важен, но в понедельник утром он ответил почти на треть запросов.",
	"Она открыла журналы, пролистала привычные предупреждения об устаревших снимках и нашла нужную строку почти в самом конце.",
	"Валидатор повторяет выборку вычислений и сравнивает полученные вероятности с теми, о которых сообщил хост.",
	"Если они расходятся больше допустимого порога, хост теряет часть награды за эпоху, и сеть это запоминает.",
	"На практике самые трудные ошибки были не в арифметике, а в порядке событий, когда два запроса приходили одновременно.",
	"Он написал миграцию в пятницу, проверил её на копии рабочей базы данных и всё равно нашёл забытый столбец.",
	"Отчёт нужно сдать в конце квартала, и в нём простыми словами должно быть объяснено, сколько стоит миллион токенов каждой модели.",
	"Если позволит погода, команда соберётся в старой библиотеке на втором этаже, где окна выходят на реку.",
	"Единственно верного ответа здесь нет, есть лишь компромисс между задержкой, стоимостью и риском отказать в запросе.",
	"Первая глава описывает задачу, вторая показывает, почему очевидное решение не работает, а третья предлагает лучшее.",
	"Для каждого принятого запроса ограничитель удерживает долю мощности, пока ответ не завершится или клиент не уйдёт.",
	"Пожалуйста, кратко изложите приложенный документ в пяти пунктах, сохраните точные числа и перечислите открытые вопросы в конце.",
	"Был конец октября, и листья во дворе приобрели цвет старой меди.",
	"Каждый участник подписывает выданный ему одноразовый номер, поэтому любой спор позже может разрешить тот, у кого есть подписи.",
	"Тест запускали десять раз на загруженной машине, и разброс между самым быстрым и самым медленным прогоном оказался больше искомого эффекта.",
	"Можете проверить, сходятся ли числа во второй таблице, и сказать, какая строка ошибочна, если не сходятся?",
	"Под нагрузкой самые медленные запросы — не самые большие, а те, что дольше всех ждут свободного места.",
	"Река в тот год замёрзла рано, и дети катались по ней на коньках с декабря до конца февраля.",
	"Если вы не уверены, так и скажите: уверенный неверный ответ обходится дороже честного вопроса.",
}

var chineseSentences = []string{
	"网关在做出任何决定之前会先读取完整的请求，因为根据一半的请求体做出的决定可能不得不撤回。",
	"大部分流量都很短：一段系统提示、一个问题，以及几百个词元的回答。",
	"当某台主机落后时，调度器会停止向它发送新任务，并等待下一个周期再重新评估它。",
	"运维团队里没有人预料到缓存会如此重要，但在周一早上，它回答了将近三分之一的请求。",
	"她打开日志，翻过那些关于快照过期的常见警告，在接近末尾的地方找到了她要找的那一行。",
	"验证者会重放一部分推理结果，并把自己得到的概率与主机报告的概率进行比较。",
	"如果两者的差异超过阈值，主机就会失去本周期的一部分奖励，而网络会记住这件事。",
	"实际上，最难的错误从来不在算术里，而在于两个请求同时到达时各个步骤发生的顺序。",
	"他在星期五写好了迁移脚本，在生产数据库的副本上测试过，却仍然发现漏掉了一列。",
	"这份报告需要在季度末提交，并且要用平实的语言说明四个模型每百万词元的成本。",
	"如果天气允许，团队将在二楼的旧图书馆开会，那里的窗户正对着河流。",
	"这里没有唯一正确的答案，只有在延迟、成本和拒绝一个本可以被服务的请求之间的权衡。",
	"第一章描述问题，第二章说明为什么显而易见的修复方法行不通，第三章提出更好的方案。",
	"对于每一个被接纳的请求，限流器都会预留一部分容量，直到回答结束或客户端离开。",
	"请用五个要点概括附件中的文档，保留准确的数字，并在最后列出所有未解决的问题。",
	"那是十月下旬，院子里的树叶已经变成了旧铜的颜色。",
	"每个参与者都会对分配给它的随机数签名，因此之后的任何争议都可以由持有签名的人来裁决。",
	"基准测试在一台繁忙的机器上运行了十次，最快和最慢之间的差距比我们要寻找的效果还要大。",
	"你能检查一下第二张表里的数字是否对得上吗？如果对不上，请告诉我哪一行有错。",
	"在高负载下，最慢的请求并不是最大的请求，而是等待空闲位置时间最长的请求。",
	"那一年河水很早就结冰了，孩子们从十二月一直在上面滑冰到二月底。",
	"如果你不确定，就直说；一个自信但错误的回答比一个诚实的问题代价更高。",
}

func sentenceText(rng *rand.Rand, sentences []string, tail []string, size int, spaced bool) string {
	var builder strings.Builder
	paragraph := 0
	for builder.Len() < size {
		sentence := sentences[rng.IntN(len(sentences))]
		if spaced {
			words := strings.Fields(sentence)
			for index := range words {
				if rng.IntN(20) == 0 {
					words[index] = tail[rng.IntN(len(tail))]
				}
			}
			sentence = strings.Join(words, " ")
		} else if rng.IntN(3) == 0 {
			runes := []rune(sentence)
			runes[rng.IntN(len(runes))] = []rune(tail[rng.IntN(len(tail))])[0]
			sentence = string(runes)
		}
		builder.WriteString(sentence)
		if paragraph++; paragraph > 3+rng.IntN(5) {
			paragraph = 0
			builder.WriteString("\n\n")
		} else if spaced {
			builder.WriteString(" ")
		}
	}
	return builder.String()
}

func zipfText(rng *rand.Rand, vocabulary []string, size int, spaced bool) string {
	zipf := rand.NewZipf(rng, 1.07, 2, uint64(len(vocabulary)-1))
	var builder strings.Builder
	sentence, paragraph := 0, 0
	for builder.Len() < size {
		word := vocabulary[zipf.Uint64()]
		if sentence == 0 && spaced {
			first, width := utf8.DecodeRuneInString(word)
			word = strings.ToUpper(string(first)) + word[width:]
		}
		builder.WriteString(word)
		sentence++
		switch {
		case sentence > 6+rng.IntN(20):
			sentence = 0
			paragraph++
			if spaced {
				builder.WriteString(".")
			} else {
				builder.WriteString("。")
			}
			if paragraph > 3+rng.IntN(6) {
				paragraph = 0
				builder.WriteString("\n\n")
			} else if spaced {
				builder.WriteString(" ")
			}
		case rng.IntN(12) == 0 && spaced:
			builder.WriteString(", ")
		case rng.IntN(8) == 0 && !spaced:
			builder.WriteString("，")
		case rng.IntN(60) == 0:
			fmt.Fprintf(&builder, " %d ", rng.IntN(100_000))
		case spaced:
			builder.WriteString(" ")
		}
	}
	return builder.String()
}

var (
	goSourceOnce sync.Once
	goSource     string
)

func benchGoSource() string {
	goSourceOnce.Do(func() {
		var builder strings.Builder
		for _, dir := range []string{"net/http", "encoding/json", "go/parser", "go/types", "runtime"} {
			paths, _ := filepath.Glob(filepath.Join(build.Default.GOROOT, "src", dir, "*.go"))
			slices.Sort(paths)
			for _, path := range paths {
				if strings.HasSuffix(path, "_test.go") {
					continue
				}
				if source, err := os.ReadFile(path); err == nil {
					builder.Write(source)
				}
			}
		}
		goSource = builder.String()
	})
	return goSource
}

func codeText(rng *rand.Rand, size int) string {
	source := benchGoSource()
	if len(source) < size {
		lines := []string{"func (s *Server) handle(w http.ResponseWriter, r *http.Request) {\n", "\tif err := s.check(r); err != nil {\n", "\t\treturn fmt.Errorf(\"check %s: %w\", r.URL.Path, err)\n", "\t}\n", "\tfor index, value := range values {\n", "\t\ttotal += value * int64(index)\n", "}\n"}
		var builder strings.Builder
		for builder.Len() < size {
			builder.WriteString(strings.ReplaceAll(lines[rng.IntN(len(lines))], "index", fmt.Sprintf("v%d", rng.IntN(1000))))
		}
		return builder.String()
	}
	start := rng.IntN(len(source) - size + 1)
	return source[start : start+size]
}

type benchRecordItem struct {
	SKU      string  `json:"sku"`
	Quantity int     `json:"quantity"`
	Price    float64 `json:"price"`
}

type benchRecord struct {
	ID        string            `json:"id"`
	CreatedAt string            `json:"created_at"`
	Customer  map[string]string `json:"customer"`
	Items     []benchRecordItem `json:"items"`
	Status    string            `json:"status"`
	Tags      []string          `json:"tags"`
	Note      string            `json:"note"`
}

func jsonText(rng *rand.Rand, size int) string {
	words := englishVocabulary(rng)
	statuses := []string{"pending", "paid", "shipped", "delivered", "refunded"}
	var builder strings.Builder
	builder.WriteString("[\n")
	for builder.Len() < size {
		record := benchRecord{
			ID:        fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", rng.Uint32(), rng.IntN(1<<16), rng.IntN(1<<16), rng.IntN(1<<16), rng.Uint64()&(1<<48-1)),
			CreatedAt: time.Date(2026, time.Month(1+rng.IntN(12)), 1+rng.IntN(28), rng.IntN(24), rng.IntN(60), rng.IntN(60), 0, time.UTC).Format(time.RFC3339),
			Customer:  map[string]string{"name": words[rng.IntN(len(words))] + " " + words[rng.IntN(len(words))], "email": fmt.Sprintf("%s%d@example.com", words[rng.IntN(len(words))], rng.IntN(1000)), "country": []string{"DE", "US", "RU", "CN", "BR"}[rng.IntN(5)]},
			Status:    statuses[rng.IntN(len(statuses))],
			Tags:      []string{words[rng.IntN(200)], words[rng.IntN(len(words))]},
			Note:      zipfText(rng, words, 20+rng.IntN(80), true),
		}
		for range 1 + rng.IntN(4) {
			record.Items = append(record.Items, benchRecordItem{SKU: fmt.Sprintf("SKU-%06d", rng.IntN(1_000_000)), Quantity: 1 + rng.IntN(9), Price: float64(rng.IntN(100_000)) / 100})
		}
		encoded, _ := json.MarshalIndent(record, "  ", "  ")
		builder.WriteString("  ")
		builder.Write(encoded)
		builder.WriteString(",\n")
	}
	builder.WriteString("  {}\n]")
	return builder.String()
}

func benchKindText(kind string, size int, seed uint64) string {
	rng := rand.New(rand.NewPCG(uint64(size), seed))
	var text string
	switch kind {
	case "code":
		text = codeText(rng, size)
	case "russian":
		text = sentenceText(rng, russianSentences, russianVocabulary(rng)[len(russianCommon):], size, true)
	case "chinese":
		text = sentenceText(rng, chineseSentences, chineseVocabulary(rng), size, false)
	case "json":
		text = jsonText(rng, size)
	default:
		text = sentenceText(rng, englishSentences, englishVocabulary(rng)[len(englishCommon):], size, true)
	}
	return truncateRunes(text, size)
}

func truncateRunes(text string, size int) string {
	if len(text) <= size {
		return text
	}
	cut := size
	for cut > 0 && !utf8.RuneStart(text[cut]) {
		cut--
	}
	return text[:cut]
}

type benchFunction struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

type benchTool struct {
	Type     string        `json:"type"`
	Function benchFunction `json:"function"`
}

type benchCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type benchMessage struct {
	Role       string      `json:"role"`
	Content    string      `json:"content"`
	ToolCalls  []benchCall `json:"tool_calls,omitempty"`
	ToolCallID string      `json:"tool_call_id,omitempty"`
}

type benchBody struct {
	Model       string         `json:"model"`
	Stream      bool           `json:"stream"`
	Temperature float64        `json:"temperature"`
	Messages    []benchMessage `json:"messages"`
	Tools       []benchTool    `json:"tools,omitempty"`
}

var benchToolDefinitions = []benchTool{
	{Type: "function", Function: benchFunction{Name: "search_docs", Description: "Search the internal documentation and return the best matching passages with their source paths.", Parameters: json.RawMessage(`{"type":"object","properties":{"query":{"type":"string","description":"What to search for, in natural language."},"limit":{"type":"integer","minimum":1,"maximum":50,"description":"How many passages to return."},"sources":{"type":"array","items":{"type":"string","enum":["wiki","code","tickets","chat"]}}},"required":["query"]}`)}},
	{Type: "function", Function: benchFunction{Name: "run_sql", Description: "Run a read-only SQL query against the analytics warehouse and return at most 500 rows as JSON.", Parameters: json.RawMessage(`{"type":"object","properties":{"sql":{"type":"string","description":"A single SELECT statement."},"timeout_seconds":{"type":"number","default":30}},"required":["sql"]}`)}},
	{Type: "function", Function: benchFunction{Name: "get_weather", Description: "Get the current weather and a three day forecast for a city.", Parameters: json.RawMessage(`{"type":"object","properties":{"city":{"type":"string"},"unit":{"type":"string","enum":["celsius","fahrenheit"]}},"required":["city"]}`)}},
	{Type: "function", Function: benchFunction{Name: "create_ticket", Description: "Open a support ticket in the tracker, assigned to the on-call engineer of the named team.", Parameters: json.RawMessage(`{"type":"object","properties":{"title":{"type":"string","maxLength":120},"body":{"type":"string"},"team":{"type":"string","enum":["gateway","chain","hosts","web"]},"priority":{"type":"string","enum":["p0","p1","p2","p3"]},"labels":{"type":"array","items":{"type":"string"}}},"required":["title","body","team"]}`)}},
}

const benchSystemPrompt = "You are a careful assistant for the Gonka network operators. Answer precisely, cite the file or table you read, and say when you are not sure."

func benchKindChatBody(kind string, size int) string {
	body := benchBody{Model: "qwen", Temperature: 0.7}
	switch {
	case kind == "tools":
		body.Tools = benchToolDefinitions
		user := benchKindText("english", max(size/8, 64), 1)
		if size <= 512 {
			body.Messages = []benchMessage{{Role: "user", Content: benchKindText("english", size, 1)}}
			break
		}
		call := benchCall{ID: "call_0001", Type: "function"}
		call.Function.Name = "search_docs"
		arguments, _ := json.Marshal(map[string]any{"query": truncateRunes(user, 80), "limit": 10, "sources": []string{"wiki", "code"}})
		call.Function.Arguments = string(arguments)
		body.Messages = []benchMessage{
			{Role: "system", Content: benchSystemPrompt},
			{Role: "user", Content: user},
			{Role: "assistant", ToolCalls: []benchCall{call}},
			{Role: "tool", ToolCallID: call.ID, Content: benchKindText("json", size*5/8, 2)},
			{Role: "assistant", Content: benchKindText("english", size/8, 3)},
			{Role: "user", Content: benchKindText("english", size/8, 4)},
		}
	case size <= 512:
		body.Messages = []benchMessage{{Role: "user", Content: benchKindText(kind, size, 1)}}
	default:
		body.Messages = []benchMessage{
			{Role: "system", Content: benchSystemPrompt},
			{Role: "user", Content: benchKindText(kind, size/8, 1)},
			{Role: "assistant", Content: benchKindText(kind, size/8, 2)},
			{Role: "user", Content: benchKindText(kind, size-size/4-len(benchSystemPrompt), 3)},
		}
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		panic(err)
	}
	return string(encoded)
}

func worstCaseText(name string, size int) string {
	rng := rand.New(rand.NewPCG(uint64(size), uint64(len(name))))
	var unit string
	switch name {
	case "space":
		unit = " "
	case "newline":
		unit = "\n"
	case "letter":
		unit = "a"
	case "han-repeat":
		unit = "中"
	case "emoji":
		unit = "😀"
	case "mark":
		unit = "́"
	case "ds-prefix":
		unit = "<｜place▁holder▁no▁"
	}
	if unit != "" {
		return truncateRunes("x"+strings.Repeat(unit, size/len(unit)+1), size)
	}
	alphabet := map[string][]rune{
		"letters": []rune("abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ"),
		"base64":  []rune("abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789+/"),
		"han":     []rune(chineseCharacters),
	}[name]
	var builder strings.Builder
	for builder.Len() < size {
		builder.WriteRune(alphabet[rng.IntN(len(alphabet))])
	}
	return truncateRunes(builder.String(), size)
}

func singleMessageBody(text string) string {
	encoded, err := json.Marshal(benchBody{Model: "qwen", Messages: []benchMessage{{Role: "user", Content: text}}})
	if err != nil {
		panic(err)
	}
	return string(encoded)
}

func largestAdmittedText(name string) string {
	const normalizeSlack = 4 << 10
	limit := min(filters.MaxBodyBytes, transport.MaxHostRequestBytes-hostCatchUpReserveBytes-transport.PromptWireBytes(0)) - normalizeSlack
	size := limit
	for {
		text := worstCaseText(name, size)
		if len(singleMessageBody(text)) <= limit {
			return text
		}
		size = size * limit / len(singleMessageBody(text)) * 99 / 100
	}
}

func benchModelServer(b *testing.B, model benchModel) *Server {
	b.Helper()
	server := benchServer(b, benchChunks(48, 24))
	next := *server.config.Load()
	pinned := model.context
	next.Limits.ModelLimits = map[string]config.ModelLimits{"qwen": {MaxModelLen: &pinned}}
	server.config.Swap(&next)
	server.promptTokens = model.prompts()
	server.statsTokenizerEnabled = readStatsTokenizerSetting(b)
	return server
}

func readStatsTokenizerSetting(b *testing.B) bool {
	b.Helper()
	values, err := env.Load()
	if err != nil {
		b.Fatal(err)
	}
	return values.StatsTokenizerEnabled != nil && *values.StatsTokenizerEnabled
}

func serveAndReport(b *testing.B, handler http.Handler, body string) {
	b.Helper()
	request := benchRequest("/v1/chat/completions", body)
	writer := newSinkWriter()
	serve := func() int {
		writer.reset()
		next := *request
		next.Body = io.NopCloser(strings.NewReader(body))
		next.ContentLength = int64(len(body))
		handler.ServeHTTP(writer, &next)
		return writer.status
	}
	want := serve()
	if want != http.StatusOK && want != http.StatusBadRequest {
		b.Fatalf("warm-up status = %d", want)
	}
	b.ReportAllocs()
	started := processCPU()
	for b.Loop() {
		if status := serve(); status != want {
			b.Fatalf("status = %d, want %d", status, want)
		}
	}
	b.ReportMetric(float64(processCPU()-started)/float64(b.N), "cpu-ns/op")
	b.ReportMetric(float64(want), "status")
}

func processCPU() int64 {
	var usage syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &usage); err != nil {
		return 0
	}
	return usage.Utime.Nano() + usage.Stime.Nano()
}

// Test flow:
//  1. Load every tokenizer.json in GATEWAY_TOKENIZER_DIR as a model, add kimi without one, read DEVSHARD_STATS_TOKENIZER as the gateway does, and skip when the directory is unset.
//  2. Pin each model's governed context length and build a realistic chat body per content kind and prompt size.
//  3. Serve each body through the chat route once to warm it, then time it, reporting the handler's time, allocations and HTTP status.
func BenchmarkModelChat(b *testing.B) {
	for _, model := range benchModels(b) {
		for _, kind := range benchModelKinds {
			for _, size := range benchModelSizes {
				body := benchKindChatBody(kind, size)
				b.Run(fmt.Sprintf("%s/%s/%dB", model.name, kind, size), func(b *testing.B) {
					serveAndReport(b, benchModelServer(b, model).Handler(), body)
				})
			}
		}
	}
}

// Test flow:
//  1. Load every tokenizer.json in GATEWAY_TOKENIZER_DIR as a model, add kimi without one, read DEVSHARD_STATS_TOKENIZER as the gateway does, and skip when the directory is unset.
//  2. Build a single-message chat body of text the pre-tokenizer cannot split, at 64 KiB and at the largest size the route admits.
//  3. Serve each through the chat route and report the handler's time and allocations.
func BenchmarkModelChatWorstCase(b *testing.B) {
	sizes := map[string]func(string) string{
		"64KiB": func(name string) string { return worstCaseText(name, 64<<10) },
		"max":   largestAdmittedText,
	}
	for _, model := range benchModels(b) {
		for _, name := range benchWorstCases {
			for _, sizeName := range []string{"64KiB", "max"} {
				body := singleMessageBody(sizes[sizeName](name))
				b.Run(fmt.Sprintf("%s/%s/%s", model.name, name, sizeName), func(b *testing.B) {
					serveAndReport(b, benchModelServer(b, model).Handler(), body)
				})
			}
		}
	}
}

var benchParallelBodies = []struct {
	name string
	body func() string
}{
	{"english-4KiB", func() string { return benchKindChatBody("english", 4<<10) }},
	{"tools-4KiB", func() string { return benchKindChatBody("tools", 4<<10) }},
	{"english-64KiB", func() string { return benchKindChatBody("english", 64<<10) }},
	{"chinese-64KiB", func() string { return benchKindChatBody("chinese", 64<<10) }},
	{"space-64KiB", func() string { return singleMessageBody(worstCaseText("space", 64<<10)) }},
}

// Test flow:
//  1. Load every tokenizer.json in GATEWAY_TOKENIZER_DIR as a model, add kimi without one, read DEVSHARD_STATS_TOKENIZER as the gateway does, and skip when the directory is unset.
//  2. Serve a typical, a long and an unsplittable chat body from GOMAXPROCS goroutines at once through one handler.
//  3. Report the time per request across all goroutines, the inverse of throughput at saturation, and the peak live heap seen while serving.
func BenchmarkModelChatParallel(b *testing.B) {
	for _, model := range benchModels(b) {
		for _, entry := range benchParallelBodies {
			body := entry.body()
			b.Run(model.name+"/"+entry.name, func(b *testing.B) {
				handler := benchModelServer(b, model).Handler()
				request := benchRequest("/v1/chat/completions", body)
				peak := watchHeap()
				var failures atomic.Int64
				b.ReportAllocs()
				b.ResetTimer()
				started := processCPU()
				b.RunParallel(func(pb *testing.PB) {
					writer := newSinkWriter()
					for pb.Next() {
						writer.reset()
						next := *request
						next.Body = io.NopCloser(strings.NewReader(body))
						next.ContentLength = int64(len(body))
						handler.ServeHTTP(writer, &next)
						if writer.status != http.StatusOK && writer.status != http.StatusBadRequest {
							failures.Add(1)
						}
					}
				})
				b.StopTimer()
				b.ReportMetric(float64(processCPU()-started)/float64(b.N), "cpu-ns/op")
				b.ReportMetric(peak()/(1<<20), "peak-heap-MiB")
				if failures.Load() > 0 {
					b.Fatalf("%d requests failed", failures.Load())
				}
			})
		}
	}
}

func watchHeap() func() float64 {
	samples := []metrics.Sample{{Name: "/memory/classes/heap/objects:bytes"}}
	var peak atomic.Uint64
	done := make(chan struct{})
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		ticker := time.NewTicker(time.Millisecond)
		defer ticker.Stop()
		for {
			metrics.Read(samples)
			if value := samples[0].Value.Uint64(); value > peak.Load() {
				peak.Store(value)
			}
			select {
			case <-done:
				return
			case <-ticker.C:
			}
		}
	}()
	return func() float64 {
		close(done)
		<-stopped
		return float64(peak.Load())
	}
}

// Test flow:
//  1. Load every tokenizer.json in GATEWAY_TOKENIZER_DIR as a model, add kimi without one, read DEVSHARD_STATS_TOKENIZER as the gateway does, and skip when the directory is unset.
//  2. Let the race win with an escrow and a finished stream so the first answer is cached, then serve the same body again and again, every one a cache hit.
//  3. Report the handler's time and allocations per cache hit.
func BenchmarkModelChatCacheHit(b *testing.B) {
	for _, model := range benchModels(b) {
		for _, entry := range benchParallelBodies {
			body := entry.body()
			b.Run(model.name+"/"+entry.name, func(b *testing.B) {
				server := benchModelServer(b, model)
				engine := server.inference.(*fakeEngine)
				engine.outcome.EscrowID = "7"
				done := len(engine.chunks) - 1
				engine.chunks = append(slices.Clone(engine.chunks[:done]), `data: {"id":"chatcmpl-stop","object":"chat.completion.chunk","created":1700000000,"model":"qwen","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`+"\n\n", engine.chunks[done])
				serveAndReport(b, server.Handler(), body)
				if runs := engine.runs.Load(); runs != 1 {
					b.Fatalf("the engine ran %d times, want the one run the cache stored", runs)
				}
			})
		}
	}
}

// Test flow:
//  1. Load every tokenizer.json in GATEWAY_TOKENIZER_DIR as a model, add kimi without one, read DEVSHARD_STATS_TOKENIZER as the gateway does, and skip when the directory is unset.
//  2. Serve one chat body per content kind and size and read the input tokens the route handed the race and the limiter, none when the context check refused it.
//  3. Report that number, the body's bytes over four, and for a tokenizer the exact count of the prompt text, with the carried number's error against it in percent.
func BenchmarkModelPromptCarried(b *testing.B) {
	for _, model := range benchModels(b) {
		for _, kind := range benchModelKinds {
			for _, size := range benchModelSizes {
				body := benchKindChatBody(kind, size)
				b.Run(fmt.Sprintf("%s/%s/%dB", model.name, kind, size), func(b *testing.B) {
					server := benchModelServer(b, model)
					engine := server.inference.(*fakeEngine)
					handler := server.Handler()
					var carried int64
					for b.Loop() {
						engine.raced.Store(nil)
						writer := newSinkWriter()
						handler.ServeHTTP(writer, benchRequest("/v1/chat/completions", body))
						if raced := engine.raced.Load(); raced != nil {
							carried = int64(raced.InputTokens)
						}
					}
					b.ReportMetric(float64(carried), "carried-tok")
					b.ReportMetric(float64(len(body))/4, "body/4")
					if model.counter == nil {
						return
					}
					parts, err := filters.PromptText([]byte(body))
					if err != nil {
						b.Fatal(err)
					}
					exact := 0
					for _, part := range parts {
						tokens, err := model.counter.Count(part)
						if err != nil {
							b.Fatal(err)
						}
						exact += tokens
					}
					b.ReportMetric(float64(exact), "exact-tok")
					if carried > 0 {
						b.ReportMetric(100*(float64(carried)-float64(exact))/float64(exact), "%err")
					}
				})
			}
		}
	}
}

// Test flow:
//  1. Load every tokenizer.json in GATEWAY_TOKENIZER_DIR as a model, add kimi without one, read DEVSHARD_STATS_TOKENIZER as the gateway does, and skip when the directory is unset.
//  2. Normalize each realistic chat body and each 64 KiB unsplittable body as the chat route does.
//  3. Time only the route's prompt step, the context check and the count it takes, reporting its time, process CPU and allocations.
func BenchmarkModelPromptStep(b *testing.B) {
	models := benchModels(b)
	bodies := map[string]string{}
	names := make([]string, 0, len(benchModelSizes)*len(benchModelKinds)+len(benchWorstCases))
	for _, kind := range benchModelKinds {
		for _, size := range benchModelSizes {
			name := fmt.Sprintf("%s/%dB", kind, size)
			bodies[name] = benchKindChatBody(kind, size)
			names = append(names, name)
		}
	}
	for _, name := range benchWorstCases {
		key := "worst-" + name + "/65536B"
		bodies[key] = singleMessageBody(worstCaseText(name, 64<<10))
		names = append(names, key)
	}
	for _, model := range models {
		server := benchModelServer(b, model)
		configuration := server.config.Load()
		for _, name := range names {
			normalized, err := filters.NormalizeRequest([]byte(bodies[name]), filterOptions(configuration.Limits, false, server.servedModelVocabulary, server.modelFilters.Profiles()))
			if err != nil {
				b.Fatal(err)
			}
			b.Run(model.name+"/"+name, func(b *testing.B) {
				refused := 0.0
				if benchContextStep(server, configuration.Limits, normalized) != nil {
					refused = 1
				}
				b.ReportAllocs()
				started := processCPU()
				for b.Loop() {
					_ = benchContextStep(server, configuration.Limits, normalized)
				}
				b.ReportMetric(float64(processCPU()-started)/float64(b.N), "cpu-ns/op")
				b.ReportMetric(refused, "refused")
			})
		}
	}
}

func benchContextStep(server *Server, configured config.Limits, normalized filters.Result) error {
	prompt, err := server.checkContext(configured, normalized)
	if err == nil {
		server.countPrompt(normalized.Model, prompt)
	}
	return err
}
