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
	"strconv"
	"strings"
	"sync"
	"time"

	"devshard/state"
	"devshard/types"
	"devshard/user"
)

var sseDoneMarker = []byte("data: [DONE]")

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

// Proxy is the OpenAI-compatible HTTP proxy backed by a devshard session.
type Proxy struct {
	session    *user.Session
	sm         *state.StateMachine
	escrowID   string
	model      string
	redundancy *Redundancy
	perf       *PerfTracker
	phaseGate  *ChainPhaseGate
}

type chatRequest struct {
	Model     string `json:"model"`
	Stream    bool   `json:"stream"`
	MaxTokens uint64 `json:"max_tokens"`
}

// normalizeContent converts multi-part content arrays to simple strings.
// [{"type":"text","text":"A"},{"type":"text","text":"B"}] → "A\nB"
func normalizeContent(body []byte) []byte {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		return body
	}
	msgsRaw, ok := raw["messages"]
	if !ok {
		return body
	}

	var msgs []map[string]json.RawMessage
	if err := json.Unmarshal(msgsRaw, &msgs); err != nil {
		return body
	}

	changed := false
	for i, msg := range msgs {
		contentRaw, ok := msg["content"]
		if !ok {
			continue
		}
		var parts []map[string]string
		if err := json.Unmarshal(contentRaw, &parts); err != nil {
			continue
		}
		var texts []string
		for _, p := range parts {
			if p["type"] == "text" && p["text"] != "" {
				texts = append(texts, p["text"])
			}
		}
		if len(texts) > 0 {
			combined, _ := json.Marshal(strings.Join(texts, "\n"))
			msgs[i]["content"] = combined
			changed = true
		}
	}

	if !changed {
		return body
	}

	newMsgs, err := json.Marshal(msgs)
	if err != nil {
		return body
	}
	raw["messages"] = newMsgs
	out, err := json.Marshal(raw)
	if err != nil {
		return body
	}
	return out
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
		http.Error(w, fmt.Sprintf(`{"error":{"message":%q}}`, err.Error()), gatewayStatusCodeForError(err))
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		logRequestStage(ctx, "proxy_read_body_failed", "error", err)
		http.Error(w, "read body: "+err.Error(), http.StatusBadRequest)
		return
	}

	body = normalizeContent(body)

	var req chatRequest
	if err := json.Unmarshal(body, &req); err != nil {
		logRequestStage(ctx, "proxy_parse_failed", "error", err)
		http.Error(w, "parse request: "+err.Error(), http.StatusBadRequest)
		return
	}

	model := req.Model
	if model == "" {
		model = p.model
	}
	maxTokens := req.MaxTokens
	if maxTokens == 0 {
		maxTokens = DefaultRequestMaxTokens
	}
	if DefaultRequestMaxTokens > 0 && maxTokens > DefaultRequestMaxTokens {
		maxTokens = DefaultRequestMaxTokens
	}

	params := user.InferenceParams{
		Model:       model,
		Prompt:      body,
		InputLength: uint64(len(body)),
		MaxTokens:   maxTokens,
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
	started        bool
	bytesWritten   int64
	sawDone        bool
	lastFlushErr   error
	flushFailed    bool
	disconnectOnce sync.Once
	flushFailOnce  sync.Once
	writeFailOnce  sync.Once
}

func newDeferredWriter(ctx context.Context, w http.ResponseWriter, escrow string) *deferredWriter {
	rid, _ := requestLogFromContext(ctx)
	return &deferredWriter{ctx: ctx, w: w, escrow: escrow, requestID: rid}
}

