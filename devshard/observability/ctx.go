package observability

import (
	"context"
	"errors"
	"net/http"
	"sync/atomic"

	"devshard/logging"
)

const RequestIDHeader = "X-Request-Id"

type Stage string
type Where string
type Reason string
type Terminal string
type Level string
type Path string
type MetricStatus string
type MetricPhase string
type TokenKind string

const (
	LevelInfo  Level = "info"
	LevelWarn  Level = "warn"
	LevelError Level = "error"
)

const (
	StageReceived           Stage = "received"
	StageReceipt            Stage = "receipt"
	StageReceiptWritten     Stage = "receipt_written"
	StageResponseStreamed   Stage = "response_streamed"
	StageFinished           Stage = "finished"
	StageTerminal           Stage = "terminal"
	StageSessionResolved    Stage = "session_resolved"
	StageValidationPicked   Stage = "validation_picked"
	StageValidationStarted  Stage = "validation_started"
	StageValidationFinished Stage = "validation_finished"
	StageVotePublished      Stage = "vote_published"
	StagePayloadRequest     Stage = "payload_request"
	StageRequest            Stage = "request"
)

const (
	WhereRequestTerminal            Where = "request.terminal"
	WhereRoutesSessionResolve       Where = "routes.session_resolve"
	WhereTransportRateLimit         Where = "transport.rate_limit"
	WhereTransportHandleInference   Where = "transport.handle_inference"
	WhereHostApplyDiff              Where = "host.apply_diff"
	WhereHostSignReceipt            Where = "host.sign_receipt"
	WhereHostSignState              Where = "host.sign_state"
	WhereHostExecute                Where = "host.execute"
	WhereHostPublishFinish          Where = "host.publish_finish"
	WhereHostValidationQueue        Where = "host.validation_queue"
	WhereHostValidate               Where = "host.validate"
	WhereHostPublishValidation      Where = "host.publish_validation"
	WhereTransportWriteReceiptSSE   Where = "transport.write_receipt_sse"
	WhereRuntimeWriteClientResponse Where = "runtime.write_client_response"
	WhereEngineMLNodeCall           Where = "engine.mlnode_call"
	WhereManagerPayloads            Where = "manager.payloads"
	WhereRuntimeExecute             Where = "runtime.execute"
	WhereRuntimeValidate            Where = "runtime.validate"
)

