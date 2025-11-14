package types

const (
	// MaxModelLen is the maximum length for model ID
	MaxModelLen = 128
	// MaxUrlLen is the maximum length for URLs (e.g., hf_repo)
	MaxUrlLen = 2048
	// MaxHashLen is the maximum length for hash strings (e.g., hf_commit)
	MaxHashLen = 128
	// MaxModelArgsCount is the maximum number of model arguments
	MaxModelArgsCount = 256
	// MaxModelArgLen is the maximum length for each model argument
	MaxModelArgLen = 512
	// HfCommitLength is the expected length for HuggingFace commit SHA (40 chars for hex)
	HfCommitLength = 40
)

