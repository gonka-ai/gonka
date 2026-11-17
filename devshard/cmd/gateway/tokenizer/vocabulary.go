package tokenizer

// The tokenizer.json type and behaviour names Load reads. See README.md, "Counting exactly".
const (
	modelTypeBPE          = "BPE"
	normalizerNFC         = "NFC"
	stepSequence          = "Sequence"
	preTokenizerByteLevel = "ByteLevel"
	preTokenizerSplit     = "Split"
	splitIsolated         = "Isolated"
	splitRemoved          = "Removed"
)

// The Hugging Face hub address a pinned tokenizer.json is read from: base, repo, the resolve segment, commit, file.
const (
	huggingFaceBaseURL = "https://huggingface.co"
	hubResolveSegment  = "resolve"
	tokenizerFileName  = "tokenizer.json"
)