const (
	ReasonOK                          Reason = "ok"
	ReasonInvalidTimestamp            Reason = "invalid_timestamp"
	ReasonBodyReadErr                 Reason = "body_read_err"
	ReasonRateLimited                 Reason = "rate_limited"
	ReasonMissingSender               Reason = "missing_sender"
	ReasonOwnerErr                    Reason = "owner_err"
	ReasonParseErr                    Reason = "parse_err"
	ReasonDecodeErr                   Reason = "decode_err"
	ReasonHandleRequestErr            Reason = "handle_request_err"
	ReasonReceiptWriteErr             Reason = "receipt_write_err"
	ReasonCachedReplayErr             Reason = "cached_replay_err"
	ReasonExecuteErr                  Reason = "execute_err"
	ReasonSignFinishErr               Reason = "sign_finish_err"
	ReasonClientCancelledAfterReceipt Reason = "client_cancelled_after_receipt"
	ReasonNoReceipt                   Reason = "no_receipt"
	ReasonReceiptNoExecution          Reason = "receipt_no_execution"
	ReasonExecutionNoFinish           Reason = "execution_no_finish"
	ReasonPayloadAbsent               Reason = "payload_absent"
	ReasonTargetDiffAbsent            Reason = "target_diff_absent"
	ReasonNotExecutor                 Reason = "not_executor"
	ReasonAlreadyExecuting            Reason = "already_executing"
	ReasonCachedResponse              Reason = "cached_response"
	ReasonApplyErr                    Reason = "apply_err"
	ReasonPersistDiffErr              Reason = "persist_diff_err"
	ReasonStateSignErr                Reason = "state_sign_err"
	ReasonStateSignatureWithheld      Reason = "state_signature_withheld"
	ReasonPayloadVerifyErr            Reason = "payload_verify_err"
	ReasonReceiptMarshalErr           Reason = "receipt_marshal_err"
	ReasonReceiptSignErr              Reason = "receipt_sign_err"
	ReasonQueueFull                   Reason = "queue_full"
	ReasonValidateErr                 Reason = "validate_err"
	ReasonInferenceDisappeared        Reason = "inference_disappeared"
	ReasonSignValidationErr           Reason = "sign_validation_err"
	ReasonSignVoteErr                 Reason = "sign_vote_err"
	ReasonValidationStatusChanged     Reason = "validation_status_changed"
	ReasonPartialResponseInterrupted  Reason = "partial_response_after_interruption"
	ReasonApplicationErr              Reason = "application_err"
	ReasonTransportErr                Reason = "transport_err"
	ReasonTimeout                     Reason = "timeout"
	ReasonHTTP5xx                     Reason = "http_5xx"
	ReasonHTTP4xx                     Reason = "http_4xx"
	ReasonAcquireErr                  Reason = "acquire_err"
	ReasonReleaseErr                  Reason = "release_err"
	ReasonMissingInferenceID          Reason = "missing_inference_id"
	ReasonPayloadNotFound             Reason = "payload_not_found"
	ReasonPayloadRetrieveErr          Reason = "payload_retrieve_err"
	ReasonPayloadResponseSignErr      Reason = "payload_response_sign_err"
	ReasonPayloadWriteErr             Reason = "payload_write_err"
	ReasonMissingValidatorHeader      Reason = "missing_validator_header"
	ReasonMissingTimestampHeader      Reason = "missing_timestamp_header"
	ReasonMissingEpochHeader          Reason = "missing_epoch_header"
	ReasonMissingSignatureHeader      Reason = "missing_signature_header"
	ReasonInvalidEpoch                Reason = "invalid_epoch"
	ReasonTimestampTooOld             Reason = "timestamp_too_old"
	ReasonTimestampInFuture           Reason = "timestamp_in_future"
	ReasonNotGroupMember              Reason = "not_group_member"
	ReasonPubkeyResolutionErr         Reason = "pubkey_resolution_err"
	ReasonInvalidSignature            Reason = "invalid_signature"
	ReasonInitializing                Reason = "initializing"
	ReasonVersionConflict             Reason = "version_conflict"
	ReasonEpochConflict               Reason = "epoch_conflict"
	ReasonBuildGroupErr               Reason = "build_group_err"
	ReasonGetEscrowErr                Reason = "get_escrow_err"
	ReasonStorageErr                  Reason = "storage_err"
	ReasonSessionResolveErr           Reason = "session_resolve_err"
	ReasonModifyRequestErr            Reason = "modify_request_err"
	ReasonCanonicalizePromptErr       Reason = "canonicalize_prompt_err"
	ReasonPayloadStoreErr             Reason = "payload_store_err"
	ReasonPayloadFetchErr             Reason = "payload_fetch_err"
	ReasonProcessResponseErr          Reason = "process_response_err"
	ReasonValidationBuildErr          Reason = "validation_build_err"
	ReasonValidationParseErr          Reason = "validation_parse_err"
	ReasonValidationReadErr           Reason = "validation_read_err"
	ReasonOriginalParseErr            Reason = "original_parse_err"
)

const (
	PathExecute  Path = "execute"
	PathValidate Path = "validate"
)

const (
	MetricStatusOK     MetricStatus = "ok"
	MetricStatusError  MetricStatus = "error"
	MetricStatusQueued MetricStatus = "queued"
	MetricStatusCached MetricStatus = "cached"
)

const (
	MetricPhaseTotal MetricPhase = "total"
)

const (
	TokenKindPrompt     TokenKind = "prompt"
	TokenKindCompletion TokenKind = "completion"
)

const (
	TerminalFinishPublished               Terminal = "finish_published"
	TerminalNoReceiptExpected             Terminal = "no_receipt_expected"
	TerminalNoReceiptInterrupted          Terminal = "no_receipt_interrupted"
	TerminalReceiptNoExecutionExpected    Terminal = "receipt_no_execution_expected"
	TerminalReceiptNoExecutionInterrupted Terminal = "receipt_no_execution_interrupted"
	TerminalExecutionNoFinish             Terminal = "execution_no_finish"
	TerminalClientCancelledAfterReceipt   Terminal = "client_cancelled_after_receipt"
)

type runtimeInfo struct {
	binary  string
	version string
	mode    string
}

var currentRuntime atomic.Value

func init() {
	currentRuntime.Store(runtimeInfo{})
}

func SetRuntime(binary, version, mode string) {
	currentRuntime.Store(runtimeInfo{binary: binary, version: version, mode: mode})
}

func BindRequestID(ctx context.Context, inboundID string) context.Context {
	ctx, _ = logging.WithRequestID(ctx, inboundID)
	return ctx
}

func SetRequestIDHeader(ctx context.Context, header http.Header) {
	if requestID, ok := logging.RequestID(ctx); ok {
		header.Set(RequestIDHeader, requestID)
	}
}

func AttachRequestID(req *http.Request) {
	if req != nil {
		SetRequestIDHeader(req.Context(), req.Header)
	}
}

type ClassifiedError struct {
	Reason Reason
	Where  Where
	Err    error
}

