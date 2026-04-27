package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"devshard/host"
	"devshard/logging"
	"devshard/transport"
	"devshard/types"
	"devshard/user"
)

// errEmptyStream marks an attempt that completed successfully at the transport
// layer but produced no content tokens. The host returned only protocol/SSE
// boilerplate (role chunk, [DONE]) without any actual delta content. We treat
// this as a failure so redundancy can retry on a different host and the
// offending host is recorded as non-responsive in the local PerfTracker.
var errEmptyStream = errors.New("empty content stream")

// sseChunkHasContent reports whether the given bytes contain at least one SSE
// data event carrying a content payload that a streaming OpenAI-compatible
// client would actually render. Only `choices[].delta.content`,
// `choices[].delta.reasoning_content`, and `choices[].delta.tool_calls`
// qualify.
//
// Deliberately NOT treated as content (even though earlier versions did):
//   - `choices[].message.*` — the non-streaming response shape. A backend
//     that wraps this inside an SSE `data:` event for a `stream=true` request
//     is misbehaving; streaming clients parse `delta` and see nothing. We
//     observed a mainnet host using exactly this trick to win races with a
//     single event while delivering no tokens to the user.
//   - `choices[].text` — the legacy `/v1/completions` shape. The proxy's
//     streaming path only serves `/v1/chat/completions`; a host emitting
//     `text` here produces the same "1 chunk, 0 rendered tokens" failure.
//
// Role-only chunks, empty deltas, finish-only chunks, and `[DONE]` markers
// continue to return false.
func sseChunkHasContent(p []byte) bool {
	_, ok := sseChunkContentSource(p)
	return ok
}

// sseChunkContentSource is the classifying variant of sseChunkHasContent: when
// content is present it returns a short label identifying the field that
// carried it ("delta.content", "delta.reasoning_content", "delta.tool_calls",
// or the convertible completion shape "message.content"). The second return
// value is false when no accepted content was found. Used for forensic logging
// so we can tell, after the fact, exactly which field a short-content winner
// was emitting.
func sseChunkContentSource(p []byte) (string, bool) {
	if len(p) == 0 {
		return "", false
	}
	for _, line := range bytes.Split(p, []byte("\n")) {
		line = bytes.TrimRight(line, "\r")
		if !bytes.HasPrefix(line, []byte("data:")) {
			continue
		}
		payload := bytes.TrimSpace(line[len("data:"):])
		if len(payload) == 0 || bytes.Equal(payload, []byte("[DONE]")) {
			continue
		}
		var evt struct {
			Choices []struct {
				Delta struct {
					Content          string          `json:"content"`
					ReasoningContent string          `json:"reasoning_content"`
					ToolCalls        json.RawMessage `json:"tool_calls"`
				} `json:"delta"`
				Message struct {
					Content string `json:"content"`
				} `json:"message"`
			} `json:"choices"`
		}
		if err := json.Unmarshal(payload, &evt); err != nil {
			continue
		}
		for _, c := range evt.Choices {
			if c.Delta.Content != "" {
				return "delta.content", true
			}
			if c.Delta.ReasoningContent != "" {
				return "delta.reasoning_content", true
			}
			if hasJSONArrayElements(c.Delta.ToolCalls) {
				return "delta.tool_calls", true
			}
			if c.Message.Content != "" {
				return "message.content", true
			}
		}
	}
	return "", false
}

// hasJSONArrayElements returns true if raw is a JSON array with at least one
// element. Returns false for null/empty/[]/non-array values.
func hasJSONArrayElements(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return false
	}
	if !bytes.HasPrefix(trimmed, []byte("[")) {
		return false
	}
	inner := bytes.TrimSpace(trimmed[1 : len(trimmed)-1])
	return len(inner) > 0
}

// Tuning knobs — exported so they can be adjusted without code changes.
var (
	ReceiptTimeout             = 5 * time.Second
	ParallelAdvantageThreshold = 0.5 // 50% better estimated time
	UnresponsiveThreshold      = 1.0 // any non-responsive history → start secondary
	MinSamplesForDecision      = 3
	LogHeartbeatInterval       = time.Minute
	FirstTokenTimeoutCap       = time.Second
	PerInputTokenFirstTokenLag = 10 * time.Millisecond
	// InterChunkStallTimeout caps how long the crowned winner may go silent
	// between forwarded chunks before we abort the stream as stalled.
	InterChunkStallTimeout   = time.Minute
	NonStreamResponseFloor   = 20 * time.Second
	PerInputTokenResponseLag = 20 * time.Millisecond
	SecondaryWaitAfterWinner = 5 * time.Minute
)

func DefaultRedundancySettings() RedundancySettings {
	return RedundancySettings{
		ReceiptTimeoutMS:             5000,
		FirstTokenTimeoutFloorMS:     1000,
		PerInputTokenFirstTokenLagMS: 10,
		InterChunkStallTimeoutMS:     60000,
		NonStreamResponseFloorMS:     20000,
		PerInputTokenResponseLagMS:   20,
		SecondaryWaitAfterWinnerMS:   300000,
		ParallelAdvantageThreshold:   0.5,
		UnresponsiveThreshold:        1.0,
	}
}

func ApplyRedundancySettings(settings RedundancySettings) {
	defaults := DefaultRedundancySettings()
	if settings.ReceiptTimeoutMS <= 0 {
		settings.ReceiptTimeoutMS = defaults.ReceiptTimeoutMS
	}
	if settings.FirstTokenTimeoutFloorMS <= 0 {
		settings.FirstTokenTimeoutFloorMS = defaults.FirstTokenTimeoutFloorMS
	}
	if settings.PerInputTokenFirstTokenLagMS < 0 {
		settings.PerInputTokenFirstTokenLagMS = defaults.PerInputTokenFirstTokenLagMS
	}
	if settings.InterChunkStallTimeoutMS < 0 {
		settings.InterChunkStallTimeoutMS = defaults.InterChunkStallTimeoutMS
	}
	if settings.NonStreamResponseFloorMS <= 0 {
		settings.NonStreamResponseFloorMS = defaults.NonStreamResponseFloorMS
	}
	if settings.PerInputTokenResponseLagMS < 0 {
		settings.PerInputTokenResponseLagMS = defaults.PerInputTokenResponseLagMS
	}
	if settings.SecondaryWaitAfterWinnerMS <= 0 {
		settings.SecondaryWaitAfterWinnerMS = defaults.SecondaryWaitAfterWinnerMS
	}
	if settings.ParallelAdvantageThreshold <= 0 || settings.ParallelAdvantageThreshold >= 1 {
		settings.ParallelAdvantageThreshold = defaults.ParallelAdvantageThreshold
	}
	if settings.UnresponsiveThreshold <= 0 || settings.UnresponsiveThreshold > 1 {
		settings.UnresponsiveThreshold = defaults.UnresponsiveThreshold
	}
	ReceiptTimeout = time.Duration(settings.ReceiptTimeoutMS) * time.Millisecond
	FirstTokenTimeoutCap = time.Duration(settings.FirstTokenTimeoutFloorMS) * time.Millisecond
	PerInputTokenFirstTokenLag = time.Duration(settings.PerInputTokenFirstTokenLagMS) * time.Millisecond
	InterChunkStallTimeout = time.Duration(settings.InterChunkStallTimeoutMS) * time.Millisecond
	NonStreamResponseFloor = time.Duration(settings.NonStreamResponseFloorMS) * time.Millisecond
	PerInputTokenResponseLag = time.Duration(settings.PerInputTokenResponseLagMS) * time.Millisecond
	SecondaryWaitAfterWinner = time.Duration(settings.SecondaryWaitAfterWinnerMS) * time.Millisecond
	ParallelAdvantageThreshold = settings.ParallelAdvantageThreshold
	UnresponsiveThreshold = settings.UnresponsiveThreshold
}

var maxSpeculativeAttempts atomic.Int64

func SetMaxSpeculativeAttempts(v int) {
	maxSpeculativeAttempts.Store(int64(v))
}

func CurrentMaxSpeculativeAttempts() int {
	return int(maxSpeculativeAttempts.Load())
}

// Decision describes whether and when to start a parallel secondary inference.
type Decision struct {
	RunSecondary bool
	Delay        time.Duration // 0 = immediate
	Reason       string
}

// Redundancy runs one request reliably, using extra attempts when needed.
// It sits between Proxy and Session: Proxy delegates request execution here,
// and Redundancy decides whether to use just one nonce or several.
type Redundancy struct {
	session              *user.Session
	perf                 *PerfTracker
	groupSize            int
	devshardID             string
	model                string // escrow's registered model; used for ghost probes when no real request is around
	metrics              *DevshardMetrics
	onEscrowMissing      func() // called (at most once per request) when a host reports escrow not found
	onBalanceExhausted   func() // called (once) when local state hits insufficient balance
	balanceExhaustedOnce sync.Once
	picker               *sessionPicker
	participantLimiter   *ParticipantRequestLimiter
}

// ErrAllHostsExcluded is returned by prepareInflight when the request
// has already tried every distinct participant in the escrow. The
// caller (RunInference or startAdditionalInflight) treats it as
// exhaustion: no further attempts are scheduled, existing in-flight
// attempts finish naturally. "Distinct participant" matters when one
// participant occupies multiple group slots -- they are counted once.
var ErrAllHostsExcluded = errors.New("redundancy: request has tried every host in escrow")

