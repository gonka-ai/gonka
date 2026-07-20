package main

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"devshard/state"
	"devshard/types"
	"devshard/user"
)

var sseDoneMarker = []byte("data: [DONE]")

// defaultMetaDrainTimeout is a last-resort backstop, not an active policy:
// it must exceed every natural completion window (SecondaryWaitAfterWinner,
// nonStreamingNoContentTimeout, nonStreamingMaxAttemptWait) so that hosts
// which are still legitimately generating after a client disconnect get to
// finish and settle their nonce normally. Cutting a host early records it
// as a timeout / empty stream through no fault of its own. The only thing
// this cap should ever terminate is a host that streams forever without
// tripping any of the normal race timeouts.
const defaultMetaDrainTimeout = 40 * time.Minute

// metaDrainTimeout applies only after client disconnect (flag.Done() in
// withMetaDrain), not during normal connected flows. If devshard_meta /
// MsgFinishInference is missed due to this cap, redundancy continues via
// timeout handling on the settle path.
var metaDrainTimeout = metaDrainTimeoutFromEnv()

func metaDrainTimeoutFromEnv() time.Duration {
	raw := strings.TrimSpace(os.Getenv("DEVSHARD_META_DRAIN_TIMEOUT_SECONDS"))
	if raw == "" {
		return defaultMetaDrainTimeout
	}
	seconds, err := strconv.Atoi(raw)
	if err != nil || seconds <= 0 {
		return defaultMetaDrainTimeout
	}
	return time.Duration(seconds) * time.Second
}

// cancelFlag is a one-shot signal used to communicate "client disconnected"
// from the request handler into redundancy / withMetaDrain. The upstream
// HTTP context is intentionally NOT used as streamCtx so protocol completion
// (devshard_meta + ProcessResponse) can run after the client goes away.
type cancelFlag struct {
	once sync.Once
	ch   chan struct{}
}

func newCancelFlag() *cancelFlag { return &cancelFlag{ch: make(chan struct{})} }

func (cf *cancelFlag) Trigger() {
	if cf == nil {
		return
	}
	cf.once.Do(func() { close(cf.ch) })
}

func (cf *cancelFlag) Gone() bool {
	if cf == nil {
		return false
	}
	select {
	case <-cf.ch:
		return true
	default:
		return false
	}
}

func (cf *cancelFlag) Done() <-chan struct{} {
	if cf == nil {
		return nil
	}
	return cf.ch
}

// watchClientCancel triggers flag when r's context is canceled (client
// disconnected). Spawns one short-lived goroutine bounded by request lifetime.
//
// net/http also cancels the request context when the handler returns after a
// normal response, which is indistinguishable from a disconnect here. Callers
// must invoke the returned stop func once RunInference has returned (i.e. the
// response was fully delivered) so that normal completion does not trip the
// flag and metaDrain-cancel speculative attempts that are still running under
// the SecondaryWaitAfterWinner grace window.
func watchClientCancel(r *http.Request, flag *cancelFlag) (stop func()) {
	if flag == nil || r == nil {
		return func() {}
	}
	stopped := make(chan struct{})
	go func() {
		select {
		case <-r.Context().Done():
			// stop() happens-before the handler returns, which happens-before
			// net/http cancels the request context, so this non-blocking
			// re-check deterministically ignores the post-completion cancel
			// even when the goroutine first wakes up here.
			select {
			case <-stopped:
				return
			default:
			}
			flag.Trigger()
		case <-stopped:
		}
	}()
	var once sync.Once
	return func() { once.Do(func() { close(stopped) }) }
}

// withMetaDrain caps upstream host reads after client disconnect so a
// malicious or slow host cannot pin the proxy indefinitely.
func withMetaDrain(parent context.Context, flag *cancelFlag) (context.Context, context.CancelFunc) {
	if flag == nil {
		ctx, cancel := context.WithCancel(parent)
		return ctx, cancel
	}
	ctx, cancel := context.WithCancel(parent)
	go func() {
		select {
		case <-flag.Done():
			select {
			case <-time.After(metaDrainTimeout):
				cancel()
			case <-ctx.Done():
			}
		case <-ctx.Done():
		}
	}()
	return ctx, cancel
}

// writeStreamReset writes a stream_reset SSE event to signal the client
// that the connection was lost and the response will be replayed from scratch.
func writeStreamReset(w io.Writer) {
	fmt.Fprintf(w, "data: {\"devshard_stream_reset\":true}\n\n")
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
}

// flushResponseWriter drives a best-effort Flush on an http.ResponseWriter
// through arbitrary middleware wrappers. It uses http.NewResponseController so
// that even wrappers that embed http.ResponseWriter without re-exposing
// http.Flusher (e.g. metricsResponseWriter) do not silently swallow flushes —
// previously SSE chunks were only delivered when Go's default chunked-encoding
// buffer happened to fill, which combined with nginx proxy_buffering caused
// clients to see zero bytes until the handler returned.
//
// Returns the underlying Flush error so callers can distinguish a clean flush
// from a kernel-level RST / EPIPE that Go surfaces only on the next write or
// flush. Previously this error was discarded, which made it impossible to tell
// "handler returned cleanly" from "client socket was already dead when we
// flushed the final [DONE]".
func flushResponseWriter(w http.ResponseWriter) error {
	if w == nil {
		return nil
	}
	return http.NewResponseController(w).Flush()
}

