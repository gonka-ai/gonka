// Package engine races escalated attempts for one inference request and reports what happened
// through a single RaceOutcome.
package engine

import (
	"time"

	"devshard/cmd/gateway/filters"
	"devshard/types"
)

// See README.md, "The exemption ladder".
const (
	longResponseExemption  = 280 * time.Second
	emptyStreamHeldTooLong = types.DefaultRefusalTimeoutSeconds * time.Second
)

// Lifecycle carries escrow facts the engine observes and must not act on.
type Lifecycle struct {
	EscrowMissing    bool
	BalanceExhausted bool
	ClientGone       bool
}

type RaceOutcome struct {
	RequestID            string
	EscrowID             string
	Model                string
	InputTokens          uint64
	InputTokensSource    filters.InputTokensSource
	InputTokensFromBytes uint64
	OutputTokens         uint64
	Decision             string
	ClientStream         bool

	ModelContextLength uint64

	WinnerNonce uint64
	Succeeded   bool
	Attempts    []AttemptOutcome

	PoCBypassActive bool
	Lifecycle       Lifecycle
}

// AttemptOutcome is what one attempt ended up doing; StartedAt is the race's, not the attempt's. See README, "Timeout votes".
type AttemptOutcome struct {
	Participant string
	HostIdx     int
	HostLabel   string
	Nonce       uint64
	Role        string
	StartReason string
	Suspicious  bool

	StartedAt    time.Time
	SendTime     time.Time
	ReceiptTime  time.Time
	FirstToken   time.Time
	FirstContent time.Time
	LastChunk    time.Time
	Completed    time.Time

	ContentChunks         int64
	StreamChunks          int64
	OutputBytes           int64
	UsageCompletionTokens int64
	UsagePromptTokens     int64
	LogprobTokens         int64
	MaxChunkGap           time.Duration
	MaxChunkGapAt         int64
	MeanChunkGap          time.Duration
	DroppedEvents         int64

	Terminal        Terminal
	LogprobsDecoded bool
	HostTerminated  bool
	Confirmed       bool
	ConfirmedAt     int64
	NonceFinished   bool

	ReceiptDeadlineMissed    bool
	FirstTokenDeadlineMissed bool

	// Capability is a refusal read off the dispatch error, where the SSE error fields stay empty.
	Capability     CapabilitySignal
	ContentSource  string
	UpstreamStatus int
	UpstreamBody   string
	FirstChunkHead string
	LastChunkHead  string
	FinishReason   string

	ErrorSource  string
	ErrorCode    string
	ErrorType    string
	ErrorMessage string
	ErrorPayload string

	MissProof *MissProof

	PhaseTransitionAborted bool
	StateDivergent         bool
	LocalFault             bool
	HostAbsent             bool
	GatewayPressure        bool
}

const executorStampTruncation = time.Second

// The stamp landed inside the round trip, so it is compared against that window's midpoint. See README, "Measurements".
func ClockOffset(attempt AttemptOutcome) (time.Duration, bool) {
	if attempt.ConfirmedAt == 0 || attempt.SendTime.IsZero() || !attempt.ReceiptTime.After(attempt.SendTime) {
		return 0, false
	}
	roundTripMidpoint := timeMidpoint(attempt.SendTime, attempt.ReceiptTime)
	confirmed := time.Unix(attempt.ConfirmedAt, 0)
	stamped := timeMidpoint(confirmed, confirmed.Add(executorStampTruncation))
	return stamped.Sub(roundTripMidpoint), true
}

// OutputTokens is what the attempt produced. See race.md, "Counting what an attempt produced".
func (a AttemptOutcome) OutputTokens() int64 {
	if a.UsageCompletionTokens > 0 {
		return a.UsageCompletionTokens
	}
	return a.LogprobTokens
}

func (o RaceOutcome) overranReservation(a AttemptOutcome) bool {
	return a.OutputTokens() > int64(o.OutputTokens)
}

func (a AttemptOutcome) emptyStream() bool {
	return a.Terminal == TerminalEmptyStream || a.Terminal == TerminalBurnEmpty
}

func (a AttemptOutcome) claimsMiss() bool {
	return a.Terminal == TerminalErrorStream && a.MissProof != nil
}

func (a AttemptOutcome) errorStream() bool {
	return a.Terminal == TerminalErrorStream || a.Terminal == TerminalCapabilityRefused
}

func (a AttemptOutcome) wonOrLost() bool {
	return a.Terminal == TerminalWon || a.Terminal == TerminalLost
}

func (a AttemptOutcome) missedADeadline() bool {
	return a.ReceiptDeadlineMissed || a.FirstTokenDeadlineMissed
}

func (a AttemptOutcome) elapsed() time.Duration {
	if a.SendTime.IsZero() || a.Completed.IsZero() {
		return 0
	}
	return a.Completed.Sub(a.SendTime)
}

func (o RaceOutcome) WinnerNonceFinished() (finished, crowned bool) {
	for _, attempt := range o.Attempts {
		if o.IsWinner(attempt) {
			return attempt.NonceFinished, true
		}
	}
	return false, false
}

// TimePerOutputToken starts at the first content chunk, so prefill is not charged to decode speed.
func TimePerOutputToken(a AttemptOutcome) time.Duration {
	if a.UsageCompletionTokens <= 0 || a.FirstContent.IsZero() || !a.LastChunk.After(a.FirstContent) {
		return 0
	}
	return a.LastChunk.Sub(a.FirstContent) / time.Duration(a.UsageCompletionTokens)
}

// NoFirstContent is FirstContentDelay's answer for an attempt that streamed no content.
const NoFirstContent time.Duration = 0

// FirstContentDelay is the host's time to first content from this attempt's send; NoFirstContent when it sent no content.
func (a AttemptOutcome) FirstContentDelay() time.Duration {
	if a.SendTime.IsZero() || !a.FirstContent.After(a.SendTime) {
		return NoFirstContent
	}
	return a.FirstContent.Sub(a.SendTime)
}

func timeMidpoint(start, end time.Time) time.Time {
	return start.Add(end.Sub(start) / 2)
}