// ErrNoAvailableHost is returned by prepareInflight when the picker
// drops the request because every currently-available (non-PoC) host
// is in its exclude set. Distinct from ErrAllHostsExcluded: that one
// fires when the request has already tried every slot in the group;
// this one fires when slots it has not tried are temporarily
// unusable (PoC-required) and the picker chose not to wait.
//
// Treated identically by callers: redundancy stops scheduling more
// attempts, lets existing in-flights finish, and surfaces this error
// to the user only when there is no other attempt to wait on.
var ErrNoAvailableHost = errors.New("redundancy: no currently-available host outside the request's exclude set")

func NewRedundancy(session *user.Session, perf *PerfTracker, groupSize int, model string) *Redundancy {
	return NewRedundancyWithThrottle(session, perf, groupSize, model, nil)
}

// NewRedundancyWithThrottle is the production constructor that wires
// in the reactive-throttle checker so the picker can short-circuit a
// throttled host's next nonce as a no-send ghost probe (see
// session_picker.go branch 1b). Tests that don't care about throttle
// behavior can use NewRedundancy and the picker treats every host as
// non-throttled (everything flows through real dispatch + the
// transport-layer admission gate as before).
func NewRedundancyWithThrottle(session *user.Session, perf *PerfTracker, groupSize int, model string, throttleBlocked func(participantKey string) bool) *Redundancy {
	e := &Redundancy{
		session:   session,
		perf:      perf,
		groupSize: groupSize,
		model:     model,
	}
	e.picker = newSessionPicker(session, model, e.runGhostProbe, throttleBlocked)
	e.picker.start()
	return e
}

// Stop terminates the dispatcher goroutine. Production callers do not
// invoke this (process lifetime). Tests should defer it for clean
// teardown.
func (e *Redundancy) Stop() {
	if e == nil || e.picker == nil {
		return
	}
	e.picker.stop()
}

func (e *Redundancy) Decide(primaryHostIdx int, inputTokens uint64) Decision {
	secondaryHostIdx := (primaryHostIdx + 1) % e.groupSize
	primaryParticipant := e.participantKeyForHost(primaryHostIdx)
	secondaryParticipant := e.participantKeyForHost(secondaryHostIdx)

	// Rule 1: primary is known unresponsive → immediate parallel
	if e.perf.IsUnresponsiveParticipant(primaryParticipant) {
		return Decision{RunSecondary: true, Delay: 0, Reason: "primary_unresponsive"}
	}

	// Rule 2: secondary is >50% faster → immediate parallel
	primaryEst := e.perf.EstimatedTimeMsForParticipant(primaryParticipant, inputTokens)
	secondaryEst := e.perf.EstimatedTimeMsForParticipant(secondaryParticipant, inputTokens)
	if primaryEst > 0 && secondaryEst > 0 && secondaryEst < primaryEst*(1-ParallelAdvantageThreshold) {
		return Decision{RunSecondary: true, Delay: 0, Reason: "secondary_faster"}
	}

	// Rule 3: default — start secondary after the request-sized receipt timeout.
	return Decision{RunSecondary: true, Delay: receiptTimeoutForInput(inputTokens), Reason: "receipt_timeout"}
}

func (e *Redundancy) participantKeyForHost(hostIdx int) string {
	if e != nil && e.session != nil {
		if key := e.session.HostParticipantKey(hostIdx); key != "" {
			return key
		}
	}
	return legacyHostPerfKey(hostIdx)
}

// inflight tracks one in-flight inference and its timing.
type inflight struct {
	prepared  *user.PreparedInference
	hostIdx   int
	hostID    string
	nonce     uint64
	escrowID  string
	sendTime  time.Time
	escalated bool
	probe     bool

	receiptOnce sync.Once
	receiptTime time.Time
	receiptCh   chan struct{} // closed when receipt arrives

	tokenOnce       sync.Once
	firstToken      time.Time
	firstTokenCh    chan struct{}
	outputChunks    atomic.Int64
	contentChunks   atomic.Int64
	outputBytes     atomic.Int64
	lastChunkAt     atomic.Int64
	forwardedLog    sync.Once
	suppressedLog   sync.Once
	ctxCancelledLog sync.Once
	sampleOnce      sync.Once
	processOnce     sync.Once
	processErr      error

	// pendingBuf holds bytes received before any content event was observed.
	// Each attempt has at most one writer goroutine driving Write/Flush, so no
	// mutex is required. The buffer is flushed in order to the race group
	// writer when this attempt becomes the winner; it is discarded if a
	// different attempt wins or the attempt ends with no content.
	pendingBuf []byte

	// contentSource labels the field that produced the first content event
	// ("delta.content", "delta.reasoning_content", "delta.tool_calls", or the
	// streaming-only convertible shape "message.content"). Set exactly once
	// when sseChunkContentSource* first returns true. Empty string means no
	// accepted content was ever observed.
	contentSource string

	resp *host.HostResponse
	err  error
	done chan struct{}

	// cancel unwinds the per-attempt context used by SendOnly. The background
	// finalizer invokes it on losers that are still running SecondaryWaitAfterWinner
	// after the winner has settled, so their transport goroutines return
	// promptly and HandleTimeout can run against the abandoned nonce.
	cancel context.CancelFunc
}

// raceGroup arbitrates which inflight's stream is forwarded to the client.
type raceGroup struct {
	mu             sync.Mutex
	clientMu       sync.Mutex
	winner         uint64 // 0 = undecided
	winnerCh       chan struct{}
	w              io.Writer
	decided        atomic.Bool
	clientDetached atomic.Bool
	logCtx         context.Context
	writeCtx       context.Context
	escrow         string
}

func newRaceGroup(logCtx, writeCtx context.Context, escrow string, w io.Writer) *raceGroup {
	return &raceGroup{
		winnerCh: make(chan struct{}),
		logCtx:   logCtx,
		writeCtx: writeCtx,
		escrow:   escrow,
		w:        w,
	}
}

func (rg *raceGroup) setWinner(nonce uint64) {
	rg.mu.Lock()
	defer rg.mu.Unlock()
	if rg.winner == 0 {
		rg.winner = nonce
		rg.decided.Store(true)
		close(rg.winnerCh)
		logInferenceStage(rg.logCtx, rg.escrow, nonce, "winner_selected")
	}
}

func (rg *raceGroup) hasDecided() bool {
	return rg.decided.Load()
}

func (rg *raceGroup) winnerNonce() uint64 {
	rg.mu.Lock()
	defer rg.mu.Unlock()
	return rg.winner
}

func (rg *raceGroup) winnerSignal() <-chan struct{} {
	if rg == nil {
		return nil
	}
	return rg.winnerCh
}

func (rg *raceGroup) detachClient() {
	if rg != nil {
		rg.clientMu.Lock()
		defer rg.clientMu.Unlock()
		rg.clientDetached.Store(true)
	}
}

func (rg *raceGroup) isClientDetached() bool {
	return rg != nil && rg.clientDetached.Load()
}

// raceWriter is an io.Writer that only forwards writes from the winning nonce.
type raceWriter struct {
	group *raceGroup
	nonce uint64
	inf   *inflight
}

func (rw *raceWriter) ctxErr() error {
	if rw.group == nil || rw.group.writeCtx == nil {
		return nil
	}
	return rw.group.writeCtx.Err()
}

