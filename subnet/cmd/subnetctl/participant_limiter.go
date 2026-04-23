package main

import (
	"fmt"
	"log"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	defaultParticipantRequestBurst             = 600
	defaultParticipantRequestRecoveryPerMinute = 10
	// httpThrottleQuarantine is the wall-clock cooldown after 429/503. It
	// matches the old ~60m "full token bucket" recovery (600 tokens at
	// 10/min) in one explicit duration so IsBlocked and IsAvailable align.
	httpThrottleQuarantine = 60 * time.Minute
	// transportFailureQuarantine is used when the HTTP request never
	// received a response (connection error, etc.).
	transportFailureQuarantine = 10 * time.Minute
	// emptyStreamQuarantine is used when a host returns contentless SSE
	// responses repeatedly. It matches the transport-failure cooldown.
	emptyStreamQuarantine = 10 * time.Minute
	// emptyStreamQuarantineThreshold is the number of consecutive empty
	// content responses before the host is temporarily quarantined.
	emptyStreamQuarantineThreshold = 3
	// participantStatusTransport is persisted in last_throttle_status when
	// the last signal was a transport failure (not an HTTP 429/503).
	participantStatusTransport = 0
	// participantStatusEmptyStream is persisted when an empty-stream streak
	// trips the short quarantine.
	participantStatusEmptyStream = -1
)

var sharedParticipantRequestLimiter = NewParticipantRequestLimiter(
	defaultParticipantRequestBurst,
	defaultParticipantRequestRecoveryPerMinute,
)

type ParticipantRateLimitError struct {
	ParticipantKey string
}

func (e *ParticipantRateLimitError) Error() string {
	if e == nil || e.ParticipantKey == "" {
		return "participant request budget exhausted"
	}
	return fmt.Sprintf("participant request budget exhausted for %s", e.ParticipantKey)
}

// EscrowParticipantRateLimitError is returned when every candidate
// escrow is at zero effective capacity. We deliberately don't carry
// the list of "blocked" participant keys: a host can drop out of W(e)
// for many reasons (chain weight 0, PoC exclusion, reactive throttle,
// share rounding) and pinning the blame on the throttled subset would
// mislead operators about the actual cause. The picker logs per-escrow
// W(e) at the call site for diagnostics.
type EscrowParticipantRateLimitError struct{}

func (e *EscrowParticipantRateLimitError) Error() string {
	return "no available escrows: participant request budget exhausted"
}

// ParticipantThrottleStore is the persistence interface for reactive throttle state.
type ParticipantThrottleStore interface {
	SaveParticipantThrottle(key string, tokens float64, lastRefillAt time.Time, status int, quarantineUntil time.Time, emptyStreamStreak int) error
	DeleteParticipantThrottle(key string) error
}

// ParticipantRequestLimiter is a reactive, per-host limiter. After 429/503
// the host is quarantined for httpThrottleQuarantine; after a transport
// failure (no HTTP response) or three consecutive empty-stream responses
// for transportFailureQuarantine / emptyStreamQuarantine. Longer of the
// overlapping quarantines wins. Legacy rows without quarantine use the
// token-bucket refill only.
type ParticipantRequestLimiter struct {
	mu                sync.Mutex
	burst             float64
	recoveryPerSecond float64
	participants      map[string]*participantRequestState
	metrics           *SubnetMetrics
	store             ParticipantThrottleStore
}

type participantRequestState struct {
	tokens            float64
	lastRefill        time.Time
	quarantineUntil   time.Time // non-zero: wall-clock unavailability
	emptyStreamStreak int
}

func NewParticipantRequestLimiter(burst int, recoveryPerMinute int) *ParticipantRequestLimiter {
	if burst <= 0 {
		burst = defaultParticipantRequestBurst
	}
	if recoveryPerMinute <= 0 {
		recoveryPerMinute = defaultParticipantRequestRecoveryPerMinute
	}
	return &ParticipantRequestLimiter{
		burst:             float64(burst),
		recoveryPerSecond: float64(recoveryPerMinute) / 60.0,
		participants:      make(map[string]*participantRequestState),
	}
}

