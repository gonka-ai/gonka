package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"devshard/user"
)

const requestCaptureDirName = "captured-requests"

const (
	defaultShortContentMinOutputChunks  = int64(1000)
	defaultShortContentMaxContentRatio  = 0.75
	defaultShortContentResponseMaxBytes = int64(16 * 1024 * 1024)
)

type requestCaptureStore struct {
	dir string
}

type requestCaptureOptions struct {
	shortContentAttempts    bool
	shortContentResponses   bool
	shortContentMinOutput   int64
	shortContentMaxRatio    float64
	shortContentMaxResponse int64
}

type capturedChatRequest struct {
	RequestID    string                `json:"request_id,omitempty"`
	CapturedAt   string                `json:"captured_at"`
	Kind         string                `json:"kind"`
	Error        string                `json:"error,omitempty"`
	Method       string                `json:"method,omitempty"`
	Path         string                `json:"path,omitempty"`
	Model        string                `json:"model,omitempty"`
	Escrow       string                `json:"escrow,omitempty"`
	Stream       bool                  `json:"stream,omitempty"`
	RequestFlags string                `json:"request_flags,omitempty"`
	Attempts     []capturedChatAttempt `json:"attempts,omitempty"`
	Body         json.RawMessage       `json:"body,omitempty"`
	BodyBase64   string                `json:"body_base64,omitempty"`
}

type capturedChatAttempt struct {
	Escrow                      string `json:"escrow,omitempty"`
	Nonce                       uint64 `json:"nonce,omitempty"`
	Host                        string `json:"host,omitempty"`
	HostIdx                     int    `json:"host_idx,omitempty"`
	Winner                      bool   `json:"winner,omitempty"`
	Probe                       bool   `json:"probe,omitempty"`
	EmptyStream                 bool   `json:"empty_stream,omitempty"`
	ErrorStream                 bool   `json:"error_stream,omitempty"`
	Error                       string `json:"error,omitempty"`
	Finished                    bool   `json:"finished,omitempty"`
	Responsive                  bool   `json:"responsive,omitempty"`
	HasReceipt                  bool   `json:"has_receipt,omitempty"`
	ConfirmedAt                 int64  `json:"confirmed_at,omitempty"`
	OutputChunks                int64  `json:"output_chunks,omitempty"`
	ContentChunks               int64  `json:"content_chunks,omitempty"`
	OutputBytes                 int64  `json:"output_bytes,omitempty"`
	StreamBytesRead             int64  `json:"stream_bytes_read,omitempty"`
	ContentSource               string `json:"content_source,omitempty"`
	ErrorSource                 string `json:"error_source,omitempty"`
	ErrorCode                   string `json:"error_code,omitempty"`
	ErrorType                   string `json:"error_type,omitempty"`
	ErrorMessage                string `json:"error_message,omitempty"`
	ResponseBodySample          string `json:"response_body_sample,omitempty"`
	ResponseBodySampleTruncated bool   `json:"response_body_sample_truncated,omitempty"`
	ResponseBodyBase64          string `json:"response_body_base64,omitempty"`
	ResponseBodyTruncated       bool   `json:"response_body_truncated,omitempty"`
	ResponseBodyBytes           int    `json:"response_body_bytes,omitempty"`
}

var (
	requestCaptureMu     sync.RWMutex
	activeRequestCapture *requestCaptureStore
	activeCaptureOptions requestCaptureOptions
)

func configureRequestCaptureStore(baseStorageDir string) {
	// Off unless explicitly enabled; DEVSHARD_REQUEST_CAPTURE_DIR is optional (defaults under the storage dir).
	if !readBoolEnv("DEVSHARD_REQUEST_CAPTURE_ENABLED", false) {
		setRequestCaptureStore(nil)
		return
	}
	dir := strings.TrimSpace(os.Getenv("DEVSHARD_REQUEST_CAPTURE_DIR"))
	if dir == "" {
		dir = filepath.Join(baseStorageDir, requestCaptureDirName)
	}
	configureRequestCaptureOptionsFromEnv()
	setRequestCaptureStore(&requestCaptureStore{dir: dir})
}

func setRequestCaptureStore(store *requestCaptureStore) {
	requestCaptureMu.Lock()
	defer requestCaptureMu.Unlock()
	activeRequestCapture = store
}

func currentRequestCaptureStore() *requestCaptureStore {
	requestCaptureMu.RLock()
	defer requestCaptureMu.RUnlock()
	return activeRequestCapture
}

func configureRequestCaptureOptionsFromEnv() {
	opts := requestCaptureOptions{
		shortContentAttempts:    readBoolEnv("DEVSHARD_CAPTURE_SHORT_CONTENT_ATTEMPTS", false),
		shortContentResponses:   readBoolEnv("DEVSHARD_CAPTURE_SHORT_CONTENT_RESPONSES", false),
		shortContentMinOutput:   readInt64Env("DEVSHARD_CAPTURE_SHORT_CONTENT_MIN_OUTPUT_CHUNKS", defaultShortContentMinOutputChunks),
		shortContentMaxRatio:    readFloat64Env("DEVSHARD_CAPTURE_SHORT_CONTENT_MAX_CONTENT_RATIO", defaultShortContentMaxContentRatio),
		shortContentMaxResponse: readInt64Env("DEVSHARD_CAPTURE_SHORT_CONTENT_RESPONSE_MAX_BYTES", defaultShortContentResponseMaxBytes),
	}
	setRequestCaptureOptions(opts)
}

