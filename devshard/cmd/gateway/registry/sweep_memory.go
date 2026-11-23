package registry

import (
	"context"
	"time"

	"devshard/cmd/gateway/quorum"
	"devshard/user"
)

const (
	sweepBackoffDoublings = 8
	sweepBackoffCapTicks  = 1 << (sweepBackoffDoublings - 1)
	firstRaceNonce        = uint64(0)
	backoffHonoured       = true
	backoffIgnored        = false
)

var noSendTime time.Time

// SweepReport is what one escrow's sweep did; Votes names the nonces worth a line, the unvoteable ones once and each failed vote. See README.md, "The execution-timeout sweep".
type SweepReport struct {
	Due     int
	Applied int
	Failed  int
	Votes   []SweptVote
}

// SweptVote is one nonce a sweep named: skipped because no round can pass it, or voted and not applied.
type SweptVote struct {
	Nonce      uint64
	Unvoteable bool
	Detail     string
	Err        error
}

type sweepRecord struct {
	failures   int
	retryTick  uint64
	unvoteable bool
}

type timeoutSweep struct {
	tick    uint64
	records map[uint64]sweepRecord
}

func newTimeoutSweep() *timeoutSweep {
	return &timeoutSweep{records: make(map[uint64]sweepRecord)}
}

func (s *timeoutSweep) scanSize(budget int) int { return budget + len(s.records) }

func (s *timeoutSweep) begin(due []uint64, window int) {
	s.tick++
	if len(due) >= window {
		return
	}
	kept := make(map[uint64]sweepRecord, len(due))
	for _, nonce := range due {
		if record, known := s.records[nonce]; known {
			kept[nonce] = record
		}
	}
	s.records = kept
}

func (s *timeoutSweep) choose(due []uint64, budget int, honourBackoff bool, voteable func(nonce uint64) bool) (votes, unvoteable []uint64) {
	var retries []uint64
	for _, nonce := range due {
		record, known := s.records[nonce]
		switch {
		case !known && !voteable(nonce):
			s.records[nonce] = sweepRecord{unvoteable: true}
			unvoteable = append(unvoteable, nonce)
		case !known:
			votes = append(votes, nonce)
		case !record.unvoteable && (!honourBackoff || record.retryTick <= s.tick):
			retries = append(retries, nonce)
		}
	}
	votes = append(votes, retries...)
	return votes[:min(budget, len(votes))], unvoteable
}

func (s *timeoutSweep) settle(nonce uint64, result user.TimeoutResult) {
	if result.Applied || result.Outcome == timeoutOutcomeSkipped {
		delete(s.records, nonce)
		return
	}
	record := s.records[nonce]
	record.failures++
	record.retryTick = s.tick + 1 + backoffFor(record.failures)
	s.records[nonce] = record
}

func backoffFor(failures int) uint64 {
	if failures > sweepBackoffDoublings {
		return sweepBackoffCapTicks
	}
	return 1 << (failures - 1)
}

func (h sessionHandle) sweepDue(ctx context.Context, grace time.Duration, budget int, honourBackoff bool) SweepReport {
	window := h.sweep.scanSize(budget)
	due := h.machine.StartedInferencesPastDeadline(user.TimeoutBuffer, grace, firstRaceNonce, window)
	h.sweep.begin(due, window)
	snapshot := h.machine.SnapshotStateNoInferences()
	votes, unvoteable := h.sweep.choose(due, budget, honourBackoff, func(nonce uint64) bool {
		return quorum.Voteable(snapshot.Group, h.machine.VoteThreshold(), nonce)
	})
	report := SweepReport{Due: len(votes)}
	for _, nonce := range unvoteable {
		report.Votes = append(report.Votes, SweptVote{Nonce: nonce, Unvoteable: true})
	}
	for _, nonce := range votes {
		if ctx.Err() != nil {
			break
		}
		result, err := h.HandleTimeout(ctx, nonce, noSendTime, nil)
		h.sweep.settle(nonce, result)
		if result.Applied {
			report.Applied++
			continue
		}
		report.Failed++
		if result.Outcome != timeoutOutcomeSkipped {
			report.Votes = append(report.Votes, SweptVote{Nonce: nonce, Detail: result.DetailReason, Err: err})
		}
	}
	return report
}
