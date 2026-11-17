package accounting

import (
	"slices"
	"strings"
	"time"

	"devshard/cmd/gateway/engine"
)

// FirstContentTiming is the hosts' time to first content over the attempts that streamed any. See docs/accounting.md, "Time to first content".
type FirstContentTiming struct {
	FirstContentMillisecondsTotal uint64 `json:"first_content_ms_total"`
	FirstContentAttempts          uint64 `json:"first_content_attempts"`
}

func (t *FirstContentTiming) add(other FirstContentTiming) {
	t.FirstContentMillisecondsTotal += other.FirstContentMillisecondsTotal
	t.FirstContentAttempts += other.FirstContentAttempts
}

// ModelFirstContent is one model's time to first content over an epoch's escrows, with its distribution.
type ModelFirstContent struct {
	Model string `json:"model"`
	FirstContentTiming
	FirstContentP50Milliseconds uint64              `json:"first_content_p50_ms"`
	FirstContentP95Milliseconds uint64              `json:"first_content_p95_ms"`
	FirstContentP99Milliseconds uint64              `json:"first_content_p99_ms"`
	FirstContentBuckets         FirstContentBuckets `json:"first_content_buckets,omitempty"`
}

func (m *ModelFirstContent) absorb(escrow *escrowLedger) {
	for _, timing := range escrow.firstContent {
		m.add(timing)
	}
	m.FirstContentBuckets = addInto(m.FirstContentBuckets, escrow.firstContentBuckets)
}

func (m *ModelFirstContent) setPercentiles() {
	m.FirstContentP50Milliseconds = m.FirstContentBuckets.percentile(firstContentMedianPercent)
	m.FirstContentP95Milliseconds = m.FirstContentBuckets.percentile(firstContentTailPercent)
	m.FirstContentP99Milliseconds = m.FirstContentBuckets.percentile(firstContentFarTailPercent)
}

func (b *Book) firstContentByModel(admitted []string) map[uint64][]ModelFirstContent {
	entries := make(map[uint64]map[string]*ModelFirstContent)
	for _, escrowID := range admitted {
		escrow := b.escrows[escrowID]
		epoch, model := escrow.metadata.CreationEpoch, escrow.metadata.Model
		if entries[epoch] == nil {
			entries[epoch] = make(map[string]*ModelFirstContent)
		}
		entry, known := entries[epoch][model]
		if !known {
			entry = &ModelFirstContent{Model: model}
			entries[epoch][model] = entry
		}
		entry.absorb(escrow)
	}
	byEpoch := make(map[uint64][]ModelFirstContent, len(entries))
	for epoch, models := range entries {
		for _, entry := range models {
			entry.setPercentiles()
			byEpoch[epoch] = append(byEpoch[epoch], *entry)
		}
		slices.SortFunc(byEpoch[epoch], func(left, right ModelFirstContent) int { return strings.Compare(left.Model, right.Model) })
	}
	return byEpoch
}

func (e *escrowLedger) addFirstContent(nonce uint64, record *nonceRecord, delay time.Duration) {
	if record.firstContentTimed || delay <= engine.NoFirstContent {
		return
	}
	record.firstContentTimed = true
	slotID := e.slotOf(nonce)
	timing := e.firstContent[slotID]
	timing.add(FirstContentTiming{FirstContentMillisecondsTotal: uint64(delay.Milliseconds()), FirstContentAttempts: 1})
	e.firstContent[slotID] = timing
	e.firstContentBuckets[bucketOf(delay)]++
}