func (rw *raceWriter) Write(p []byte) (int, error) {
	now := time.Now()
	rw.inf.tokenOnce.Do(func() {
		rw.inf.firstToken = now
		if rw.inf.firstTokenCh != nil {
			close(rw.inf.firstTokenCh)
		}
	})
	rw.inf.outputChunks.Add(1)
	rw.inf.outputBytes.Add(int64(len(p)))
	rw.inf.lastChunkAt.Store(now.UnixNano())

	// Detect whether this Write contains the first content-bearing event for
	// this attempt. Only content events promote a nonce to winner; role-only
	// chunks and [DONE] markers do not. Probes never produce winner content.
	hadContentBefore := rw.inf.contentChunks.Load() > 0
	var chunkHasContent bool
	if !rw.inf.probe {
		if src, ok := sseChunkContentSource(p); ok {
			chunkHasContent = true
			if rw.inf.contentSource == "" {
				rw.inf.contentSource = src
			}
		}
	}
	if chunkHasContent {
		rw.inf.contentChunks.Add(1)
		rw.group.setWinner(rw.nonce)
	}

	rw.group.mu.Lock()
	isWinner := rw.group.winner == rw.nonce
	winnerNonce := rw.group.winner
	rw.group.mu.Unlock()

	if rw.inf.firstToken.Equal(now) {
		route := "loser"
		if isWinner {
			route = "winner"
		} else if rw.inf.probe {
			route = "probe"
		} else if winnerNonce == 0 {
			route = "pending"
		}
		logInferenceStage(rw.group.logCtx, rw.inf.escrowID, rw.nonce, "first_token", "host", rw.inf.hostID, "route", route, "winner_nonce", winnerNonce)
	}

	if rw.inf.probe {
		rw.inf.suppressedLog.Do(func() {
			logInferenceStage(rw.group.logCtx, rw.inf.escrowID, rw.nonce, "poc_probe_stream_suppressed", "host", rw.inf.hostID, "winner_nonce", winnerNonce, "poc_reason", currentPoCPhaseReason())
		})
		return len(p), nil
	}

	switch {
	case isWinner:
		rw.group.clientMu.Lock()
		defer rw.group.clientMu.Unlock()
		if rw.group.isClientDetached() {
			rw.inf.pendingBuf = nil
			return len(p), nil
		}
		if err := rw.ctxErr(); err != nil {
			rw.inf.pendingBuf = nil
			rw.inf.ctxCancelledLog.Do(func() {
				logInferenceStage(rw.group.logCtx, rw.inf.escrowID, rw.nonce, "winner_write_ctx_cancelled",
					"host", rw.inf.hostID,
					"output_chunks", rw.inf.outputChunks.Load(),
					"content_chunks", rw.inf.contentChunks.Load(),
					"output_bytes", rw.inf.outputBytes.Load(),
					"where", "write",
					"error", err,
				)
			})
			return 0, err
		}
		rw.inf.forwardedLog.Do(func() {
			logInferenceStage(rw.group.logCtx, rw.inf.escrowID, rw.nonce, "stream_forwarding_started", "host", rw.inf.hostID)
		})
		// On first content for the winner, flush any buffered pre-content
		// bytes (role chunk, etc.) before the current write so SSE event
		// ordering is preserved end-to-end.
		if !hadContentBefore && len(rw.inf.pendingBuf) > 0 && rw.group.w != nil {
			if _, err := rw.group.w.Write(rw.inf.pendingBuf); err != nil {
				rw.inf.pendingBuf = nil
				return 0, err
			}
		}
		rw.inf.pendingBuf = nil
		if rw.group.w == nil {
			return len(p), nil
		}
		return rw.group.w.Write(p)

	case winnerNonce != 0:
		// Another attempt has already won; suppress this attempt's stream
		// entirely (existing behavior). Discard any buffered pre-content
		// bytes — they will never be forwarded.
		rw.inf.pendingBuf = nil
		rw.inf.suppressedLog.Do(func() {
			logInferenceStage(rw.group.logCtx, rw.inf.escrowID, rw.nonce, "stream_suppressed", "host", rw.inf.hostID, "winner_nonce", winnerNonce)
		})
		return len(p), nil

	default:
		// No winner yet and this attempt has not produced content. Buffer
		// the bytes locally; if this attempt eventually produces content it
		// will become the winner and these bytes will be flushed in order.
		// If the attempt completes with no content at all, the buffer is
		// discarded by startInflight's empty-stream handling.
		rw.inf.pendingBuf = append(rw.inf.pendingBuf, p...)
		return len(p), nil
	}
}

func (rw *raceWriter) Flush() {
	if rw.inf.probe {
		return
	}
	rw.group.mu.Lock()
	isWinner := rw.group.winner == rw.nonce
	rw.group.mu.Unlock()
	if !isWinner {
		return
	}
	rw.group.clientMu.Lock()
	defer rw.group.clientMu.Unlock()
	if rw.group.isClientDetached() {
		return
	}
	if err := rw.ctxErr(); err != nil {
		rw.inf.ctxCancelledLog.Do(func() {
			logInferenceStage(rw.group.logCtx, rw.inf.escrowID, rw.nonce, "winner_write_ctx_cancelled",
				"host", rw.inf.hostID,
				"output_chunks", rw.inf.outputChunks.Load(),
				"content_chunks", rw.inf.contentChunks.Load(),
				"output_bytes", rw.inf.outputBytes.Load(),
				"where", "flush",
				"error", err,
			)
		})
		return
	}
	if f, ok := rw.group.w.(http.Flusher); ok {
		f.Flush()
	}
}

// RunInference prepares and sends an inference, optionally racing a secondary.
// It replaces the old retry-based runInference in proxy.go.
func (e *Redundancy) RunInference(ctx context.Context, params user.InferenceParams, w io.Writer) error {
	ctx, _ = ensureRequestLogContext(ctx)
	settleCtx, _ := ensureRequestLogContext(context.Background())
	settleCtx = logging.PropagateRequestID(settleCtx, ctx)
	logRequestStage(ctx, "runner_started", "escrow", e.devshardID, "input_tokens", params.InputLength, "model", params.Model)

	// triedParticipants is the per-request memory the picker uses to
	// avoid re-dispatching to a participant this request has already
	// tried. Keyed by participant identity (NOT slot index) so that
	// participants occupying multiple group slots count as one --
	// otherwise a request could be retried against the same physical
	// host through sibling slots, which is exactly what the picker
	// exists to prevent. Populated synchronously after each successful
	// prepareInflight; mutated by startAdditionalInflight (called from
	// awaitRace in the same goroutine), so no synchronisation needed.
	triedParticipants := map[string]bool{}

	primary, err := e.prepareInflight(ctx, params, triedParticipants)
	if err != nil {
		logRequestStage(ctx, "runner_prepare_failed", "escrow", e.devshardID, "error", err)
		if errors.Is(err, types.ErrInsufficientBalance) {
			e.fireBalanceExhausted()
		}
		return err
	}
	triedParticipants[e.session.HostParticipantKey(primary.hostIdx)] = true

	decision := e.Decide(primary.hostIdx, params.InputLength)
	maxAttempts := e.maxAttempts()
	if e.metrics != nil {
		e.metrics.RecordSpeculativeDecision(decision.Reason)
	}
	logInferenceStage(ctx, primary.escrowID, primary.nonce, "decision_made",
		"host", primary.hostID,
		"decision", decision.Reason,
		"delay_ms", decision.Delay.Milliseconds(),
		"max_attempts", maxAttempts,
		"group_size", e.groupSize,
	)
	race := newRaceGroup(settleCtx, ctx, e.devshardID, w)
	attempts := []*inflight{primary}

	// Always start the primary.
	e.startInflight(settleCtx, primary, race)

	if decision.RunSecondary && decision.Delay == 0 && len(attempts) < maxAttempts {
		logRequestStage(ctx, "secondary_immediate_start", "escrow", e.devshardID, "decision", decision.Reason)
		primary.escalated = true
		if secondary := e.startAdditionalInflight(ctx, settleCtx, race, params, "secondary_immediate_start", primary, decision.Reason, triedParticipants); secondary != nil {
			attempts = append(attempts, secondary)
		}
	} else if decision.RunSecondary && decision.Delay == 0 {
		logInferenceStage(ctx, primary.escrowID, primary.nonce, "secondary_immediate_skipped",
			"host", primary.hostID,
			"reason", "attempt_limit",
			"decision", decision.Reason,
			"current_attempts", len(attempts),
			"max_attempts", maxAttempts,
		)
	}

	return e.awaitRace(ctx, settleCtx, attempts, race, params, decision, triedParticipants)
}

// prepareInflight enqueues a request with the session picker and waits
// for a nonce to be assigned. exclude is the set of participant keys
// this request has already tried; the picker matches the request to a
// nonce whose host's participant is NOT in exclude. There are two
// exhaustion paths:
//
//   - ErrAllHostsExcluded (synchronous): the request has already tried
//     every distinct participant in the group. No need to wake the
//     picker. We compare against the unique-participant count rather
//     than groupSize because a single participant can hold multiple
//     slots; using groupSize here would let us submit doomed requests.
//   - ErrNoAvailableHost (from picker): some not-yet-tried participants
//     exist but they are all PoC-required right now. The picker drops
//     the request immediately rather than queueing it indefinitely.
//
// The picker -- not this function -- decides whether the dispatch is a
// real inference or a PoC-style probe-burn. The probe flag flows back
// through pickerResult.isProbe and is recorded on the inflight so the
// rest of the lifecycle (raceWriter, perf tracking, escalation) can
// react accordingly.
func (e *Redundancy) prepareInflight(ctx context.Context, params user.InferenceParams, exclude map[string]bool) (*inflight, error) {
	if len(exclude) >= len(e.session.ParticipantKeys()) {
		return nil, ErrAllHostsExcluded
	}
	req := &pickerRequest{
		params:              params,
		excludeParticipants: exclude,
		ctx:                 ctx,
		submitTime:          time.Now(),
		reply:               make(chan pickerResult, 1),
	}
	e.picker.submit(req)

	select {
	case <-ctx.Done():
		// Abandon the reply channel; the picker will write into its
		// buffered slot and the result will be GC'd.
		return nil, ctx.Err()
	case res := <-req.reply:
		if res.err != nil {
			// Exhaustion sentinels are surfaced unwrapped so callers
			// can errors.Is() against them. Other errors are wrapped
			// for diagnostic context.
			if errors.Is(res.err, ErrNoAvailableHost) || errors.Is(res.err, ErrAllHostsExcluded) {
				return nil, res.err
			}
			return nil, fmt.Errorf("prepare: %w", res.err)
		}
		return &inflight{
			prepared:     res.prepared,
			hostIdx:      res.prepared.HostIdx(),
			hostID:       e.session.HostLabel(res.prepared.HostIdx()),
			nonce:        res.prepared.Nonce(),
			escrowID:     e.devshardID,
			probe:        res.isProbe,
			done:         make(chan struct{}),
			receiptCh:    make(chan struct{}),
			firstTokenCh: make(chan struct{}),
		}, nil
	}
}

