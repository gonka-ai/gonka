// Package devshardctl: session_picker is the request dispatcher that decides
// WHICH queued request consumes each newly available nonce. It exists to
// solve the per-request retry-host-collision problem, where a big share
// of dispatches re-hit a host that the same request had already tried,
// because the picker was a stateless round-robin (nonce % len(group))
// with no per-request memory.
//
// Branches per nonce
// ------------------
//
// Every branch that does NOT dispatch a real user request marks the
// nonce as a silent ghost probe: the MsgStart is composed inside
// PrepareInferenceFn (and lives in s.diffs for catch-up), but the
// dispatcher does not contact the host. The nonce stream advances,
// no HTTP call is made, no vote is posted from this node, no response
// is awaited. The kind is preserved for log labeling only.
//
//	1a. PoC-required host (host needs a probe under relaxed bypass):
//	    fire ghostPoC. The host cannot serve user traffic now.
//
//	1b. Reactively throttled host (limiter bucket below 1 token after
//	    a recent 429/503): fire ghostThrottled. The host just told us
//	    it is overloaded; do not pile on. Queue is untouched.
//
//	2. Healthy host with a queued request that has not already
//	   excluded this host's participant: dispatch it as a real
//	   inference. The matched request is removed from the queue
//	   and replied to. This is the only branch that actually hits
//	   the host.
//
//	3. Healthy host but every queued request has already excluded
//	   this host's participant: hold the nonce up to
//	   pickerStaleThreshold (200ms). If a request that has not
//	   excluded this host arrives in time, dispatch it on the held
//	   nonce. Otherwise fire ghostExclude (silent, same as the
//	   other ghost kinds -- see ghostDispatcher doc) to advance
//	   past this nonce without dequeueing any real request. Queued
//	   requests stay queued and will match the next nonce that
//	   binds to a host they have not excluded.
//
// Per-iteration exhaustion sweep
// ------------------------------
//
// At the top of every dispatch iteration the picker recomputes the set
// of "currently available" hosts -- slots that are neither PoC-required
// nor reactively throttled (limiter bucket drained by a recent 429/503).
// Any queued request whose exclude set covers every available host is
// dropped immediately with ErrNoAvailableHost. This makes exhaustion a
// picker-level decision (not a per-prepareInflight precheck) so dynamic
// transitions are handled naturally: a host flipping into PoC or
// having its bucket drained by a 503 burst may strand a queued request
// that previously had options, and the next iteration catches it.
// Requests that still have at least one viable host stay queued until
// matched or until their submitter cancels ctx.
//
// Throttle is included in "unavailable" because the default recovery
// rate (10 tokens/minute) is slow enough that once a host's bucket
// hits zero it stays effectively unusable for seconds-to-minutes --
// long enough that fast-failing the request is better than burning
// ghostThrottled nonces until recovery.
//
// Liveness invariant: every nonce that the session advances through is
// dispatched to exactly one party -- either a real request or a ghost
// probe. The picker never drops a nonce on the floor and never leaves
// a real request waiting on a host it explicitly excluded.
package main

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"devshard/user"
)

// pickerStaleThreshold is how long the picker will hold an available
// nonce hoping a compatible queued request arrives, before burning the
// nonce as a synthetic ghost probe. Tuned so a single request landing
// in an empty queue does not immediately consume nonces that
// co-arriving traffic could have used productively.
const pickerStaleThreshold = 200 * time.Millisecond

// errPickerEmpty is returned by the chooser when the queue is empty
// (e.g. every queued request was canceled or dropped between wakeup
// and the chooser running). PrepareInferenceFn propagates it; the run
// loop catches it and goes back to waiting without consuming the
// nonce.
var errPickerEmpty = errors.New("session picker: queue empty")

// errPickerHold is returned by the chooser when it is voluntarily
// declining the nonce -- no compatible request and the oldest queued
// request is fresher than pickerStaleThreshold. PrepareInferenceFn
// propagates it without consuming a nonce; the run loop arms a timer
// for the stale-mark and goes back to waiting.
var errPickerHold = errors.New("session picker: holding nonce; waiting for stale threshold")

var errPickerStopped = errors.New("session picker: stopped")

// ghostKind classifies why a nonce is being burned as a synthetic
// probe. All kinds are dispatched identically -- the MsgStart is
// composed inside PrepareInferenceFn and added to s.diffs, but no
// HTTP call is made and no response is awaited. The kind exists for
// log-label differentiation so operators can tell at a glance whether
// a burn was driven by PoC, exclude-stale, or reactive throttle.
type ghostKind int