func (d *deferredWriter) Write(p []byte) (int, error) {
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
	dw := newDeferredWriter(r.Context(), w, p.escrowID)

	var doneWriteErr error
	err := p.redundancy.RunInference(r.Context(), params, dw)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			logRequestStage(r.Context(), "proxy_stream_terminated", "escrow", p.escrowID, "error", err, "bytes_written", dw.bytesWritten, "elapsed_ms", time.Since(started).Milliseconds())
			return
		}
		logRequestStage(r.Context(), "proxy_stream_failed", "escrow", p.escrowID, "error", err)
		statusCode := gatewayStatusCodeForError(err)
		if !dw.started {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(statusCode)
			fmt.Fprintf(w, `{"error":{"message":%q}}`, err.Error())
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

	err := p.redundancy.RunInference(r.Context(), params, &buf)
	if err != nil {
		logRequestStage(r.Context(), "proxy_request_failed", "escrow", p.escrowID, "error", err)
		http.Error(w, fmt.Sprintf(`{"error":{"message":%q}}`, err.Error()), gatewayStatusCodeForError(err))
		return
	}

	assembled := assembleSSEChunks(buf.String())
	assembled = filterClientLogprobs(assembled)
	if rid, ok := requestLogFromContext(r.Context()); ok {
		w.Header().Set("X-Request-Id", rid)
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(assembled)
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

func (p *Proxy) handleFinalize(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if err := p.session.Finalize(r.Context()); err != nil {
		http.Error(w, fmt.Sprintf(`{"error":{"message":%q}}`, err.Error()), http.StatusInternalServerError)
		return
	}

	st := p.sm.SnapshotState()
	finalNonce := p.session.Nonce()
	payload, err := state.BuildSettlementForProtocol(p.escrowID, st, p.session.Signatures()[finalNonce], finalNonce, p.sm.ProtocolVersion())
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
type statusSessionConfig struct {
	RefusalTimeout    int64  `json:"refusal_timeout"`
	ExecutionTimeout  int64  `json:"execution_timeout"`
	TokenPrice        uint64 `json:"token_price"`
	CreateDevshardFee uint64 `json:"create_devshard_fee"`
	FeePerNonce       uint64 `json:"fee_per_nonce"`
	VoteThreshold     uint32 `json:"vote_threshold"`
	ValidationRate    uint32 `json:"validation_rate"`
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
		"receipt_timeout_ms":     ReceiptTimeout.Milliseconds(),
		"advantage_threshold":    ParallelAdvantageThreshold,
		"unresponsive_threshold": UnresponsiveThreshold,
		"host_window_size":       PerfWindowSize,
		"participant_window_ms":  ParticipantPerfWindow.Milliseconds(),
		"request_log_size":       requestLogSize,
	})
}

func (p *Proxy) handleDebugState(w http.ResponseWriter, r *http.Request) {
	st := p.sm.SnapshotState()

	statusNames := map[types.InferenceStatus]string{
		types.StatusPending:     "pending",
		types.StatusStarted:     "started",
		types.StatusFinished:    "finished",
		types.StatusChallenged:  "challenged",
		types.StatusValidated:   "validated",
		types.StatusInvalidated: "invalidated",
		types.StatusTimedOut:    "timed_out",
	}

	counts := make(map[string]int)
	for _, rec := range st.Inferences {
		name := statusNames[rec.Status]
		if name == "" {
			name = fmt.Sprintf("unknown(%d)", rec.Status)
		}
		counts[name]++
	}

	writeJSON(w, map[string]any{
		"nonce":            st.LatestNonce,
		"balance":          st.Balance,
		"total_inferences": len(st.Inferences),
		"status_counts":    counts,
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

	st := p.sm.SnapshotState()
	cfg := st.Config
	status := statusResponse{
		EscrowID: p.escrowID,
		Nonce:    p.session.Nonce(),
		Phase:    phaseStr,
		Balance:  st.Balance,
		Config: statusSessionConfig{
			RefusalTimeout:    cfg.RefusalTimeout,
			ExecutionTimeout:  cfg.ExecutionTimeout,
			TokenPrice:        cfg.TokenPrice,
			CreateDevshardFee: cfg.CreateDevshardFee,
			FeePerNonce:       cfg.FeePerNonce,
			VoteThreshold:     cfg.VoteThreshold,
			ValidationRate:    cfg.ValidationRate,
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

func (p *Proxy) admissionError() error {
	if p == nil || p.phaseGate == nil {
		return nil
	}
	return p.phaseGate.AdmissionError()
}

func (p *Proxy) handleInference(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	inferenceID := r.Header.Get("X-Inference-Id")
	if inferenceID == "" {
		http.Error(w, "X-Inference-Id required", http.StatusBadRequest)
		return
	}

	parsedID, err := strconv.ParseUint(inferenceID, 10, 64)
	if err != nil {
		http.Error(w, fmt.Sprintf("invalid inference ID %s: %v", inferenceID, err), http.StatusBadRequest)
		return
	}

	st := p.sm.SnapshotState()
	inference, found := st.Inferences[parsedID]
	if !found {
		http.Error(w, fmt.Sprintf("inference not found for inference ID: %s", inferenceID), http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(inference)
}