func (e *Redundancy) startInflight(ctx context.Context, inf *inflight, race *raceGroup) {
	// Per-attempt context derived from the settle context so the background
	// finalizer can cut off stragglers after the winner's grace window expires
	// without disturbing the settle context itself (which is shared across all
	// attempts). The cancel is called on the send goroutine's exit path as a
	// no-op after natural completion; explicit invocation from the finalizer
	// is what unwinds SendOnly for speculative losers that outlived the winner.
	attemptCtx, cancel := context.WithCancel(ctx)
	inf.cancel = cancel
	rw := &raceWriter{group: race, nonce: inf.nonce, inf: inf}
	receiptHandler := func() {
		inf.receiptOnce.Do(func() {
			inf.receiptTime = time.Now()
			logInferenceStage(ctx, inf.escrowID, inf.nonce, "receipt_received", "host", inf.hostID, "elapsed_ms", inf.receiptTime.Sub(inf.sendTime).Milliseconds())
			close(inf.receiptCh)
		})
	}
	logInferenceStage(ctx, inf.escrowID, inf.nonce, "prepared", "host", inf.hostID)
	if inf.probe {
		logInferenceStage(ctx, inf.escrowID, inf.nonce, "poc_probe_prepared", "host", inf.hostID, "max_tokens", pocProbeMaxTokens, "poc_reason", currentPoCPhaseReason())
	}
	// Stamp sendTime synchronously, BEFORE spawning the send goroutine, so
	// that awaitRace's first iteration is guaranteed to see a non-zero
	// sendTime and schedule the receipt-timeout / first-token escalation.
	// Previously sendTime was assigned inside the goroutine below, which
	// introduced a scheduler race: if awaitRace iterated before the goroutine
	// ran, nextEscalationTrigger skipped this attempt (sendTime IsZero check)
	// and no escalation timer was ever scheduled. The main loop then only
	// woke on doneCh, so a slow or silent primary never got a secondary —
	// producing both tail-latency regressions (receipts that took seconds to
	// arrive) and full stalls (primary goes silent after receipt, first-token
	// fallback never fires). Setting sendTime here makes the invariant hold
	// before awaitRace can observe the attempt.
	inf.sendTime = time.Now()
	go e.monitorInflight(ctx, inf, race)

	go func() {
		defer close(inf.done)
		defer cancel()
		logInferenceStage(ctx, inf.escrowID, inf.nonce, "started", "host", inf.hostID)
		inf.resp, inf.err = e.session.SendOnly(attemptCtx, inf.prepared, rw, receiptHandler)
		streamBytes := int64(0)
		if inf.resp != nil {
			streamBytes = inf.resp.StreamBytesRead
		}
		if inf.err != nil {
			logInferenceStage(ctx, inf.escrowID, inf.nonce, "send_failed",
				"host", inf.hostID,
				"output_chunks", inf.outputChunks.Load(),
				"content_chunks", inf.contentChunks.Load(),
				"output_bytes", inf.outputBytes.Load(),
				"stream_bytes_read", streamBytes,
				"error", inf.err,
			)
			return
		}
		logInferenceStage(ctx, inf.escrowID, inf.nonce, "send_completed",
			"host", inf.hostID,
			"output_chunks", inf.outputChunks.Load(),
			"content_chunks", inf.contentChunks.Load(),
			"output_bytes", inf.outputBytes.Load(),
			"stream_bytes_read", streamBytes,
		)
		// A transport-level success that streamed bytes but produced zero
		// content events indicates the host returned only protocol/SSE
		// boilerplate (role chunk, [DONE]) without any delta content. Treat
		// this as an attempt failure so redundancy can retry on a different
		// host and the bad host gets recorded as non-responsive in the local
		// PerfTracker. We require outputChunks > 0 to avoid penalising
		// transports (e.g. in-process test clients) that never invoke the
		// stream writer at all.
		if !inf.probe && inf.outputChunks.Load() > 0 && inf.contentChunks.Load() == 0 {
			inf.err = errEmptyStream
			// Discard any buffered bytes so they are never flushed if this
			// attempt is later promoted incorrectly.
			inf.pendingBuf = nil
			logInferenceStage(ctx, inf.escrowID, inf.nonce, "empty_stream",
				"host", inf.hostID,
				"output_chunks", inf.outputChunks.Load(),
				"output_bytes", inf.outputBytes.Load(),
				"content_source", inf.contentSource,
			)
		}
	}()
}

// startDelayed waits for receipt or timeout, then starts a secondary if needed.
// Returns nil if receipt arrived before timeout (no secondary needed).
func (e *Redundancy) startAdditionalInflight(streamCtx, settleCtx context.Context, race *raceGroup, params user.InferenceParams, stage string, trigger *inflight, reason string, triedParticipants map[string]bool) *inflight {
	if streamCtx.Err() != nil {
		return nil
	}
	if race.hasDecided() {
		return nil
	}
	fields := []any{"host", trigger.hostID}
	if delay := escalationDelay(stage, params.InputLength); delay > 0 {
		fields = append(fields, "delay_ms", delay.Milliseconds())
	}
	logInferenceStage(settleCtx, trigger.escrowID, trigger.nonce, stage, fields...)
	next, err := e.prepareInflight(streamCtx, params, triedParticipants)
	if err != nil {
		// Distinguish exhaustion from generic prepare failures so the
		// next stress test can measure how often the per-request
		// exclude set actually saturates the escrow. When exhausted,
		// existing in-flight attempts will run to completion and the
		// race will resolve naturally; we just stop scheduling more.
		if errors.Is(err, ErrAllHostsExcluded) || errors.Is(err, ErrNoAvailableHost) {
			// Both exhaustion paths land here: either we have tried
			// every slot or the picker says no untried slot is
			// currently usable. In either case stop scheduling more
			// attempts and let in-flight ones finish naturally.
			logRequestStage(settleCtx, "picker_exhausted",
				"escrow", e.devshardID,
				"decision", reason,
				"tried_participants", len(triedParticipants),
				"unique_participants", len(e.session.ParticipantKeys()),
				"group_size", e.groupSize,
				"reason_err", err.Error(),
			)
			return nil
		}
		logRequestStage(settleCtx, "secondary_prepare_failed", "escrow", e.devshardID, "decision", reason, "error", err)
		return nil
	}
	triedParticipants[e.session.HostParticipantKey(next.hostIdx)] = true
	if e.metrics != nil {
		e.metrics.RecordSpeculativeAttemptStart(reason)
	}
	e.startInflight(settleCtx, next, race)
	return next
}

func firstTokenFallbackDelay(inputTokens uint64) time.Duration {
	delay := time.Duration(inputTokens) * PerInputTokenFirstTokenLag
	if delay < FirstTokenTimeoutCap {
		delay = FirstTokenTimeoutCap
	}
	if inputTokens > 50_000 {
		delay = time.Duration(float64(delay) * (float64(inputTokens) / 50_000.0))
	}
	return delay
}

func receiptTimeoutForInput(inputTokens uint64) time.Duration {
	if inputTokens > 100_000 {
		return ReceiptTimeout * 2
	}
	return ReceiptTimeout
}

func nonStreamingFallbackDelay(inputTokens uint64) time.Duration {
	delay := time.Duration(inputTokens) * PerInputTokenResponseLag
	if delay < NonStreamResponseFloor {
		return NonStreamResponseFloor
	}
	return delay
}

func winnerInterChunkDeadline(inf *inflight) (time.Time, bool) {
	if inf == nil || inf.probe || inflightDone(inf) || InterChunkStallTimeout <= 0 {
		return time.Time{}, false
	}
	if inf.contentChunks.Load() == 0 {
		return time.Time{}, false
	}
	lastChunkAt := inf.lastChunkAt.Load()
	if lastChunkAt <= 0 {
		return time.Time{}, false
	}
	return time.Unix(0, lastChunkAt).Add(InterChunkStallTimeout), true
}

func waitForFirstTokenUntil(ctx context.Context, inf *inflight, deadline time.Time) bool {
	if !inf.firstToken.IsZero() {
		return true
	}
	d := time.Until(deadline)
	if d <= 0 {
		return false
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-inf.firstTokenCh:
		return true
	case <-inf.done:
		return !inf.firstToken.IsZero()
	case <-timer.C:
		return !inf.firstToken.IsZero()
	case <-ctx.Done():
		return false
	}
}

func waitForInflightDoneUntil(ctx context.Context, inf *inflight, deadline time.Time) bool {
	d := time.Until(deadline)
	if d <= 0 {
		select {
		case <-inf.done:
			return true
		default:
			return false
		}
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-inf.done:
		return true
	case <-timer.C:
		select {
		case <-inf.done:
			return true
		default:
			return false
		}
	case <-ctx.Done():
		return false
	}
}

type escalationTrigger struct {
	inf      *inflight
	deadline time.Time
	stage    string
	reason   string
}

