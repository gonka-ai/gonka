package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"subnet/host"
	"subnet/state"
	"subnet/types"
	"subnet/user"
)

// streamRegistry routes SSE lines to per-request writers by nonce.
type streamRegistry struct {
	mu      sync.RWMutex
	writers map[uint64]io.Writer
}

func newStreamRegistry() *streamRegistry {
	return &streamRegistry{writers: make(map[uint64]io.Writer)}
}

func (r *streamRegistry) register(nonce uint64, w io.Writer) {
	r.mu.Lock()
	r.writers[nonce] = w
	r.mu.Unlock()
}

func (r *streamRegistry) unregister(nonce uint64) {
	r.mu.Lock()
	delete(r.writers, nonce)
	r.mu.Unlock()
}

func (r *streamRegistry) callback(nonce uint64, line string) {
	r.mu.RLock()
	w := r.writers[nonce]
	r.mu.RUnlock()
	if w != nil {
		fmt.Fprintf(w, "%s\n\n", line)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	}
}

// writeStreamReset writes a stream_reset SSE event to signal the client
// that the connection was lost and the response will be replayed from scratch.
func writeStreamReset(w io.Writer) {
	fmt.Fprintf(w, "data: {\"subnet_stream_reset\":true}\n\n")
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
}

// Proxy is the OpenAI-compatible HTTP proxy backed by a subnet session.
type Proxy struct {
	session  *user.Session
	sm       *state.StateMachine
	escrowID string
	model    string
	registry *streamRegistry
}

type chatRequest struct {
	Model     string `json:"model"`
	Stream    bool   `json:"stream"`
	MaxTokens uint64 `json:"max_tokens"`
}

func (p *Proxy) handleChatCompletions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "read body: "+err.Error(), http.StatusBadRequest)
		return
	}

	var req chatRequest
	if err := json.Unmarshal(body, &req); err != nil {
		http.Error(w, "parse request: "+err.Error(), http.StatusBadRequest)
		return
	}

	model := req.Model
	if model == "" {
		model = p.model
	}
	maxTokens := req.MaxTokens
	if maxTokens == 0 {
		maxTokens = 2048
	}

	params := user.InferenceParams{
		Model:       model,
		Prompt:      body,
		InputLength: uint64(len(body)),
		MaxTokens:   maxTokens,
		StartedAt:   time.Now().Unix(),
	}

	if req.Stream {
		p.handleStreaming(w, r, params)
	} else {
		p.handleNonStreaming(w, r, params)
	}
}

// timeoutBuffer is added to session config deadlines so verifiers have
// passed their own deadline before the proxy fires the timeout.
// Var (not const) so tests can set it to 0 for fast execution.
var timeoutBuffer = 5 * time.Second

// runInference sends the inference to the host with at most two attempts.
// On first failure, waits for the appropriate deadline then retries once.
// If both attempts fail, collects timeout votes and submits MsgTimeoutInference.
func (p *Proxy) runInference(ctx context.Context, params user.InferenceParams, w io.Writer) error {
	prepared, err := p.session.PrepareInference(params)
	if err != nil {
		return fmt.Errorf("prepare: %w", err)
	}

	nonce := prepared.Nonce()
	if w != nil {
		p.registry.register(nonce, w)
		defer p.registry.unregister(nonce)
	}

	cfg := p.sm.SnapshotState().Config
	now := time.Now()

	// Attempt 1.
	finished, confirmedAt, err := p.sendAndProcess(ctx, prepared, nonce)
	if err != nil {
		return err
	}
	if finished {
		return nil
	}

	// Wait for the appropriate deadline.
	var reason types.TimeoutReason
	if confirmedAt > 0 {
		deadline := time.Unix(confirmedAt, 0).Add(
			time.Duration(cfg.ExecutionTimeout)*time.Second + timeoutBuffer)
		if !sleepUntil(ctx, deadline) {
			return ctx.Err()
		}
		reason = types.TimeoutReason_TIMEOUT_REASON_EXECUTION
	} else {
		deadline := now.Add(time.Duration(cfg.RefusalTimeout)*time.Second + timeoutBuffer)
		if !sleepUntil(ctx, deadline) {
			return ctx.Err()
		}
		reason = types.TimeoutReason_TIMEOUT_REASON_REFUSED
	}

	// Attempt 2 (final).
	if w != nil {
		writeStreamReset(w)
	}
	finished, confirmedAt, err = p.sendAndProcess(ctx, prepared, nonce)
	if err != nil {
		return err
	}
	if finished {
		return nil
	}

	// Update reason if attempt 2 revealed a receipt.
	if confirmedAt > 0 {
		reason = types.TimeoutReason_TIMEOUT_REASON_EXECUTION
	}

	return p.handleTimeout(ctx, prepared, nonce, reason, params)
}

// sendAndProcess sends the prepared inference and processes the response.
// Returns finished=true when MsgFinishInference is in the host's mempool.
// confirmedAt is the executor's receipt timestamp (0 if no receipt received).
func (p *Proxy) sendAndProcess(ctx context.Context, prepared *user.PreparedInference, nonce uint64) (finished bool, confirmedAt int64, err error) {
	resp, sendErr := p.session.SendOnly(ctx, prepared)
	if sendErr != nil && resp == nil {
		return false, 0, nil
	}

	if err := p.session.ProcessResponse(prepared.HostIdx(), resp, nonce); err != nil {
		return false, 0, fmt.Errorf("process response: %w", err)
	}

	if sendErr == nil && hasMsgFinish(resp.Mempool, nonce) {
		return true, resp.ConfirmedAt, nil
	}

	return false, resp.ConfirmedAt, nil
}