func setRequestCaptureOptions(opts requestCaptureOptions) {
	if opts.shortContentMinOutput < 1 {
		opts.shortContentMinOutput = defaultShortContentMinOutputChunks
	}
	if opts.shortContentMaxRatio <= 0 || opts.shortContentMaxRatio >= 1 {
		opts.shortContentMaxRatio = defaultShortContentMaxContentRatio
	}
	if opts.shortContentMaxResponse < 0 {
		opts.shortContentMaxResponse = 0
	}
	requestCaptureMu.Lock()
	defer requestCaptureMu.Unlock()
	activeCaptureOptions = opts
}

func currentRequestCaptureOptions() requestCaptureOptions {
	requestCaptureMu.RLock()
	defer requestCaptureMu.RUnlock()
	return activeCaptureOptions
}

func captureFilterRejectedRequest(r *http.Request, body []byte, err error, model, escrow string) {
	if r == nil || err == nil || len(body) == 0 {
		return
	}
	store := currentRequestCaptureStore()
	if store == nil {
		return
	}
	record := capturedChatRequest{
		RequestID:  requestIDOrEmpty(r.Context()),
		CapturedAt: time.Now().UTC().Format(time.RFC3339Nano),
		Kind:       "filter_rejected",
		Error:      err.Error(),
		Method:     r.Method,
		Path:       requestPath(r),
		Model:      firstNonEmpty(model, chatRequestModel(body)),
		Escrow:     escrow,
		Stream:     chatRequestStream(body),
	}
	setCapturedRequestBody(&record, body)
	_ = store.write(record)
}

func captureAllAttemptsFailedRequest(ctx context.Context, escrow string, params user.InferenceParams, err error) {
	if len(params.Prompt) == 0 {
		return
	}
	store := currentRequestCaptureStore()
	if store == nil {
		return
	}
	errText := ""
	if err != nil {
		errText = err.Error()
	}
	record := capturedChatRequest{
		RequestID:  requestIDOrEmpty(ctx),
		CapturedAt: time.Now().UTC().Format(time.RFC3339Nano),
		Kind:       "all_attempts_failed",
		Error:      errText,
		Path:       "/v1/chat/completions",
		Model:      params.Model,
		Escrow:     escrow,
		Stream:     params.Stream,
	}
	setCapturedRequestBody(&record, params.Prompt)
	_ = store.write(record)
}

func captureEmptyStreamAttemptRequest(ctx context.Context, escrow string, params user.InferenceParams, attempts []*inflight, winnerNonce uint64) {
	if len(params.Prompt) == 0 || !hasEmptyStreamAttempt(attempts) {
		return
	}
	store := currentRequestCaptureStore()
	if store == nil {
		return
	}
	record := capturedChatRequest{
		RequestID:    requestIDOrEmpty(ctx),
		CapturedAt:   time.Now().UTC().Format(time.RFC3339Nano),
		Kind:         "empty_stream_attempt",
		Path:         "/v1/chat/completions",
		Model:        params.Model,
		Escrow:       escrow,
		Stream:       params.Stream,
		RequestFlags: requestFlagsForLog(params),
		Attempts:     capturedAttempts(attempts, winnerNonce),
	}
	setCapturedRequestBody(&record, params.Prompt)
	_ = store.write(record)
}

func captureShortContentAttemptRequest(ctx context.Context, escrow string, params user.InferenceParams, attempts []*inflight, winnerNonce uint64) {
	opts := currentRequestCaptureOptions()
	if len(params.Prompt) == 0 || !opts.shortContentAttempts || !hasShortContentAttempt(attempts, opts) {
		return
	}
	store := currentRequestCaptureStore()
	if store == nil {
		return
	}
	record := capturedChatRequest{
		RequestID:    requestIDOrEmpty(ctx),
		CapturedAt:   time.Now().UTC().Format(time.RFC3339Nano),
		Kind:         "short_content_attempt",
		Error:        shortContentCaptureReason(opts),
		Path:         "/v1/chat/completions",
		Model:        params.Model,
		Escrow:       escrow,
		Stream:       params.Stream,
		RequestFlags: requestFlagsForLog(params),
		Attempts:     capturedAttempts(attempts, winnerNonce),
	}
	setCapturedRequestBody(&record, params.Prompt)
	_ = store.write(record)
}

func shortContentCaptureReason(opts requestCaptureOptions) string {
	return fmt.Sprintf("content_chunks/output_chunks below %.4g with output_chunks >= %d", opts.shortContentMaxRatio, opts.shortContentMinOutput)
}