// winningInflightTerminalFailure reports whether the race winner's HTTP
// attempt has finished in a state that must surface as a client error
// (transport error, process failure, or chain protocol incomplete for the
// crowned nonce). Caller must ensure inflightDone(inf) and that inf is the
// current race winner (the race writer crowned this nonce after at least one
// accepted content chunk).
func (e *Redundancy) winningInflightTerminalFailure(inf *inflight) (failed bool, err error) {
	if inf == nil || inf.probe {
		return false, nil
	}
	if inf.err != nil {
		return true, inf.err
	}
	if inf.resp == nil {
		return true, fmt.Errorf("inference: winner host returned no response")
	}
	if err := e.processInflightOnce(inf); err != nil {
		return true, err
	}
	nonceFinished := e.session.IsNonceFinished(inf.nonce)
	ok := nonceFinished && !isEmptyStreamAttempt(inf)
	if ok {
		return false, nil
	}
	return true, fmt.Errorf("inference: winner inference incomplete (nonce_finished=%v)", nonceFinished)
}

func (e *Redundancy) awaitRace(streamCtx, settleCtx context.Context, attempts []*inflight, race *raceGroup, params user.InferenceParams, decision Decision, triedParticipants map[string]bool) error {
	doneCh := make(chan *inflight, e.maxAttempts())
	for _, inf := range attempts {
		e.watchInflightDone(inf, doneCh)
	}

	for {
		winner := race.winnerNonce()
		var winnerC <-chan struct{}
		if winner == 0 {
			winnerC = race.winnerSignal()
		}
		// As soon as the winner has fully delivered its stream and committed
		// the chain protocol, return to the caller so the handler can write
		// `[DONE]` and close the connection. Any still-running speculative
		// losers are handed off to a background finalizer that waits up to
		// SecondaryWaitAfterWinner for them to complete naturally; anything
		// still outstanding at that point is cancelled, which triggers the
		// normal failure path (HandleTimeout vote, perf tracking) through
		// finishRaceOutcome.
		if winner != 0 {
			if winning := inflightByNonce(attempts, winner); winning != nil && inflightDone(winning) && inflightFinished(winning) {
				if pending := pendingInflights(attempts); len(pending) > 0 {
					logRequestStage(settleCtx, "request_returned_while_speculation_pending",
						"escrow", e.devshardID,
						"winner_nonce", winner,
						"pending", len(pending),
						"max_wait_ms", SecondaryWaitAfterWinner.Milliseconds(),
						"decision", decision.Reason,
					)
					go e.finishRaceWhenPendingDone(settleCtx, attempts, params, decision, winner, raceFinishOptions{recordFailureSamples: true})
					return nil
				}
			}
		}

		trigger, hasTrigger := nextEscalationTrigger(attempts, params)
		maxAttempts := e.maxAttempts()
		var escalationTimer *time.Timer
		var escalationC <-chan time.Time
		if hasTrigger && winner == 0 && len(attempts) < maxAttempts {
			wait := time.Until(trigger.deadline)
			if wait < 0 {
				wait = 0
			}
			escalationTimer = time.NewTimer(wait)
			escalationC = escalationTimer.C
		} else if hasTrigger && winner == 0 {
			logInferenceStage(settleCtx, trigger.inf.escrowID, trigger.inf.nonce, "escalation_skipped",
				"host", trigger.inf.hostID,
				"stage", trigger.stage,
				"reason", "attempt_limit",
				"current_attempts", len(attempts),
				"max_attempts", maxAttempts,
			)
		}
		var stallTimer *time.Timer
		var stallC <-chan time.Time
		if winner != 0 {
			if winning := inflightByNonce(attempts, winner); winning != nil {
				if deadline, ok := winnerInterChunkDeadline(winning); ok {
					wait := time.Until(deadline)
					if wait < 0 {
						wait = 0
					}
					stallTimer = time.NewTimer(wait)
					stallC = stallTimer.C
				}
			}
		}
		if allInflightsDone(attempts) && escalationC == nil {
			if stallTimer != nil {
				stopTimer(stallTimer)
			}
			return e.finishRaceOutcome(settleCtx, attempts, params, decision, winner, raceFinishOptions{recordFailureSamples: true})
		}

		select {
		case inf := <-doneCh:
			w := race.winnerNonce()
			if w != 0 && inf != nil && inf.nonce == w {
				if failed, err := e.winningInflightTerminalFailure(inf); failed {
					if escalationTimer != nil {
						stopTimer(escalationTimer)
					}
					go e.finishRaceWhenPendingDone(settleCtx, attempts, params, decision, w, raceFinishOptions{
						forceTreatAsFailure:  true,
						recordFailureSamples: true,
					})
					logRequestStage(settleCtx, "winner_failed_after_content", "escrow", e.devshardID, "winner_nonce", w, "error", err)
					return err
				}
			}
		case <-escalationC:
			// Re-validate the trigger at fire time. Because the select does
			// not watch receiptCh / firstTokenCh directly, the attempt's
			// state may have advanced between scheduling the timer and it
			// firing (e.g. receipt arrived 400ms in, timer fired at 500ms).
			// In that case the ORIGINAL stage is stale — nextEscalationTrigger
			// would now return a later-stage trigger (or no trigger at all).
			// Escalating on stale info starts unnecessary secondaries: after
			// moving sendTime into the synchronous path this would affect
			// every primary that receipts-in under ReceiptTimeout, i.e. the
			// majority of a healthy run. Skip and let the loop re-schedule
			// the correct next trigger.
			current, stillValid := escalationForInflight(trigger.inf, params)
			if !stillValid || current.stage != trigger.stage {
				break
			}
			trigger.inf.escalated = true
			if len(attempts) < maxAttempts {
				if next := e.startAdditionalInflight(streamCtx, settleCtx, race, params, trigger.stage, trigger.inf, trigger.reason, triedParticipants); next != nil {
					attempts = append(attempts, next)
					e.watchInflightDone(next, doneCh)
				}
			}
		case <-stallC:
			w := race.winnerNonce()
			winning := inflightByNonce(attempts, w)
			deadline, stalled := winnerInterChunkDeadline(winning)
			if !stalled || time.Now().Before(deadline) {
				break
			}
			sinceLastChunk := int64(0)
			if winning != nil {
				lastChunkAt := winning.lastChunkAt.Load()
				if lastChunkAt > 0 {
					sinceLastChunk = time.Since(time.Unix(0, lastChunkAt)).Milliseconds()
				}
			}
			race.detachClient()
			err := fmt.Errorf("inference: winner stalled waiting for next chunk after %s", InterChunkStallTimeout)
			go e.finishStalledWinnerAfterClientTimeout(settleCtx, attempts, params, decision, w)
			logRequestStage(settleCtx, "winner_stalled_after_content",
				"escrow", e.devshardID,
				"winner_nonce", w,
				"stall_timeout_ms", InterChunkStallTimeout.Milliseconds(),
				"since_last_chunk_ms", sinceLastChunk,
				"background_wait_ms", SecondaryWaitAfterWinner.Milliseconds(),
				"error", err,
			)
			return err
		case <-winnerC:
		case <-streamCtx.Done():
			if escalationTimer != nil {
				stopTimer(escalationTimer)
			}
			if stallTimer != nil {
				stopTimer(stallTimer)
			}
			pending := pendingInflights(attempts)
			logRequestStage(settleCtx, "request_stream_canceled", "escrow", e.devshardID, "winner_nonce", winner, "pending", len(pending), "decision", decision.Reason, "error", streamCtx.Err())
			go e.finishRaceWhenPendingDone(settleCtx, attempts, params, decision, winner, raceFinishOptions{})
			return streamCtx.Err()
		}

		if escalationTimer != nil {
			stopTimer(escalationTimer)
		}
		if stallTimer != nil {
			stopTimer(stallTimer)
		}
	}
}

func (e *Redundancy) watchInflightDone(inf *inflight, doneCh chan<- *inflight) {
	go func() {
		<-inf.done
		doneCh <- inf
	}()
}

func nextEscalationTrigger(attempts []*inflight, params user.InferenceParams) (escalationTrigger, bool) {
	var (
		chosen escalationTrigger
		ok     bool
	)
	for _, inf := range attempts {
		trigger, candidate := escalationForInflight(inf, params)
		if !candidate {
			continue
		}
		if !ok || trigger.deadline.Before(chosen.deadline) {
			chosen = trigger
			ok = true
		}
	}
	return chosen, ok
}

func escalationForInflight(inf *inflight, params user.InferenceParams) (escalationTrigger, bool) {
	if inf == nil || inf.escalated {
		return escalationTrigger{}, false
	}
	if inf.probe {
		return escalationTrigger{
			inf:      inf,
			deadline: time.Now(),
			stage:    "poc_probe_immediate_escalation",
			reason:   "poc_probe",
		}, true
	}
	if inflightDone(inf) {
		if inflightFinished(inf) {
			return escalationTrigger{}, false
		}
		return escalationTrigger{
			inf:      inf,
			deadline: time.Now(),
			stage:    "attempt_failed",
			reason:   "attempt_failed",
		}, true
	}
	if inf.sendTime.IsZero() {
		return escalationTrigger{}, false
	}
	if inf.receiptTime.IsZero() {
		return escalationTrigger{
			inf:      inf,
			deadline: inf.sendTime.Add(receiptTimeoutForInput(params.InputLength)),
			stage:    "receipt_timeout_wait_elapsed",
			reason:   "receipt_timeout",
		}, true
	}
	if !params.Stream {
		return escalationTrigger{
			inf:      inf,
			deadline: inf.sendTime.Add(nonStreamingFallbackDelay(params.InputLength)),
			stage:    "response_timeout_wait_elapsed",
			reason:   "response_timeout",
		}, true
	}
	if !inf.firstToken.IsZero() {
		return escalationTrigger{}, false
	}
	return escalationTrigger{
		inf:      inf,
		deadline: inf.sendTime.Add(firstTokenFallbackDelay(params.InputLength)),
		stage:    "first_token_timeout_wait_elapsed",
		reason:   "first_token_timeout",
	}, true
}

