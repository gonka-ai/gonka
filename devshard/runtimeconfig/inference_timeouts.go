package runtimeconfig

import devshardpkg "devshard"

// InferenceTimeoutsFromProvider adapts a long-poll Provider for devshardctl proxy
// timeout reads.
func InferenceTimeoutsFromProvider(p Provider) devshardpkg.InferenceTimeouts {
	return inferenceTimeoutsAdapter{p: p}
}

type inferenceTimeoutsAdapter struct {
	p Provider
}

func (a inferenceTimeoutsAdapter) RefusalTimeout() int64 {
	return a.p.Snapshot().RefusalTimeout
}

func (a inferenceTimeoutsAdapter) ExecutionTimeout() int64 {
	return a.p.Snapshot().ExecutionTimeout
}
