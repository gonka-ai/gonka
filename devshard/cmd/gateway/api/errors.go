package api

import (
	"errors"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	json "github.com/goccy/go-json"

	"devshard/cmd/gateway/chain"
	"devshard/cmd/gateway/config"
	"devshard/cmd/gateway/engine"
	"devshard/cmd/gateway/escrow"
	"devshard/cmd/gateway/filters"
	"devshard/cmd/gateway/internal/logkey"
	"devshard/cmd/gateway/internal/strictjson"
	"devshard/cmd/gateway/limits"
	"devshard/cmd/gateway/registry"
	"devshard/cmd/gateway/scheduler"
	"devshard/logging"
	"devshard/transport"
	"devshard/types"
)

const noHostRetryAfter = time.Second

var (
	ErrUnknownDevshard = errors.New("unknown devshard")

	ErrUnknownParticipant = errors.New("unknown participant")

	ErrPrivateKeyEnvRequired = errors.New("private_key_env is required; a raw private_key is not accepted")

	ErrDevshardExists = errors.New("devshard already registered")

	ErrDevshardNotActivatable = errors.New("devshard cannot be activated")

	ErrInvalidEscrowID = errors.New("escrow id is not a chain escrow id")
)

// UnsupportedModelError lists the routable models so a client can correct the request in one trip.
type UnsupportedModelError struct {
	Model     string
	Supported []string
}

func (e *UnsupportedModelError) Error() string {
	return fmt.Sprintf("unsupported model %q; supported models: %s", e.Model, strings.Join(e.Supported, ", "))
}

// ModelUnavailableError is a gateway that is not ready, not a request that is wrong.
type ModelUnavailableError struct{ Model string }

func (e *ModelUnavailableError) Error() string {
	return fmt.Sprintf("model %q is temporarily unavailable: no devshard is currently routable", e.Model)
}

type AccessDeniedError struct {
	Model   string
	Message string
}

func (e *AccessDeniedError) Error() string { return e.Message }

type BlockedError struct {
	Reason     chain.BlockReason
	Phase      chain.EpochPhase
	Confirming chain.ConfirmationPoCPhase
}

// ChainStaleError says the chain snapshot is too old to serve under. See README.md, "Errors and statuses".
type ChainStaleError struct {
	Age time.Duration
}

func (e *ChainStaleError) Error() string {
	return fmt.Sprintf("chain snapshot is %s old; the gateway cannot tell which phase it is serving under", e.Age.Round(time.Second))
}

func (e *BlockedError) Error() string {
	return "devshard temporarily unavailable during " + e.phaseName()
}

func (e *BlockedError) phaseName() string {
	switch e.Reason {
	case chain.BlockReasonPoC:
		switch e.Phase {
		case chain.EpochPhasePoCGenerate:
			return "PoC generation"
		case chain.EpochPhasePoCGenerateWindDown:
			return "PoC generation wind down"
		case chain.EpochPhasePoCValidate:
			return "PoC validation"
		case chain.EpochPhasePoCValidateWindDown:
			return "PoC validation wind down"
		}
		return "PoC"
	case chain.BlockReasonConfirmationPoC:
		switch e.Confirming {
		case chain.ConfirmationPoCGracePeriod:
			return "confirmation PoC grace period"
		case chain.ConfirmationPoCGeneration:
			return "confirmation PoC generation"
		case chain.ConfirmationPoCValidation:
			return "confirmation PoC validation"
		}
		return "confirmation PoC"
	}
	if e.Reason != chain.BlockReasonNone {
		return strings.ReplaceAll(string(e.Reason), "_", " ")
	}
	if e.Phase != chain.EpochPhaseUnknown {
		return string(e.Phase)
	}
	return "chain admission controls"
}

func tooLargeForHosts(bodyBytes int) error {
	return &filters.RejectError{
		Status: http.StatusRequestEntityTooLarge,
		Message: fmt.Sprintf("request body of %d bytes does not fit the %d-byte limit once encoded for a host, which also carries this escrow's catch-up",
			bodyBytes, transport.MaxHostRequestBytes-hostCatchUpReserveBytes),
	}
}