// inferenceStatusName maps status codes to human-readable names.
var inferenceStatusName = map[types.InferenceStatus]string{
	types.StatusPending:     "pending",
	types.StatusStarted:     "started",
	types.StatusFinished:    "finished",
	types.StatusChallenged:  "challenged",
	types.StatusValidated:   "validated",
	types.StatusInvalidated: "invalidated",
	types.StatusTimedOut:    "timed_out",
}

// Proxy is the OpenAI-compatible HTTP proxy backed by a devshard session.
type Proxy struct {
	session                 *user.Session
	sm                      *state.StateMachine
	escrowID                string
	model                   string
	redundancy              *Redundancy
	perf                    *PerfTracker
	phaseGate               *ChainPhaseGate
	defaultRequestMaxTokens uint64
	requestMaxTokensCap     uint64
}

func (p *Proxy) handleChatCompletions(w http.ResponseWriter, r *http.Request) {
	ctx, _ := ensureRequestLogContext(r.Context())
	r = r.WithContext(ctx)
	if r.Method != http.MethodPost {
		logRequestStage(ctx, "proxy_method_not_allowed", "method", r.Method)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := p.admissionError(); err != nil {
		logRequestStage(ctx, "proxy_request_blocked", "escrow", p.escrowID, "error", err)
		writeGatewayJSONError(w, gatewayStatusCodeForError(err), err.Error())
		return
	}

	body, req, err := prepareChatRequestBodyWithTokenLimits(r, p.outputTokenLimits(), p.model)
	if err != nil {
		logRequestStage(ctx, "proxy_read_body_failed", "error", err)
		writeGatewayJSONError(w, chatRequestErrorStatus(err, http.StatusBadRequest), err.Error())
		return
	}

	model := req.Model
	if model == "" {
		model = p.model
	}
	params := user.InferenceParams{
		Model:       model,
		Prompt:      body,
		InputLength: uint64(len(body)),
		MaxTokens:   req.MaxTokens,
		StartedAt:   time.Now().Unix(),
		Stream:      req.Stream,
	}
	logRequestStage(ctx, "proxy_request_started", "escrow", p.escrowID, "model", model, "stream", req.Stream, "input_tokens", params.InputLength)

	if req.Stream {
		p.handleStreaming(w, r, params)
	} else {
		p.handleNonStreaming(w, r, params)
	}
}

func (p *Proxy) outputTokenLimits() outputTokenLimits {
	if p == nil {
		return defaultOutputTokenLimits()
	}
	return normalizedOutputTokenLimits(outputTokenLimits{
		DefaultMaxTokens: p.defaultRequestMaxTokens,
		MaxTokensCap:     p.requestMaxTokensCap,
	})
}

// deferredWriter delays WriteHeader(200) until the first Write call.
// If runInference errors before any streaming data arrives, the proxy
// can still return a proper HTTP error status.
//
// It also tracks total bytes written and the last flush error so the
// streaming handler can emit a single proxy_response_finished record at the
// very end with a truthful picture of whether the final [DONE] actually
// reached the wire or whether Go's chunked encoder hit EPIPE/ECONNRESET on
// the final flush.
type deferredWriter struct {
	ctx            context.Context
	w              http.ResponseWriter
	escrow         string
	requestID      string
	clientFlag     *cancelFlag
	started        bool
	bytesWritten   int64
	sawDone        bool
	lastFlushErr   error
	flushFailed    bool
	disconnectOnce sync.Once
	flushFailOnce  sync.Once
	writeFailOnce  sync.Once
}

func newDeferredWriter(ctx context.Context, w http.ResponseWriter, escrow string, flag *cancelFlag) *deferredWriter {
	rid, _ := requestLogFromContext(ctx)
	return &deferredWriter{ctx: ctx, w: w, escrow: escrow, requestID: rid, clientFlag: flag}
}

func (d *deferredWriter) Write(p []byte) (int, error) {
	if d.clientFlag != nil && d.clientFlag.Gone() {
		// Client disconnected: swallow output so upstream protocol drain can
		// continue without surfacing write errors to the race writer.
		return len(p), nil
	}
	if err := d.ctx.Err(); err != nil {
		d.logDisconnectOnce(err, "write")
		return 0, err
	}
	if !d.started {
		if d.requestID != "" {
			// Emit before WriteHeader so nginx sees it and the aiohttp
			// client can read it from the response headers. This gives
			// us a 1:1 mapping between any client-side ClientPayloadError
			// and a specific request=<id> entry in devshardctl logs.
			d.w.Header().Set("X-Request-Id", d.requestID)
		}
		d.w.Header().Set("Content-Type", "text/event-stream")
		d.w.Header().Set("Cache-Control", "no-cache")
		d.w.Header().Set("Connection", "keep-alive")
		d.w.WriteHeader(http.StatusOK)
		d.started = true
	}
	rewritten := rewriteStreamingPayload(p)
	if bytes.Contains(rewritten, sseDoneMarker) {
		d.sawDone = true
	}
	n, err := d.w.Write(rewritten)
	d.bytesWritten += int64(n)
	if err != nil {
		d.writeFailOnce.Do(func() {
			logRequestStage(d.ctx, "proxy_write_failed",
				"escrow", d.escrow,
				"bytes_written", d.bytesWritten,
				"error", err,
			)
		})
	}
	return n, err
}

func (d *deferredWriter) Flush() {
	d.flush("mid_stream")
}

// flush performs the Flush, records any error, and emits a single
// proxy_flush_failed log entry per deferredWriter so the logs don't explode
// if every subsequent flush fails after the first break.
func (d *deferredWriter) flush(where string) error {
	if d.clientFlag != nil && d.clientFlag.Gone() {
		return nil
	}
	if err := d.ctx.Err(); err != nil {
		d.logDisconnectOnce(err, "flush")
		d.lastFlushErr = err
		return err
	}
	err := flushResponseWriter(d.w)
	if err != nil {
		d.lastFlushErr = err
		d.logFlushFailedOnce(err, where)
	}
	return err
}

func (d *deferredWriter) logDisconnectOnce(err error, where string) {
	d.disconnectOnce.Do(func() {
		logRequestStage(d.ctx, "proxy_client_disconnected",
			"escrow", d.escrow,
			"where", where,
			"started", d.started,
			"bytes_written", d.bytesWritten,
			"error", err,
		)
	})
}

func (d *deferredWriter) logFlushFailedOnce(err error, where string) {
	d.flushFailOnce.Do(func() {
		d.flushFailed = true
		logRequestStage(d.ctx, "proxy_flush_failed",
			"escrow", d.escrow,
			"where", where,
			"bytes_written", d.bytesWritten,
			"error", err,
		)
	})
}

func (p *Proxy) handleStreaming(w http.ResponseWriter, r *http.Request, params user.InferenceParams) {
	started := time.Now()
	flag := newCancelFlag()
	stopClientWatch := watchClientCancel(r, flag)
	defer stopClientWatch()
	dw := newDeferredWriter(r.Context(), w, p.escrowID, flag)

	// Upstream redundancy is NOT bound to r.Context(): host SSE must be
	// drained through devshard_meta even if the client disconnects.
	// metaDrainTimeout (via withMetaDrain in redundancy) bounds how long
	// upstream may run after the client is gone.
	var doneWriteErr error
	err := p.redundancy.RunInference(context.Background(), params, dw, flag)
	if flag.Gone() {
		logRequestStage(r.Context(), "proxy_stream_client_gone",
			"escrow", p.escrowID,
			"bytes_written", dw.bytesWritten,
			"elapsed_ms", time.Since(started).Milliseconds(),
		)
		return
	}
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			logRequestStage(r.Context(), "proxy_stream_terminated", "escrow", p.escrowID, "error", err, "bytes_written", dw.bytesWritten, "elapsed_ms", time.Since(started).Milliseconds())
			return
		}
		logRequestStage(r.Context(), "proxy_stream_failed", "escrow", p.escrowID, "error", err)
		statusCode := gatewayStatusCodeForError(err)
		var hostErr *hostApplicationError
		if errors.As(err, &hostErr) {
			if !dw.started {
				if _, werr := fmt.Fprintf(dw, "data: %s\n\n", hostErr.jsonPayload()); werr != nil {
					doneWriteErr = werr
					logRequestStage(r.Context(), "proxy_error_write_failed", "escrow", p.escrowID, "error", werr)
				}
				if _, werr := fmt.Fprint(dw, "data: [DONE]\n\n"); werr != nil {
					doneWriteErr = werr
					logRequestStage(r.Context(), "proxy_done_write_failed", "escrow", p.escrowID, "error", werr)
				}
				finalErr := dw.flush("host_error_final")
				logProxyResponseFinished(r.Context(), p.escrowID, "host_error", dw, finalErr, doneWriteErr, started)
				return
			}
			if !dw.sawDone {
				if _, werr := fmt.Fprint(dw, "data: [DONE]\n\n"); werr != nil {
					doneWriteErr = werr
					logRequestStage(r.Context(), "proxy_done_write_failed", "escrow", p.escrowID, "error", werr)
				}
			}
			finalErr := dw.flush("host_error_final")
			logProxyResponseFinished(r.Context(), p.escrowID, "host_error", dw, finalErr, doneWriteErr, started)
			return
		}
		if !dw.started {
			writeGatewayJSONError(w, statusCode, err.Error())
			return
		}
		if dw.sawDone {
			logRequestStage(r.Context(), "proxy_gateway_error_after_done_suppressed", "escrow", p.escrowID, "error", err)
			finalErr := dw.flush("gateway_error_after_done")
			logProxyResponseFinished(r.Context(), p.escrowID, "error_after_done", dw, finalErr, nil, started)
			return
		}
		log.Printf("inference error (mid-stream): %v", err)
		if _, werr := fmt.Fprintf(dw, "data: {\"error\":{\"message\":%q}}\n\n", err.Error()); werr != nil {
			logRequestStage(r.Context(), "proxy_error_write_failed", "escrow", p.escrowID, "error", werr)
		}
		finalErr := dw.flush("error_final")
		logProxyResponseFinished(r.Context(), p.escrowID, "error", dw, finalErr, werrOrNil(nil), started)
		return
	}

	logRequestStage(r.Context(), "proxy_stream_completed", "escrow", p.escrowID, "bytes_written", dw.bytesWritten)
	var finalErr error
	if !dw.sawDone {
		if _, werr := fmt.Fprint(dw, "data: [DONE]\n\n"); werr != nil {
			doneWriteErr = werr
			logRequestStage(r.Context(), "proxy_done_write_failed", "escrow", p.escrowID, "error", werr)
		}
		finalErr = dw.flush("done")
	}
	logProxyResponseFinished(r.Context(), p.escrowID, "ok", dw, finalErr, doneWriteErr, started)
}

