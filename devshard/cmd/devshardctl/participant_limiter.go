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
	// participantFailureStrikeThreshold is the unified per-model strike count
	// where soft bad signals move from probation to quarantine.
	participantFailureStrikeThreshold = 3
	emptyStreamQuarantineThreshold    = participantFailureStrikeThreshold
	eofTransportFailureThreshold      = participantFailureStrikeThreshold
	// participantStrikesAfterQuarantine keeps recently recovered hosts one bad
	// signal away from re-quarantine while they prove they can finish normally.
	participantStrikesAfterQuarantine = participantFailureStrikeThreshold - 1
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

type participantQuarantineMode int

const (
	participantQuarantineNone participantQuarantineMode = iota
	participantQuarantineProbe
	participantQuarantineShadow
)

func participantQuarantineModeFromStatus(status int) participantQuarantineMode {
	switch status {
	case participantStatusEmptyStream, participantStatusStalledWinner:
		return participantQuarantineShadow
	default:
		return participantQuarantineProbe
	}
}

func (m participantQuarantineMode) String() string {
	switch m {
	case participantQuarantineProbe:
		return "probe"
	case participantQuarantineShadow:
		return "shadow"
	default:
		return ""
	}
}

func normalizeModelID(modelID string) string {
	return strings.TrimSpace(modelID)
}

func normalizeModelIDs(modelIDs []string) []string {
	if len(modelIDs) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(modelIDs))
	out := make([]string, 0, len(modelIDs))
	for _, modelID := range modelIDs {
		modelID = normalizeModelID(modelID)
		if modelID == "" {
			continue
		}
		if _, ok := seen[modelID]; ok {
			continue
		}
		seen[modelID] = struct{}{}
		out = append(out, modelID)
	}
	sort.Strings(out)
	return out
}

func splitModelIDs(modelIDs string) []string {
	modelIDs = strings.TrimSpace(modelIDs)
	if modelIDs == "" {
		return nil
	}
	return normalizeModelIDs(strings.Split(modelIDs, ","))
}

func modelIDSet(modelIDs []string) map[string]struct{} {
	modelIDs = normalizeModelIDs(modelIDs)
	if len(modelIDs) == 0 {
		return nil
	}
	set := make(map[string]struct{}, len(modelIDs))
	for _, modelID := range modelIDs {
		set[modelID] = struct{}{}
	}
	return set
}

func modelIDsFromSet(set map[string]struct{}) []string {
	if len(set) == 0 {
		return nil
	}
	modelIDs := make([]string, 0, len(set))
	for modelID := range set {
		modelIDs = append(modelIDs, modelID)
	}
	sort.Strings(modelIDs)
	return modelIDs
}

func stateAppliesToModel(state *participantRequestState, modelID string) bool {
	if state == nil || len(state.modelIDs) == 0 {
		return true
	}
	modelID = normalizeModelID(modelID)
	if modelID == "" {
		return true
	}
	_, ok := state.modelIDs[modelID]
	return ok
}

var sharedParticipantRequestLimiter = NewParticipantRequestLimiter(
	defaultParticipantRequestBurst,
	defaultParticipantRequestRecoveryPerMinute,
)

type modelScopedParticipantAdmission struct {
	limiter *ParticipantRequestLimiter
	modelID string
}

func (a modelScopedParticipantAdmission) AllowRequest(participantKey, path string) error {
	if a.limiter == nil {
		return nil
	}
	return a.limiter.AllowRequestForModel(participantKey, a.modelID, path)
}

func (a modelScopedParticipantAdmission) ObserveResult(participantKey, path string, statusCode int) {
	if a.limiter == nil {
		return
	}
	a.limiter.ObserveResultForModel(participantKey, a.modelID, path, statusCode)
}

func (a modelScopedParticipantAdmission) ObserveResultWithBody(participantKey, path string, statusCode int, body string) {
	if a.limiter == nil {
		return
	}
	a.limiter.ObserveResultWithBodyForModel(participantKey, a.modelID, path, statusCode, body)
}

