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
	WhereTransportAuth              Where = "transport.auth"
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
	WhereRuntimeModifyRequest       Where = "runtime.modify_request"
	WhereRuntimeFetchPayloads       Where = "runtime.fetch_payloads"
	WhereRuntimeProcessExecution    Where = "runtime.process_execution_response"
	WhereRuntimeWriteClientResponse Where = "runtime.write_client_response"
	WhereRuntimeStorePayloads       Where = "runtime.store_payloads"
	WhereEngineMLNodeCall           Where = "engine.mlnode_call"
	WhereManagerPayloads            Where = "manager.payloads"
)

const (
	ReasonOK                            Reason = "ok"
	ReasonError                         Reason = "error"
	ReasonQueued                        Reason = "queued"
	ReasonCached                        Reason = "cached"
	ReasonAuthErr                       Reason = "auth_err"
	ReasonMissingAuthHeaders            Reason = "missing_auth_headers"
	ReasonInvalidSignatureHex           Reason = "invalid_signature_hex"
	ReasonInvalidTimestamp              Reason = "invalid_timestamp"
	ReasonBodyReadErr                   Reason = "body_read_err"
	ReasonSignatureVerifyErr            Reason = "signature_verify_err"
	ReasonSenderNotAllowed              Reason = "sender_not_allowed"
	ReasonRateLimited                   Reason = "rate_limited"
	ReasonMissingSender                 Reason = "missing_sender"
	ReasonOwnerErr                      Reason = "owner_err"
	ReasonParseErr                      Reason = "parse_err"
	ReasonDecodeErr                     Reason = "decode_err"
	ReasonHandleRequestErr              Reason = "handle_request_err"
	ReasonReceiptWriteErr               Reason = "receipt_write_err"
	ReasonCachedReplayErr               Reason = "cached_replay_err"
	ReasonExecuteErr                    Reason = "execute_err"
	ReasonSignFinishErr                 Reason = "sign_finish_err"
	ReasonClientCancelledAfterReceipt   Reason = "client_cancelled_after_receipt"
	ReasonMetaWriteErr                  Reason = "meta_write_err"
	ReasonNoReceiptInterrupted          Reason = "no_receipt_interrupted"
	ReasonNoReceipt                     Reason = "no_receipt"
	ReasonReceiptNoExecutionInterrupted Reason = "receipt_no_execution_interrupted"
	ReasonReceiptNoExecution            Reason = "receipt_no_execution"
	ReasonExecutionNoFinish             Reason = "execution_no_finish"
	ReasonFinishPublished               Reason = "finish_published"
	ReasonNoReceiptExpected             Reason = "no_receipt_expected"
	ReasonReceiptNoExecutionExpected    Reason = "receipt_no_execution_expected"
	ReasonPayloadAbsent                 Reason = "payload_absent"
	ReasonTargetDiffAbsent              Reason = "target_diff_absent"
	ReasonNotExecutor                   Reason = "not_executor"
	ReasonAlreadyExecuting              Reason = "already_executing"
	ReasonCachedResponse                Reason = "cached_response"
	ReasonExecutionExpected             Reason = "execution_expected"
	ReasonApplyErr                      Reason = "apply_err"
	ReasonPersistDiffErr                Reason = "persist_diff_err"
	ReasonStateSignErr                  Reason = "state_sign_err"
	ReasonStateSignatureWithheld        Reason = "state_signature_withheld"
	ReasonPayloadVerifyErr              Reason = "payload_verify_err"
	ReasonReceiptMarshalErr             Reason = "receipt_marshal_err"
	ReasonReceiptSignErr                Reason = "receipt_sign_err"
	ReasonQueueFull                     Reason = "queue_full"
	ReasonValidateErr                   Reason = "validate_err"
	ReasonInferenceDisappeared          Reason = "inference_disappeared"
	ReasonSignValidationErr             Reason = "sign_validation_err"
	ReasonSignVoteErr                   Reason = "sign_vote_err"
	ReasonValidationStatusChanged       Reason = "validation_status_changed"
	ReasonRejectedPayload               Reason = "rejected_payload"
	ReasonModifyRequestErr              Reason = "modify_request_err"
	ReasonCanonicalizePromptErr         Reason = "canonicalize_prompt_err"
	ReasonPayloadStoreErr               Reason = "payload_store_err"
	ReasonPayloadFetchErr               Reason = "payload_fetch_err"
	ReasonResponseWriteErr              Reason = "response_write_err"
	ReasonResponseProcessErr            Reason = "response_process_err"
	ReasonUsageParseErr                 Reason = "usage_parse_err"
	ReasonValidationBodyErr             Reason = "validation_body_err"
	ReasonOriginalResponseParseErr      Reason = "original_response_parse_err"
	ReasonEnforcedTokensErr             Reason = "enforced_tokens_err"
	ReasonValidationResponseErr         Reason = "validation_response_err"
	ReasonApplicationErr                Reason = "application_err"
	ReasonTransportErr                  Reason = "transport_err"
	ReasonTimeout                       Reason = "timeout"
	ReasonHTTP5xx                       Reason = "http_5xx"
	ReasonHTTP4xx                       Reason = "http_4xx"
	ReasonAcquireErr                    Reason = "acquire_err"
	ReasonReleaseErr                    Reason = "release_err"
	ReasonPromptTokens                  Reason = "prompt"
	ReasonCompletionTokens              Reason = "completion"
	ReasonTotalPhase                    Reason = "total"
	ReasonMissingInferenceID            Reason = "missing_inference_id"
	ReasonPayloadNotFound               Reason = "payload_not_found"
	ReasonPayloadRetrieveErr            Reason = "payload_retrieve_err"
	ReasonPayloadResponseSignErr        Reason = "payload_response_sign_err"
	ReasonPayloadWriteErr               Reason = "payload_write_err"
	ReasonPayloadAuthErr                Reason = "payload_auth_err"
	ReasonMissingValidatorHeader        Reason = "missing_validator_header"
	ReasonMissingTimestampHeader        Reason = "missing_timestamp_header"
	ReasonMissingEpochHeader            Reason = "missing_epoch_header"
	ReasonMissingSignatureHeader        Reason = "missing_signature_header"
	ReasonInvalidEpoch                  Reason = "invalid_epoch"
	ReasonTimestampTooOld               Reason = "timestamp_too_old"
	ReasonTimestampInFuture             Reason = "timestamp_in_future"
	ReasonNotGroupMember                Reason = "not_group_member"
	ReasonPubkeyResolutionErr           Reason = "pubkey_resolution_err"
	ReasonInvalidSignature              Reason = "invalid_signature"
	ReasonInitializing                  Reason = "initializing"
	ReasonVersionConflict               Reason = "version_conflict"
	ReasonEpochConflict                 Reason = "epoch_conflict"
	ReasonBuildGroupErr                 Reason = "build_group_err"
	ReasonGetEscrowErr                  Reason = "get_escrow_err"
	ReasonStorageErr                    Reason = "storage_err"
	ReasonSessionResolveErr             Reason = "session_resolve_err"
)

