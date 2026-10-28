package journal

import (
	"devshard/cmd/gateway/accounting"
	"devshard/cmd/gateway/engine"
	"devshard/cmd/gateway/scheduler"
)

// queuedEvent is one accepted step; a payload wider than a few words is a pointer the producer filled before admission. See README.md, "Two lanes".
type queuedEvent struct {
	kind      Kind
	escrowID  string
	race      *engine.RaceOutcome
	timeout   *engine.TimeoutEvent
	burn      scheduler.Burn
	raceStep  *engine.RaceStep
	request   *RequestLine
	facts     []DiffFact
	attempt   *accounting.Attempt
	probeVote bool
	render    func(lines logSink)
}