// werrOrNil normalizes an error so the varargs passthrough below stays tidy.
func werrOrNil(err error) error { return err }

func writeGatewayJSONError(w http.ResponseWriter, statusCode int, message string) {
	writeJSONPayload(w, statusCode, []byte(fmt.Sprintf(`{"error":{"message":%q}}`, message)))
}

func writeJSONPayload(w http.ResponseWriter, statusCode int, payload []byte) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	w.Write(payload)
}

func jsonErrorPayloadDetails(payload []byte) (sseErrorDetails, bool) {
	var evt struct {
		Error *struct {
			Type    string `json:"type"`
			Code    any    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
		Object  string `json:"object"`
		Type    string `json:"type"`
		Code    any    `json:"code"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(payload, &evt); err != nil {
		return sseErrorDetails{}, false
	}
	if evt.Error != nil {
		details := sseErrorDetails{
			Type:    evt.Error.Type,
			Code:    fmt.Sprint(evt.Error.Code),
			Message: evt.Error.Message,
		}
		if evt.Error.Code == nil {
			details.Code = ""
		}
		return details, true
	}
	if evt.Object == "error" && evt.Message != "" {
		details := sseErrorDetails{
			Type:    evt.Type,
			Code:    fmt.Sprint(evt.Code),
			Message: evt.Message,
		}
		if evt.Code == nil {
			details.Code = ""
		}
		return details, true
	}
	return sseErrorDetails{}, false
}

// logProxyResponseFinished is the authoritative "request left the building"
// log entry. It fires after the final Flush on every success/error streaming
// path, carrying everything needed to correlate with a client-side RST:
//
//	outcome        ok | error
//	bytes_written  total bytes handed to the chunked-encoding writer
//	elapsed_ms     full streaming duration from handleStreaming entry
//	done_write_err non-nil ⇒ the [DONE] write itself returned an error
//	final_flush_err non-nil ⇒ Go surfaced a kernel-level error (EPIPE /
//	                        ECONNRESET / closed network connection) on the
//	                        final Flush, meaning the client socket was dead
//	                        before our [DONE] made it onto the wire
//	flush_failed   a previous mid-stream flush had already errored
func logProxyResponseFinished(ctx context.Context, escrowID, outcome string, dw *deferredWriter, finalFlushErr, doneWriteErr error, started time.Time) {
	kv := []any{
		"escrow", escrowID,
		"outcome", outcome,
		"bytes_written", dw.bytesWritten,
		"elapsed_ms", time.Since(started).Milliseconds(),
		"flush_failed", dw.flushFailed,
	}
	if doneWriteErr != nil {
		kv = append(kv, "done_write_err", doneWriteErr)
	}
	if finalFlushErr != nil {
		kv = append(kv, "final_flush_err", finalFlushErr)
	}
	logRequestStage(ctx, "proxy_response_finished", kv...)
}

func (p *Proxy) handleNonStreaming(w http.ResponseWriter, r *http.Request, params user.InferenceParams) {
	var buf bytes.Buffer
	flag := newCancelFlag()
	stopClientWatch := watchClientCancel(r, flag)
	defer stopClientWatch()

	err := p.redundancy.RunInference(context.Background(), params, &buf, flag)
	if flag.Gone() {
		return
	}
	if err != nil {
		logRequestStage(r.Context(), "proxy_request_failed", "escrow", p.escrowID, "error", err)
		var hostErr *hostApplicationError
		if errors.As(err, &hostErr) {
			writeJSONPayload(w, hostErr.statusCode(), hostErr.jsonPayload())
			return
		}
		writeGatewayJSONError(w, gatewayStatusCodeForError(err), err.Error())
		return
	}

	assembled := assembleSSEChunks(buf.String())
	assembled = filterClientInternalFields(assembled)
	if rid, ok := requestLogFromContext(r.Context()); ok {
		w.Header().Set("X-Request-Id", rid)
	}
	if details, ok := jsonErrorPayloadDetails(assembled); ok {
		writeJSONPayload(w, (&hostApplicationError{details: details, payload: assembled}).statusCode(), assembled)
		logRequestStage(r.Context(), "proxy_request_completed_with_host_error", "escrow", p.escrowID, "error", details.Message)
		return
	}
	writeJSONPayload(w, http.StatusOK, assembled)
	logRequestStage(r.Context(), "proxy_request_completed", "escrow", p.escrowID)
}

// assembleSSEChunks extracts the last data line from SSE output as the response.
func assembleSSEChunks(raw string) []byte {
	var lastData string
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")
		if data == "[DONE]" {
			continue
		}
		lastData = data
	}
	if lastData != "" {
		return []byte(lastData)
	}
	return []byte(`{"error":{"message":"no response data"}}`)
}

func (p *Proxy) settlementJSON() (SettlementJSON, error) {
	finalNonce := p.session.Nonce()
	st := p.sm.SnapshotState()
	payload, err := state.BuildSettlement(p.escrowID, st, p.session.Signatures()[finalNonce], finalNonce)
	if err != nil {
		return SettlementJSON{}, err
	}
	return buildSettlementJSON(payload)
}

func (p *Proxy) writeSettlement(w http.ResponseWriter) {
	finalNonce := p.session.Nonce()
	st := p.sm.SnapshotState()
	payload, err := state.BuildSettlement(p.escrowID, st, p.session.Signatures()[finalNonce], finalNonce)
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"error":{"message":%q}}`, err.Error()), http.StatusInternalServerError)
		return
	}

	data, err := marshalSettlement(payload)
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"error":{"message":%q}}`, err.Error()), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Write(data)
}

func (p *Proxy) handleFinalize(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if err := p.session.Finalize(r.Context()); err != nil {
		http.Error(w, fmt.Sprintf(`{"error":{"message":%q}}`, err.Error()), http.StatusInternalServerError)
		return
	}

	p.writeSettlement(w)
}

func (p *Proxy) handleGetFinalize(w http.ResponseWriter, r *http.Request) {
	if p.sm.Phase() != types.PhaseSettlement {
		http.Error(w, `{"error":{"message":"session not yet finalized"}}`, http.StatusConflict)
		return
	}
	p.writeSettlement(w)
}

type statusResponse struct {
	EscrowID             string              `json:"escrow_id"`
	Nonce                uint64              `json:"nonce"`
	Phase                string              `json:"phase"`
	Balance              uint64              `json:"balance"`
	ChainPhase           string              `json:"chain_phase,omitempty"`
	ConfirmationPoCPhase string              `json:"confirmation_poc_phase,omitempty"`
	RequestsBlocked      bool                `json:"requests_blocked"`
	BlockReason          string              `json:"block_reason,omitempty"`
	Config               statusSessionConfig `json:"config"`
}

// statusSessionConfig is the JSON representation of session config values
// returned by the devshardctl status endpoint.
//
// These fields are written as JSON numbers (native Go encode). They are not
// used to decode Cosmos REST; grpc-gateway stringified uint64/int64 fields are
// handled in devshard/bridge/rest.go (escrowResponse) with `json:"...,string"`.
type statusSessionConfig struct {
	RefusalTimeout            int64  `json:"refusal_timeout"`
	ExecutionTimeout          int64  `json:"execution_timeout"`
	TokenPrice                uint64 `json:"token_price"`
	CreateDevshardFee         uint64 `json:"create_devshard_fee"`
	FeePerNonce               uint64 `json:"fee_per_nonce"`
	VoteThreshold             uint32 `json:"vote_threshold"`
	ValidationRate            uint32 `json:"validation_rate"`
	InferenceSealGraceNonces  uint32 `json:"inference_seal_grace_nonces"`
	InferenceSealGraceSeconds uint32 `json:"inference_seal_grace_seconds"`
	AutoSealEveryNNonces      uint32 `json:"auto_seal_every_n_nonces"`
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.Encode(v)
}

func (p *Proxy) handleDebugPending(w http.ResponseWriter, r *http.Request) {
	pending := p.session.PendingTxs()
	warmKeys := p.sm.WarmKeys()

	type txInfo struct {
		Type string `json:"type"`
		ID   uint64 `json:"id,omitempty"`
	}
	var txs []txInfo
	for _, tx := range pending {
		switch inner := tx.GetTx().(type) {
		case *types.DevshardTx_ConfirmStart:
			txs = append(txs, txInfo{Type: "confirm_start", ID: inner.ConfirmStart.InferenceId})
		case *types.DevshardTx_FinishInference:
			txs = append(txs, txInfo{Type: "finish", ID: inner.FinishInference.InferenceId})
		case *types.DevshardTx_Validation:
			txs = append(txs, txInfo{Type: "validation", ID: inner.Validation.InferenceId})
		case *types.DevshardTx_ValidationVote:
			txs = append(txs, txInfo{Type: "vote", ID: inner.ValidationVote.InferenceId})
		case *types.DevshardTx_RevealSeed:
			txs = append(txs, txInfo{Type: "reveal_seed", ID: uint64(inner.RevealSeed.SlotId)})
		default:
			txs = append(txs, txInfo{Type: fmt.Sprintf("%T", tx.GetTx())})
		}
	}

	writeJSON(w, map[string]any{
		"nonce":     p.session.Nonce(),
		"pending":   txs,
		"warm_keys": warmKeys,
	})
}

func (p *Proxy) handleDebugPerf(w http.ResponseWriter, r *http.Request) {
	stats := p.perf.AllStats()
	requests := p.perf.RecentRequests()
	writeJSON(w, map[string]any{
		"hosts":                  stats,
		"requests":               requests,
		"pairwise":               p.perf.PairwiseSummaries(),
		"context_limits":         p.perf.ContextLimits(),
		"tool_unsupported":       p.perf.ToolUnsupported(),
		"receipt_timeout_ms":     ReceiptTimeout.Milliseconds(),
		"advantage_threshold":    ParallelAdvantageThreshold,
		"unresponsive_threshold": UnresponsiveThreshold,
		"host_window_size":       PerfWindowSize,
		"participant_window_ms":  ParticipantPerfWindow.Milliseconds(),
		"request_log_size":       requestLogSize,
	})
}

func (p *Proxy) handleDebugPairwise(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]any{
		"speed_policy":                    RedundancySpeedPolicy,
		"budget_percentile":               PairwiseBudgetPercentile,
		"max_proactive_attempts":          PairwiseMaxProactiveAttempts,
		"min_direct_comparisons":          PairwiseMinDirectComparisons,
		"chained_confidence_decay":        PairwiseChainedConfidenceDecay,
		"ab_sample_rate":                  PairwiseABSampleRate,
		"ab_sparse_sample_rate":           PairwiseABSparseSampleRate,
		"ab_sparse_sample_threshold":      PairwiseABSparseSampleThreshold,
		"winner_hold_ms":                  PairwiseWinnerHold.Milliseconds(),
		"winner_hold_min_speedup":         PairwiseWinnerHoldMinSpeedup,
		"winner_hold_min_samples":         PairwiseWinnerHoldMinSamples,
		"request_shape_buckets":           []string{"lt_1k", "1k_5k", "5k_15k", "15k_30k", "30k_100k", "gte_100k"},
		"comparisons":                     p.perf.PairwiseSummaries(),
		"legacy_secondary_faster_enabled": RedundancySpeedPolicy != RedundancySpeedPolicyPairwise,
	})
}

func (p *Proxy) handleDebugState(w http.ResponseWriter, r *http.Request) {
	// Live counts come from InferenceStatusCounts (computed under the read
	// lock without deep-copying records); sealed records are evicted from the
	// live map, so they are reported separately from the seal-nonce index.
	liveTotal, statusCounts := p.sm.InferenceStatusCounts()
	sealed := p.sm.ExportSealedNonces()

	liveStatusCounts := make(map[string]int, len(statusCounts))
	for status, n := range statusCounts {
		name := inferenceStatusName[status]
		if name == "" {
			name = fmt.Sprintf("unknown(%d)", status)
		}
		liveStatusCounts[name] = n
	}

	phaseStr := "active"
	switch p.sm.Phase() {
	case types.PhaseFinalizing:
		phaseStr = "finalizing"
	case types.PhaseSettlement:
		phaseStr = "settlement"
	}

	writeJSON(w, map[string]any{
		"nonce":              p.sm.LatestNonce(),
		"phase":              phaseStr,
		"balance":            p.sm.Balance(),
		"live_inferences":    liveTotal,
		"sealed_inferences":  len(sealed),
		"live_status_counts": liveStatusCounts,
		// Deprecated: same as live_inferences; kept for older scripts.
		"total_inferences": liveTotal,
		"status_counts":    liveStatusCounts,
	})
}

func (p *Proxy) handleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	phase := p.sm.Phase()
	var phaseStr string
	switch phase {
	case 0:
		phaseStr = "active"
	case 1:
		phaseStr = "finalizing"
	case 2:
		phaseStr = "settlement"
	default:
		phaseStr = fmt.Sprintf("unknown(%d)", phase)
	}

	cfg := p.sm.Config()
	status := statusResponse{
		EscrowID: p.escrowID,
		Nonce:    p.session.Nonce(),
		Phase:    phaseStr,
		Balance:  p.sm.Balance(),
		Config: statusSessionConfig{
			RefusalTimeout:            cfg.RefusalTimeout,
			ExecutionTimeout:          cfg.ExecutionTimeout,
			TokenPrice:                cfg.TokenPrice,
			CreateDevshardFee:         cfg.CreateDevshardFee,
			FeePerNonce:               cfg.FeePerNonce,
			VoteThreshold:             cfg.VoteThreshold,
			ValidationRate:            cfg.ValidationRate,
			InferenceSealGraceNonces:  cfg.InferenceSealGraceNonces,
			InferenceSealGraceSeconds: cfg.InferenceSealGraceSeconds,
			AutoSealEveryNNonces:      cfg.AutoSealEveryNNonces,
		},
	}
	if p.phaseGate != nil {
		snapshot := p.phaseGate.Snapshot()
		status.ChainPhase = snapshot.EpochPhase
		status.ConfirmationPoCPhase = snapshot.ConfirmationPoCPhase
		status.RequestsBlocked = snapshot.RequestsBlocked
		status.BlockReason = snapshot.BlockReason
	}
	writeJSON(w, status)
}

type requestAccountingCostResponse struct {
	WinnerActualCost        uint64 `json:"winner_actual_cost"`
	OtherAttemptsActualCost uint64 `json:"other_attempts_actual_cost"`
	AllAttemptsActualCost   uint64 `json:"all_attempts_actual_cost"`
}

type requestAccountingAttemptResponse struct {
	Nonce          uint64 `json:"nonce"`
	HostIdx        int    `json:"host_idx"`
	ParticipantKey string `json:"participant_key,omitempty"`
	Probe          bool   `json:"probe"`
	Winner         bool   `json:"winner"`
	Status         string `json:"status"`
	Model          string `json:"model,omitempty"`
	ExecutorSlot   uint32 `json:"executor_slot,omitempty"`
	InputLength    uint64 `json:"input_length,omitempty"`
	MaxTokens      uint64 `json:"max_tokens,omitempty"`
	InputTokens    uint64 `json:"input_tokens,omitempty"`
	OutputTokens   uint64 `json:"output_tokens,omitempty"`
	ReservedCost   uint64 `json:"reserved_cost,omitempty"`
	ActualCost     uint64 `json:"actual_cost,omitempty"`
	StartedAt      int64  `json:"started_at,omitempty"`
	ConfirmedAt    int64  `json:"confirmed_at,omitempty"`
	CreatedAt      string `json:"created_at,omitempty"`
}

type requestAccountingResponse struct {
	RequestID           string                             `json:"request_id"`
	EscrowID            string                             `json:"escrow_id"`
	Model               string                             `json:"model,omitempty"`
	Outcome             string                             `json:"outcome"`
	Decision            string                             `json:"decision,omitempty"`
	WinnerNonce         uint64                             `json:"winner_nonce,omitempty"`
	CachedFromRequestID string                             `json:"cached_from_request_id,omitempty"`
	CachedFromEscrowID  string                             `json:"cached_from_escrow_id,omitempty"`
	Winner              *requestAccountingAttemptResponse  `json:"winner,omitempty"`
	Attempts            []requestAccountingAttemptResponse `json:"attempts"`
	Cost                requestAccountingCostResponse      `json:"cost"`
	StartedAt           string                             `json:"started_at,omitempty"`
	CompletedAt         string                             `json:"completed_at,omitempty"`
}

func (p *Proxy) handleRequestAccounting(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	requestID := strings.TrimSpace(r.PathValue("request_id"))
	if requestID == "" {
		http.Error(w, `{"error":{"message":"request_id is required"}}`, http.StatusBadRequest)
		return
	}
	if p.perf == nil {
		http.Error(w, `{"error":{"message":"request accounting unavailable"}}`, http.StatusServiceUnavailable)
		return
	}
	rec, ok, err := p.perf.FindAccountingRequest(requestID, p.escrowID)
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"error":{"message":%q}}`, err.Error()), http.StatusInternalServerError)
		return
	}
	if !ok {
		http.Error(w, fmt.Sprintf(`{"error":{"message":"request %s not found for devshard %s"}}`, requestID, p.escrowID), http.StatusNotFound)
		return
	}

	stateSnapshot := p.sm.SnapshotState()
	resp := requestAccountingResponse{
		RequestID:           rec.RequestID,
		EscrowID:            rec.EscrowID,
		Model:               rec.Model,
		Outcome:             rec.Outcome,
		Decision:            rec.Decision,
		WinnerNonce:         rec.WinnerNonce,
		CachedFromRequestID: rec.CachedFromRequestID,
		CachedFromEscrowID:  rec.CachedFromEscrowID,
		Attempts:            make([]requestAccountingAttemptResponse, 0, len(rec.Attempts)),
	}
	if !rec.StartedAt.IsZero() {
		resp.StartedAt = rec.StartedAt.Format(time.RFC3339Nano)
	}
	if !rec.CompletedAt.IsZero() {
		resp.CompletedAt = rec.CompletedAt.Format(time.RFC3339Nano)
	}

	for _, attempt := range rec.Attempts {
		view := requestAccountingAttemptResponse{
			Nonce:          attempt.Nonce,
			HostIdx:        attempt.HostIdx,
			ParticipantKey: attempt.ParticipantKey,
			Probe:          attempt.Probe,
			Winner:         attempt.Winner || attempt.Nonce == rec.WinnerNonce,
			Status:         "not_in_state",
		}
		if !attempt.CreatedAt.IsZero() {
			view.CreatedAt = attempt.CreatedAt.Format(time.RFC3339Nano)
		}
		if inf, found := stateSnapshot.Inferences[attempt.Nonce]; found && inf != nil {
			status := inferenceStatusName[inf.Status]
			if status == "" {
				status = fmt.Sprintf("unknown(%d)", inf.Status)
			}
			view.Status = status
			view.Model = inf.Model
			view.ExecutorSlot = inf.ExecutorSlot
			view.InputLength = inf.InputLength
			view.MaxTokens = inf.MaxTokens
			view.InputTokens = inf.InputTokens
			view.OutputTokens = inf.OutputTokens
			view.ReservedCost = inf.ReservedCost
			view.ActualCost = inf.ActualCost
			view.StartedAt = inf.StartedAt
			view.ConfirmedAt = inf.ConfirmedAt
			if view.Winner {
				resp.Cost.WinnerActualCost += inf.ActualCost
			} else {
				resp.Cost.OtherAttemptsActualCost += inf.ActualCost
			}
			resp.Cost.AllAttemptsActualCost += inf.ActualCost
		}
		resp.Attempts = append(resp.Attempts, view)
		if view.Winner {
			winner := view
			resp.Winner = &winner
		}
	}

	writeJSON(w, resp)
}

