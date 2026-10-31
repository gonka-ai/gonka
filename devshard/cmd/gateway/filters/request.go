package filters

// Options carries NormalizeRequest's per-call configuration. See README.md, "Output token limits".
type Options struct {
	Admin               bool
	KeepClientStream    bool
	DefaultMaxTokens    uint64
	MaxTokensCap        uint64
	RoutedModel         string
	Profiles            *ProfileSet
	ModelTokenLimits    func(model string) (defaultMaxTokens, maxTokensCap uint64)
	StopTokenVocabulary func(model string) int
}

// VocabularySizeUnknown is what Options.StopTokenVocabulary answers for a model whose vocabulary it cannot resolve.
const VocabularySizeUnknown = 0

// Result is the normalized body plus the typed fields callers need without re-parsing it.
type Result struct {
	Body                    []byte
	Model                   string
	ClientStream            bool
	ClientUsage             bool
	MaxTokens               uint64
	MaxCompletionTokens     uint64
	Logprobs                LogprobIntent
	EmptyAnswerCountsTokens bool
}

func NormalizeRequest(body []byte, options Options) (Result, error) {
	document, err := ParseDocument(body)
	if err != nil {
		return Result{}, err
	}
	return runPipeline(document, options)
}