// LoadState restores a previously throttled participant from persistent storage.
// Time-based recovery since lastRefill is applied. If the participant has fully
// recovered (tokens >= burst), the record is deleted from the store instead.
func (l *ParticipantRequestLimiter) LoadState(key string, tokens float64, lastRefill time.Time) {
	l.LoadStateWithQuarantine(key, tokens, lastRefill, 0, time.Time{}, 0)
}

// LoadStateWithQuarantine is like LoadState but supports persisted quarantine
// and upgrades legacy 429/503 rows to a quarantine end time when needed.
func (l *ParticipantRequestLimiter) LoadStateWithQuarantine(key string, tokens float64, lastRefill time.Time, status int, quarantineFromDB time.Time, emptyStreamStreak int) {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := time.Now()

	if !quarantineFromDB.IsZero() {
		if now.Before(quarantineFromDB) {
			l.participants[key] = &participantRequestState{
				tokens:            0,
				lastRefill:        now,
				quarantineUntil:   quarantineFromDB,
				emptyStreamStreak: emptyStreamStreak,
			}
			log.Printf("participant_limit_loaded_from_db participant_key=%s quarantine_until=%s", key, quarantineFromDB.Format(time.RFC3339))
			return
		}
		// already expired; drop persisted row
		if l.store != nil {
			if err := l.store.DeleteParticipantThrottle(key); err != nil {
				log.Printf("participant_throttle_cleanup_failed participant_key=%s error=%v", key, err)
			}
		}
		log.Printf("participant_limit_stale_on_load participant_key=%s", key)
		return
	}

	elapsed := now.Sub(lastRefill).Seconds()
	if elapsed > 0 {
		tokens += elapsed * l.recoveryPerSecond
	}
	if tokens >= l.burst && emptyStreamStreak == 0 {
		if l.store != nil {
			if err := l.store.DeleteParticipantThrottle(key); err != nil {
				log.Printf("participant_throttle_cleanup_failed participant_key=%s error=%v", key, err)
			}
		}
		log.Printf("participant_limit_recovered_on_load participant_key=%s", key)
		return
	}

	st := &participantRequestState{tokens: tokens, lastRefill: now, quarantineUntil: time.Time{}, emptyStreamStreak: emptyStreamStreak}
	// Legacy rows from 429/503: time-to-full (token refill) approximates the old
	// IsAvailable horizon; cap at 60m.
	if (status == http.StatusTooManyRequests || status == http.StatusServiceUnavailable) && tokens < l.burst {
		remain := l.burst - tokens
		if l.recoveryPerSecond > 0 {
			toFull := time.Duration(remain / l.recoveryPerSecond * float64(time.Second))
			if toFull > httpThrottleQuarantine {
				toFull = httpThrottleQuarantine
			}
			st.quarantineUntil = now.Add(toFull)
		}
	}
	l.participants[key] = st
	log.Printf("participant_limit_loaded participant_key=%s tokens=%.1f", key, st.tokens)
}

func (l *ParticipantRequestLimiter) SetStore(store ParticipantThrottleStore) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.store = store
}

// AllowRequest checks whether a request to this participant is allowed.
// Participants that have never been throttled (no state) are always allowed.
//
// During relaxed PoC the legacy behavior bypasses the limiter entirely;
// when capacity-aware mode is on we keep the reactive throttle active
// and rely on CapacityState-driven scaling for relief instead.
func (l *ParticipantRequestLimiter) AllowRequest(participantKey, _ string) error {
	if participantKey == "" {
		return nil
	}
	if !capacityAwareLimitsEnabled() && relaxedPoCBypassActive() {
		return nil
	}
	if l.allow(participantKey, time.Now()) {
		return nil
	}
	if l.metrics != nil {
		l.metrics.RecordParticipantLimitRejection("transport_request")
	}
	log.Printf("participant_limit_rejected participant_key=%s", participantKey)
	return &ParticipantRateLimitError{ParticipantKey: participantKey}
}