func statusForError(err error) int {
	if err == nil {
		return http.StatusOK
	}
	if status := filters.ErrorStatus(err, 0); status != 0 {
		return status
	}
	var unsupported *UnsupportedModelError
	var tooLong *ContextLengthError
	if errors.As(err, &unsupported) || errors.As(err, &tooLong) {
		return http.StatusBadRequest
	}
	if modelUnavailable(err) {
		return http.StatusServiceUnavailable
	}
	var denied *AccessDeniedError
	if errors.As(err, &denied) {
		return http.StatusUnauthorized
	}
	var blocked *BlockedError
	if errors.As(err, &blocked) {
		return http.StatusServiceUnavailable
	}
	if chainUnreadable(err) {
		return http.StatusServiceUnavailable
	}
	// Our limiter is a quota the caller exceeded (429); no room on the shard is not. See README.md, "Errors and statuses".
	if _, ours := rateLimited(err); ours {
		return http.StatusTooManyRequests
	}
	if shardHasNoRoom(err) {
		return http.StatusServiceUnavailable
	}
	switch {
	case errors.Is(err, scheduler.ErrEscrowGone):
		return http.StatusConflict
	case errors.Is(err, engine.ErrStopped), errors.Is(err, registry.ErrClosed):
		return http.StatusServiceUnavailable
	case errors.Is(err, ErrPrivateKeyEnvRequired), errors.Is(err, ErrInvalidEscrowID), errors.Is(err, config.ErrInvalid), errors.Is(err, escrow.ErrAmountBelowFloor):
		return http.StatusBadRequest
	case errors.Is(err, ErrDevshardNotActivatable):
		return http.StatusConflict
	case errors.Is(err, ErrUnknownDevshard), errors.Is(err, ErrUnknownParticipant):
		return http.StatusNotFound
	case errors.Is(err, escrow.ErrEscrowPruned), errors.Is(err, escrow.ErrEscrowSettledOnChain):
		return http.StatusGone
	case errors.Is(err, chain.ErrWalletUnderfunded):
		return http.StatusPaymentRequired
	case errors.Is(err, escrow.ErrDevshardBusy), errors.Is(err, escrow.ErrSettlementInFlight),
		errors.Is(err, ErrDevshardExists), errors.Is(err, registry.ErrDraining):
		return http.StatusConflict
	}
	return engine.StatusForError(err)
}

type errorEnvelope struct {
	Error errorDetail `json:"error"`
}

type errorDetail struct {
	Message string `json:"message"`
	Type    string `json:"type,omitempty"`
	Code    string `json:"code,omitempty"`
}

// writeError renders the JSON envelope; http.Error would send text/plain instead.
func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, errorEnvelope{Error: errorDetail{Message: message}})
}

func writeCodedError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, errorEnvelope{Error: errorDetail{Message: message, Code: code}})
}

// writeControlFailure is deliberately not writeErrorFor: 502 is wrong for a store this process owns.
func writeControlFailure(w http.ResponseWriter, err error) bool {
	if err == nil {
		return false
	}
	writeError(w, http.StatusInternalServerError, err.Error())
	return true
}

// rateLimited singles out the gateway limiter's own rejection: the status, Retry-After and the counter all ask for it.
func rateLimited(err error) (*limits.RateLimitError, bool) {
	var throttled *limits.RateLimitError
	if errors.As(err, &throttled) {
		return throttled, true
	}
	return nil, false
}

func chainUnreadable(err error) bool {
	var stale *ChainStaleError
	return errors.As(err, &stale)
}

// modelUnavailable singles out a refusal whose Retry-After is the escrow tick. See README.md, "Errors and statuses".
func modelUnavailable(err error) bool {
	var unavailable *ModelUnavailableError
	return errors.As(err, &unavailable)
}

// outOfFunds singles out a refusal that only a replacement escrow or a settling inference lifts. See README.md, "Errors and statuses".
func outOfFunds(err error) bool {
	return errors.Is(err, types.ErrInsufficientBalance)
}