const (
	ghostNone       ghostKind = iota
	ghostPoC                  // host requires PoC under relaxed bypass
	ghostExclude              // queue had no compatible request after pickerStaleThreshold
	ghostThrottled            // host is reactively throttled (tokens<1)
	ghostCapability           // host is known incompatible with queued request shape
)

func (g ghostKind) reason() string {
	switch g {
	case ghostPoC:
		return "poc_unavailable_host"
	case ghostExclude:
		return "no_compatible_request_after_stale"
	case ghostThrottled:
		return "participant_throttled_no_send"
	case ghostCapability:
		return "participant_capability_no_send"
	default:
		return ""
	}
}

// pickerRequest is one queued request waiting for a nonce.
//
// excludeParticipants is keyed by participant identity (the value
// returned by Session.HostParticipantKey for a host slot), not by
// slot index. A single participant can occupy multiple slots in the
// group (Session.ParticipantKeys deduplicates precisely because of
// this), so excluding by slot would let a request retry the same
// physical host through a sibling slot -- defeating the whole point
// of the per-request retry memory. Excluding by participant key
// guarantees one-attempt-per-host even when a participant has
// multiple group slots.
type pickerRequest struct {
	params              user.InferenceParams
	excludeParticipants map[string]bool // participant keys this request has already tried
	ctx                 context.Context
	submitTime          time.Time
	reply               chan pickerResult // buffered; one write only
}

// pickerResult is the dispatch outcome delivered back to the submitter.
type pickerResult struct {
	prepared *user.PreparedInference
	isProbe  bool
	err      error
}

// ghostDispatcher dispatches a synthetic probe inference. Implemented
// by Redundancy.runGhostProbe. Picker calls it with a freshly prepared
// inference (params already set to a tiny probe), the kind classifying
// why the nonce is being burned, and a reason string for log labeling.
//
// Contract: the dispatcher is synchronous and MUST NOT contact the
// host. The MsgStart for the nonce is composed and applied locally
// inside PrepareInferenceFn (it lives in s.diffs, so the host will
// see it as catch-up on its next real dispatch); the dispatcher only
// records the burn for observability. The kind is preserved on the
// signature for log-label differentiation.
type ghostDispatcher func(prepared *user.PreparedInference, kind ghostKind, reason string)

// throttleChecker reports whether a participant is currently
// reactively throttled (the limiter would reject AllowRequest right
// now). The picker consults this on every nonce binding to decide
// between branch 1b (throttled -> ghost-no-send) and branch 2 (real
// dispatch). MUST NOT take Session.mu or sessionPicker.mu; the
// chooser holds both when calling. nil is treated as "no throttle
// info available" (everything passes through to branch 2).
type throttleChecker func(participantKey string) bool

type capabilityChecker func(participantKey string, params user.InferenceParams) (string, bool)

// sessionPicker serializes nonce dispatch for one Session. It owns the
// run loop goroutine that drains the queue.
type sessionPicker struct {
	session         *user.Session
	model           string // escrow's registered model; used for ghost probe params
	dispatchGhost   ghostDispatcher
	throttleBlocked throttleChecker
	capabilityBlock capabilityChecker
	logCtx          context.Context

	mu     sync.Mutex
	queue  []*pickerRequest
	done   bool
	notify chan struct{} // signaled (non-blocking) on submit or stop

	stopOnce sync.Once
	stopped  chan struct{}
}

func newSessionPicker(session *user.Session, model string, dispatchGhost ghostDispatcher, throttleBlocked throttleChecker, capabilityBlock capabilityChecker) *sessionPicker {
	return &sessionPicker{
		session:         session,
		model:           model,
		dispatchGhost:   dispatchGhost,
		throttleBlocked: throttleBlocked,
		capabilityBlock: capabilityBlock,
		logCtx:          context.Background(),
		notify:          make(chan struct{}, 1),
		stopped:         make(chan struct{}),
	}
}

// start launches the dispatcher goroutine. Idempotent only when called
// from the same caller -- not safe to call twice from different
// goroutines, but production constructs the picker exactly once.
func (p *sessionPicker) start() {
	go p.run()
}

// stop signals the dispatcher to drain and exit. Blocks until exit.
// Pending requests still waiting in queue receive errPickerStopped.
// Tests should call this in cleanup; production callers do not.
func (p *sessionPicker) stop() {
	p.stopOnce.Do(func() {
		p.mu.Lock()
		p.done = true
		for _, r := range p.queue {
			r.reply <- pickerResult{err: errPickerStopped}
		}
		p.queue = nil
		p.mu.Unlock()
		p.wakeUp()
		<-p.stopped
	})
}