func (l *ParticipantRequestLimiter) allow(participantKey string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	state, tracked := l.participants[participantKey]
	if !tracked {
		return true
	}
	l.clearExpiredQuarantineIfAnyLocked(participantKey, state, now)
	if _, still := l.participants[participantKey]; !still {
		return true
	}
	state = l.participants[participantKey]

	if l.inQuarantineLocked(state, now) {
		return false
	}
	l.refillLocked(state, now)
	if state.tokens >= l.burst {
		if state.emptyStreamStreak > 0 {
			return true
		}
		delete(l.participants, participantKey)
		l.persistDeleteLocked(participantKey)
		log.Printf("participant_limit_expired participant_key=%s", participantKey)
		return true
	}
	if state.tokens < 1 {
		return false
	}
	state.tokens--
	return true
}

// CanAcceptEscrow returns EscrowParticipantRateLimitError if any of
// the supplied participant keys are currently throttled. The gateway's
// pooled routing path no longer calls this (it relies on per-host
// W(e) instead) but unit tests and admin tooling still find the
// boolean form convenient.
func (l *ParticipantRequestLimiter) CanAcceptEscrow(participantKeys []string) error {
	if !capacityAwareLimitsEnabled() && relaxedPoCBypassActive() {
		return nil
	}
	if len(l.BlockedParticipants(participantKeys)) == 0 {
		return nil
	}
	return &EscrowParticipantRateLimitError{}
}

func (l *ParticipantRequestLimiter) ObserveResult(participantKey, path string, statusCode int) {
	if participantKey == "" || statusCode <= 0 {
		return
	}
	if l.metrics != nil && statusCode >= http.StatusBadRequest {
		l.metrics.RecordParticipantTransportError(participantPathKind(path), statusCode)
	}
	quarantineFor := participantHTTPQuarantine(path, statusCode)
	if quarantineFor == 0 {
		return
	}

	now := time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()
	l.applyQuarantineLocked(participantKey, now.Add(quarantineFor), now)
	if st := l.participants[participantKey]; st != nil {
		st.emptyStreamStreak = 0
	}

	log.Printf("participant_limit_activated participant_key=%s status=%d path_kind=%s",
		participantKey, statusCode, participantPathKind(path))

	l.persistThrottledStateLocked(participantKey, l.participants[participantKey], statusCode)
}

// ObserveTransportFailure records that a request to this host never
// received an HTTP response. Uses a short quarantine
// (transportFailureQuarantine); if a 429/503 quarantine is already
// longer, it is kept.
func (l *ParticipantRequestLimiter) ObserveTransportFailure(participantKey, path string) {
	if participantKey == "" {
		return
	}
	now := time.Now()
	if l.metrics != nil {
		// status label "0" = no HTTP response (distinguishable from 503/429 in dashboards).
		l.metrics.RecordParticipantTransportError(participantPathKind(path), 0)
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.applyQuarantineLocked(participantKey, now.Add(transportFailureQuarantine), now)
	if st := l.participants[participantKey]; st != nil {
		st.emptyStreamStreak = 0
	}
	log.Printf("participant_limit_transport_failure participant_key=%s path_kind=%s",
		participantKey, participantPathKind(path))
	l.persistThrottledStateLocked(participantKey, l.participants[participantKey], participantStatusTransport)
}

// ObserveEmptyStream increments the consecutive empty-stream streak for a
// participant. On the third consecutive strike, the participant enters the
// short quarantine and the streak resets to zero.
func (l *ParticipantRequestLimiter) ObserveEmptyStream(participantKey string) {
	if participantKey == "" {
		return
	}
	now := time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()

	state := l.ensureStateLocked(participantKey, now)
	l.clearExpiredQuarantineIfAnyLocked(participantKey, state, now)
	state, ok := l.participants[participantKey]
	if !ok {
		state = l.ensureStateLocked(participantKey, now)
	}
	if l.inQuarantineLocked(state, now) {
		return
	}
	state.emptyStreamStreak++
	if state.emptyStreamStreak >= emptyStreamQuarantineThreshold {
		l.applyQuarantineLocked(participantKey, now.Add(emptyStreamQuarantine), now)
		state.emptyStreamStreak = 0
		log.Printf("participant_limit_empty_stream_quarantine participant_key=%s threshold=%d", participantKey, emptyStreamQuarantineThreshold)
		l.persistThrottledStateLocked(participantKey, state, participantStatusEmptyStream)
		return
	}
	log.Printf("participant_limit_empty_stream_streak participant_key=%s streak=%d", participantKey, state.emptyStreamStreak)
	l.persistThrottledStateLocked(participantKey, state, participantStatusEmptyStream)
}

// ObserveSuccessfulInference clears any accumulated empty-stream streak for a
// participant after a good finished response.
func (l *ParticipantRequestLimiter) ObserveSuccessfulInference(participantKey string) {
	if participantKey == "" {
		return
	}
	now := time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()

	state, ok := l.participants[participantKey]
	if !ok {
		return
	}
	l.clearExpiredQuarantineIfAnyLocked(participantKey, state, now)
	state, ok = l.participants[participantKey]
	if !ok {
		return
	}
	if state.emptyStreamStreak == 0 {
		return
	}
	state.emptyStreamStreak = 0
	if state.tokens >= l.burst && state.quarantineUntil.IsZero() {
		delete(l.participants, participantKey)
		l.persistDeleteLocked(participantKey)
		return
	}
	l.persistThrottledStateLocked(participantKey, state, participantStatusTransport)
}

func (l *ParticipantRequestLimiter) SetMetrics(metrics *SubnetMetrics) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.metrics = metrics
}