func escalationDelay(stage string, inputTokens uint64) time.Duration {
	switch stage {
	case "receipt_timeout_wait_elapsed":
		return receiptTimeoutForInput(inputTokens)
	case "first_token_timeout_wait_elapsed":
		return firstTokenFallbackDelay(inputTokens)
	case "response_timeout_wait_elapsed":
		return nonStreamingFallbackDelay(inputTokens)
	case "attempt_failed":
		return 0
	default:
		return 0
	}
}

func (e *Redundancy) monitorInflight(ctx context.Context, inf *inflight, race *raceGroup) {
	ticker := time.NewTicker(LogHeartbeatInterval)
	defer ticker.Stop()

	for {
		select {
		case <-inf.done:
			return
		case <-ticker.C:
			if inf.sendTime.IsZero() {
				continue
			}
			stage := "waiting_for_receipt"
			fields := []any{
				"host", inf.hostID,
				"elapsed_ms", time.Since(inf.sendTime).Milliseconds(),
				"output_chunks", inf.outputChunks.Load(),
			}
			if !inf.receiptTime.IsZero() {
				stage = "waiting_for_first_token"
				fields = append(fields, "since_receipt_ms", time.Since(inf.receiptTime).Milliseconds())
			}
			if !inf.firstToken.IsZero() {
				stage = "streaming_inflight"
				fields = append(fields, "since_first_token_ms", time.Since(inf.firstToken).Milliseconds())
				if lastChunkAt := inf.lastChunkAt.Load(); lastChunkAt > 0 {
					fields = append(fields, "since_last_chunk_ms", time.Since(time.Unix(0, lastChunkAt)).Milliseconds())
				}
				winnerNonce := race.winnerNonce()
				role := "unknown"
				if winnerNonce == inf.nonce {
					role = "winner"
				} else if winnerNonce != 0 {
					role = "loser"
				}
				fields = append(fields, "role", role, "winner_nonce", winnerNonce)
			}
			logInferenceStage(ctx, inf.escrowID, inf.nonce, stage, fields...)
		case <-ctx.Done():
			return
		}
	}
}

type raceFinishOptions struct {
	forceTreatAsFailure  bool
	recordFailureSamples bool
}

func (e *Redundancy) finishRaceWhenPendingDone(ctx context.Context, attempts []*inflight, params user.InferenceParams, decision Decision, winnerNonce uint64, opts raceFinishOptions) {
	bgCtx, _ := ensureRequestLogContext(context.Background())
	bgCtx = logging.PropagateRequestID(bgCtx, ctx)

	e.waitForPendingLosers(bgCtx, winnerNonce, attempts)

	if err := e.finishRaceOutcome(bgCtx, attempts, params, decision, winnerNonce, opts); err != nil {
		logRequestStage(bgCtx, "background_race_finalize_failed", "escrow", e.devshardID, "error", err)
	}
}

func (e *Redundancy) finishStalledWinnerAfterClientTimeout(ctx context.Context, attempts []*inflight, params user.InferenceParams, decision Decision, winnerNonce uint64) {
	bgCtx, _ := ensureRequestLogContext(context.Background())
	bgCtx = logging.PropagateRequestID(bgCtx, ctx)

	winner := inflightByNonce(attempts, winnerNonce)
	abandonedWinner := e.waitForClientTimedOutAttempts(bgCtx, winnerNonce, attempts)
	if abandonedWinner {
		e.recordStalledWinnerFailureOnce(winner, params)
		if err := e.finishRaceOutcome(bgCtx, attempts, params, decision, winnerNonce, raceFinishOptions{
			forceTreatAsFailure: true,
		}); err != nil {
			logRequestStage(bgCtx, "background_stalled_winner_finalize_failed", "escrow", e.devshardID, "error", err)
		}
		return
	}

	if winner != nil && winner.err == nil && inflightFinished(winner) {
		logInferenceStage(bgCtx, winner.escrowID, winner.nonce, "winner_completed_after_client_timeout",
			"host", winner.hostID,
			"output_chunks", winner.outputChunks.Load(),
			"content_chunks", winner.contentChunks.Load(),
			"output_bytes", winner.outputBytes.Load(),
		)
	}
	if err := e.finishRaceOutcome(bgCtx, attempts, params, decision, winnerNonce, raceFinishOptions{
		recordFailureSamples: true,
	}); err != nil {
		logRequestStage(bgCtx, "background_stalled_winner_finalize_failed", "escrow", e.devshardID, "error", err)
	}
}

func (e *Redundancy) waitForClientTimedOutAttempts(ctx context.Context, winnerNonce uint64, attempts []*inflight) bool {
	pending := pendingInflights(attempts)
	if len(pending) == 0 {
		return false
	}

	timer := time.NewTimer(SecondaryWaitAfterWinner)
	defer stopTimer(timer)

	naturalDone := make(chan *inflight, len(pending))
	for _, inf := range pending {
		inf := inf
		go func() {
			<-inf.done
			naturalDone <- inf
		}()
	}

	abandonedWinner := false
	remaining := len(pending)
	for remaining > 0 {
		select {
		case <-naturalDone:
			remaining--
		case <-timer.C:
			still := pendingInflights(attempts)
			logRequestStage(ctx, "client_timeout_wait_abandoned",
				"escrow", e.devshardID,
				"winner_nonce", winnerNonce,
				"pending", len(still),
				"wait_ms", SecondaryWaitAfterWinner.Milliseconds(),
			)
			for _, inf := range still {
				reason := "client_timeout_grace_expired"
				stage := "speculative_attempt_canceled"
				if inf.nonce == winnerNonce {
					stage = "stalled_winner_canceled_after_client_timeout"
					abandonedWinner = true
				}
				logInferenceStage(ctx, inf.escrowID, inf.nonce, stage,
					"host", inf.hostID,
					"reason", reason,
				)
				if inf.cancel != nil {
					inf.cancel()
				}
			}
			for remaining > 0 {
				<-naturalDone
				remaining--
			}
			return abandonedWinner
		}
	}
	return false
}

// waitForPendingLosers waits for all not-yet-done attempts to close their done
// channel, giving them at most SecondaryWaitAfterWinner to finish naturally.
// Anything still running at the deadline has its per-attempt context cancelled
// so SendOnly unwinds, and we drain the resulting done signals before
// returning. Callers rely on this drain so finishRaceOutcome sees stable
// inf.resp/inf.err state before invoking ProcessResponse / HandleTimeout.
func (e *Redundancy) waitForPendingLosers(ctx context.Context, winnerNonce uint64, attempts []*inflight) {
	pending := pendingInflights(attempts)
	if len(pending) == 0 {
		return
	}

	timer := time.NewTimer(SecondaryWaitAfterWinner)
	defer stopTimer(timer)

	naturalDone := make(chan *inflight, len(pending))
	for _, inf := range pending {
		inf := inf
		go func() {
			<-inf.done
			naturalDone <- inf
		}()
	}

	remaining := len(pending)
	for remaining > 0 {
		select {
		case <-naturalDone:
			remaining--
		case <-timer.C:
			still := pendingInflights(attempts)
			logRequestStage(ctx, "speculative_wait_abandoned",
				"escrow", e.devshardID,
				"winner_nonce", winnerNonce,
				"pending", len(still),
				"wait_ms", SecondaryWaitAfterWinner.Milliseconds(),
			)
			for _, inf := range still {
				logInferenceStage(ctx, inf.escrowID, inf.nonce, "speculative_attempt_canceled",
					"host", inf.hostID,
					"reason", "winner_grace_expired",
				)
				if inf.cancel != nil {
					inf.cancel()
				}
			}
			// Drain the remaining signals. SendOnly honors ctx cancellation,
			// so these should arrive promptly; the wait is unbounded so a
			// hung transport leaks its own goroutine rather than corrupting
			// finalization with a concurrent write to inf.resp/inf.err.
			for remaining > 0 {
				<-naturalDone
				remaining--
			}
			return
		}
	}
}

func pendingInflights(attempts []*inflight) []*inflight {
	var pending []*inflight
	for _, inf := range attempts {
		select {
		case <-inf.done:
		default:
			pending = append(pending, inf)
		}
	}
	return pending
}

func allInflightsDone(attempts []*inflight) bool {
	for _, inf := range attempts {
		if !inflightDone(inf) {
			return false
		}
	}
	return true
}

func inflightDone(inf *inflight) bool {
	select {
	case <-inf.done:
		return true
	default:
		return false
	}
}