func hasEmptyStreamAttempt(attempts []*inflight) bool {
	for _, inf := range attempts {
		if inf != nil && !inf.probe && isEmptyStreamAttempt(inf) {
			return true
		}
	}
	return false
}

func hasShortContentAttempt(attempts []*inflight, opts requestCaptureOptions) bool {
	for _, inf := range attempts {
		if isShortContentAttempt(inf, opts) {
			return true
		}
	}
	return false
}

func isShortContentAttempt(inf *inflight, opts requestCaptureOptions) bool {
	if inf == nil || inf.probe {
		return false
	}
	outputChunks := inf.outputChunks.Load()
	if outputChunks < opts.shortContentMinOutput {
		return false
	}
	contentChunks := inf.contentChunks.Load()
	return float64(contentChunks)/float64(outputChunks) < opts.shortContentMaxRatio
}

func capturedAttempts(attempts []*inflight, winnerNonce uint64) []capturedChatAttempt {
	opts := currentRequestCaptureOptions()
	captured := make([]capturedChatAttempt, 0, len(attempts))
	for _, inf := range attempts {
		if inf == nil {
			continue
		}
		var (
			confirmedAt     int64
			hasReceipt      bool
			streamBytesRead int64
		)
		if inf.resp != nil {
			confirmedAt = inf.resp.ConfirmedAt
			hasReceipt = len(inf.resp.Receipt) > 0
			streamBytesRead = inf.resp.StreamBytesRead
		}
		errText := ""
		if inf.err != nil {
			errText = inf.err.Error()
		}
		attempt := capturedChatAttempt{
			Escrow:                      inf.escrowID,
			Nonce:                       inf.nonce,
			Host:                        inf.hostID,
			HostIdx:                     inf.hostIdx,
			Winner:                      inf.nonce == winnerNonce,
			Probe:                       inf.probe,
			EmptyStream:                 isEmptyStreamAttempt(inf),
			ErrorStream:                 isErrorStreamAttempt(inf),
			Error:                       errText,
			Finished:                    inf.resp != nil && inf.resp.ConfirmedAt > 0 && !isFailedStreamAttempt(inf),
			Responsive:                  confirmedAt > 0,
			HasReceipt:                  hasReceipt,
			ConfirmedAt:                 confirmedAt,
			OutputChunks:                inf.outputChunks.Load(),
			ContentChunks:               inf.contentChunks.Load(),
			OutputBytes:                 inf.outputBytes.Load(),
			StreamBytesRead:             streamBytesRead,
			ContentSource:               inf.contentSource,
			ErrorSource:                 inf.errorSource,
			ErrorCode:                   inf.errorCode,
			ErrorType:                   inf.errorType,
			ErrorMessage:                inf.errorMessage,
			ResponseBodySample:          inf.emptyResponseBodySample,
			ResponseBodySampleTruncated: inf.emptyResponseBodySampleTruncated,
		}
		if opts.shortContentResponses && isShortContentAttempt(inf, opts) && len(inf.shortContentResponseBody) > 0 {
			attempt.ResponseBodyBase64 = base64.StdEncoding.EncodeToString(inf.shortContentResponseBody)
			attempt.ResponseBodyTruncated = inf.shortContentResponseBodyTruncated
			attempt.ResponseBodyBytes = len(inf.shortContentResponseBody)
		}
		captured = append(captured, attempt)
	}
	return captured
}

func (s *requestCaptureStore) write(record capturedChatRequest) error {
	if s == nil || strings.TrimSpace(s.dir) == "" {
		return nil
	}
	kind := safeFilenameComponent(record.Kind)
	if kind == "" {
		kind = "request"
	}
	dir := filepath.Join(s.dir, kind)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	body, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return err
	}
	body = append(body, '\n')

	name := fmt.Sprintf("%s_%s_%s.json",
		time.Now().UTC().Format("20060102T150405.000000000Z"),
		safeFilenameComponent(firstNonEmpty(record.RequestID, "no-request-id")),
		kind,
	)
	path := filepath.Join(dir, name)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, body, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func setCapturedRequestBody(record *capturedChatRequest, body []byte) {
	if record == nil || len(body) == 0 {
		return
	}
	if json.Valid(body) {
		record.Body = append(json.RawMessage(nil), body...)
		return
	}
	record.BodyBase64 = base64.StdEncoding.EncodeToString(body)
}

func requestIDOrEmpty(ctx context.Context) string {
	if requestID, ok := requestLogFromContext(ctx); ok {
		return requestID
	}
	return ""
}

func requestPath(r *http.Request) string {
	if r == nil || r.URL == nil {
		return ""
	}
	return r.URL.Path
}

func chatRequestStream(body []byte) bool {
	var req chatRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return false
	}
	return req.Stream
}

func safeFilenameComponent(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	var b strings.Builder
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
		case r >= 'A' && r <= 'Z':
			b.WriteRune(r)
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-', r == '_', r == '.':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	return strings.Trim(b.String(), "._")
}