// submit enqueues a request. Non-blocking. The submitter must read
// from req.reply to receive the dispatch outcome.
func (p *sessionPicker) submit(req *pickerRequest) {
	if req.reply == nil {
		req.reply = make(chan pickerResult, 1)
	}
	if req.submitTime.IsZero() {
		req.submitTime = time.Now()
	}
	if req.ctx == nil {
		req.ctx = context.Background()
	}
	p.mu.Lock()
	if p.done {
		p.mu.Unlock()
		req.reply <- pickerResult{err: errPickerStopped}
		return
	}
	p.queue = append(p.queue, req)
	p.mu.Unlock()
	p.wakeUp()
}

// wakeUp signals the run loop. Non-blocking; if a wakeup is already
// pending the signal is coalesced (we only need to be woken once).
func (p *sessionPicker) wakeUp() {
	select {
	case p.notify <- struct{}{}:
	default:
	}
}

// run is the dispatcher loop. Each iteration either dispatches one
// nonce or sleeps until something interesting happens (submit, stale
// timer, stop).
func (p *sessionPicker) run() {
	defer close(p.stopped)
	for {
		// Phase 0: snapshot which participants are non-PoC right now.
		// Done outside p.mu because Session.HostParticipantKey acquires
		// Session.mu, and the chooser later acquires p.mu while
		// holding Session.mu (PrepareInferenceFn calls our chooser
		// under s.mu). Reverse order would deadlock.
		//
		// Set is keyed by participant identity, not slot, because the
		// exhaustion check for each request also runs against the
		// per-request excludeParticipants set.
		available := p.computeAvailableParticipants()

		// Phase 1: prune canceled and exhausted requests, decide
		// whether there is anything to dispatch right now.
		p.mu.Lock()
		p.dropCanceledLocked()
		p.dropExhaustedLocked(available)
		empty := len(p.queue) == 0
		stopping := p.done
		p.mu.Unlock()

		if stopping {
			return
		}

		if empty {
			// Nothing queued. Wait for a submit or stop.
			<-p.notify
			continue
		}

		// Phase 2: try to dispatch one nonce. The chooser runs under
		// Session.mu and decides between five outcomes, all handled
		// by the same PrepareInferenceFn-produced diff:
		//   - ghost-PoC         (Branch 1a) silent, PoC-required host
		//   - ghost-throttled   (Branch 1b) silent, 429/503 bucket empty
		//   - real match        (Branch 2)  dispatch a queued request
		//   - hold              (Branch 3)  wait for stale threshold
		//   - ghost-exclude     (Branch 3)  silent, stale threshold hit
		// No ghost outcome contacts the host; the kind is purely a
		// log label (see ghostDispatcher doc).
		var (
			chosen    *pickerRequest
			ghost     ghostKind
			holdUntil time.Time
			ghostParticipantKey string
		)
		prepared, err := p.session.PrepareInferenceFn(func(b user.HostBinding) (user.InferenceParams, bool, error) {
			p.mu.Lock()
			defer p.mu.Unlock()
			p.dropCanceledLocked()

			// Branch 1a: PoC required for this host. Real requests
			// must not be dispatched here -- ghost probe instead. No
			// need to consult the queue.
			//
			// PoC is checked before throttle so that a host which is
			// BOTH PoC-required and reactively throttled gets the
			// ghostPoC log label rather than ghostThrottled. The
			// dispatch path is identical (every kind is silent on
			// the wire after the all-silent refactor), but the kind
			// shows up in metrics + logs and PoC is the more
			// informative cause: it's a phase-level constraint
			// (host cannot serve user traffic at all right now)
			// whereas throttle is a reactive, host-level signal that
			// will be subsumed by the PoC label until the phase ends.
			if shouldUseProbeForParticipant(p.model, b.ParticipantKey) {
				ghost = ghostPoC
				return ghostProbeParams(p.model), true, nil
			}

			// Branch 1b: host is reactively throttled (just 503'd or
			// 429'd, bucket below 1 token). Burn the nonce as a
			// silent ghost probe so the queue keeps flowing without
			// poisoning a real request's per-host retry budget on a
			// host the transport-layer admission gate would reject
			// anyway. The MsgStart is already composed in the diff
			// produced by PrepareInferenceFn; the dispatcher just
			// logs and returns -- no HTTP call, so we don't pile
			// more load on a host that just told us it's overwhelmed.
			if p.throttleBlocked != nil && p.throttleBlocked(b.ParticipantKey) {
				ghost = ghostThrottled
				ghostParticipantKey = b.ParticipantKey
				return ghostProbeParams(p.model), true, nil
			}

			if len(p.queue) == 0 {
				return user.InferenceParams{}, false, errPickerEmpty
			}

			// Branch 2: try to match a queued request whose exclude
			// set permits this host's participant. Slot != participant:
			// excluding by ParticipantKey ensures a request that
			// already failed on participant X is not re-dispatched to
			// another slot owned by X.
			blockReason := ""
			for i, r := range p.queue {
				if r.excludeParticipants[b.ParticipantKey] {
					continue
				}
				if p.capabilityBlock != nil {
					if reason, blocked := p.capabilityBlock(b.ParticipantKey, r.params); blocked {
						if blockReason == "" {
							blockReason = reason
						}
						continue
					}
				}
				chosen = r
				p.removeAtLocked(i)
				return r.params, false, nil
			}

			// Branch 3: no compatible request. Hold the nonce briefly
			// to give co-arriving traffic a chance to match it. If the
			// oldest queued request is already past pickerStaleThreshold,
			// burn the nonce now as a ghost probe so the queue keeps
			// flowing.
			oldest := p.queue[0]
			mature := oldest.submitTime.Add(pickerStaleThreshold)
			if time.Now().Before(mature) {
				holdUntil = mature
				return user.InferenceParams{}, false, errPickerHold
			}
			if blockReason != "" {
				ghost = ghostCapability
				logRequestStage(p.logCtx, "session_picker_capability_blocked",
					"reason", blockReason,
					"participant_key", b.ParticipantKey,
					"host_idx", b.HostIdx,
					"queue_depth", len(p.queue),
				)
				return ghostProbeParams(p.model), true, nil
			}
			ghost = ghostExclude
			return ghostProbeParams(p.model), true, nil
		})

		// Phase 3: act on chooser outcome.
		switch {
		case errors.Is(err, errPickerEmpty):
			// Queue went empty during dispatch (race with cancel /
			// drop). Loop and re-evaluate.
			continue

		case errors.Is(err, errPickerHold):
			// Held a nonce. Sleep until either a new submit (which may
			// match) or the stale timer fires (which will burn).
			wait := time.Until(holdUntil)
			if wait < 0 {
				wait = 0
			}
			timer := time.NewTimer(wait)
			select {
			case <-p.notify:
				stopTimer(timer)
			case <-timer.C:
			}
			continue

		case err != nil:
			if chosen != nil {
				chosen.reply <- pickerResult{err: err}
				continue
			}
			// Unclassified error from chooser. Should not happen with
			// the current branches, but if it does then nonce preparation
			// is not making progress. Fail queued callers instead of
			// spinning on the same unusable nonce.
			logRequestStage(p.logCtx, "session_picker_chooser_error", "error", err)
			p.mu.Lock()
			for _, r := range p.queue {
				r.reply <- pickerResult{err: err}
			}
			p.queue = nil
			p.mu.Unlock()
			continue
		}

		// Phase 4: dispatch.
		if ghost != ghostNone {
			logFields := []any{
				"reason", ghost.reason(),
				"host_idx", prepared.HostIdx(),
				"nonce", prepared.Nonce(),
				"queue_depth", p.queueLen(),
			}
			if ghost == ghostThrottled {
				logFields = append(logFields,
					"no_send", true,
					"participant_key", ghostParticipantKey,
					"model_id", p.model,
				)
			}
			logRequestStage(p.logCtx, "session_picker_ghost_probe", logFields...)
			if p.dispatchGhost != nil {
				p.dispatchGhost(prepared, ghost, ghost.reason())
			}
			// Loop straight into the next iteration. Ghost burns are
			// the cost of advancing the nonce stream past hosts that
			// no real request can use right now (PoC-required, queue
			// has no compatible request past the stale threshold, or
			// reactively throttled after a recent 429/503), so
			// throttling the loop only delays the next real dispatch.
			// PrepareInferenceFn is local-only (a single Session.mu
			// increment plus diff composition) -- no chain RPC -- so
			// there is nothing downstream that benefits from us
			// spacing the bursts out.
			continue
		}

		// Real dispatch. reply is buffered so this never blocks even
		// if the submitter abandoned (ctx canceled and already returned
		// to its own caller).
		chosen.reply <- pickerResult{prepared: prepared, isProbe: false, err: nil}
	}
}