// shouldRunHandleTimeout reports whether HandleTimeout should be invoked for a
// failed attempt. Empty-stream attempts post MsgFinishInference at the
// protocol layer despite returning no content; firing a timeout vote against
// such a nonce would conflict with the chain state, so we only run timeout
// handling when the protocol layer agrees the nonce is unfinished.
func shouldRunHandleTimeout(inf *inflight, session *user.Session) bool {
	if inf == nil || session == nil {
		return false
	}
	if inf.probe {
		return false
	}
	return !session.IsNonceFinished(inf.nonce)
}

// inflightFinished checks the raw response for MsgFinishInference.
// Used during the race loop before ProcessResponse has been called.
// Non-probe attempts that completed the chain protocol but produced no
// content (empty SSE, or stalled with no first-token) are treated as
// failed so redundancy can retry on a different host.
func inflightFinished(inf *inflight) bool {
	if inf.err != nil || inf.resp == nil {
		return false
	}
	if isEmptyStreamAttempt(inf) {
		return false
	}
	return user.HasMsgFinish(inf.resp.Mempool, inf.nonce)
}

// isEmptyStreamAttempt reports whether a non-probe attempt that confirmed
// receipt failed to deliver any content. This covers two patterns:
//
//   - Empty SSE: bytes were streamed but no content events parsed (role
//     marker + [DONE] only). Caught by contentChunks == 0.
//   - Stall: receipt came back fast, then the host went silent for the
//     full deadline before completing the chain protocol with zero output.
//     Same condition: contentChunks == 0.
//
// We gate on receiptTime being set so attempts that never even confirmed
// receipt fall through to the upstream error/timeout path instead.
func isEmptyStreamAttempt(inf *inflight) bool {
	if inf == nil || inf.probe {
		return false
	}
	if inf.receiptTime.IsZero() {
		return false
	}
	return inf.contentChunks.Load() == 0
}

func inflightByNonce(attempts []*inflight, nonce uint64) *inflight {
	for _, inf := range attempts {
		if inf.nonce == nonce {
			return inf
		}
	}
	return nil
}

func (e *Redundancy) recordSampleOnce(inf *inflight, params user.InferenceParams) {
	inf.sampleOnce.Do(func() {
		e.recordSample(inf, params)
	})
}

func (e *Redundancy) recordStartedAttemptSamples(attempts []*inflight, params user.InferenceParams) {
	for _, inf := range attempts {
		if inf == nil || inf.probe || inf.sendTime.IsZero() {
			continue
		}
		e.recordSampleOnce(inf, params)
	}
}

func (e *Redundancy) recordStalledWinnerFailureOnce(inf *inflight, params user.InferenceParams) {
	if inf == nil {
		return
	}
	inf.sampleOnce.Do(func() {
		participantKey := e.participantKeyForHost(inf.hostIdx)
		sample := RequestSample{
			HostIdx:        inf.hostIdx,
			ParticipantKey: participantKey,
			Responsive:     false,
			SendTime:       inf.sendTime,
			ReceiptTime:    inf.receiptTime,
			FirstToken:     inf.firstToken,
			InputTokens:    params.InputLength,
		}
		if !inf.sendTime.IsZero() {
			sample.TotalTime = time.Since(inf.sendTime)
		}
		e.perf.Record(sample)
		if e.participantLimiter != nil && e.perf.ParticipantFailureThresholdExceeded(participantKey) {
			e.participantLimiter.ObserveStalledWinner(participantKey)
		}
		if e.metrics != nil {
			e.metrics.ObserveRequestSample(e.devshardID, sample)
		}
	})
}

func (e *Redundancy) processInflightOnce(inf *inflight) error {
	inf.processOnce.Do(func() {
		if inf.resp == nil {
			return
		}
		inf.processErr = e.session.ProcessResponse(inf.hostIdx, inf.resp, inf.nonce)
	})
	return inf.processErr
}

// finishRaceOutcome aggregates attempt outcomes and returns a user-visible
// error when no non-probe attempt fully succeeded. When forceTreatAsFailure
// is true (winner failed after content while other inflights were still
// running), the request is always settled as a failure even if another
// attempt later completes successfully on the protocol layer.
func (e *Redundancy) finishRaceOutcome(ctx context.Context, attempts []*inflight, params user.InferenceParams, decision Decision, winnerNonce uint64, opts raceFinishOptions) error {
	// Process all responses first so Session has complete protocol state.
	for _, inf := range attempts {
		if err := e.processInflightOnce(inf); err != nil {
			logInferenceStage(ctx, inf.escrowID, inf.nonce, "process_response_failed", "host", inf.hostID, "error", err)
		}
	}

	winnerNonce = e.resolvedWinnerNonce(attempts, winnerNonce)
	var winnerIdx int
	if len(attempts) > 0 {
		winnerIdx = attempts[0].hostIdx
	}
	if winner := inflightByNonce(attempts, winnerNonce); winner != nil {
		winnerIdx = winner.hostIdx
	}

	var (
		anySucceeded bool
		failed       []*inflight
	)
	for _, inf := range attempts {
		nonceFinished := e.session.IsNonceFinished(inf.nonce)
		// A successful attempt must finalise the protocol nonce AND must
		// not be an empty stream (streamed bytes with no content). Attempts
		// that never streamed at all (e.g. in-process clients) still count
		// as successful purely on the protocol-level finish.
		ok := nonceFinished && !isEmptyStreamAttempt(inf)
		if !inf.probe {
			anySucceeded = anySucceeded || ok
		}
		streamBytes := int64(0)
		if inf.resp != nil {
			streamBytes = inf.resp.StreamBytesRead
		}
		var confirmedAt int64
		var hasReceipt bool
		if inf.resp != nil {
			confirmedAt = inf.resp.ConfirmedAt
			hasReceipt = len(inf.resp.Receipt) > 0
		}
		fields := []any{
			"host", inf.hostID,
			"winner", inf.nonce == winnerNonce,
			"finished", ok,
			"responsive", confirmedAt > 0,
			"has_receipt", hasReceipt,
			"confirmed_at", confirmedAt,
			"output_chunks", inf.outputChunks.Load(),
			"content_chunks", inf.contentChunks.Load(),
			"output_bytes", inf.outputBytes.Load(),
			"stream_bytes_read", streamBytes,
			"content_source", inf.contentSource,
			"probe", inf.probe,
		}
		logInferenceStage(ctx, inf.escrowID, inf.nonce, "race_completed", fields...)
		if !ok {
			failed = append(failed, inf)
		}
	}
	effectiveSuccess := anySucceeded && !opts.forceTreatAsFailure
	if !effectiveSuccess {
		if opts.recordFailureSamples {
			e.recordStartedAttemptSamples(attempts, params)
		}
		for _, inf := range failed {
			if inf.probe {
				logInferenceStage(ctx, inf.escrowID, inf.nonce, "poc_probe_failed_no_timeout", "host", inf.hostID, "poc_reason", currentPoCPhaseReason())
				continue
			}
			if !shouldRunHandleTimeout(inf, e.session) {
				logInferenceStage(ctx, inf.escrowID, inf.nonce, "timeout_skipped",
					"host", inf.hostID, "reason", "nonce_already_finished")
				continue
			}
			payload := &host.InferencePayload{
				Prompt:      params.Prompt,
				Model:       params.Model,
				InputLength: params.InputLength,
				MaxTokens:   params.MaxTokens,
				StartedAt:   params.StartedAt,
			}
			result, err := e.session.HandleTimeout(ctx, inf.nonce, inf.sendTime, payload)
			if result.Reason != "" && e.metrics != nil {
				e.metrics.RecordInferenceTimeout(result.Reason)
			}
			if err != nil {
				logInferenceStage(ctx, inf.escrowID, inf.nonce, "timeout_failed", "host", inf.hostID, "error", err)
			}
		}
		errMsg := "inference: no non-probe attempt finished"
		if opts.forceTreatAsFailure && anySucceeded {
			errMsg = "inference: winner failed after streaming started (alternate completion ignored)"
		}
		logRequestStage(ctx, "request_failed", "escrow", e.devshardID, "error", errMsg)
		e.logRequestSettled(ctx, 0, decision, "failed")
		e.checkEscrowMissing(ctx, attempts)
		return fmt.Errorf("%s", errMsg)
	}

	var involvement []HostInvolvement
	for _, inf := range attempts {
		if inf.probe {
			continue
		}
		e.recordSampleOnce(inf, params)
		involvement = append(involvement, e.buildInvolvement(inf, winnerNonce))
	}
	e.perf.RecordRequest(RequestRecord{
		Timestamp:     time.Now(),
		InputTokens:   params.InputLength,
		WinnerHostIdx: winnerIdx,
		WinnerNonce:   winnerNonce,
		Decision:      decision.Reason,
		Hosts:         involvement,
	})
	if len(failed) > 0 {
		payload := &host.InferencePayload{
			Prompt:      params.Prompt,
			Model:       params.Model,
			InputLength: params.InputLength,
			MaxTokens:   params.MaxTokens,
			StartedAt:   params.StartedAt,
		}
		if anySucceeded {
			go func() {
				bgCtx, _ := ensureRequestLogContext(context.Background())
				bgCtx = logging.PropagateRequestID(bgCtx, ctx)
				for _, inf := range failed {
					if inf.probe {
						logInferenceStage(bgCtx, inf.escrowID, inf.nonce, "poc_probe_failed_no_timeout", "host", inf.hostID, "poc_reason", currentPoCPhaseReason())
						continue
					}
					if !shouldRunHandleTimeout(inf, e.session) {
						logInferenceStage(bgCtx, inf.escrowID, inf.nonce, "timeout_skipped",
							"host", inf.hostID, "reason", "nonce_already_finished")
						continue
					}
					result, err := e.session.HandleTimeout(bgCtx, inf.nonce, inf.sendTime, payload)
					if result.Reason != "" && e.metrics != nil {
						e.metrics.RecordInferenceTimeout(result.Reason)
					}
					if err != nil {
						logInferenceStage(bgCtx, inf.escrowID, inf.nonce, "background_timeout_failed", "host", inf.hostID, "error", err)
					}
				}
				e.logRequestSettled(bgCtx, winnerNonce, decision, "success")
			}()
		}
	}

	logRequestStage(ctx, "request_succeeded", "escrow", e.devshardID, "winner_nonce", winnerNonce, "decision", decision.Reason)
	if len(failed) == 0 {
		e.logRequestSettled(ctx, winnerNonce, decision, "success")
	}

	e.checkEscrowMissing(ctx, attempts)

	return nil
}