func (l *ParticipantRequestLimiter) BlockedParticipants(participantKeys []string) []string {
	if len(participantKeys) == 0 {
		return nil
	}
	now := time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()

	seen := make(map[string]struct{}, len(participantKeys))
	var blocked []string
	for _, key := range participantKeys {
		if key == "" {
			continue
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		state, tracked := l.participants[key]
		if !tracked {
			continue
		}
		l.clearExpiredQuarantineIfAnyLocked(key, state, now)
		if _, still := l.participants[key]; !still {
			continue
		}
		state = l.participants[key]
		if l.inQuarantineLocked(state, now) {
			blocked = append(blocked, key)
			continue
		}
		l.refillLocked(state, now)
		if state.tokens < 1 {
			blocked = append(blocked, key)
		}
	}
	sort.Strings(blocked)
	return blocked
}

func (l *ParticipantRequestLimiter) refillLocked(state *participantRequestState, now time.Time) {
	elapsed := now.Sub(state.lastRefill).Seconds()
	if elapsed > 0 {
		state.tokens += elapsed * l.recoveryPerSecond
		if state.tokens > l.burst {
			state.tokens = l.burst
		}
		state.lastRefill = now
	}
}

func (l *ParticipantRequestLimiter) persistThrottledStateLocked(key string, state *participantRequestState, status int) {
	if l.store == nil {
		return
	}
	quar := time.Time{}
	if !state.quarantineUntil.IsZero() {
		quar = state.quarantineUntil
	}
	if err := l.store.SaveParticipantThrottle(key, state.tokens, state.lastRefill, status, quar, state.emptyStreamStreak); err != nil {
		log.Printf("participant_throttle_persist_failed participant_key=%s error=%v", key, err)
	}
}

func (l *ParticipantRequestLimiter) persistDeleteLocked(key string) {
	if l.store != nil {
		if err := l.store.DeleteParticipantThrottle(key); err != nil {
			log.Printf("participant_throttle_cleanup_failed participant_key=%s error=%v", key, err)
		}
	}
}

// ExhaustedCount returns the number of currently blocked (tokens < 1) participants.
func (l *ParticipantRequestLimiter) ExhaustedCount() int {
	now := time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()

	keys := make([]string, 0, len(l.participants))
	for k := range l.participants {
		keys = append(keys, k)
	}
	for _, key := range keys {
		if st, ok := l.participants[key]; ok {
			l.clearExpiredQuarantineIfAnyLocked(key, st, now)
		}
	}
	n := 0
	for _, state := range l.participants {
		if l.inQuarantineLocked(state, now) {
			n++
			continue
		}
		l.refillLocked(state, now)
		if state.tokens < 1 {
			n++
		}
	}
	return n
}

// TrackedCount returns the number of participants currently in reactive tracking.
func (l *ParticipantRequestLimiter) TrackedCount() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.participants)
}