const (
	PathExecute  Path = "execute"
	PathValidate Path = "validate"
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
	return logging.WithRequestID(ctx, inboundID)
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

func Log(ctx context.Context, level Level, msg string, stage Stage, where Where, escrowID string, status, reason Reason, err error, kv ...any) {
	if level == "" {
		level = LevelInfo
	}
	fields := make([]any, 0, len(kv)+6)
	if status != "" {
		fields = append(fields, "status", string(status))
	}
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
	Terminal          Terminal
	Reason            Reason
	FailureWhere      Where
	EscrowID          string
	InferenceID       uint64
	Nonce             uint64
	ReceiptExpected   bool
	ReceiptObserved   bool
	ExecutionExpected bool
	ExecutionStarted  bool
	FinishExpected    bool
	FinishPublished   bool
}

func RecordNoReceiptInterrupted(ctx context.Context, escrowID string, reason Reason, failureWhere Where) {
	recordTerminal(ctx, terminalOutcome{
		Terminal:        TerminalNoReceiptInterrupted,
		Reason:          reason,
		FailureWhere:    failureWhere,
		EscrowID:        escrowID,
		ReceiptExpected: true,
	})
}

func RecordReceiptWriteFailure(ctx context.Context, escrowID string, inferenceID, nonce uint64, reason Reason, failureWhere Where, receiptExpected, executionExpected bool) {
	recordTerminal(ctx, terminalOutcome{
		Terminal:          TerminalNoReceiptInterrupted,
		Reason:            reason,
		FailureWhere:      failureWhere,
		EscrowID:          escrowID,
		InferenceID:       inferenceID,
		Nonce:             nonce,
		ReceiptExpected:   receiptExpected,
		ReceiptObserved:   false,
		ExecutionExpected: executionExpected,
		FinishExpected:    executionExpected,
	})
}

func RecordReceiptNoExecutionExpected(ctx context.Context, escrowID string, inferenceID, nonce uint64, reason Reason, failureWhere Where) {
	recordTerminal(ctx, terminalOutcome{
		Terminal:          TerminalReceiptNoExecutionExpected,
		Reason:            reason,
		FailureWhere:      failureWhere,
		EscrowID:          escrowID,
		InferenceID:       inferenceID,
		Nonce:             nonce,
		ReceiptExpected:   true,
		ReceiptObserved:   true,
		ExecutionExpected: false,
	})
}

func RecordReceiptNoExecutionInterrupted(ctx context.Context, escrowID string, inferenceID, nonce uint64, reason Reason, failureWhere Where) {
	recordTerminal(ctx, terminalOutcome{
		Terminal:          TerminalReceiptNoExecutionInterrupted,
		Reason:            reason,
		FailureWhere:      failureWhere,
		EscrowID:          escrowID,
		InferenceID:       inferenceID,
		Nonce:             nonce,
		ReceiptExpected:   true,
		ReceiptObserved:   true,
		ExecutionExpected: false,
	})
}

func RecordNoReceiptExpected(ctx context.Context, escrowID string, inferenceID, nonce uint64, reason Reason, failureWhere Where) {
	recordTerminal(ctx, terminalOutcome{
		Terminal:        TerminalNoReceiptExpected,
		Reason:          reason,
		FailureWhere:    failureWhere,
		EscrowID:        escrowID,
		InferenceID:     inferenceID,
		Nonce:           nonce,
		ReceiptExpected: false,
	})
}

func RecordExecutionNoFinish(ctx context.Context, escrowID string, inferenceID, nonce uint64, reason Reason, failureWhere Where) {
	recordTerminal(ctx, terminalOutcome{
		Terminal:          TerminalExecutionNoFinish,
		Reason:            reason,
		FailureWhere:      failureWhere,
		EscrowID:          escrowID,
		InferenceID:       inferenceID,
		Nonce:             nonce,
		ReceiptExpected:   true,
		ReceiptObserved:   true,
		ExecutionExpected: true,
		ExecutionStarted:  true,
		FinishExpected:    true,
		FinishPublished:   false,
	})
}

func RecordClientCancelledAfterReceipt(ctx context.Context, escrowID string, inferenceID, nonce uint64, failureWhere Where) {
	recordTerminal(ctx, terminalOutcome{
		Terminal:          TerminalClientCancelledAfterReceipt,
		Reason:            ReasonClientCancelledAfterReceipt,
		FailureWhere:      failureWhere,
		EscrowID:          escrowID,
		InferenceID:       inferenceID,
		Nonce:             nonce,
		ReceiptExpected:   true,
		ReceiptObserved:   true,
		ExecutionExpected: true,
		ExecutionStarted:  true,
		FinishExpected:    true,
		FinishPublished:   false,
	})
}

func RecordFinishPublished(ctx context.Context, escrowID string, inferenceID, nonce uint64) {
	recordTerminal(ctx, terminalOutcome{
		Terminal:          TerminalFinishPublished,
		Reason:            ReasonOK,
		EscrowID:          escrowID,
		InferenceID:       inferenceID,
		Nonce:             nonce,
		ReceiptExpected:   true,
		ReceiptObserved:   true,
		ExecutionExpected: true,
		ExecutionStarted:  true,
		FinishExpected:    true,
		FinishPublished:   true,
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
		"receipt_expected", outcome.ReceiptExpected,
		"receipt_observed", outcome.ReceiptObserved,
		"execution_expected", outcome.ExecutionExpected,
		"execution_started", outcome.ExecutionStarted,
		"finish_expected", outcome.FinishExpected,
		"finish_published", outcome.FinishPublished,
	}
	if outcome.FailureWhere != "" {
		fields = append(fields, "failure_where", string(outcome.FailureWhere))
	}

	Log(ctx, LevelInfo, "devshard request terminal", StageTerminal, WhereRequestTerminal, outcome.EscrowID, "", outcome.Reason, nil, fields...)
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