// computeAvailableParticipants returns the set of participant keys
// that can currently serve a real inference. A participant is
// available iff it is:
//
//   - NOT PoC-required (structural, phase-level constraint), and
//   - NOT reactively throttled (limiter bucket below 1 token after
//     a recent 429/503).
//
// Throttled participants are excluded here because the default
// recovery rate is 10 tokens/minute: once the bucket hits zero the
// host is effectively unavailable for seconds-to-minutes, which is
// long enough that keeping a queued request waiting on nonces that
// can only bind to throttled hosts wastes ghost burns for no
// productive outcome. Dropping fast via ErrNoAvailableHost lets the
// caller fail / retry cleanly.
//
// The throttle filter activates only when the picker has a
// throttleBlocked checker wired (NewRedundancyWithThrottle); with a
// nil checker the behavior degrades to "PoC-only", matching the
// historical exhaustion semantics.
//
// Acquires Session.mu briefly per slot via HostParticipantKey; safe
// to call without holding p.mu (and unsafe to call while holding
// it -- the chooser holds p.mu while inside PrepareInferenceFn
// which holds Session.mu, so the reverse order here would deadlock).
//
// Slots that share a participant collapse to one entry: that's the
// whole reason the exclude domain is participant-keyed. When relaxed
// PoC bypass is inactive and no host is throttled (the common case),
// every participant is available and the result has
// Session.ParticipantKeys-many entries.
func (p *sessionPicker) computeAvailableParticipants() map[string]bool {
	clients := p.session.Clients()
	available := make(map[string]bool, len(clients))
	for i := range clients {
		key := p.session.HostParticipantKey(i)
		if key == "" {
			continue
		}
		if shouldUseProbeForParticipant(p.model, key) {
			continue
		}
		if p.throttleBlocked != nil && p.throttleBlocked(key) {
			continue
		}
		available[key] = true
	}
	return available
}