// IsAvailable reports whether the participant is currently considered
// available for capacity-aware routing. During quarantine the host is
// unavailable; after legacy refills, full burst means available.
func (l *ParticipantRequestLimiter) IsAvailable(participantKey string) bool {
	if participantKey == "" {
		return true
	}
	now := time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()

	state, tracked := l.participants[participantKey]
	if !tracked {
		return true
	}
	l.clearExpiredQuarantineIfAnyLocked(participantKey, state, now)
	if _, still := l.participants[participantKey]; !still {
		return true
	}
	state = l.participants[participantKey]
	if l.inQuarantineLocked(state, now) {
		return false
	}
	l.refillLocked(state, now)
	if state.tokens >= l.burst {
		if state.emptyStreamStreak > 0 {
			return true
		}
		delete(l.participants, participantKey)
		l.persistDeleteLocked(participantKey)
		return true
	}
	return false
}

// IsBlocked reports whether AllowRequest would currently reject, or
// the host is in quarantine (same unified notion for 429/503, transport
// failure, and legacy token exhaustion).
func (l *ParticipantRequestLimiter) IsBlocked(participantKey string) bool {
	if participantKey == "" {
		return false
	}
	now := time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()

	state, tracked := l.participants[participantKey]
	if !tracked {
		return false
	}
	l.clearExpiredQuarantineIfAnyLocked(participantKey, state, now)
	if _, still := l.participants[participantKey]; !still {
		return false
	}
	state = l.participants[participantKey]
	if l.inQuarantineLocked(state, now) {
		return true
	}
	l.refillLocked(state, now)
	return state.tokens < 1
}

func (l *ParticipantRequestLimiter) inQuarantineLocked(state *participantRequestState, now time.Time) bool {
	return !state.quarantineUntil.IsZero() && now.Before(state.quarantineUntil)
}

func (l *ParticipantRequestLimiter) applyQuarantineLocked(participantKey string, end time.Time, now time.Time) {
	st := l.ensureStateLocked(participantKey, now)
	st.tokens = 0
	st.lastRefill = now
	if st.quarantineUntil.IsZero() || end.After(st.quarantineUntil) {
		st.quarantineUntil = end
	}
}

func (l *ParticipantRequestLimiter) clearExpiredQuarantineIfAnyLocked(key string, state *participantRequestState, now time.Time) {
	if state == nil {
		return
	}
	if l.inQuarantineLocked(state, now) {
		return
	}
	if !state.quarantineUntil.IsZero() && !now.Before(state.quarantineUntil) {
		state.quarantineUntil = time.Time{}
		state.tokens = l.burst
		state.lastRefill = now
		state.emptyStreamStreak = 0
		delete(l.participants, key)
		l.persistDeleteLocked(key)
		log.Printf("participant_quarantine_ended participant_key=%s", key)
	}
}

func participantHTTPQuarantine(path string, statusCode int) time.Duration {
	switch {
	case isParticipantThrottleStatus(statusCode):
		return httpThrottleQuarantine
	case statusCode == http.StatusNotFound && participantPathKind(path) == "inference":
		return transportFailureQuarantine
	default:
		return 0
	}
}

func isParticipantThrottleStatus(statusCode int) bool {
	return statusCode == http.StatusTooManyRequests || statusCode == http.StatusServiceUnavailable
}

func (l *ParticipantRequestLimiter) ensureStateLocked(participantKey string, now time.Time) *participantRequestState {
	st, ok := l.participants[participantKey]
	if !ok {
		st = &participantRequestState{
			tokens:     l.burst,
			lastRefill: now,
		}
		l.participants[participantKey] = st
	}
	return st
}

func participantPathKind(path string) string {
	switch {
	case strings.Contains(path, "/chat/completions"):
		return "inference"
	case strings.Contains(path, "/verify-timeout"):
		return "verify_timeout"
	case strings.Contains(path, "/challenge-receipt"):
		return "challenge_receipt"
	case strings.Contains(path, "/gossip/"):
		return "gossip"
	case strings.Contains(path, "/diffs"), strings.Contains(path, "/signatures"), strings.Contains(path, "/mempool"):
		return "query"
	default:
		return "other"
	}
}
