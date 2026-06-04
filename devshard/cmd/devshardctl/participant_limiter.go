package main

import (
	"errors"
	"fmt"
	"io"
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
	transportFailureQuarantine = 30 * time.Minute
	// emptyStreamQuarantine is used when a host returns contentless SSE
	// responses repeatedly.
	emptyStreamQuarantine = 30 * time.Minute
	// stalledWinnerQuarantine is used when a crowned winner emits some
	// content, then goes silent long enough to fail the user-visible stream.
	stalledWinnerQuarantine = 30 * time.Minute
	// emptyStreamQuarantineThreshold is the number of consecutive empty
	// content responses before the host is temporarily quarantined.
	emptyStreamQuarantineThreshold = 3
	// eofTransportFailureThreshold is the number of consecutive EOF-style
	// inference transport failures before the host is temporarily quarantined.
	eofTransportFailureThreshold = 3
	// participantProbationSuccessesAfterQuarantine keeps recently recovered hosts
	// out of proactive pairwise/A-B routing until they prove they can finish
	// normal inference requests again.
	participantProbationSuccessesAfterQuarantine = 3
	// participantStatusTransport is persisted in last_throttle_status when
	// the last signal was a transport failure (not an HTTP 429/503).
	participantStatusTransport = 0
	// participantStatusEmptyStream is persisted when an empty-stream streak
	// trips the short quarantine.
	participantStatusEmptyStream = -1
	// participantStatusStalledWinner is persisted when a crowned winner
	// stalls after streaming content to the client.
	participantStatusStalledWinner = -2
	// participantStatusEOFTransport is persisted when EOF transport failures
	// trip the short quarantine.
	participantStatusEOFTransport = -3
)

var sharedParticipantRequestLimiter = NewParticipantRequestLimiter(
	defaultParticipantRequestBurst,
	defaultParticipantRequestRecoveryPerMinute,
)

func DefaultParticipantThrottleSettings() ParticipantThrottleSettings {
	return ParticipantThrottleSettings{
		RequestBurst:                   defaultParticipantRequestBurst,
		RecoveryPerMinute:              defaultParticipantRequestRecoveryPerMinute,
		HTTPQuarantineMS:               httpThrottleQuarantine.Milliseconds(),
		TransportFailureQuarantineMS:   transportFailureQuarantine.Milliseconds(),
		EmptyStreamQuarantineMS:        emptyStreamQuarantine.Milliseconds(),
		StalledWinnerQuarantineMS:      stalledWinnerQuarantine.Milliseconds(),
		EmptyStreamQuarantineThreshold: emptyStreamQuarantineThreshold,
		EOFTransportFailureThreshold:   eofTransportFailureThreshold,
	}
}

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
// for many reasons (raw capacity 0, PoC exclusion, reactive throttle,
// share rounding) and pinning the blame on the throttled subset would
// mislead operators about the actual cause. The picker logs per-escrow
// W(e) at the call site for diagnostics.
type EscrowParticipantRateLimitError struct{}

func (e *EscrowParticipantRateLimitError) Error() string {
	return "no available escrows: participant request budget exhausted"
}

// ParticipantThrottleStore is the persistence interface for reactive throttle state.
type ParticipantThrottleStore interface {
	SaveParticipantThrottle(key string, tokens float64, lastRefillAt time.Time, status int, quarantineUntil time.Time, emptyStreamStreak int, eofTransportFailureStreak int) error
	DeleteParticipantThrottle(key string) error
}

