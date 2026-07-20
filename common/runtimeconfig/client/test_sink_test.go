package client

import "sync"

// recordingSink is a test double for AvailabilitySink.
type recordingSink struct {
	mu sync.Mutex
	v  AvailabilityView
}

func newRecordingSink(enabled bool, ts int64, epoch uint64) *recordingSink {
	return &recordingSink{v: AvailabilityView{Enabled: enabled, Time: ts, EpochID: epoch}}
}

func (r *recordingSink) Record(enabled bool, ts int64, epoch uint64) {
	r.mu.Lock()
	r.v = AvailabilityView{Enabled: enabled, Time: ts, EpochID: epoch}
	r.mu.Unlock()
}

func (r *recordingSink) view() AvailabilityView {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.v
}