func (e *ClassifiedError) Error() string {
	if e == nil || e.Err == nil {
		return ""
	}
	return e.Err.Error()
}

func (e *ClassifiedError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

func Classify(reason Reason, where Where, err error) error {
	if err == nil {
		return nil
	}
	return &ClassifiedError{Reason: reason, Where: where, Err: err}
}

func ErrorReason(err error, fallbackReason Reason, fallbackWhere Where) (Reason, Where) {
	var classified *ClassifiedError
	if errors.As(err, &classified) {
		reason := classified.Reason
		where := classified.Where
		if reason == "" {
			reason = fallbackReason
		}
		if where == "" {
			where = fallbackWhere
		}
		return reason, where
	}
	return fallbackReason, fallbackWhere
}

func Fields(ctx context.Context, stage Stage, where Where, escrowID string, kv ...any) []any {
	info := currentRuntime.Load().(runtimeInfo)
	fields := make([]any, 0, 14+len(kv))
	if requestID, ok := logging.RequestID(ctx); ok {
		fields = append(fields, "request_id", requestID)
	}
	if escrowID != "" {
		fields = append(fields, "escrow_id", escrowID)
	}
	fields = append(fields,
		"stage", string(stage),
		"where", string(where),
		"binary", info.binary,
		"version", info.version,
		"mode", info.mode,
	)
	fields = append(fields, kv...)
	return fields
}

func Log(ctx context.Context, level Level, msg string, stage Stage, where Where, escrowID string, reason Reason, err error, kv ...any) {
	if level == "" {
		level = LevelInfo
	}
	fields := make([]any, 0, len(kv)+4)
	if reason != "" {
		fields = append(fields, "reason", string(reason))
	}
	if err != nil {
		fields = append(fields, "error", err)
	}
	fields = append(fields, kv...)
	fields = Fields(ctx, stage, where, escrowID, fields...)
	switch level {
	case LevelError:
		logging.Error(msg, fields...)
	case LevelWarn:
		logging.Warn(msg, fields...)
	default:
		logging.Info(msg, fields...)
	}
}

type terminalOutcome struct {
	Terminal     Terminal
	Reason       Reason
	FailureWhere Where
	EscrowID     string
	InferenceID  uint64
	Nonce        uint64
}

func RecordNoReceiptInterrupted(ctx context.Context, escrowID string, reason Reason, failureWhere Where) {
	recordTerminal(ctx, terminalOutcome{
		Terminal:     TerminalNoReceiptInterrupted,
		Reason:       reason,
		FailureWhere: failureWhere,
		EscrowID:     escrowID,
	})
}

// FailNoReceipt is a convenience helper for early-exit branches in request
// handlers (auth, parse, decode). It logs the failure, emits a
// no-receipt-interrupted terminal, and returns httpErr unchanged so Echo's
// default error handler can derive the response status code.
func FailNoReceipt(ctx context.Context, escrowID string, reason Reason, failureWhere Where, msg string, httpErr error, kv ...any) error {
	Log(ctx, LevelError, msg, StageRequest, failureWhere, escrowID, reason, httpErr, kv...)
	RecordNoReceiptInterrupted(ctx, escrowID, reason, failureWhere)
	return httpErr
}

// LogPayloadRequest is the standard pair for HandlePayloads error branches:
// bumps the payload-request lifecycle counter and emits a structured log line
// in StagePayloadRequest / WhereManagerPayloads. Status is typically
// MetricStatusError on failure and MetricStatusOK on the success branch.
func LogPayloadRequest(ctx context.Context, level Level, escrowID string, status MetricStatus, reason Reason, msg string, err error, kv ...any) {
	IncPayloadRequest(status, reason)
	Log(ctx, level, msg, StagePayloadRequest, WhereManagerPayloads, escrowID, reason, err, kv...)
}

// FailReceiptOrphan logs an error, increments the receipt-orphan counter, and
// returns Classify(reason, where, err) so the caller can return the wrapped
// error in one statement.
func FailReceiptOrphan(ctx context.Context, escrowID string, reason Reason, where Where, stage Stage, msg string, err error, kv ...any) error {
	Log(ctx, LevelError, msg, stage, where, escrowID, reason, err, kv...)
	IncReceiptOrphan(reason)
	return Classify(reason, where, err)
}

// LogValidationOrphan logs an error and increments the validation-orphan
// counter. Used in fire-and-forget validation paths that do not propagate the
// error back to a caller.
func LogValidationOrphan(ctx context.Context, escrowID string, reason Reason, where Where, stage Stage, msg string, err error, kv ...any) {
	Log(ctx, LevelError, msg, stage, where, escrowID, reason, err, kv...)
	IncValidationOrphan(reason)
}

// FailValidationFinished is the standard "validation finished with failure"
// trio: bumps the validation lifecycle counter (status=error) and emits the
// orphan log+counter. Use from fire-and-forget runValidation flows where the
// error has no caller to propagate to.
func FailValidationFinished(ctx context.Context, escrowID string, reason Reason, where Where, msg string, err error, kv ...any) {
	IncValidation(StageValidationFinished, MetricStatusError)
	LogValidationOrphan(ctx, escrowID, reason, where, StageValidationFinished, msg, err, kv...)
}

func RecordReceiptWriteFailure(ctx context.Context, escrowID string, inferenceID, nonce uint64, reason Reason, failureWhere Where) {
	recordTerminal(ctx, terminalOutcome{
		Terminal:     TerminalNoReceiptInterrupted,
		Reason:       reason,
		FailureWhere: failureWhere,
		EscrowID:     escrowID,
		InferenceID:  inferenceID,
		Nonce:        nonce,
	})
}

func RecordReceiptNoExecutionExpected(ctx context.Context, escrowID string, inferenceID, nonce uint64, reason Reason, failureWhere Where) {
	recordTerminal(ctx, terminalOutcome{
		Terminal:     TerminalReceiptNoExecutionExpected,
		Reason:       reason,
		FailureWhere: failureWhere,
		EscrowID:     escrowID,
		InferenceID:  inferenceID,
		Nonce:        nonce,
	})
}

func RecordReceiptNoExecutionInterrupted(ctx context.Context, escrowID string, inferenceID, nonce uint64, reason Reason, failureWhere Where) {
	recordTerminal(ctx, terminalOutcome{
		Terminal:     TerminalReceiptNoExecutionInterrupted,
		Reason:       reason,
		FailureWhere: failureWhere,
		EscrowID:     escrowID,
		InferenceID:  inferenceID,
		Nonce:        nonce,
	})
}

func RecordNoReceiptExpected(ctx context.Context, escrowID string, inferenceID, nonce uint64, reason Reason, failureWhere Where) {
	recordTerminal(ctx, terminalOutcome{
		Terminal:     TerminalNoReceiptExpected,
		Reason:       reason,
		FailureWhere: failureWhere,
		EscrowID:     escrowID,
		InferenceID:  inferenceID,
		Nonce:        nonce,
	})
}

func RecordExecutionNoFinish(ctx context.Context, escrowID string, inferenceID, nonce uint64, reason Reason, failureWhere Where) {
	recordTerminal(ctx, terminalOutcome{
		Terminal:     TerminalExecutionNoFinish,
		Reason:       reason,
		FailureWhere: failureWhere,
		EscrowID:     escrowID,
		InferenceID:  inferenceID,
		Nonce:        nonce,
	})
}

func RecordClientCancelledAfterReceipt(ctx context.Context, escrowID string, inferenceID, nonce uint64, failureWhere Where) {
	recordTerminal(ctx, terminalOutcome{
		Terminal:     TerminalClientCancelledAfterReceipt,
		Reason:       ReasonClientCancelledAfterReceipt,
		FailureWhere: failureWhere,
		EscrowID:     escrowID,
		InferenceID:  inferenceID,
		Nonce:        nonce,
	})
}

func RecordFinishPublished(ctx context.Context, escrowID string, inferenceID, nonce uint64, reason Reason, failureWhere Where) {
	if reason == "" {
		reason = ReasonOK
	}
	recordTerminal(ctx, terminalOutcome{
		Terminal:     TerminalFinishPublished,
		Reason:       reason,
		FailureWhere: failureWhere,
		EscrowID:     escrowID,
		InferenceID:  inferenceID,
		Nonce:        nonce,
	})
}

func recordTerminal(ctx context.Context, outcome terminalOutcome) {
	IncTerminal(outcome.Terminal, outcome.Reason)
	if class := interruptionClass(outcome.Terminal); class != "" {
		IncInterruption(class, outcome.Reason)
	}

	fields := []any{
		"terminal", string(outcome.Terminal),
		"inference_id", outcome.InferenceID,
		"nonce", outcome.Nonce,
	}
	if outcome.FailureWhere != "" {
		fields = append(fields, "failure_where", string(outcome.FailureWhere))
	}

	Log(ctx, LevelInfo, "devshard request terminal", StageTerminal, WhereRequestTerminal, outcome.EscrowID, outcome.Reason, nil, fields...)
}

func interruptionClass(terminal Terminal) Reason {
	switch terminal {
	case TerminalNoReceiptInterrupted:
		return ReasonNoReceipt
	case TerminalReceiptNoExecutionInterrupted:
		return ReasonReceiptNoExecution
	case TerminalExecutionNoFinish, TerminalClientCancelledAfterReceipt:
		return ReasonExecutionNoFinish
	default:
		return ""
	}
}