func (a modelScopedParticipantAdmission) ObserveTransportFailure(participantKey, path string, err error) {
	if a.limiter == nil {
		return
	}
	a.limiter.ObserveTransportFailureForModel(participantKey, a.modelID, path, err)
}

func DefaultParticipantThrottleSettings() ParticipantThrottleSettings {
	return ParticipantThrottleSettings{
		RequestBurst:                   defaultParticipantRequestBurst,
		RecoveryPerMinute:              defaultParticipantRequestRecoveryPerMinute,
		HTTPQuarantineMS:               httpThrottleQuarantine.Milliseconds(),
		TransportFailureQuarantineMS:   transportFailureQuarantine.Milliseconds(),
		EmptyStreamQuarantineMS:        emptyStreamQuarantine.Milliseconds(),
		StalledWinnerQuarantineMS:      stalledWinnerQuarantine.Milliseconds(),
		EmptyStreamQuarantineThreshold: participantFailureStrikeThreshold,
		EOFTransportFailureThreshold:   participantFailureStrikeThreshold,
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
	SaveParticipantThrottle(key string, modelIDs []string, tokens float64, lastRefillAt time.Time, status int, quarantineUntil time.Time, failureStrikes int) error
	DeleteParticipantThrottle(key string) error
}

// ParticipantRequestLimiter is a reactive, per-host limiter. Probe quarantine
// is no-send and burns silent probes; shadow quarantine still sends real
// attempts but marks them no-winner. Longer of the overlapping quarantines
// wins. Legacy rows without quarantine use the token-bucket refill only.
type ParticipantRequestLimiter struct {
	mu                         sync.Mutex
	burst                      float64
	recoveryPerSecond          float64
	httpThrottleQuarantine     time.Duration
	transportFailureQuarantine time.Duration
	emptyStreamQuarantine      time.Duration
	stalledWinnerQuarantine    time.Duration
	failureStrikeThreshold     int
	participants               map[string]*participantRequestState
	metrics                    *DevshardMetrics
	store                      ParticipantThrottleStore
}

type participantRequestState struct {
	tokens          float64
	lastRefill      time.Time
	modelIDs        map[string]struct{} // empty means all models / legacy global
	quarantineUntil time.Time           // non-zero: wall-clock unavailability
	quarantineMode  participantQuarantineMode
	failureStrikes  int
}

type ParticipantThrottleSnapshot struct {
	Tracked               bool     `json:"tracked"`
	Quarantined           bool     `json:"quarantined"`
	Blocked               bool     `json:"blocked"`
	RequestAllowed        bool     `json:"request_allowed"`
	AvailableForCapacity  bool     `json:"available_for_capacity"`
	QuarantineMode        string   `json:"quarantine_mode,omitempty"`
	ModelIDs              []string `json:"model_ids,omitempty"`
	ShadowQuarantined     bool     `json:"shadow_quarantined,omitempty"`
	ProbeQuarantined      bool     `json:"probe_quarantined,omitempty"`
	Probationary          bool     `json:"probationary,omitempty"`
	Tokens                float64  `json:"tokens"`
	Burst                 float64  `json:"burst"`
	QuarantineUntil       string   `json:"quarantine_until,omitempty"`
	QuarantineRemainingMS int64    `json:"quarantine_remaining_ms,omitempty"`
	FailureStrikes        int      `json:"failure_strikes,omitempty"`
}

type participantNoWinnerStatus struct {
	reason         string
	quarantineMode string
	failureStrikes int
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
	l.failureStrikeThreshold = settings.EmptyStreamQuarantineThreshold
	if settings.EOFTransportFailureThreshold < l.failureStrikeThreshold {
		l.failureStrikeThreshold = settings.EOFTransportFailureThreshold
	}
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
	l.LoadStateWithQuarantine(key, nil, tokens, lastRefill, 0, time.Time{}, 0)
}

// LoadStateWithQuarantine is like LoadState but supports persisted quarantine
// and upgrades legacy 429/503 rows to a quarantine end time when needed.
func (l *ParticipantRequestLimiter) LoadStateWithQuarantine(key string, modelIDs []string, tokens float64, lastRefill time.Time, status int, quarantineFromDB time.Time, failureStrikes int) {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := time.Now()

	if !quarantineFromDB.IsZero() {
		if now.Before(quarantineFromDB) {
			if failureStrikes < l.failureStrikeThreshold {
				failureStrikes = l.failureStrikeThreshold
			}
			l.participants[key] = &participantRequestState{
				tokens:          0,
				lastRefill:      now,
				modelIDs:        modelIDSet(modelIDs),
				quarantineUntil: quarantineFromDB,
				quarantineMode:  participantQuarantineModeFromStatus(status),
				failureStrikes:  failureStrikes,
			}
			log.Printf("participant_limit_loaded_from_db participant_key=%s quarantine_until=%s", key, quarantineFromDB.Format(time.RFC3339))
			return
		}
		// Already expired; resume in persisted probation instead of deleting
		// so rebooting cannot bypass the post-quarantine proof period.
		if failureStrikes < participantStrikesAfterQuarantine {
			failureStrikes = participantStrikesAfterQuarantine
		}
		tokens = l.burst
		quarantineFromDB = time.Time{}
		status = participantStatusTransport
		log.Printf("participant_limit_stale_on_load participant_key=%s", key)
	}

	elapsed := now.Sub(lastRefill).Seconds()
	if elapsed > 0 {
		tokens += elapsed * l.recoveryPerSecond
	}
	if tokens >= l.burst && failureStrikes == 0 {
		if l.store != nil {
			if err := l.store.DeleteParticipantThrottle(key); err != nil {
				log.Printf("participant_throttle_cleanup_failed participant_key=%s error=%v", key, err)
			}
		}
		log.Printf("participant_limit_recovered_on_load participant_key=%s", key)
		return
	}

	st := &participantRequestState{
		tokens:          tokens,
		lastRefill:      now,
		modelIDs:        modelIDSet(modelIDs),
		quarantineUntil: time.Time{},
		quarantineMode:  participantQuarantineNone,
		failureStrikes:  failureStrikes,
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
			st.quarantineMode = participantQuarantineProbe
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
	return l.AllowRequestForModel(participantKey, "", "")
}

func (l *ParticipantRequestLimiter) AllowRequestForModel(participantKey, modelID, _ string) error {
	if participantKey == "" {
		return nil
	}
	if !capacityAwareLimitsEnabled() && relaxedPoCBypassActive() {
		return nil
	}
	if l.allowForModel(participantKey, modelID, time.Now()) {
		return nil
	}
	if l.metrics != nil {
		l.metrics.RecordParticipantLimitRejection(participantKey, normalizeModelID(modelID), "transport_request")
	}
	log.Printf("participant_limit_rejected participant_key=%s", participantKey)
	return &ParticipantRateLimitError{ParticipantKey: participantKey}
}

func (l *ParticipantRequestLimiter) allow(participantKey string, now time.Time) bool {
	return l.allowForModel(participantKey, "", now)
}

func (l *ParticipantRequestLimiter) allowForModel(participantKey string, modelID string, now time.Time) bool {
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
	if !stateAppliesToModel(state, modelID) {
		return true
	}

	if l.inProbeQuarantineLocked(state, now) {
		return false
	}
	if l.inShadowQuarantineLocked(state, now) {
		return true
	}
	l.refillLocked(state, now)
	if state.tokens >= l.burst {
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
	l.ObserveResultWithBodyForModel(participantKey, "", path, statusCode, "")
}

func (l *ParticipantRequestLimiter) ObserveResultWithBody(participantKey, path string, statusCode int, body string) {
	l.ObserveResultWithBodyForModel(participantKey, "", path, statusCode, body)
}

func (l *ParticipantRequestLimiter) ObserveResultForModel(participantKey, modelID, path string, statusCode int) {
	l.ObserveResultWithBodyForModel(participantKey, modelID, path, statusCode, "")
}

func (l *ParticipantRequestLimiter) ObserveResultWithBodyForModel(participantKey, modelID, path string, statusCode int, body string) {
	if participantKey == "" || statusCode <= 0 {
		return
	}
	if l.metrics != nil && statusCode >= http.StatusBadRequest {
		l.metrics.RecordParticipantTransportError(participantKey, normalizeModelID(modelID), participantPathKind(path), statusCode)
	}
	quarantineFor := l.participantHTTPQuarantine(path, statusCode, body)
	if quarantineFor == 0 {
		return
	}

	now := time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()
	l.applyQuarantineLocked(participantKey, modelID, now.Add(quarantineFor), now, participantQuarantineProbe)
	l.recordQuarantineTransition(participantKey, modelID, participantQuarantineProbe.String(), participantHTTPQuarantineReason(path, statusCode, body))

	log.Printf("participant_limit_activated participant_key=%s status=%d path_kind=%s",
		participantKey, statusCode, participantPathKind(path))

	l.persistThrottledStateLocked(participantKey, l.participants[participantKey], statusCode)
}

// ObserveTransportFailure records that a request to this host never received an
// HTTP response. Only inference-path failures (/chat/completions) trigger
// quarantine. EOF-style inference failures require consecutive strikes; other
// inference transport failures still quarantine immediately.
func (l *ParticipantRequestLimiter) ObserveTransportFailure(participantKey, path string, err error) {
	l.ObserveTransportFailureForModel(participantKey, "", path, err)
}

func (l *ParticipantRequestLimiter) ObserveTransportFailureForModel(participantKey, modelID, path string, err error) {
	if participantKey == "" {
		return
	}
	kind := participantPathKind(path)
	if l.metrics != nil {
		l.metrics.RecordParticipantTransportError(participantKey, normalizeModelID(modelID), kind, 0)
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
		l.addModelLocked(state, modelID)
		state.failureStrikes++
		if state.failureStrikes >= l.failureStrikeThreshold {
			l.applyQuarantineLocked(participantKey, modelID, now.Add(l.transportFailureQuarantine), now, participantQuarantineProbe)
			l.recordQuarantineTransition(participantKey, modelID, participantQuarantineProbe.String(), "eof_transport_quarantine")
			log.Printf("participant_limit_eof_transport_quarantine participant_key=%s model_id=%q reason=eof_transport strikes=%d threshold=%d quarantine_mode=%s error=%q",
				participantKey, normalizeModelID(modelID), state.failureStrikes, l.failureStrikeThreshold, participantQuarantineProbe.String(), truncateError(err))
			l.persistThrottledStateLocked(participantKey, state, participantStatusEOFTransport)
			return
		}
		log.Printf("participant_limit_eof_transport_streak participant_key=%s model_id=%q reason=eof_transport strikes=%d threshold=%d error=%q",
			participantKey, normalizeModelID(modelID), state.failureStrikes, l.failureStrikeThreshold, truncateError(err))
		l.persistThrottledStateLocked(participantKey, state, participantStatusEOFTransport)
		return
	}

	l.applyQuarantineLocked(participantKey, modelID, now.Add(l.transportFailureQuarantine), now, participantQuarantineProbe)
	l.recordQuarantineTransition(participantKey, modelID, participantQuarantineProbe.String(), "transport_failure_quarantine")
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

// ObserveModelBurnEmpty: empty stream caused by model behavior, not host
// fault. Telemetry-only — no streak, no quarantine.
func (l *ParticipantRequestLimiter) ObserveModelBurnEmpty(participantKey, modelID string) {
	if participantKey == "" {
		return
	}
	log.Printf("participant_limit_model_burn_empty participant_key=%s model_id=%q", participantKey, normalizeModelID(modelID))
}

// ObserveEmptyStream increments the unified failure-strike counter for a
// participant. On the threshold strike, the participant enters shadow
// quarantine.
func (l *ParticipantRequestLimiter) ObserveEmptyStream(participantKey string) {
	l.ObserveEmptyStreamForModel(participantKey, "")
}

func (l *ParticipantRequestLimiter) ObserveEmptyStreamForModel(participantKey, modelID string) {
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
	l.addModelLocked(state, modelID)
	state.failureStrikes++
	if state.failureStrikes >= l.failureStrikeThreshold {
		l.applyQuarantineLocked(participantKey, modelID, now.Add(l.emptyStreamQuarantine), now, participantQuarantineShadow)
		l.recordQuarantineTransition(participantKey, modelID, participantQuarantineShadow.String(), "empty_stream_quarantine")
		log.Printf("participant_limit_empty_stream_quarantine participant_key=%s model_id=%q reason=empty_stream strikes=%d threshold=%d quarantine_mode=%s",
			participantKey, normalizeModelID(modelID), state.failureStrikes, l.failureStrikeThreshold, participantQuarantineShadow.String())
		l.persistThrottledStateLocked(participantKey, state, participantStatusEmptyStream)
		return
	}
	log.Printf("participant_limit_empty_stream_streak participant_key=%s model_id=%q reason=empty_stream strikes=%d threshold=%d",
		participantKey, normalizeModelID(modelID), state.failureStrikes, l.failureStrikeThreshold)
	l.persistThrottledStateLocked(participantKey, state, participantStatusEmptyStream)
}

// ObserveStalledWinner records a host that won the race, emitted some content,
// then stalled long enough to fail the request. This is treated as an immediate
// short quarantine because it is user-visible breakage, not just a loser-side
// transport blip.
func (l *ParticipantRequestLimiter) ObserveStalledWinner(participantKey string) {
	l.ObserveStalledWinnerForModel(participantKey, "")
}

func (l *ParticipantRequestLimiter) ObserveStalledWinnerForModel(participantKey, modelID string) {
	if participantKey == "" {
		return
	}
	now := time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()

	state := l.ensureStateLocked(participantKey, now)
	l.applyQuarantineLocked(participantKey, modelID, now.Add(l.stalledWinnerQuarantine), now, participantQuarantineShadow)
	l.recordQuarantineTransition(participantKey, modelID, participantQuarantineShadow.String(), "stalled_winner_quarantine")
	log.Printf("participant_limit_stalled_winner_quarantine participant_key=%s", participantKey)
	l.persistThrottledStateLocked(participantKey, state, participantStatusStalledWinner)
}

// ObserveSuccessfulInference decrements the unified failure-strike counter after
// a good finished response. When the counter reaches zero, probation ends.
func (l *ParticipantRequestLimiter) ObserveSuccessfulInference(participantKey string) {
	l.ObserveSuccessfulInferenceForModel(participantKey, "")
}

func (l *ParticipantRequestLimiter) ObserveSuccessfulInferenceForModel(participantKey, modelID string) {
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
	if !stateAppliesToModel(state, modelID) {
		return
	}
	if state.failureStrikes == 0 {
		return
	}
	if state.failureStrikes > 0 {
		state.failureStrikes--
	}
	if state.tokens >= l.burst && state.quarantineUntil.IsZero() {
		if state.failureStrikes > 0 {
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
	state.quarantineMode = participantQuarantineNone
	state.failureStrikes = participantStrikesAfterQuarantine
	l.persistThrottledStateLocked(participantKey, state, participantStatusTransport)
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
		if l.inProbeQuarantineLocked(state, now) {
			blocked = append(blocked, key)
			continue
		}
		if l.inShadowQuarantineLocked(state, now) {
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
		probeQuarantined := l.inProbeQuarantineLocked(state, now)
		shadowQuarantined := l.inShadowQuarantineLocked(state, now)
		probationary := l.probationActiveLocked(state)
		if !quarantined {
			l.refillLocked(state, now)
			if state.tokens >= l.burst && state.failureStrikes == 0 {
				if probationary {
					snapshot := ParticipantThrottleSnapshot{
						Tracked:              true,
						Quarantined:          false,
						Blocked:              false,
						RequestAllowed:       true,
						AvailableForCapacity: true,
						ShadowQuarantined:    true,
						Probationary:         true,
						ModelIDs:             modelIDsFromSet(state.modelIDs),
						Tokens:               state.tokens,
						Burst:                l.burst,
						FailureStrikes:       state.failureStrikes,
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
		blocked := probeQuarantined || (!shadowQuarantined && state.tokens < 1)
		available := !probeQuarantined && (shadowQuarantined || state.tokens >= l.burst || state.failureStrikes > 0)
		snapshot := ParticipantThrottleSnapshot{
			Tracked:              true,
			Quarantined:          quarantined,
			Blocked:              blocked,
			RequestAllowed:       !blocked,
			AvailableForCapacity: available,
			QuarantineMode:       state.quarantineMode.String(),
			ModelIDs:             modelIDsFromSet(state.modelIDs),
			ShadowQuarantined:    shadowQuarantined,
			ProbeQuarantined:     probeQuarantined,
			Probationary:         probationary,
			Tokens:               state.tokens,
			Burst:                l.burst,
			FailureStrikes:       state.failureStrikes,
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
	if err := l.store.SaveParticipantThrottle(key, modelIDsFromSet(state.modelIDs), state.tokens, state.lastRefill, status, quar, state.failureStrikes); err != nil {
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
		if l.inProbeQuarantineLocked(state, now) {
			n++
			continue
		}
		if l.inShadowQuarantineLocked(state, now) {
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
	return l.IsRecentlyQuarantinedForModel(participantKey, "")
}

func (l *ParticipantRequestLimiter) IsRecentlyQuarantinedForModel(participantKey, modelID string) bool {
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
	if !stateAppliesToModel(state, modelID) {
		return false
	}
	if l.inQuarantineLocked(state, now) {
		return true
	}
	return l.probationActiveLocked(state)
}

// IsShadowQuarantined reports whether the participant should receive only
// no-winner attempts. That includes temporary shadow quarantine and the
// post-quarantine probation window.
func (l *ParticipantRequestLimiter) IsShadowQuarantined(participantKey string) bool {
	return l.IsShadowQuarantinedForModel(participantKey, "")
}

func (l *ParticipantRequestLimiter) IsShadowQuarantinedForModel(participantKey, modelID string) bool {
	_, ok := l.NoWinnerStatusForModel(participantKey, modelID)
	return ok
}

func (l *ParticipantRequestLimiter) NoWinnerStatusForModel(participantKey, modelID string) (participantNoWinnerStatus, bool) {
	if participantKey == "" {
		return participantNoWinnerStatus{}, false
	}
	now := time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()

	state, tracked := l.participants[participantKey]
	if !tracked {
		return participantNoWinnerStatus{}, false
	}
	l.clearExpiredQuarantineIfAnyLocked(participantKey, state, now)
	state, tracked = l.participants[participantKey]
	if !tracked {
		return participantNoWinnerStatus{}, false
	}
	if !stateAppliesToModel(state, modelID) {
		return participantNoWinnerStatus{}, false
	}
	if l.inShadowQuarantineLocked(state, now) {
		return participantNoWinnerStatus{
			reason:         "shadow_quarantine",
			quarantineMode: participantQuarantineShadow.String(),
			failureStrikes: state.failureStrikes,
		}, true
	}
	if l.probationActiveLocked(state) {
		return participantNoWinnerStatus{
			reason:         "probation",
			failureStrikes: state.failureStrikes,
		}, true
	}
	return participantNoWinnerStatus{}, false
}

// IsAvailable reports whether the participant is currently considered
// available for capacity-aware routing. During quarantine the host is
// unavailable; after legacy refills, full burst means available.
func (l *ParticipantRequestLimiter) IsAvailable(participantKey string) bool {
	return l.IsAvailableForModel(participantKey, "")
}

func (l *ParticipantRequestLimiter) IsAvailableForModel(participantKey, modelID string) bool {
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
	if !stateAppliesToModel(state, modelID) {
		return true
	}
	if l.inProbeQuarantineLocked(state, now) {
		return false
	}
	if l.inShadowQuarantineLocked(state, now) {
		return true
	}
	l.refillLocked(state, now)
	if state.tokens >= l.burst {
		if state.failureStrikes > 0 {
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

// IsBlocked reports whether AllowRequest would currently reject. Shadow
// quarantine is intentionally not blocked: it still sends real attempts, but
// redundancy marks them no-winner.
func (l *ParticipantRequestLimiter) IsBlocked(participantKey string) bool {
	return l.IsBlockedForModel(participantKey, "")
}

func (l *ParticipantRequestLimiter) IsBlockedForModel(participantKey, modelID string) bool {
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
	if !stateAppliesToModel(state, modelID) {
		return false
	}
	if l.inProbeQuarantineLocked(state, now) {
		return true
	}
	if l.inShadowQuarantineLocked(state, now) {
		return false
	}
	l.refillLocked(state, now)
	return state.tokens < 1
}

func (l *ParticipantRequestLimiter) inQuarantineLocked(state *participantRequestState, now time.Time) bool {
	return !state.quarantineUntil.IsZero() && now.Before(state.quarantineUntil)
}

func (l *ParticipantRequestLimiter) inProbeQuarantineLocked(state *participantRequestState, now time.Time) bool {
	return l.inQuarantineLocked(state, now) && state.quarantineMode != participantQuarantineShadow
}

func (l *ParticipantRequestLimiter) inShadowQuarantineLocked(state *participantRequestState, now time.Time) bool {
	return l.inQuarantineLocked(state, now) && state.quarantineMode == participantQuarantineShadow
}

func (l *ParticipantRequestLimiter) probationActiveLocked(state *participantRequestState) bool {
	return state != nil && state.failureStrikes > 0 && state.quarantineUntil.IsZero()
}

func (l *ParticipantRequestLimiter) applyQuarantineLocked(participantKey, modelID string, end time.Time, now time.Time, mode participantQuarantineMode) {
	st := l.ensureStateLocked(participantKey, now)
	l.addModelLocked(st, modelID)
	if mode == participantQuarantineProbe {
		st.tokens = 0
	} else if st.tokens < l.burst {
		st.tokens = l.burst
	}
	st.lastRefill = now
	if st.failureStrikes < l.failureStrikeThreshold {
		st.failureStrikes = l.failureStrikeThreshold
	}
	if st.quarantineUntil.IsZero() || end.After(st.quarantineUntil) {
		st.quarantineUntil = end
		st.quarantineMode = mode
	}
}

func (l *ParticipantRequestLimiter) addModelLocked(state *participantRequestState, modelID string) {
	if state == nil {
		return
	}
	modelID = normalizeModelID(modelID)
	if modelID == "" || len(state.modelIDs) == 0 && state.quarantineMode != participantQuarantineNone {
		return
	}
	if state.modelIDs == nil {
		state.modelIDs = make(map[string]struct{}, 1)
	}
	state.modelIDs[modelID] = struct{}{}
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
		state.quarantineMode = participantQuarantineNone
		state.tokens = l.burst
		state.lastRefill = now
		state.failureStrikes = participantStrikesAfterQuarantine
		l.recordQuarantineTransition(key, "", "probation", "quarantine_expired")
		l.persistThrottledStateLocked(key, state, participantStatusTransport)
		log.Printf("participant_quarantine_ended participant_key=%s", key)
	}
}

func (l *ParticipantRequestLimiter) recordQuarantineTransition(participantKey, modelID, mode, reason string) {
	if l == nil || l.metrics == nil {
		return
	}
	l.metrics.RecordGatewayQuarantineTransition(participantKey, normalizeModelID(modelID), mode, reason)
}

func participantHTTPQuarantineReason(path string, statusCode int, body string) string {
	switch {
	case isParticipantThrottleStatus(statusCode):
		return "http_throttle_quarantine"
	case statusCode == http.StatusUnauthorized && participantPathKind(path) == "inference" && strings.Contains(strings.ToLower(body), "timestamp drift"):
		return "http_timestamp_drift"
	case statusCode == http.StatusNotFound && participantPathKind(path) == "inference":
		return "http_not_found"
	case statusCode == http.StatusForbidden && participantPathKind(path) == "inference":
		return "http_forbidden"
	default:
		return "transport_failure_quarantine"
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
