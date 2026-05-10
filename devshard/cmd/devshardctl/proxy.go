package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"devshard/host"
	"devshard/state"
	"devshard/types"
	"devshard/user"
)

// metaDrainTimeout caps how long the upstream SSE drain may continue after
// the OpenAI client has disconnected. The drain must run long enough to
// observe devshard_meta (which the host emits after [DONE]) so that the
// session can merge mempool txs (e.g. MsgFinishInference) into pending,
// but a malicious upstream host must not be able to pin the proxy
// indefinitely once the client is gone.
const metaDrainTimeout = 5 * time.Second

// cancelFlag is a one-shot signal used to communicate "client disconnected"
// from the request handler down into runInference / sendAndProcess. The
// upstream HTTP context is intentionally NOT canceled when the client goes
// away -- protocol completion (devshard_meta + ProcessResponse) must run to
// preserve session sequencing of MsgFinishInference into the next user diff.
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
func watchClientCancel(r *http.Request, flag *cancelFlag) {
	if flag == nil || r == nil {
		return
	}
	go func() {
		<-r.Context().Done()
		flag.Trigger()
	}()
}

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
	fmt.Fprintf(w, "data: {\"devshard_stream_reset\":true}\n\n")
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
}

// inferenceLookupResponse is the JSON shape for GET /v1/inference. InferenceRecord
// fields are promoted to the top level so testermint and curl see a flat object;
// sealed marks whether the id is still in the live map (false) or only in
// sealedNonces / SealedAcc (true).
type inferenceLookupResponse struct {
	Sealed      bool   `json:"sealed"`
	InferenceID uint64 `json:"inference_id"`
	SealNonce   uint64 `json:"seal_nonce,omitempty"`
	types.InferenceRecord
}

// Proxy is the OpenAI-compatible HTTP proxy backed by a devshard session.
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
//
// flag may be nil. When non-nil and triggered (client disconnected), the
// second attempt is skipped (mitigation 2): no point streaming a reset to
// nobody, but the timeout flow still runs because the network needs
// MsgTimeoutInference recorded if the executor truly didn't finish.
func (p *Proxy) runInference(ctx context.Context, params user.InferenceParams, w io.Writer, flag *cancelFlag) error {
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
	finished, confirmedAt, err := p.sendAndProcess(ctx, prepared, nonce, flag)
	if err != nil {
		return err
	}
	if finished {
		return nil
	}

	// Wait for the appropriate deadline before retrying or collecting timeout votes.
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

	// Attempt 2 (final). Skipped when the client is gone.
	if !flag.Gone() {
		if w != nil {
			writeStreamReset(w)
		}
		finished, confirmedAt, err = p.sendAndProcess(ctx, prepared, nonce, flag)
		if err != nil {
			return err
		}
		if finished {
			return nil
		}
		if confirmedAt > 0 {
			reason = types.TimeoutReason_TIMEOUT_REASON_EXECUTION
		}
	}

	return p.handleTimeout(ctx, prepared, nonce, reason, params)
}