// ParticipantRequestLimiter is a reactive, per-host limiter. After 429/503
// the host is quarantined for the configured HTTP throttle duration; after
// a transport failure (no HTTP response) or configured consecutive
// empty-stream responses for the configured short quarantine. Longer of the
// overlapping quarantines wins. Legacy rows without quarantine use the
// token-bucket refill only.
type ParticipantRequestLimiter struct {
	mu                             sync.Mutex
	burst                          float64
	recoveryPerSecond              float64
	httpThrottleQuarantine         time.Duration
	transportFailureQuarantine     time.Duration
	emptyStreamQuarantine          time.Duration
	stalledWinnerQuarantine        time.Duration
	emptyStreamQuarantineThreshold int
	eofTransportFailureThreshold   int
	participants                   map[string]*participantRequestState
	metrics                        *DevshardMetrics
	store                          ParticipantThrottleStore
}

type participantRequestState struct {
	tokens                      float64
	lastRefill                  time.Time
	quarantineUntil             time.Time // non-zero: wall-clock unavailability
	probationSuccessesRemaining int       // recently exited quarantine; not blocked
	emptyStreamStreak           int
	eofTransportFailureStreak   int
}

type ParticipantThrottleSnapshot struct {
	Tracked               bool    `json:"tracked"`
	Quarantined           bool    `json:"quarantined"`
	Blocked               bool    `json:"blocked"`
	RequestAllowed        bool    `json:"request_allowed"`
	AvailableForCapacity  bool    `json:"available_for_capacity"`
	Tokens                float64 `json:"tokens"`
	Burst                 float64 `json:"burst"`
	QuarantineUntil       string  `json:"quarantine_until,omitempty"`
	QuarantineRemainingMS int64   `json:"quarantine_remaining_ms,omitempty"`
	EmptyStreamStreak     int     `json:"empty_stream_streak,omitempty"`
	EOFTransportStreak    int     `json:"eof_transport_failure_streak,omitempty"`
}

func NewParticipantRequestLimiter(burst int, recoveryPerMinute int) *ParticipantRequestLimiter {
	settings := DefaultParticipantThrottleSettings()
	if burst > 0 {
		settings.RequestBurst = burst
	}
	if recoveryPerMinute > 0 {
		settings.RecoveryPerMinute = recoveryPerMinute
	}
	l := &ParticipantRequestLimiter{
		participants: make(map[string]*participantRequestState),
	}
	l.applySettingsLocked(settings)
	return l
}

func (l *ParticipantRequestLimiter) UpdateSettings(settings ParticipantThrottleSettings) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.applySettingsLocked(settings)
}

func (l *ParticipantRequestLimiter) applySettingsLocked(settings ParticipantThrottleSettings) {
	defaults := DefaultParticipantThrottleSettings()
	if settings.RequestBurst <= 0 {
		settings.RequestBurst = defaults.RequestBurst
	}
	if settings.RecoveryPerMinute <= 0 {
		settings.RecoveryPerMinute = defaults.RecoveryPerMinute
	}
	if settings.HTTPQuarantineMS <= 0 {
		settings.HTTPQuarantineMS = defaults.HTTPQuarantineMS
	}
	if settings.TransportFailureQuarantineMS <= 0 {
		settings.TransportFailureQuarantineMS = defaults.TransportFailureQuarantineMS
	}
	if settings.EmptyStreamQuarantineMS <= 0 {
		settings.EmptyStreamQuarantineMS = defaults.EmptyStreamQuarantineMS
	}
	if settings.StalledWinnerQuarantineMS <= 0 {
		settings.StalledWinnerQuarantineMS = defaults.StalledWinnerQuarantineMS
	}
	if settings.EmptyStreamQuarantineThreshold <= 0 {
		settings.EmptyStreamQuarantineThreshold = defaults.EmptyStreamQuarantineThreshold
	}
	if settings.EOFTransportFailureThreshold <= 0 {
		settings.EOFTransportFailureThreshold = defaults.EOFTransportFailureThreshold
	}
	l.burst = float64(settings.RequestBurst)
	l.recoveryPerSecond = float64(settings.RecoveryPerMinute) / 60.0
	l.httpThrottleQuarantine = time.Duration(settings.HTTPQuarantineMS) * time.Millisecond
	l.transportFailureQuarantine = time.Duration(settings.TransportFailureQuarantineMS) * time.Millisecond
	l.emptyStreamQuarantine = time.Duration(settings.EmptyStreamQuarantineMS) * time.Millisecond
	l.stalledWinnerQuarantine = time.Duration(settings.StalledWinnerQuarantineMS) * time.Millisecond
	l.emptyStreamQuarantineThreshold = settings.EmptyStreamQuarantineThreshold
	l.eofTransportFailureThreshold = settings.EOFTransportFailureThreshold
	for _, state := range l.participants {
		if state.tokens > l.burst {
			state.tokens = l.burst
		}
	}
}