func hostsUnavailable(err error) bool {
	return errors.Is(err, engine.ErrHostsUnavailable)
}

func shardHasNoRoom(err error) bool {
	return errors.Is(err, scheduler.ErrHostsBusy) ||
		errors.Is(err, scheduler.ErrNoEscrowCapacity) ||
		errors.Is(err, scheduler.ErrEscrowBusy) ||
		errors.Is(err, ErrResponseBufferFull)
}

// Retry-After is rounded up: a zero would tell a client to retry immediately, which a queue timeout does not mean.
func (s *Server) writeErrorFor(w http.ResponseWriter, err error) {
	throttled, ours := rateLimited(err)
	switch {
	case ours && throttled.RetryAfter > 0:
		seconds := int64(math.Ceil(throttled.RetryAfter.Seconds()))
		w.Header().Set("Retry-After", strconv.FormatInt(seconds, 10))
	case chainUnreadable(err):
		w.Header().Set("Retry-After", strconv.FormatInt(int64(chain.DefaultObserverPollInterval.Seconds()), 10))
	case modelUnavailable(err), outOfFunds(err):
		w.Header().Set("Retry-After", strconv.FormatInt(int64(escrow.TickInterval.Seconds()), 10))
	case hostsUnavailable(err):
		baseMS := s.config.Load().Limits.HostCutoff.BaseMS
		seconds := int64(math.Ceil(float64(baseMS) / float64(time.Second.Milliseconds())))
		w.Header().Set("Retry-After", strconv.FormatInt(seconds, 10))
	case ours, shardHasNoRoom(err):
		w.Header().Set("Retry-After", strconv.FormatInt(int64(noHostRetryAfter.Seconds()), 10))
	}
	writeJSON(w, statusForError(err), errorEnvelope{Error: detailForError(err)})
}

func detailForError(err error) errorDetail {
	var tooLong *ContextLengthError
	if errors.As(err, &tooLong) {
		return errorDetail{Message: err.Error(), Type: errorTypeInvalidRequest, Code: errorCodeContextLengthExceeded}
	}
	return errorDetail{Message: err.Error()}
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	body, err := json.Marshal(payload)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(append(body, '\n'))
}

// Bounds host-controlled text in a log line: a message-less HostApplicationError renders its whole payload.
const maxLoggedErrorBytes = 256

// adminFailure exists because auditAdmin records only the successful path.
func adminFailure(label string, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		recorder := &failureRecorder{ResponseWriter: w, status: http.StatusOK}
		next(recorder, r)
		if recorder.status < http.StatusBadRequest {
			return
		}
		fields := []any{logkey.Route, label, logkey.Method, r.Method, logkey.Status, recorder.status, logkey.Error, recorder.reason()}
		if recorder.status >= http.StatusInternalServerError {
			logging.Error("admin request failed", fields...)
			return
		}
		logging.Warn("admin request refused", fields...)
	}
}

// failureRecorder forwards Flush so a wrapped handler keeps streaming.
type failureRecorder struct {
	http.ResponseWriter
	status int
	body   []byte
}

func (rec *failureRecorder) WriteHeader(status int) {
	rec.status = status
	rec.ResponseWriter.WriteHeader(status)
}

func (rec *failureRecorder) Write(data []byte) (int, error) {
	if rec.status >= http.StatusBadRequest && len(rec.body) < maxLoggedErrorBytes {
		rec.body = append(rec.body, data[:min(len(data), maxLoggedErrorBytes-len(rec.body))]...)
	}
	return rec.ResponseWriter.Write(data)
}

func (rec *failureRecorder) Unwrap() http.ResponseWriter { return rec.ResponseWriter }

func (rec *failureRecorder) Flush() {
	if flusher, ok := rec.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

// A body truncated at the cap no longer parses, so the raw text is the fallback, not an empty field.
func (rec *failureRecorder) reason() string {
	var envelope errorEnvelope
	if err := strictjson.Unmarshal(rec.body, &envelope); err == nil && envelope.Error.Message != "" {
		return envelope.Error.Message
	}
	return strings.TrimSpace(string(rec.body))
}