// sleepUntil blocks until deadline or context cancellation.
// Returns true if the deadline was reached, false if cancelled.
func sleepUntil(ctx context.Context, deadline time.Time) bool {
	d := time.Until(deadline)
	if d <= 0 {
		return true
	}
	return sleep(ctx, d)
}

// hasMsgFinish returns true if mempool contains MsgFinishInference for the given nonce.
func hasMsgFinish(txs []*types.SubnetTx, nonce uint64) bool {
	for _, tx := range txs {
		if fi := tx.GetFinishInference(); fi != nil && fi.InferenceId == nonce {
			return true
		}
	}
	return false
}

// sleep returns false if context was cancelled during the wait.
func sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}

// handleTimeout collects timeout votes from verifier hosts and submits
// MsgTimeoutInference. Single attempt -- the timeoutBuffer ensures verifiers
// have already passed their own deadline before the proxy fires.
func (p *Proxy) handleTimeout(ctx context.Context, prepared *user.PreparedInference, nonce uint64, reason types.TimeoutReason, params user.InferenceParams) error {
	payload := &host.InferencePayload{
		Prompt:      params.Prompt,
		Model:       params.Model,
		InputLength: params.InputLength,
		MaxTokens:   params.MaxTokens,
		StartedAt:   params.StartedAt,
	}

	verifiers := p.session.TimeoutVerifiers()
	storedDiffs := p.session.Diffs()

	votes, err := p.session.CollectTimeoutVotes(ctx, nonce, reason, payload, verifiers, storedDiffs)
	if err != nil {
		return fmt.Errorf("collect timeout votes: %w", err)
	}

	if p.session.HasSufficientTimeoutVotes(votes) {
		p.session.AddPendingTimeoutTx(nonce, reason, votes)
		if err := p.session.SendPendingDiff(ctx); err != nil {
			return fmt.Errorf("send timeout diff: %w", err)
		}
		return fmt.Errorf("inference %d timed out: %s", nonce, reason)
	}

	log.Printf("inference %d: insufficient timeout votes, skipping timeout tx", nonce)
	return fmt.Errorf("inference %d timed out but insufficient votes to prove it", nonce)
}

// deferredWriter delays WriteHeader(200) until the first Write call.
// If runInference errors before any streaming data arrives, the proxy
// can still return a proper HTTP error status.
type deferredWriter struct {
	w       http.ResponseWriter
	started bool
}

func (d *deferredWriter) Write(p []byte) (int, error) {
	if !d.started {
		d.w.Header().Set("Content-Type", "text/event-stream")
		d.w.Header().Set("Cache-Control", "no-cache")
		d.w.Header().Set("Connection", "keep-alive")
		d.w.WriteHeader(http.StatusOK)
		d.started = true
	}
	return d.w.Write(p)
}

func (d *deferredWriter) Flush() {
	if f, ok := d.w.(http.Flusher); ok {
		f.Flush()
	}
}

func (p *Proxy) handleStreaming(w http.ResponseWriter, r *http.Request, params user.InferenceParams) {
	dw := &deferredWriter{w: w}

	err := p.runInference(r.Context(), params, dw)
	if err != nil {
		if !dw.started {
			// No streaming data sent yet -- return proper HTTP error.
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadGateway)
			fmt.Fprintf(w, `{"error":{"message":%q}}`, err.Error())
			return
		}
		// Already streaming -- send error as SSE data.
		log.Printf("inference error (mid-stream): %v", err)
		fmt.Fprintf(dw, "data: {\"error\":{\"message\":%q}}\n\n", err.Error())
		dw.Flush()
		return
	}

	fmt.Fprint(dw, "data: [DONE]\n\n")
	dw.Flush()
}

func (p *Proxy) handleNonStreaming(w http.ResponseWriter, r *http.Request, params user.InferenceParams) {
	var buf bytes.Buffer

	err := p.runInference(r.Context(), params, &buf)
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"error":{"message":%q}}`, err.Error()), http.StatusBadGateway)
		return
	}

	assembled := assembleSSEChunks(buf.String())
	w.Header().Set("Content-Type", "application/json")
	w.Write(assembled)
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

type statusResponse struct {
	EscrowID string `json:"escrow_id"`
	Nonce    uint64 `json:"nonce"`
	Phase    string `json:"phase"`
	Balance  uint64 `json:"balance"`
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
		case *types.SubnetTx_ConfirmStart:
			txs = append(txs, txInfo{Type: "confirm_start", ID: inner.ConfirmStart.InferenceId})
		case *types.SubnetTx_FinishInference:
			txs = append(txs, txInfo{Type: "finish", ID: inner.FinishInference.InferenceId})
		case *types.SubnetTx_Validation:
			txs = append(txs, txInfo{Type: "validation", ID: inner.Validation.InferenceId})
		case *types.SubnetTx_ValidationVote:
			txs = append(txs, txInfo{Type: "vote", ID: inner.ValidationVote.InferenceId})
		case *types.SubnetTx_RevealSeed:
			txs = append(txs, txInfo{Type: "reveal_seed", ID: uint64(inner.RevealSeed.SlotId)})
		default:
			txs = append(txs, txInfo{Type: fmt.Sprintf("%T", tx.GetTx())})
		}
	}

	resp := map[string]any{
		"nonce":     p.session.Nonce(),
		"pending":   txs,
		"warm_keys": warmKeys,
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
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

	resp := map[string]any{
		"nonce":             st.LatestNonce,
		"balance":           st.Balance,
		"total_inferences":  len(st.Inferences),
		"status_counts":     counts,
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
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
	resp := statusResponse{
		EscrowID: p.escrowID,
		Nonce:    p.session.Nonce(),
		Phase:    phaseStr,
		Balance:  st.Balance,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}
