package types

const (
	// DefaultMaxVoters is the default number of valid votes required for fallback verification.
	DefaultMaxVoters uint32 = 5
	// DefaultMaxSkips is the default maximum number of voters that may be skipped during sampling.
	DefaultMaxSkips uint32 = 1
	// DefaultMaxVotersToSample is the default number of voters to sample for fallback verification.
	// We sample 2x the number needed (2 * DefaultMaxVoters) so that if the first batch has
	// invalid voters (unreachable, timeout, bad signature), we can still reach 5 valid votes.
	DefaultMaxVotersToSample uint32 = 10
)
