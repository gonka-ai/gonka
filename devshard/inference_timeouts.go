package devshard

// InferenceTimeouts supplies live proxy-side refusal/execution deadlines (seconds).
// Read on each inference attempt; not part of the state-root preimage.
type InferenceTimeouts interface {
	RefusalTimeout() int64
	ExecutionTimeout() int64
}