func (p *Proxy) admissionError() error {
	if p == nil || p.phaseGate == nil {
		return nil
	}
	return p.phaseGate.AdmissionError()
}

func (p *Proxy) handleDebugSignatures(w http.ResponseWriter, r *http.Request) {
	entries, highestQuorum, hasQuorum := p.session.SignatureStatus()

	resp := map[string]any{
		"current_nonce":        p.session.Nonce(),
		"total_slots":          p.sm.TotalSlots(),
		"quorum_threshold":     p.sm.QuorumThreshold(),
		"highest_quorum_nonce": highestQuorum,
		"has_quorum":           hasQuorum,
		"nonces":               entries,
	}

	writeJSON(w, resp)
}

func (p *Proxy) handleCollectSignatures(w http.ResponseWriter, r *http.Request) {
	nonceStr := r.URL.Query().Get("nonce")
	if nonceStr == "" {
		http.Error(w, `{"error":{"message":"missing 'nonce' query parameter"}}`, http.StatusBadRequest)
		return
	}
	nonce, err := strconv.ParseUint(nonceStr, 10, 64)
	if err != nil {
		http.Error(w, `{"error":{"message":"invalid 'nonce' parameter"}}`, http.StatusBadRequest)
		return
	}

	currentNonce := p.session.Nonce()
	if nonce > currentNonce {
		http.Error(w, fmt.Sprintf(`{"error":{"message":"nonce %d is ahead of current nonce %d"}}`, nonce, currentNonce),
			http.StatusBadRequest)
		return
	}

	weight, threshold, total := p.session.CollectSignatures(r.Context(), nonce)

	resp := map[string]any{
		"nonce":            nonce,
		"sig_weight":       weight,
		"quorum_threshold": threshold,
		"total_slots":      total,
		"has_quorum":       weight >= threshold,
	}

	writeJSON(w, resp)
}