func (e *Redundancy) maxAttempts() int {
	if e.groupSize <= 0 {
		return 1
	}
	maxSpeculativeAttempts := CurrentMaxSpeculativeAttempts()
	if maxSpeculativeAttempts <= 0 || maxSpeculativeAttempts > e.groupSize {
		return e.groupSize
	}
	return maxSpeculativeAttempts
}

func (e *Redundancy) resolvedWinnerNonce(attempts []*inflight, winnerNonce uint64) uint64 {
	if winnerNonce != 0 {
		return winnerNonce
	}
	for _, inf := range attempts {
		if inf.probe {
			continue
		}
		if e.session.IsNonceFinished(inf.nonce) {
			return inf.nonce
		}
	}
	return 0
}

func stopTimer(t *time.Timer) {
	if t == nil {
		return
	}
	if !t.Stop() {
		select {
		case <-t.C:
		default:
		}
	}
}

func (e *Redundancy) logRequestSettled(ctx context.Context, winnerNonce uint64, decision Decision, outcome string) {
	logRequestStage(ctx, "request_fully_settled",
		"escrow", e.devshardID,
		"winner_nonce", winnerNonce,
		"decision", decision.Reason,
		"outcome", outcome,
	)
}

func (e *Redundancy) buildInvolvement(inf *inflight, winnerNonce uint64) HostInvolvement {
	notEmpty := !isEmptyStreamAttempt(inf)
	hi := HostInvolvement{
		HostIdx:      inf.hostIdx,
		Nonce:        inf.nonce,
		OutputChunks: inf.outputChunks.Load(),
		Responsive:   inf.resp != nil && inf.resp.ConfirmedAt > 0 && notEmpty,
		Finished:     e.session.IsNonceFinished(inf.nonce) && notEmpty,
		Winner:       inf.nonce == winnerNonce,
	}
	if !inf.sendTime.IsZero() {
		if !inf.receiptTime.IsZero() {
			hi.ReceiptTimeMs = float64(inf.receiptTime.Sub(inf.sendTime).Milliseconds())
		}
		if !inf.firstToken.IsZero() {
			hi.FirstTokenMs = float64(inf.firstToken.Sub(inf.sendTime).Milliseconds())
		}
		hi.TotalTimeMs = float64(time.Since(inf.sendTime).Milliseconds())
	}
	return hi
}

func (e *Redundancy) recordSample(inf *inflight, params user.InferenceParams) {
	if inf.probe {
		return
	}
	// An attempt is responsive only if it confirmed receipt AND did not
	// degenerate into an empty stream (bytes streamed with no content). A
	// host that returns an empty stream is recorded as non-responsive so
	// the local PerfTracker routes future requests away from it.
	responsive := inf.resp != nil && inf.resp.ConfirmedAt > 0 && !isEmptyStreamAttempt(inf)
	participantKey := e.participantKeyForHost(inf.hostIdx)
	sample := RequestSample{
		HostIdx:        inf.hostIdx,
		ParticipantKey: participantKey,
		Responsive:     responsive,
		SendTime:       inf.sendTime,
		ReceiptTime:    inf.receiptTime,
		FirstToken:     inf.firstToken,
		InputTokens:    params.InputLength,
	}
	if !inf.sendTime.IsZero() {
		sample.TotalTime = time.Since(inf.sendTime)
	}
	e.perf.Record(sample)
	if e.participantLimiter != nil {
		switch {
		case isEmptyStreamAttempt(inf):
			e.participantLimiter.ObserveEmptyStream(participantKey)
		case e.session.IsNonceFinished(inf.nonce):
			e.participantLimiter.ObserveSuccessfulInference(participantKey)
		}
	}
	if e.metrics != nil {
		e.metrics.ObserveRequestSample(e.devshardID, sample)
	}
}

func probeParams(params user.InferenceParams) user.InferenceParams {
	params.Prompt = pocProbePromptBody
	params.InputLength = uint64(len(pocProbePromptBody))
	params.MaxTokens = pocProbeMaxTokens
	return params
}

// ghostProbeParams returns the params for a synthetic probe that is not
// tied to any user request. The model is taken from the escrow
// registration (passed into NewRedundancy) so the host receives a
// well-formed inference for the configured model.
func ghostProbeParams(model string) user.InferenceParams {
	return probeParams(user.InferenceParams{
		Model:     model,
		StartedAt: time.Now().UnixMilli(),
	})
}

// runGhostProbe records a synthetic probe inference WITHOUT contacting
// the host. The picker invokes this when it must consume a nonce but
// no real request should land on the host (PoC-required, queue
// excluded all available hosts past pickerStaleThreshold, or host is
// reactively throttled). Every kind behaves identically: log + return.
//
// Why silent for every kind:
//
//   - PoC: the host cannot serve user traffic during PoC. We previously
//     sent a tiny inference so the host produced MsgFinishInference
//     for the nonce; that produces the same chain settlement an idle
//     host's own probe would, but at the cost of an HTTP round-trip
//     per burned nonce. Skipping the round-trip removes the per-nonce
//     load on a host that is already busy with PoC stitching.
//
//   - Exclude: the queue had no compatible request for this host
//     after the stale-hold window. Sending a tiny inference settled
//     the chain protocol, but again at HTTP cost. Skipping it leaves
//     the nonce as an orphan MsgStart -- chain-side, other validators
//     may post a timeout vote; we don't.
//
//   - Throttled: the host just 503'd / 429'd and is over capacity.
//     Sending anything would only deepen the overload. This was the
//     original silent path; PoC and Exclude now match it.
//
// Side effects accepted across all kinds:
//
//   - The MsgStart for the burned nonce is composed inside
//     PrepareInferenceFn and lives in s.diffs. It will replay to the
//     host as catch-up on the host's next real dispatch (so the chain
//     view eventually converges). For PoC-required hosts that means a
//     backlog of orphan MsgStarts arriving once PoC ends.
//
//   - We do not post a timeout vote from this node: there is no
//     inflight, so HandleTimeout never runs. Other validators may.
//
//   - PerfTracker is not updated (no attempt happened from our POV).
//
// Liveness: every nonce the session advances through is accounted for
// exactly once -- by a real request via the picker, or by this
// log-only no-op. Without this method the picker would have to dequeue
// a real request and turn IT into a probe, costing that request a turn.
//
// kind is retained on the signature for log-label differentiation only;
// the dispatch path is identical for every kind.
func (e *Redundancy) runGhostProbe(prepared *user.PreparedInference, kind ghostKind, reason string) {
	if prepared == nil || e.session == nil {
		return
	}
	ctx, _ := ensureRequestLogContext(context.Background())
	logInferenceStage(ctx, e.devshardID, prepared.Nonce(), "ghost_probe_skipped",
		"host", e.session.HostLabel(prepared.HostIdx()),
		"kind", int(kind),
		"reason", reason,
		"poc_reason", currentPoCPhaseReason(),
	)
}

// fireBalanceExhausted fires onBalanceExhausted at most once per Redundancy
// lifetime. The callback deactivates the runtime at the gateway level so no
// more requests are routed to this escrow.
func (e *Redundancy) fireBalanceExhausted() {
	if e.onBalanceExhausted == nil {
		return
	}
	e.balanceExhaustedOnce.Do(func() {
		log.Printf("escrow_balance_exhausted escrow=%s", e.devshardID)
		e.onBalanceExhausted()
	})
}

// checkEscrowMissing fires onEscrowMissing if any attempt got "escrow not found"
// from its host. The callback is expected to trigger a verified chain check.
func (e *Redundancy) checkEscrowMissing(ctx context.Context, attempts []*inflight) {
	if e.onEscrowMissing == nil {
		return
	}
	for _, inf := range attempts {
		if inf.err != nil && transport.IsUpstreamEscrowNotFound(inf.err) {
			logRequestStage(ctx, "escrow_not_found_reported_by_host",
				"escrow", e.devshardID, "host", inf.hostID, "nonce", inf.nonce)
			e.onEscrowMissing()
			return
		}
	}
}