// dropExhaustedLocked removes any queued request whose exclude set
// covers every currently-available participant, replying
// ErrNoAvailableHost. Must be called with p.mu held.
//
// "Available" here means a participant that is neither PoC-required
// nor reactively throttled at the moment of the sweep (computed in
// phase 0 of the run loop; see computeAvailableParticipants). A
// request that was viable when submitted but became non-viable
// because participants flipped into PoC, or because the only
// non-excluded hosts were hit by 429/503 and drained their token
// buckets, is dropped here; the caller's redundancy layer treats
// this exactly like ErrAllHostsExcluded and stops scheduling more
// attempts.
func (p *sessionPicker) dropExhaustedLocked(available map[string]bool) {
	if len(p.queue) == 0 {
		return
	}
	kept := p.queue[:0]
	for _, r := range p.queue {
		if p.hasCompatibleParticipantLocked(r, available) {
			kept = append(kept, r)
			continue
		}
		r.reply <- pickerResult{err: ErrNoAvailableHost}
	}
	p.queue = kept
}

// hasCompatibleParticipantLocked returns true iff at least one available
// participant is not in the request's exclude set and is not known to be
// incompatible with this request shape. Capability-blocked participants are
// ghost-skipped while a compatible unknown/larger participant remains; if none
// remains, the request is exhausted so redundancy can return the first host
// capability error it already observed.
func (p *sessionPicker) hasCompatibleParticipantLocked(req *pickerRequest, available map[string]bool) bool {
	if req == nil {
		return false
	}
	for key := range available {
		if req.excludeParticipants[key] {
			continue
		}
		if p.capabilityBlock != nil {
			if _, blocked := p.capabilityBlock(key, req.params); blocked {
				continue
			}
		}
		return true
	}
	return false
}

// dropCanceledLocked removes requests whose context was canceled,
// replying with the wrapped ctx error. Must be called with p.mu held.
func (p *sessionPicker) dropCanceledLocked() {
	if len(p.queue) == 0 {
		return
	}
	kept := p.queue[:0]
	for _, r := range p.queue {
		if r.ctx.Err() != nil {
			r.reply <- pickerResult{err: fmt.Errorf("picker: %w", r.ctx.Err())}
			continue
		}
		kept = append(kept, r)
	}
	p.queue = kept
}

// removeAtLocked removes p.queue[i] preserving order. Must be called
// with p.mu held.
func (p *sessionPicker) removeAtLocked(i int) {
	p.queue = append(p.queue[:i], p.queue[i+1:]...)
}

// queueLen returns the current queue depth. Safe for concurrent use.
func (p *sessionPicker) queueLen() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.queue)
}