func (p *Proxy) handleSyncHosts(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if err := p.session.SyncHosts(r.Context()); err != nil {
		http.Error(w, fmt.Sprintf(`{"error":{"message":%q}}`, err.Error()), http.StatusInternalServerError)
		return
	}

	var phaseStr string
	switch p.sm.Phase() {
	case types.PhaseActive:
		phaseStr = "active"
	case types.PhaseFinalizing:
		phaseStr = "finalizing"
	case types.PhaseSettlement:
		phaseStr = "settlement"
	default:
		phaseStr = fmt.Sprintf("unknown(%d)", p.sm.Phase())
	}

	writeJSON(w, map[string]any{
		"escrow_id": p.escrowID,
		"nonce":     p.session.Nonce(),
		"phase":     phaseStr,
	})
}

func hostStatsDebugEntry(hs *types.HostStats) map[string]any {
	entry := map[string]any{
		"missed":  hs.Missed,
		"invalid": hs.Invalid,
		"cost":    hs.Cost,
	}
	if hs.RequiredValidations != 0 {
		entry["required_validations"] = hs.RequiredValidations
	}
	if hs.CompletedValidations != 0 {
		entry["completed_validations"] = hs.CompletedValidations
	}
	return entry
}

// handleState returns the escrow summary: session scalars, config, group, and
// the small per-slot maps. It deliberately omits the (potentially large)
// inference map -- that lives at /v1/debug/inferences -- so this endpoint stays
// bounded regardless of how many inferences an escrow has accumulated.
func (p *Proxy) handleState(w http.ResponseWriter, r *http.Request) {
	st := p.sm.SnapshotStateNoInferences()

	var phaseStr string
	switch st.Phase {
	case types.PhaseActive:
		phaseStr = "active"
	case types.PhaseFinalizing:
		phaseStr = "finalizing"
	case types.PhaseSettlement:
		phaseStr = "settlement"
	default:
		phaseStr = fmt.Sprintf("unknown(%d)", st.Phase)
	}

	session := map[string]any{
		"escrow_id":      st.EscrowID,
		"phase":          phaseStr,
		"balance":        st.Balance,
		"latest_nonce":   st.LatestNonce,
		"finalize_nonce": st.FinalizeNonce,
		"config": map[string]any{
			"refusal_timeout":   st.Config.RefusalTimeout,
			"execution_timeout": st.Config.ExecutionTimeout,
			"token_price":       st.Config.TokenPrice,
			"vote_threshold":    st.Config.VoteThreshold,
			"validation_rate":   st.Config.ValidationRate,
		},
	}

	group := make([]map[string]any, len(st.Group))
	for i, s := range st.Group {
		group[i] = map[string]any{
			"slot_id":           s.SlotID,
			"validator_address": s.ValidatorAddress,
		}
	}

	hostStats := make(map[string]any, len(st.HostStats))
	for slot, hs := range st.HostStats {
		hostStats[fmt.Sprintf("%d", slot)] = hostStatsDebugEntry(hs)
	}

	revealedSeeds := map[string]int64{}

	warmKeys := make(map[string]string, len(st.WarmKeys))
	for slot, addr := range st.WarmKeys {
		warmKeys[fmt.Sprintf("%d", slot)] = addr
	}

	resp := map[string]any{
		"session":        session,
		"group":          group,
		"host_stats":     hostStats,
		"revealed_seeds": revealedSeeds,
		"warm_keys":      warmKeys,
	}

	writeJSON(w, resp)
}