// LoadState restores a previously throttled participant from persistent storage.
// Time-based recovery since lastRefill is applied. If the participant has fully
// recovered (tokens >= burst), the record is deleted from the store instead.
func (l *ParticipantRequestLimiter) LoadState(key string, tokens float64, lastRefill time.Time) {
	l.LoadStateWithQuarantine(key, tokens, lastRefill, 0, time.Time{}, 0, 0)
}

// LoadStateWithQuarantine is like LoadState but supports persisted quarantine
// and upgrades legacy 429/503 rows to a quarantine end time when needed.
func (l *ParticipantRequestLimiter) LoadStateWithQuarantine(key string, tokens float64, lastRefill time.Time, status int, quarantineFromDB time.Time, emptyStreamStreak int, eofTransportFailureStreak int) {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := time.Now()

	if !quarantineFromDB.IsZero() {
		if now.Before(quarantineFromDB) {
			l.participants[key] = &participantRequestState{
				tokens:                    0,
				lastRefill:                now,
				quarantineUntil:           quarantineFromDB,
				emptyStreamStreak:         emptyStreamStreak,
				eofTransportFailureStreak: eofTransportFailureStreak,
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
	if tokens >= l.burst && emptyStreamStreak == 0 && eofTransportFailureStreak == 0 {
		if l.store != nil {
			if err := l.store.DeleteParticipantThrottle(key); err != nil {
				log.Printf("participant_throttle_cleanup_failed participant_key=%s error=%v", key, err)
			}
		}
		log.Printf("participant_limit_recovered_on_load participant_key=%s", key)
		return
	}

	st := &participantRequestState{
		tokens:                    tokens,
		lastRefill:                now,
		quarantineUntil:           time.Time{},
		emptyStreamStreak:         emptyStreamStreak,
		eofTransportFailureStreak: eofTransportFailureStreak,
	}
	// Legacy rows from 429/503: time-to-full (token refill) approximates the old
	// IsAvailable horizon; cap at 60m.
	if (status == http.StatusTooManyRequests || status == http.StatusServiceUnavailable) && tokens < l.burst {
		remain := l.burst - tokens
		if l.recoveryPerSecond > 0 {
			toFull := time.Duration(remain / l.recoveryPerSecond * float64(time.Second))
			if toFull > l.httpThrottleQuarantine {
				toFull = l.httpThrottleQuarantine
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
		if state.emptyStreamStreak > 0 || state.eofTransportFailureStreak > 0 {
			return true
		}
		if l.probationActiveLocked(state) {
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
	l.ObserveResultWithBody(participantKey, path, statusCode, "")
}

func (l *ParticipantRequestLimiter) ObserveResultWithBody(participantKey, path string, statusCode int, body string) {
	if participantKey == "" || statusCode <= 0 {
		return
	}
	if l.metrics != nil && statusCode >= http.StatusBadRequest {
		l.metrics.RecordParticipantTransportError(participantPathKind(path), statusCode)
	}
	quarantineFor := l.participantHTTPQuarantine(path, statusCode, body)
	if quarantineFor == 0 {
		return
	}

	now := time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()
	l.applyQuarantineLocked(participantKey, now.Add(quarantineFor), now)
	if st := l.participants[participantKey]; st != nil {
		st.emptyStreamStreak = 0
		st.eofTransportFailureStreak = 0
	}

	log.Printf("participant_limit_activated participant_key=%s status=%d path_kind=%s",
		participantKey, statusCode, participantPathKind(path))

	l.persistThrottledStateLocked(participantKey, l.participants[participantKey], statusCode)
}

// ObserveTransportFailure records that a request to this host never received an
// HTTP response. Only inference-path failures (/chat/completions) trigger
// quarantine. EOF-style inference failures require consecutive strikes; other
// inference transport failures still quarantine immediately.
func (l *ParticipantRequestLimiter) ObserveTransportFailure(participantKey, path string, err error) {
	if participantKey == "" {
		return
	}
	kind := participantPathKind(path)
	if l.metrics != nil {
		l.metrics.RecordParticipantTransportError(kind, 0)
	}
	if kind != "inference" {
		log.Printf("participant_transport_failure_ignored participant_key=%s path_kind=%s error=%q",
			participantKey, kind, truncateError(err))
		return
	}
	now := time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()

	if isEOFTransportFailure(err) {
		state := l.ensureStateLocked(participantKey, now)
		l.clearExpiredQuarantineIfAnyLocked(participantKey, state, now)
		state, ok := l.participants[participantKey]
		if !ok {
			state = l.ensureStateLocked(participantKey, now)
		}
		if l.inQuarantineLocked(state, now) {
			return
		}
		state.eofTransportFailureStreak++
		if state.eofTransportFailureStreak >= l.eofTransportFailureThreshold {
			l.applyQuarantineLocked(participantKey, now.Add(l.transportFailureQuarantine), now)
			state.eofTransportFailureStreak = 0
			state.emptyStreamStreak = 0
			log.Printf("participant_limit_eof_transport_quarantine participant_key=%s threshold=%d error=%q",
				participantKey, l.eofTransportFailureThreshold, truncateError(err))
			l.persistThrottledStateLocked(participantKey, state, participantStatusEOFTransport)
			return
		}
		log.Printf("participant_limit_eof_transport_streak participant_key=%s streak=%d error=%q",
			participantKey, state.eofTransportFailureStreak, truncateError(err))
		l.persistThrottledStateLocked(participantKey, state, participantStatusEOFTransport)
		return
	}

	l.applyQuarantineLocked(participantKey, now.Add(l.transportFailureQuarantine), now)
	if st := l.participants[participantKey]; st != nil {
		st.emptyStreamStreak = 0
		st.eofTransportFailureStreak = 0
	}
	log.Printf("participant_limit_transport_failure participant_key=%s path_kind=%s error=%q",
		participantKey, kind, truncateError(err))
	l.persistThrottledStateLocked(participantKey, l.participants[participantKey], participantStatusTransport)
}

func isEOFTransportFailure(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	return strings.Contains(strings.ToLower(err.Error()), "eof")
}

func truncateError(err error) string {
	if err == nil {
		return ""
	}
	s := err.Error()
	if len(s) > 200 {
		return s[:200]
	}
	return s
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
	if state.emptyStreamStreak >= l.emptyStreamQuarantineThreshold {
		l.applyQuarantineLocked(participantKey, now.Add(l.emptyStreamQuarantine), now)
		state.emptyStreamStreak = 0
		state.eofTransportFailureStreak = 0
		log.Printf("participant_limit_empty_stream_quarantine participant_key=%s threshold=%d", participantKey, l.emptyStreamQuarantineThreshold)
		l.persistThrottledStateLocked(participantKey, state, participantStatusEmptyStream)
		return
	}
	log.Printf("participant_limit_empty_stream_streak participant_key=%s streak=%d", participantKey, state.emptyStreamStreak)
	l.persistThrottledStateLocked(participantKey, state, participantStatusEmptyStream)
}

// ObserveStalledWinner records a host that won the race, emitted some content,
// then stalled long enough to fail the request. This is treated as an immediate
// short quarantine because it is user-visible breakage, not just a loser-side
// transport blip.
func (l *ParticipantRequestLimiter) ObserveStalledWinner(participantKey string) {
	if participantKey == "" {
		return
	}
	now := time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()

	state := l.ensureStateLocked(participantKey, now)
	l.applyQuarantineLocked(participantKey, now.Add(l.stalledWinnerQuarantine), now)
	state.emptyStreamStreak = 0
	state.eofTransportFailureStreak = 0
	log.Printf("participant_limit_stalled_winner_quarantine participant_key=%s", participantKey)
	l.persistThrottledStateLocked(participantKey, state, participantStatusStalledWinner)
}

// ObserveSuccessfulInference clears accumulated soft-failure streaks and
// advances post-quarantine probation after a good finished response.
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
	if state.emptyStreamStreak == 0 && state.eofTransportFailureStreak == 0 && state.probationSuccessesRemaining == 0 {
		return
	}
	state.emptyStreamStreak = 0
	state.eofTransportFailureStreak = 0
	if state.probationSuccessesRemaining > 0 {
		state.probationSuccessesRemaining--
	}
	if state.tokens >= l.burst && state.quarantineUntil.IsZero() {
		if state.probationSuccessesRemaining > 0 {
			return
		}
		delete(l.participants, participantKey)
		l.persistDeleteLocked(participantKey)
		return
	}
	l.persistThrottledStateLocked(participantKey, state, participantStatusTransport)
}

// ClearQuarantine removes quarantine and resets the token bucket for the
// given participant, making it immediately available for requests while
// keeping it on the same post-quarantine probation path as natural expiry.
// Returns true if the participant had state to clear.
func (l *ParticipantRequestLimiter) ClearQuarantine(participantKey string) bool {
	if participantKey == "" {
		return false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	state, ok := l.participants[participantKey]
	if !ok {
		return false
	}
	now := time.Now()
	state.tokens = l.burst
	state.lastRefill = now
	state.quarantineUntil = time.Time{}
	state.emptyStreamStreak = 0
	state.eofTransportFailureStreak = 0
	state.probationSuccessesRemaining = participantProbationSuccessesAfterQuarantine
	l.persistDeleteLocked(participantKey)
	log.Printf("participant_quarantine_cleared participant_key=%s", participantKey)
	return true
}

func (l *ParticipantRequestLimiter) SetMetrics(metrics *DevshardMetrics) {
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

func (l *ParticipantRequestLimiter) Snapshot(participantKeys []string) map[string]ParticipantThrottleSnapshot {
	snapshots := make(map[string]ParticipantThrottleSnapshot, len(participantKeys))
	if l == nil {
		for _, key := range participantKeys {
			key = strings.TrimSpace(key)
			if key == "" {
				continue
			}
			snapshots[key] = ParticipantThrottleSnapshot{
				RequestAllowed:       true,
				AvailableForCapacity: true,
			}
		}
		return snapshots
	}
	now := time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()

	seen := make(map[string]struct{}, len(participantKeys))
	for _, key := range participantKeys {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}

		state, tracked := l.participants[key]
		if !tracked {
			snapshots[key] = ParticipantThrottleSnapshot{
				Tracked:              false,
				Quarantined:          false,
				Blocked:              false,
				RequestAllowed:       true,
				AvailableForCapacity: true,
				Tokens:               l.burst,
				Burst:                l.burst,
			}
			continue
		}
		l.clearExpiredQuarantineIfAnyLocked(key, state, now)
		state, tracked = l.participants[key]
		if !tracked {
			snapshots[key] = ParticipantThrottleSnapshot{
				Tracked:              false,
				Quarantined:          false,
				Blocked:              false,
				RequestAllowed:       true,
				AvailableForCapacity: true,
				Tokens:               l.burst,
				Burst:                l.burst,
			}
			continue
		}

		quarantined := l.inQuarantineLocked(state, now)
		if !quarantined {
			l.refillLocked(state, now)
			if state.tokens >= l.burst && state.emptyStreamStreak == 0 && state.eofTransportFailureStreak == 0 {
				if l.probationActiveLocked(state) {
					snapshot := ParticipantThrottleSnapshot{
						Tracked:              true,
						Quarantined:          false,
						Blocked:              false,
						RequestAllowed:       true,
						AvailableForCapacity: true,
						Tokens:               state.tokens,
						Burst:                l.burst,
					}
					snapshots[key] = snapshot
					continue
				}
				delete(l.participants, key)
				l.persistDeleteLocked(key)
				snapshots[key] = ParticipantThrottleSnapshot{
					Tracked:              false,
					Quarantined:          false,
					Blocked:              false,
					RequestAllowed:       true,
					AvailableForCapacity: true,
					Tokens:               l.burst,
					Burst:                l.burst,
				}
				continue
			}
		}
		blocked := quarantined || state.tokens < 1
		available := !quarantined && (state.tokens >= l.burst || state.emptyStreamStreak > 0 || state.eofTransportFailureStreak > 0)
		snapshot := ParticipantThrottleSnapshot{
			Tracked:              true,
			Quarantined:          quarantined,
			Blocked:              blocked,
			RequestAllowed:       !blocked,
			AvailableForCapacity: available,
			Tokens:               state.tokens,
			Burst:                l.burst,
			EmptyStreamStreak:    state.emptyStreamStreak,
			EOFTransportStreak:   state.eofTransportFailureStreak,
		}
		if quarantined {
			snapshot.QuarantineUntil = state.quarantineUntil.UTC().Format(time.RFC3339)
			snapshot.QuarantineRemainingMS = state.quarantineUntil.Sub(now).Milliseconds()
		}
		snapshots[key] = snapshot
	}
	return snapshots
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
	if err := l.store.SaveParticipantThrottle(key, state.tokens, state.lastRefill, status, quar, state.emptyStreamStreak, state.eofTransportFailureStreak); err != nil {
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

func (l *ParticipantRequestLimiter) IsRecentlyQuarantined(participantKey string) bool {
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
	state, tracked = l.participants[participantKey]
	if !tracked {
		return false
	}
	if l.inQuarantineLocked(state, now) {
		return true
	}
	if l.probationActiveLocked(state) {
		return true
	}
	return false
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
		if state.emptyStreamStreak > 0 || state.eofTransportFailureStreak > 0 {
			return true
		}
		if l.probationActiveLocked(state) {
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

func (l *ParticipantRequestLimiter) probationActiveLocked(state *participantRequestState) bool {
	return state != nil && state.probationSuccessesRemaining > 0
}

func (l *ParticipantRequestLimiter) applyQuarantineLocked(participantKey string, end time.Time, now time.Time) {
	st := l.ensureStateLocked(participantKey, now)
	st.tokens = 0
	st.lastRefill = now
	st.probationSuccessesRemaining = 0
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
		state.eofTransportFailureStreak = 0
		state.probationSuccessesRemaining = participantProbationSuccessesAfterQuarantine
		l.persistDeleteLocked(key)
		log.Printf("participant_quarantine_ended participant_key=%s", key)
	}
}

func (l *ParticipantRequestLimiter) participantHTTPQuarantine(path string, statusCode int, body string) time.Duration {
	switch {
	case isParticipantThrottleStatus(statusCode):
		return l.httpThrottleQuarantine
	case statusCode == http.StatusUnauthorized && participantPathKind(path) == "inference" && strings.Contains(strings.ToLower(body), "timestamp drift"):
		return l.transportFailureQuarantine
	case (statusCode == http.StatusNotFound || statusCode == http.StatusForbidden) && participantPathKind(path) == "inference":
		return l.transportFailureQuarantine
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