// sendAndProcess sends the prepared inference and processes the response.
// Returns finished=true when MsgFinishInference is in the host's mempool.
// confirmedAt is the executor's receipt timestamp (0 if no receipt received).
//
// When flag is non-nil and triggered, the underlying upstream context is
// capped by metaDrainTimeout so a malicious upstream host cannot pin the
// proxy indefinitely after the client has disconnected.
func (p *Proxy) sendAndProcess(ctx context.Context, prepared *user.PreparedInference, nonce uint64, flag *cancelFlag) (finished bool, confirmedAt int64, err error) {
	sendCtx := ctx
	if flag != nil {
		var cancel context.CancelFunc
		sendCtx, cancel = context.WithCancel(ctx)
		defer cancel()
		go func() {
			select {
			case <-flag.Done():
				select {
				case <-time.After(metaDrainTimeout):
					cancel()
				case <-sendCtx.Done():
				}
			case <-sendCtx.Done():
			}
		}()
	}

	resp, sendErr := p.session.SendOnly(sendCtx, prepared)
	if sendErr != nil && resp == nil {
		return false, 0, nil
	}

	// Always process whatever response we got -- even partial. The host
	// emits devshard_meta after [DONE]; if the client disconnected and
	// metaDrainTimeout fired mid-parse, the partial result still carries
	// receipt/state and any mempool txs that were read before the cap.
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
func hasMsgFinish(txs []*types.DevshardTx, nonce uint64) bool {
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

// proxyWriter delays WriteHeader(200) until the first Write call and
// swallows all output once the client has disconnected. This is the
// downstream side of the "decouple upstream from r.Context()" design:
// upstream work (host SSE drain, ProcessResponse, timeout flow) keeps
// running while the client-facing writer becomes a no-op.
type proxyWriter struct {
	w       http.ResponseWriter
	started bool
	flag    *cancelFlag
}

func (pw *proxyWriter) Write(p []byte) (int, error) {
	if pw.flag.Gone() {
		// Client is gone; pretend success so callers don't error mid-protocol.
		return len(p), nil
	}
	if !pw.started {
		pw.w.Header().Set("Content-Type", "text/event-stream")
		pw.w.Header().Set("Cache-Control", "no-cache")
		pw.w.Header().Set("Connection", "keep-alive")
		pw.w.WriteHeader(http.StatusOK)
		pw.started = true
	}
	return pw.w.Write(p)
}

func (pw *proxyWriter) Flush() {
	if pw.flag.Gone() {
		return
	}
	if f, ok := pw.w.(http.Flusher); ok {
		f.Flush()
	}
}

func (p *Proxy) handleStreaming(w http.ResponseWriter, r *http.Request, params user.InferenceParams) {
	flag := newCancelFlag()
	pw := &proxyWriter{w: w, flag: flag}
	watchClientCancel(r, flag)

	// Upstream work is intentionally NOT bound to r.Context(): the host
	// SSE body must be drained through devshard_meta even if the client
	// disconnects, so the session can merge MsgFinishInference into pending.
	// metaDrainTimeout (applied inside sendAndProcess) bounds how long the
	// upstream may run after the client is gone.
	err := p.runInference(context.Background(), params, pw, flag)
	if flag.Gone() {
		// Client already gone. Protocol work has been completed (or
		// capped by metaDrainTimeout). Nothing more to send back.
		return
	}
	if err != nil {
		if !pw.started {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadGateway)
			fmt.Fprintf(w, `{"error":{"message":%q}}`, err.Error())
			return
		}
		log.Printf("inference error (mid-stream): %v", err)
		fmt.Fprintf(pw, "data: {\"error\":{\"message\":%q}}\n\n", err.Error())
		pw.Flush()
		return
	}

	fmt.Fprint(pw, "data: [DONE]\n\n")
	pw.Flush()
}

func (p *Proxy) handleNonStreaming(w http.ResponseWriter, r *http.Request, params user.InferenceParams) {
	var buf bytes.Buffer
	flag := newCancelFlag()
	watchClientCancel(r, flag)

	err := p.runInference(context.Background(), params, &buf, flag)
	if flag.Gone() {
		// Client gone; skip the response write but protocol work has run.
		return
	}
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
	// Config is the active session configuration used by the devshard state machine.
	Config statusSessionConfig `json:"config"`
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
	SealGraceNonces   uint32 `json:"seal_grace_nonces"`
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
	sealed := p.sm.ExportSealedNonces()

	statusNames := map[types.InferenceStatus]string{
		types.StatusPending:     "pending",
		types.StatusStarted:     "started",
		types.StatusFinished:    "finished",
		types.StatusChallenged:  "challenged",
		types.StatusValidated:   "validated",
		types.StatusInvalidated: "invalidated",
		types.StatusTimedOut:    "timed_out",
	}

	liveStatusCounts := make(map[string]int)
	for _, rec := range st.Inferences {
		name := statusNames[rec.Status]
		if name == "" {
			name = fmt.Sprintf("unknown(%d)", rec.Status)
		}
		liveStatusCounts[name]++
	}

	phaseStr := "active"
	switch st.Phase {
	case types.PhaseFinalizing:
		phaseStr = "finalizing"
	case types.PhaseSettlement:
		phaseStr = "settlement"
	}

	resp := map[string]any{
		"nonce":             st.LatestNonce,
		"phase":             phaseStr,
		"balance":           st.Balance,
		"live_inferences":   len(st.Inferences),
		"sealed_inferences": len(sealed),
		"live_status_counts": liveStatusCounts,
		// Deprecated: same as live_inferences; kept for older scripts.
		"total_inferences": len(st.Inferences),
		"status_counts":    liveStatusCounts,
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
	cfg := st.Config
	resp := statusResponse{
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
			SealGraceNonces:   cfg.SealGraceNonces,
		},
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
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

	writeInferenceLookup := func(resp inferenceLookupResponse) {
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(resp); err != nil {
			http.Error(w, "encode response: "+err.Error(), http.StatusInternalServerError)
		}
	}

	if rec, ok := p.sm.GetInference(parsedID); ok {
		writeInferenceLookup(inferenceLookupResponse{
			Sealed:          false,
			InferenceID:     parsedID,
			InferenceRecord: rec,
		})
		return
	}

	sealed := p.sm.ExportSealedNonces()
	sealNonce, isSealed := sealed[parsedID]
	if !isSealed {
		http.Error(w, fmt.Sprintf("inference not found for inference ID: %s", inferenceID), http.StatusNotFound)
		return
	}

	// Committed entry bytes may still exist briefly after seal; usually empty post-drain.
	if rec, ok := p.sm.GetCommittedRecord(parsedID); ok {
		writeInferenceLookup(inferenceLookupResponse{
			Sealed:          true,
			InferenceID:     parsedID,
			SealNonce:       sealNonce,
			InferenceRecord: rec,
		})
		return
	}

	writeInferenceLookup(inferenceLookupResponse{
		Sealed:      true,
		InferenceID: parsedID,
		SealNonce:   sealNonce,
	})
}
