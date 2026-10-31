package journal

import (
	"time"

	"devshard/cmd/gateway/engine"
)

// RequestLine is what a finished, throttled or uncached request can no longer be asked, read on the handler goroutine.
type RequestLine struct {
	RequestID     string
	Model         string
	EscrowID      string
	ClientStream  bool
	Outcome       engine.RaceOutcome
	Verdict       string
	Bytes         int64
	Terminated    bool
	Elapsed       time.Duration
	RaceErr       error
	DeliverErr    error
	CacheHit      bool
	LimiterReason string
}