// handleDebugInferences returns the full inference map for the escrow. It is a
// debug-only endpoint (potentially large: up to the per-escrow inference cap)
// split out of /v1/state so that summary reads stay cheap. It uses
// ExportAllInferenceRecords rather than the live map so records already
// sealed to storage still appear in the dump.
func (p *Proxy) handleDebugInferences(w http.ResponseWriter, r *http.Request) {
	inferenceMap := p.sm.ExportAllInferenceRecords()
	inferences := make(map[string]any, len(inferenceMap))
	for id, rec := range inferenceMap {
		name := inferenceStatusName[rec.Status]
		if name == "" {
			name = fmt.Sprintf("unknown(%d)", rec.Status)
		}
		inferences[fmt.Sprintf("%d", id)] = map[string]any{
			"status":        name,
			"executor_slot": rec.ExecutorSlot,
			"model":         rec.Model,
			"prompt_hash":   hex.EncodeToString(rec.PromptHash),
			"response_hash": hex.EncodeToString(rec.ResponseHash),
			"input_length":  rec.InputLength,
			"max_tokens":    rec.MaxTokens,
			"input_tokens":  rec.InputTokens,
			"output_tokens": rec.OutputTokens,
			"reserved_cost": rec.ReservedCost,
			"actual_cost":   rec.ActualCost,
			"started_at":    rec.StartedAt,
			"confirmed_at":  rec.ConfirmedAt,
			"votes_valid":   rec.VotesValid,
			"votes_invalid": rec.VotesInvalid,
			"validated_by":  rec.ValidatedBy.SetBits(),
		}
	}

	writeJSON(w, map[string]any{
		"total_inferences": len(inferences),
		"inferences":       inferences,
	})
}
