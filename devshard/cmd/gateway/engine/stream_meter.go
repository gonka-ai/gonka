package engine

import (
	"io"
	"sync"
	"time"

	"devshard/cmd/gateway/config"
	"devshard/cmd/gateway/scheduler"
)

const meterCarryBytes = 1 << 20

// StreamMeter reads a stream no race owns the way a race reads an attempt's, so its output tokens and timings come from the same code. See race.md, "Counting what an attempt produced".
type StreamMeter struct {
	mu     sync.Mutex
	spec   AttemptSpec
	state  *attemptState
	writer *attemptWriter
}

func NewStreamMeter(now func() time.Time, participant string, nonce scheduler.Prepared) *StreamMeter {
	reported := make(chan struct{})
	close(reported)
	budget := newCarryBudget(config.Stream{
		ClassifyMaxAttemptBytes:     meterCarryBytes,
		ClassifyMaxParticipantBytes: meterCarryBytes,
		ClassifyMaxGlobalBytes:      meterCarryBytes,
	})
	spec := AttemptSpec{
		Classifier:  newSSEClassifier(budget, participant, false, nil),
		Nonce:       nonce,
		Participant: participant,
		Sink:        io.Discard,
		Now:         now,
		Reported:    reported,
	}
	state := &attemptState{sendTime: now()}
	return &StreamMeter{spec: spec, state: state, writer: &attemptWriter{spec: spec, state: state}}
}

func (m *StreamMeter) Write(chunk []byte) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.writer.Write(chunk)
}

// Sent restamps the send time once the catch-up is done, as an attempt stamps it after its own.
func (m *StreamMeter) Sent() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.state.sendTime = m.spec.Now()
}

// Receipt stamps the first receipt only, as an attempt does.
func (m *StreamMeter) Receipt() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.state.receiptTime.IsZero() {
		m.state.receiptTime = m.spec.Now()
	}
}

// Outcome closes the stream and reads it; confirmedAt is the executor's signed stamp, 0 when the reply carried none, and a failed send leaves the unterminated last event unread, as an attempt does. Call it once, after the last write.
func (m *StreamMeter) Outcome(confirmedAt int64, sendErr error) AttemptOutcome {
	m.mu.Lock()
	defer m.mu.Unlock()
	if sendErr == nil {
		m.state.record(m.spec.Classifier.Flush())
	}
	m.state.completed = m.spec.Now()
	m.state.confirmed, m.state.confirmedAt = confirmedAt > 0, confirmedAt
	outcome := m.state.outcome(m.spec)
	m.spec.Classifier.Release()
	return *outcome
}
