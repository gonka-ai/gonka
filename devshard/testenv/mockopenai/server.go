package mockopenai

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"devshard/testenv/replay"

	"github.com/labstack/echo/v4"
)

// Server serves OpenAI-compatible /v1/chat/completions.
type Server struct {
	echo       *echo.Echo
	replay     *replay.Dataset
	mu         sync.RWMutex
	fault      FaultConfig
	seedUsage  map[int]FaultConfig
	streamGate chan struct{}
	capacity   chan struct{}
	workers    chan struct{}
	journal    journalState
	chaos      ChaosConfig
	stats      struct {
		requestsReceived    atomic.Uint64
		successfulResponses atomic.Uint64
		failedResponses     atomic.Uint64
		timeouts            atomic.Uint64
		replayHits          atomic.Uint64
		replayMisses        atomic.Uint64
	}
}

// NewServer builds the HTTP server.
func NewServer(cfg Config) *Server {
	server, err := NewServerWithError(cfg)
	if err != nil {
		panic(err)
	}
	return server
}

// NewServerWithError builds the HTTP server and loads an optional replay file.
func NewServerWithError(cfg Config) (*Server, error) {
	if err := cfg.Chaos.Validate(); err != nil {
		return nil, err
	}
	s := &Server{fault: cfg.Faults, chaos: cfg.Chaos}
	if cfg.ReplayFile != "" {
		dataset, err := replay.LoadFile(cfg.ReplayFile)
		if err != nil {
			return nil, fmt.Errorf("load replay file: %w", err)
		}
		s.replay = dataset
	}
	if cfg.Workers > 0 {
		s.capacity = make(chan struct{}, cfg.Workers+cfg.Queue)
		s.workers = make(chan struct{}, cfg.Workers)
	}
	if s.fault.PauseStream {
		s.streamGate = make(chan struct{})
	}
	e := echo.New()
	e.HideBanner = true
	e.POST("/v1/chat/completions", s.handleChatCompletions)
	e.GET("/healthz", func(c echo.Context) error { return c.String(http.StatusOK, "ok") })
	e.GET("/testenv/stats", s.handleStats)
	e.GET("/testenv/log", s.handleLog)
	e.POST("/api/v1/models/status", s.handleModelStatus)
	e.POST("/testenv/fault", s.handleFaultPatch)
	e.POST("/testenv/rules", s.handleRules)
	e.POST("/testenv/stream/release", s.handleStreamRelease)
	s.echo = e
	return s, nil
}

// handleModelStatus is the small ML-node management contract real DAPI uses
// before it makes a node available for inference.
func (s *Server) handleModelStatus(c echo.Context) error {
	return c.JSON(http.StatusOK, map[string]string{"status": "DOWNLOADED"})
}

func (s *Server) patchFault(p FaultPatch) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if p.Chaos != nil {
		s.chaos = *p.Chaos
	}
	if p.Seed != nil {
		var knobs FaultConfig
		p.apply(&knobs)
		knobs = usageKnobs(knobs)
		if knobs == (FaultConfig{}) {
			delete(s.seedUsage, *p.Seed)
			return
		}
		if s.seedUsage == nil {
			s.seedUsage = map[int]FaultConfig{}
		}
		s.seedUsage[*p.Seed] = knobs
		return
	}
	if p.PauseStream != nil {
		if *p.PauseStream {
			s.releaseStreamLocked()
			s.streamGate = make(chan struct{})
		} else {
			s.releaseStreamLocked()
		}
	}
	p.apply(&s.fault)
}

// usageKnobs keeps the knobs a seed patch may scope.
func usageKnobs(f FaultConfig) FaultConfig {
	return FaultConfig{
		UsageCompletionTokens: f.UsageCompletionTokens,
		UsagePromptTokens:     f.UsagePromptTokens,
		PadEmptyChunks:        f.PadEmptyChunks,
		DropTokens:            f.DropTokens,
	}
}

// withSeedUsage replaces the usage knobs of f with the ones patched for
// seed, if any.
func (s *Server) withSeedUsage(f FaultConfig, seed *int) FaultConfig {
	if seed == nil {
		return f
	}
	s.mu.RLock()
	knobs, ok := s.seedUsage[*seed]
	s.mu.RUnlock()
	if ok {
		f.UsageCompletionTokens = knobs.UsageCompletionTokens
		f.UsagePromptTokens = knobs.UsagePromptTokens
		f.PadEmptyChunks = knobs.PadEmptyChunks
		f.DropTokens = knobs.DropTokens
	}
	return f
}

func (s *Server) chaosConfig() ChaosConfig {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.chaos
}

func (s *Server) streamFaults() (FaultConfig, <-chan struct{}) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.fault, s.streamGate
}

func (s *Server) releaseStreamLocked() {
	if s.streamGate == nil {
		return
	}
	select {
	case <-s.streamGate:
	default:
		close(s.streamGate)
	}
}

func (s *Server) handleStreamRelease(c echo.Context) error {
	s.mu.Lock()
	s.releaseStreamLocked()
	s.fault.PauseStream = false
	s.mu.Unlock()
	return c.JSON(http.StatusOK, map[string]string{"status": "released"})
}

func (s *Server) handleFaultPatch(c echo.Context) error {
	var p FaultPatch
	if err := c.Bind(&p); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	if p.Chaos != nil {
		if err := p.Chaos.Validate(); err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, err.Error())
		}
	}
	s.patchFault(p)
	return c.JSON(http.StatusOK, map[string]string{"status": "ok"})
}

// SetRules replaces the fault rules (same as POST /testenv/rules).
func (s *Server) SetRules(rules []FaultRule) error { return s.journal.setRules(rules) }

func (s *Server) handleRules(c echo.Context) error {
	var rules []FaultRule
	if err := c.Bind(&rules); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	if err := s.SetRules(rules); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	return c.JSON(http.StatusOK, map[string]int{"rules": len(rules)})
}

func (s *Server) handleStats(c echo.Context) error {
	st := s.journal.snapshotStats()
	st.RequestsReceived = s.stats.requestsReceived.Load()
	st.SuccessfulResponses = s.stats.successfulResponses.Load()
	st.FailedResponses = s.stats.failedResponses.Load()
	st.Timeouts = s.stats.timeouts.Load()
	st.ReplayHits = s.stats.replayHits.Load()
	st.ReplayMisses = s.stats.replayMisses.Load()
	return c.JSON(http.StatusOK, st)
}

// handleLog exports the whole request log. "complete" is false when records were dropped at the bound.
func (s *Server) handleLog(c echo.Context) error {
	recs, dropped := s.journal.snapshotLog()
	return c.JSON(http.StatusOK, map[string]any{"complete": dropped == 0, "dropped": dropped, "records": recs})
}

func (s *Server) recordFailure(ctx context.Context) {
	if ctx.Err() != nil {
		s.stats.timeouts.Add(1)
		return
	}
	s.stats.failedResponses.Add(1)
}

func (s *Server) handleChatCompletions(c echo.Context) (retErr error) {
	start := time.Now()
	ctx := c.Request().Context()
	s.stats.requestsReceived.Add(1)
	// The body is read before any fault applies, so a faulted answer is logged with its request.
	body, err := readBody(c.Request())
	if err != nil {
		s.recordFailure(ctx)
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}

	var req ChatRequest
	if err := json.Unmarshal(body, &req); err != nil {
		s.recordFailure(ctx)
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	if req.Model == "" {
		req.Model = "test-model"
	}

	phase := PhaseExecute
	if req.EnforcedTokens != nil {
		phase = PhaseReplay
	}
	rec := RequestRecord{
		StartUnixMs: start.UnixMilli(), Phase: phase, Seed: req.Seed, PromptKey: PromptKey(body), Model: req.Model,
		Stream: req.Stream, Outcome: "ok", Wire: wireViewOf(body),
	}
	if req.EnforcedTokens != nil {
		rec.EnforcedTokens = len(req.EnforcedTokens.Tokens)
		rec.EnforcedTops = countTops(req.EnforcedTokens)
	}
	defer func() {
		// Status stays 0 when nothing was written: a dropped connection or a request left hanging.
		if rec.Status == 0 && c.Response().Committed {
			rec.Status = c.Response().Status
		}
		// An answer cut short because the caller hung up (its context ended, or a write to the
		// closed connection failed) is not a full answer, whatever status went out first.
		if (rec.Outcome == "ok" || rec.Outcome == "prelude") && (retErr != nil || ctx.Err() != nil) {
			rec.Outcome = OutcomeClientGone
		}
		rec.DurationMs = time.Since(start).Milliseconds()
		s.journal.record(rec)
	}()
	queuedAt := time.Now()
	acquired := s.acquire(ctx)
	rec.QueuedMs = time.Since(queuedAt).Milliseconds()
	if !acquired {
		s.recordFailure(ctx)
		rec.Status, rec.Outcome = http.StatusServiceUnavailable, "capacity_exhausted"
		return c.JSON(http.StatusServiceUnavailable, map[string]string{"error": "mock-openai capacity exhausted"})
	}
	defer s.release()
	rec.ActiveAtStart = s.journal.enter()
	defer s.journal.leave()

	// Faults are read once a worker is free: a request that waited behind a hang is answered by
	// the faults in force when its turn comes, not by those of its arrival.
	f, streamGate := s.streamFaults()
	if f.Hang {
		<-ctx.Done()
		s.stats.timeouts.Add(1)
		rec.Outcome = "fault_hang"
		return ctx.Err()
	}
	if f.StreamErrorEnvelope {
		s.stats.failedResponses.Add(1)
		rec.Outcome = "fault_stream_error_envelope"
		return s.streamErrorEnvelope(c)
	}
	if f.HTTPStatus >= 400 {
		s.stats.failedResponses.Add(1)
		rec.Outcome = "fault_status"
		return c.JSON(f.HTTPStatus, OpenAIErrorBody(f.HTTPStatus, "mock-openai fault injection"))
	}
	f = s.withSeedUsage(f, req.Seed)

	// Chaos shapes executions of synthetic answers; a node that serves a replay file answers as the
	// recorded ML node did.
	var prelude preludeSpec
	if chaos := s.chaosConfig(); chaos.Enabled && phase == PhaseExecute && s.replay == nil {
		rec.Chaos = chaos.Pick(rec.PromptKey, req.Seed, req.Stream)
		switch rec.Chaos {
		case ChaosDelay:
			select {
			case <-time.After(time.Duration(chaos.delayMs()) * time.Millisecond):
			case <-ctx.Done():
				rec.Outcome = "client_gone_during_chaos_delay"
				return ctx.Err()
			}
		case ChaosClientError:
			s.stats.failedResponses.Add(1)
			rec.Outcome = "chaos_status"
			return c.JSON(http.StatusBadRequest, OpenAIErrorBody(http.StatusBadRequest, "mock-openai chaos: request rejected"))
		case ChaosServerError:
			s.stats.failedResponses.Add(1)
			rec.Outcome = "chaos_status"
			return c.JSON(http.StatusInternalServerError, OpenAIErrorBody(http.StatusInternalServerError, "mock-openai chaos: engine failure"))
		case ChaosErrorString:
			s.stats.failedResponses.Add(1)
			rec.Outcome = "chaos_status"
			return c.JSON(http.StatusInternalServerError, map[string]any{"error": "mock-openai chaos: error as a string"})
		case ChaosAbort:
			rec.Outcome = "chaos_abort"
			panic(http.ErrAbortHandler)
		case ChaosPrelude:
			prelude = preludeSpec{chunks: 3, hold: 2 * time.Second}
		case ChaosPartial:
			f.PartialStream = true
		case ChaosSSEError:
			f.SSEErrorMessage = "mock-openai chaos: engine failure mid-stream"
		case ChaosEmpty:
			f.emptyAnswer = true
		case ChaosOddUsage:
			f.oddUsage = true
		case ChaosWrongAnswer:
			f.wrongAnswer = true
		}
	}
	// Only a synthetic streamed answer carries a prelude: a recorded stream and an oracle replay do not.
	preludeFits := req.Stream && s.replay == nil && !(f.Oracle && phase == PhaseReplay)
	if rule := s.journal.matchRule(phase, req.Seed, promptText(req.Messages), preludeFits); rule != nil {
		rec.Rule = rule.Name
		if rule.PreludeChunks > 0 {
			prelude = preludeSpec{chunks: rule.PreludeChunks, hold: time.Duration(rule.PreludeHoldMs) * time.Millisecond}
		}
		if rule.DelayMs > 0 {
			select {
			case <-time.After(time.Duration(rule.DelayMs) * time.Millisecond):
			case <-ctx.Done():
				rec.Outcome = "client_gone_during_rule_delay"
				return ctx.Err()
			}
		}
		if rule.Abort {
			// Dropping the connection is what a transport error looks like to the caller.
			rec.Outcome = "rule_abort"
			panic(http.ErrAbortHandler)
		}
		if rule.HTTPStatus >= 400 {
			s.stats.failedResponses.Add(1)
			rec.Outcome = "rule_status"
			return c.JSON(rule.HTTPStatus, OpenAIErrorBody(rule.HTTPStatus, "mock-openai fault rule "+rule.Name))
		}
	}

	// Latency delays Validate (non-stream JSON or an enforced-token replay) only.
	// Stream first-token delay is StreamFirstTokenDelay inside streamCompletion;
	// StreamChunkDelay sleeps after each flushed chunk.
	validate := !req.Stream || isValidationReplay(body)
	if validate {
		f.StreamFirstTokenDelay = 0
		f.StreamChunkDelay = 0
		f.FirstContentDelay = 0
		f.ExecDuration = 0
		f.DropFirstChunk = false
		f.PartialStream = false
		f.SSEErrorMessage = ""
		f.PauseStream = false
		f.UsageCompletionTokens = 0
		f.UsagePromptTokens = 0
		f.PadEmptyChunks = 0
		f.DropTokens = false
	}
	latency := f.Latency
	if phase == PhaseReplay && f.ReplayDelay > 0 {
		latency = f.ReplayDelay
	}
	if latency > 0 && validate {
		if err := waitForLatency(ctx, latency); err != nil {
			s.recordFailure(ctx)
			rec.Outcome = "client_gone_during_latency"
			return err
		}
	}

	if f.HTTPStatus >= 400 || f.FailureRate > 0 {
		selected := f.FailureRate <= 0 || f.ShouldFail(body)
		if selected {
			status := f.HTTPStatus
			if status < 400 {
				status = http.StatusServiceUnavailable
			}
			s.stats.failedResponses.Add(1)
			rec.Outcome = "fault_failure_rate"
			return c.JSON(status, map[string]string{"error": "mock-openai fault injection"})
		}
	}

	if f.Oracle && phase == PhaseReplay {
		if err := s.replayCompletion(c, req, f.ChunkTokens, &rec); err != nil {
			s.recordFailure(ctx)
			return err
		}
		s.stats.successfulResponses.Add(1)
		return nil
	}
	text := completionText(body)
	var recorded *replay.Recorded
	if s.replay != nil {
		sample, ok := s.lookupReplay(req, body)
		if !ok {
			s.stats.replayMisses.Add(1)
			rec.Outcome = "replay_miss"
			return c.JSON(http.StatusNotFound, map[string]string{"error": "mock-openai replay sample not found"})
		}
		s.stats.replayHits.Add(1)
		text = sample.Response.Content
		recorded = sample.Recorded
	}
	if f.emptyAnswer {
		text = ""
	}
	if req.Stream {
		var err error
		if recorded != nil {
			err = s.streamRecorded(c, recorded.Stream, f, streamGate, &rec)
		} else {
			err = s.streamCompletion(c, req, text, body, f, streamGate, start, &rec, prelude)
		}
		if err != nil || f.PartialStream || f.DropFirstChunk {
			// A stream the fault cut is not a full answer, even though it ended cleanly.
			if err == nil && (rec.Outcome == "ok" || rec.Outcome == "prelude") {
				switch {
				case f.PartialStream:
					rec.Outcome = "fault_partial_stream"
				case f.DropFirstChunk:
					rec.Outcome = "fault_drop_first_chunk"
				}
			}
			s.recordFailure(ctx)
			return err
		}
		s.stats.successfulResponses.Add(1)
		return nil
	}
	if recorded != nil {
		err = c.Blob(http.StatusOK, echo.MIMEApplicationJSON, recorded.NonStream)
		if err == nil {
			positions, completion := recordedAnswer(recorded.NonStream)
			rec.TokensOut = positions
			if completion != nil {
				rec.UsageCompletion = *completion
			}
		}
	} else {
		err = s.jsonCompletion(c, req, text, body, f, &rec)
	}
	if err != nil {
		s.recordFailure(ctx)
		return err
	}
	s.stats.successfulResponses.Add(1)
	return nil
}

// lookupReplay prefers a recorded sample for the exact request and falls back
// to the text-only samples matched by model and messages.
func (s *Server) lookupReplay(req ChatRequest, body []byte) (replay.Sample, bool) {
	if sample, _, ok := s.replay.LookupRequest(body); ok {
		return sample, true
	}
	sample, _, ok := s.replay.Lookup(req.Model, replayMessages(req.Messages))
	return sample, ok
}

func (s *Server) acquire(ctx context.Context) bool {
	if s.capacity == nil {
		return true
	}
	select {
	case s.capacity <- struct{}{}:
	case <-ctx.Done():
		return false
	default:
		return false
	}
	select {
	case s.workers <- struct{}{}:
		return true
	case <-ctx.Done():
		<-s.capacity
		return false
	}
}

func (s *Server) release() {
	if s.capacity == nil {
		return
	}
	<-s.workers
	<-s.capacity
}

// preludeSpec is the role-only prefix a chaos action or a fault rule puts in front of a streamed answer.
type preludeSpec struct {
	chunks int
	hold   time.Duration
}

// oraclePromptTokens counts prompt tokens from the message contents alone, so an execution and the
// replay of it report the same figure even though the two request bodies differ in options.
func oraclePromptTokens(req ChatRequest) int {
	n := len(promptText(req.Messages)) / 4
	if n < 1 {
		return 1
	}
	return n
}

// replayCompletion answers a validator replay from the enforced tokens it carries, as received. A
// replay that asks to stream (the validator always does) gets SSE as from vLLM: chunks of chunkTokens
// tokens, each with its logprob entries, then the usage chunk and [DONE]. Otherwise one JSON answer.
func (s *Server) replayCompletion(c echo.Context, req ChatRequest, chunkTokens int, rec *RequestRecord) error {
	n := len(req.EnforcedTokens.Tokens)
	rec.TokensOut, rec.UsageCompletion = n, n
	promptTok := oraclePromptTokens(req)
	usage := map[string]any{"prompt_tokens": promptTok, "completion_tokens": n, "total_tokens": promptTok + n}
	var content []map[string]any
	if req.Logprobs {
		content = replayLogprobContent(req.EnforcedTokens, req.TopLogprobs)
	}
	created := time.Now().Unix()
	if !req.Stream {
		choice := map[string]any{
			"index":         0,
			"message":       map[string]any{"role": "assistant", "content": fmt.Sprintf("replay of %d enforced tokens", n)},
			"finish_reason": "stop",
			"logprobs":      nil,
		}
		if req.Logprobs {
			choice["logprobs"] = map[string]any{"content": content}
		}
		return c.JSON(http.StatusOK, map[string]any{
			"id": "chatcmpl-mockopenai-replay", "object": "chat.completion", "created": created, "model": req.Model,
			"choices": []map[string]any{choice}, "usage": usage,
		})
	}
	rec.ChunkTokens = max(chunkTokens, 1)
	c.Response().Header().Set(echo.HeaderContentType, "text/event-stream")
	c.Response().Header().Set("Cache-Control", "no-cache")
	c.Response().WriteHeader(http.StatusOK)
	w := c.Response().Writer
	flusher, _ := w.(http.Flusher)
	write := func(data []byte) error {
		if _, err := fmt.Fprintf(w, "data: %s\n\n", data); err != nil {
			return err
		}
		if flusher != nil {
			flusher.Flush()
		}
		return nil
	}
	chunk := func(choice map[string]any, extra map[string]any) error {
		ev := map[string]any{"id": "chatcmpl-mockopenai-replay", "object": "chat.completion.chunk", "created": created,
			"model": req.Model, "choices": []map[string]any{choice}}
		for k, v := range extra {
			ev[k] = v
		}
		data, err := json.Marshal(ev)
		if err != nil {
			return err
		}
		return write(data)
	}
	pos := 0
	for _, size := range chunkSizes(n, chunkTokens) {
		ids := make([]string, 0, size)
		for _, t := range req.EnforcedTokens.Tokens[pos : pos+size] {
			ids = append(ids, t.Token)
		}
		var lp any
		if req.Logprobs {
			lp = map[string]any{"content": content[pos : pos+size]}
		}
		pos += size
		if err := chunk(map[string]any{"index": 0, "delta": map[string]any{"content": strings.Join(ids, " ")},
			"logprobs": lp, "finish_reason": nil}, nil); err != nil {
			return err
		}
	}
	if err := chunk(map[string]any{"index": 0, "delta": map[string]any{"content": ""}, "logprobs": nil, "finish_reason": "stop"}, nil); err != nil {
		return err
	}
	if err := chunk(map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": "stop"}, map[string]any{"usage": usage}); err != nil {
		return err
	}
	return write([]byte("[DONE]"))
}

func (s *Server) jsonCompletion(c echo.Context, req ChatRequest, text string, body []byte, f FaultConfig, rec *RequestRecord) error {
	oracle := f.Oracle
	promptTok := promptTokenEstimate(body)
	completionTok := len(text) / 4
	rec.TokensOut = len([]rune(text))
	if oracle || f.HonestUsage {
		completionTok = rec.TokensOut
	}
	if oracle {
		promptTok = oraclePromptTokens(req)
	}
	if completionTok < 1 && !f.emptyAnswer {
		completionTok = 1
	}
	if f.oddUsage {
		completionTok = oddUsage(rec.TokensOut)
	}
	rec.UsageCompletion = completionTok
	choice := map[string]any{
		"index": 0,
		"message": map[string]any{
			"role":    "assistant",
			"content": text,
		},
		"finish_reason": "stop",
	}
	if req.Logprobs {
		content := buildLogprobContent(text, req.TopLogprobs)
		if f.wrongAnswer {
			forgeLogprobs(content)
		}
		choice["logprobs"] = map[string]any{"content": content}
	} else {
		choice["logprobs"] = nil
	}
	resp := map[string]any{
		"id":      "chatcmpl-mockopenai",
		"object":  "chat.completion",
		"created": time.Now().Unix(),
		"model":   req.Model,
		"choices": []map[string]any{choice},
		"usage": map[string]any{
			"prompt_tokens":     promptTok,
			"completion_tokens": completionTok,
			"total_tokens":      promptTok + completionTok,
		},
	}
	return c.JSON(http.StatusOK, resp)
}

func (s *Server) streamCompletion(c echo.Context, req ChatRequest, text string, body []byte, f FaultConfig, streamGate <-chan struct{}, start time.Time, rec *RequestRecord, prelude preludeSpec) error {
	if f.FirstContentDelay <= 0 && f.ExecDuration <= 0 {
		if err := waitForLatency(c.Request().Context(), f.StreamFirstTokenDelay); err != nil {
			return err
		}
	}
	c.Response().Header().Set(echo.HeaderContentType, "text/event-stream")
	c.Response().Header().Set("Cache-Control", "no-cache")
	c.Response().Header().Set("Connection", "keep-alive")
	c.Response().WriteHeader(http.StatusOK)

	w := c.Response().Writer
	flusher, _ := w.(http.Flusher)
	created := time.Now().Unix()
	id := "chatcmpl-mockopenai"
	model := req.Model
	scheduled := f.FirstContentDelay > 0 || f.ExecDuration > 0

	writeChunk := func(delta map[string]any, finish *string, logprobs any) error {
		choice := map[string]any{"index": 0, "delta": delta, "logprobs": logprobs}
		if finish != nil {
			choice["finish_reason"] = *finish
		} else {
			choice["finish_reason"] = nil
		}
		chunk := map[string]any{
			"id": id, "object": "chat.completion.chunk", "created": created, "model": model,
			"choices": []map[string]any{choice},
		}
		data, err := json.Marshal(chunk)
		if err != nil {
			return err
		}
		if _, err := fmt.Fprintf(w, "data: %s\n\n", data); err != nil {
			return err
		}
		if flusher != nil {
			flusher.Flush()
		}
		if !scheduled && f.StreamChunkDelay > 0 {
			time.Sleep(f.StreamChunkDelay)
		}
		return nil
	}

	if prelude.chunks > 0 {
		for i := 0; i < prelude.chunks; i++ {
			if err := writeChunk(map[string]any{"role": "assistant"}, nil, nil); err != nil {
				rec.Outcome = "client_gone_during_prelude"
				return err
			}
		}
		rec.Outcome = "prelude"
		if prelude.hold > 0 {
			select {
			case <-time.After(prelude.hold):
			case <-c.Request().Context().Done():
				rec.Outcome = "client_gone_during_prelude_hold"
				return c.Request().Context().Err()
			}
		}
	}

	tokens := []rune(text)
	if len(tokens) == 0 && !f.emptyAnswer {
		tokens = []rune(" ")
	}
	// vLLM-style streaming error: optional content prefix, then error event + [DONE].
	if msg := strings.TrimSpace(f.SSEErrorMessage); msg != "" {
		rec.ChunkTokens = 1
		// Emit a short content prefix so partial body capture has bytes before the error.
		prefix := tokens
		if len(prefix) > 4 {
			prefix = prefix[:4]
		}
		for _, r := range prefix {
			// vLLM sends logprobs with every content chunk before the error, and a host counts only
			// the chunks that carry them.
			var lp any
			if req.Logprobs {
				content := buildLogprobContent(string(r), req.TopLogprobs)
				if f.wrongAnswer {
					forgeLogprobs(content)
				}
				lp = map[string]any{"content": content}
			}
			if err := writeChunk(map[string]any{"content": string(r)}, nil, lp); err != nil {
				return err
			}
			rec.TokensOut++
		}
		errPayload, err := json.Marshal(OpenAIErrorBody(500, msg))
		if err != nil {
			return err
		}
		if _, err := fmt.Fprintf(w, "data: %s\n\n", errPayload); err != nil {
			return err
		}
		if flusher != nil {
			flusher.Flush()
		}
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
		if flusher != nil {
			flusher.Flush()
		}
		return nil
	}
	// ChunkTokens groups tokens into chunks; 0 and 1 keep one token per chunk.
	groups := chunkSizes(len(tokens), f.ChunkTokens)
	rec.ChunkTokens = max(f.ChunkTokens, 1)
	first := true
	paused := false
	pos := 0
	for i, size := range groups {
		tok := string(tokens[pos : pos+size])
		pos += size
		if first && f.DropFirstChunk {
			first = false
			continue
		}
		first = false
		if scheduled {
			wait := time.Until(start.Add(emitOffset(i, len(groups), f.FirstContentDelay, f.ExecDuration)))
			select {
			case <-time.After(wait):
			case <-c.Request().Context().Done():
				return c.Request().Context().Err()
			}
		}
		var lp any
		if req.Logprobs {
			content := buildLogprobContent(tok, req.TopLogprobs)
			if f.wrongAnswer {
				forgeLogprobs(content)
			}
			lp = map[string]any{"content": content}
		}
		if err := writeChunk(map[string]any{"content": tok}, nil, lp); err != nil {
			return err
		}
		rec.TokensOut += size
		if !paused && f.PauseStream && streamGate != nil {
			paused = true
			select {
			case <-streamGate:
			case <-c.Request().Context().Done():
				return c.Request().Context().Err()
			}
		}
		if f.PartialStream && i == len(groups)/2 {
			return nil
		}
	}
	if f.PartialStream {
		return nil
	}
	stop := "stop"
	if err := writeChunk(map[string]any{"content": ""}, &stop, nil); err != nil {
		return err
	}
	promptTok := promptTokenEstimate(body)
	completionTok := len(text) / 4
	if f.Oracle || f.HonestUsage {
		completionTok = len(tokens)
	}
	if f.Oracle {
		promptTok = oraclePromptTokens(req)
	}
	if completionTok < 1 && !f.emptyAnswer {
		completionTok = 1
	}
	if f.oddUsage {
		completionTok = oddUsage(len(tokens))
	}
	rec.UsageCompletion = completionTok
	usageChunk := map[string]any{
		"id": id, "object": "chat.completion.chunk", "created": created, "model": model,
		"choices": []map[string]any{{"index": 0, "delta": map[string]any{}, "finish_reason": "stop"}},
		"usage": map[string]any{
			"prompt_tokens": promptTok, "completion_tokens": completionTok, "total_tokens": promptTok + completionTok,
		},
	}
	ud, _ := json.Marshal(usageChunk)
	if _, err := fmt.Fprintf(w, "data: %s\n\n", ud); err != nil {
		return err
	}
	if flusher != nil {
		flusher.Flush()
	}
	_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	if flusher != nil {
		flusher.Flush()
	}
	return nil
}

func isValidationReplay(body []byte) bool {
	var probe struct {
		EnforcedTokens json.RawMessage `json:"enforced_tokens"`
	}
	return json.Unmarshal(body, &probe) == nil && len(probe.EnforcedTokens) > 0 && string(probe.EnforcedTokens) != "null"
}

// streamRecorded writes recorded SSE payloads as they are, so the replay keeps
// the ML node's chunk boundaries, key order and number spelling. The fault
// knobs act on it as in streamCompletion, with the recorded token chunks (the
// ones carrying a sampled token, before the chunk that finishes the choice)
// standing in for its one-rune chunks: DropFirstChunk drops the first token
// chunk, PauseStream holds after the first token chunk written, PartialStream
// stops after the middle one, and SSEErrorMessage keeps up to four of them
// ahead of the error event. The usage and padding knobs rewrite the
// recording first.
func (s *Server) streamRecorded(c echo.Context, chunks []json.RawMessage, f FaultConfig, streamGate <-chan struct{}, rec *RequestRecord) error {
	chunks, err := overstateRecordedStream(chunks, f)
	if err != nil {
		return err
	}
	if err := waitForLatency(c.Request().Context(), f.StreamFirstTokenDelay); err != nil {
		return err
	}
	c.Response().Header().Set(echo.HeaderContentType, "text/event-stream")
	c.Response().Header().Set("Cache-Control", "no-cache")
	c.Response().Header().Set("Connection", "keep-alive")
	c.Response().WriteHeader(http.StatusOK)

	w := c.Response().Writer
	flusher, _ := w.(http.Flusher)
	writeData := func(data []byte) error {
		if _, err := fmt.Fprintf(w, "data: %s\n\n", data); err != nil {
			return err
		}
		positions, completion := recordedAnswer(data)
		rec.TokensOut += positions
		if completion != nil {
			rec.UsageCompletion = *completion
		}
		if flusher != nil {
			flusher.Flush()
		}
		if f.StreamChunkDelay > 0 {
			time.Sleep(f.StreamChunkDelay)
		}
		return nil
	}

	isToken, final := recordedLayout(chunks)
	if msg := strings.TrimSpace(f.SSEErrorMessage); msg != "" {
		written := 0
		for i, chunk := range chunks[:final] {
			if isToken[i] {
				if written == 4 {
					break
				}
				written++
			}
			if err := writeData(chunk); err != nil {
				return err
			}
		}
		errPayload, err := json.Marshal(OpenAIErrorBody(500, msg))
		if err != nil {
			return err
		}
		if _, err := fmt.Fprintf(w, "data: %s\n\n", errPayload); err != nil {
			return err
		}
		if flusher != nil {
			flusher.Flush()
		}
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
		if flusher != nil {
			flusher.Flush()
		}
		return nil
	}
	tokens := 0
	for _, token := range isToken[:final] {
		if token {
			tokens++
		}
	}
	token := -1
	paused := false
	for i, chunk := range chunks {
		if f.PartialStream && i >= final {
			return nil
		}
		if isToken[i] {
			token++
			if token == 0 && f.DropFirstChunk {
				continue
			}
		}
		if err := writeData(chunk); err != nil {
			return err
		}
		if !isToken[i] {
			continue
		}
		if !paused && f.PauseStream && streamGate != nil {
			paused = true
			select {
			case <-streamGate:
			case <-c.Request().Context().Done():
				return c.Request().Context().Err()
			}
		}
		if f.PartialStream && token == tokens/2 {
			return nil
		}
	}
	if f.PartialStream {
		return nil
	}
	_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	if flusher != nil {
		flusher.Flush()
	}
	return nil
}

// overstateRecordedStream applies the usage and padding knobs to a recording.
// A chunk with usage is re-encoded with the new counts, so its keys come out
// sorted; every other recorded chunk keeps its bytes.
func overstateRecordedStream(chunks []json.RawMessage, f FaultConfig) ([]json.RawMessage, error) {
	if f.UsageCompletionTokens <= 0 && f.UsagePromptTokens <= 0 && f.PadEmptyChunks <= 0 && !f.DropTokens {
		return chunks, nil
	}
	isToken, _ := recordedLayout(chunks)
	out := make([]json.RawMessage, 0, len(chunks)+max(f.PadEmptyChunks, 0))
	for i, chunk := range chunks {
		rewritten, err := overstateUsage(chunk, f.UsagePromptTokens, f.UsageCompletionTokens)
		if err == nil && f.DropTokens && isToken[i] {
			rewritten, err = withoutTokens(rewritten)
		}
		if err != nil {
			return nil, err
		}
		out = append(out, rewritten)
	}
	if f.PadEmptyChunks <= 0 || len(out) == 0 {
		return out, nil
	}
	pad, err := emptyContentChunk(out[0])
	if err != nil {
		return nil, err
	}
	_, final := recordedLayout(out)
	padded := make([]json.RawMessage, 0, len(out)+f.PadEmptyChunks)
	padded = append(padded, out[:final]...)
	for i := 0; i < f.PadEmptyChunks; i++ {
		padded = append(padded, pad)
	}
	return append(padded, out[final:]...), nil
}

// overstateUsage replaces the positive counts in chunk's usage and keeps
// total_tokens their sum. A chunk without usage is returned unchanged.
func overstateUsage(chunk json.RawMessage, promptTokens, completionTokens int) (json.RawMessage, error) {
	if promptTokens <= 0 && completionTokens <= 0 {
		return chunk, nil
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(chunk, &fields); err != nil {
		return nil, fmt.Errorf("decode recorded chunk: %w", err)
	}
	raw, ok := fields["usage"]
	if !ok || string(raw) == "null" {
		return chunk, nil
	}
	var usage map[string]json.RawMessage
	if err := json.Unmarshal(raw, &usage); err != nil {
		return nil, fmt.Errorf("decode recorded usage: %w", err)
	}
	var counts struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	}
	if err := json.Unmarshal(raw, &counts); err != nil {
		return nil, fmt.Errorf("decode recorded usage: %w", err)
	}
	if promptTokens > 0 {
		counts.PromptTokens = promptTokens
	}
	if completionTokens > 0 {
		counts.CompletionTokens = completionTokens
	}
	usage["prompt_tokens"] = json.RawMessage(strconv.Itoa(counts.PromptTokens))
	usage["completion_tokens"] = json.RawMessage(strconv.Itoa(counts.CompletionTokens))
	usage["total_tokens"] = json.RawMessage(strconv.Itoa(counts.PromptTokens + counts.CompletionTokens))
	encoded, err := json.Marshal(usage)
	if err != nil {
		return nil, err
	}
	fields["usage"] = encoded
	return json.Marshal(fields)
}

// withoutTokens empties the delta of every choice in chunk and drops its
// logprobs and token_ids; finish_reason and usage stay.
func withoutTokens(chunk json.RawMessage) (json.RawMessage, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(chunk, &fields); err != nil {
		return nil, fmt.Errorf("decode recorded chunk: %w", err)
	}
	var choices []map[string]json.RawMessage
	if err := json.Unmarshal(fields["choices"], &choices); err != nil {
		return nil, fmt.Errorf("decode recorded choices: %w", err)
	}
	for _, choice := range choices {
		choice["delta"] = json.RawMessage(`{"content":""}`)
		choice["logprobs"] = json.RawMessage(`null`)
		delete(choice, "token_ids")
	}
	encoded, err := json.Marshal(choices)
	if err != nil {
		return nil, err
	}
	fields["choices"] = encoded
	return json.Marshal(fields)
}

// emptyContentChunk is a chunk of the recorded answer whose only choice
// carries an empty content delta, no logprobs and no finish_reason.
func emptyContentChunk(template json.RawMessage) (json.RawMessage, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(template, &fields); err != nil {
		return nil, fmt.Errorf("decode recorded chunk: %w", err)
	}
	delete(fields, "usage")
	// vLLM reports the prompt on the first chunk of a stream only; a pad
	// is one of the chunks after it.
	delete(fields, "prompt_text")
	delete(fields, "prompt_token_ids")
	delete(fields, "prompt_logprobs")
	fields["choices"] = json.RawMessage(`[{"index":0,"delta":{"content":""},"logprobs":null,"finish_reason":null}]`)
	return json.Marshal(fields)
}

// recordedLayout marks the chunks that carry a sampled token (token_ids or
// logprobs entries) and returns the index of the first chunk that sets a
// finish_reason or carries usage, len(chunks) when there is none.
func recordedLayout(chunks []json.RawMessage) ([]bool, int) {
	isToken := make([]bool, len(chunks))
	final := len(chunks)
	for i, chunk := range chunks {
		var head struct {
			Choices []struct {
				FinishReason *string `json:"finish_reason"`
				TokenIDs     []int64 `json:"token_ids"`
				Logprobs     *struct {
					Content []json.RawMessage `json:"content"`
				} `json:"logprobs"`
			} `json:"choices"`
			Usage json.RawMessage `json:"usage"`
		}
		if err := json.Unmarshal(chunk, &head); err != nil {
			continue
		}
		for _, choice := range head.Choices {
			if len(choice.TokenIDs) > 0 || (choice.Logprobs != nil && len(choice.Logprobs.Content) > 0) {
				isToken[i] = true
			}
			if choice.FinishReason != nil && final == len(chunks) {
				final = i
			}
		}
		if len(head.Usage) > 0 && string(head.Usage) != "null" && final == len(chunks) {
			final = i
		}
	}
	return isToken, final
}

func waitForLatency(ctx context.Context, latency time.Duration) error {
	if latency <= 0 {
		return nil
	}
	timer := time.NewTimer(latency)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

const engineCoreErrorEvent = `data: {"error":{"code":500,"message":"EngineCore encountered an issue. See stack trace (above) for the root cause.","param":null,"type":"InternalServerError"},"id":"chatcmpl-mockopenai"}`

func (s *Server) streamErrorEnvelope(c echo.Context) error {
	c.Response().Header().Set(echo.HeaderContentType, "text/event-stream")
	c.Response().Header().Set("Cache-Control", "no-cache")
	c.Response().Header().Set("Connection", "keep-alive")
	c.Response().WriteHeader(http.StatusOK)
	w := c.Response().Writer
	flusher, _ := w.(http.Flusher)
	if _, err := fmt.Fprintf(w, "%s\n\n", engineCoreErrorEvent); err != nil {
		return err
	}
	if flusher != nil {
		flusher.Flush()
	}
	if _, err := fmt.Fprint(w, "data: [DONE]\n\n"); err != nil {
		return err
	}
	if flusher != nil {
		flusher.Flush()
	}
	return nil
}

func readBody(r *http.Request) ([]byte, error) {
	if r.Body == nil {
		return []byte("{}"), nil
	}
	defer r.Body.Close()
	const max = 4 << 20
	lr := http.MaxBytesReader(nil, r.Body, max)
	body, err := io.ReadAll(lr)
	if err != nil {
		return nil, err
	}
	if len(body) == 0 {
		return []byte("{}"), nil
	}
	return body, nil
}

// Handler returns the root http.Handler (tests).
func (s *Server) Handler() http.Handler {
	if s == nil || s.echo == nil {
		return nil
	}
	return s.echo
}

// Serve listens until ctx is cancelled.
func (s *Server) Serve(ctx context.Context, addr string) error {
	if s == nil || s.echo == nil {
		return nil
	}
	s.echo.Server.BaseContext = func(net.Listener) context.Context { return ctx }
	go func() {
		<-ctx.Done()
		shCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = s.echo.Shutdown(shCtx)
	}()
	if err := s.echo.Start(addr); err != nil && err != http.ErrServerClosed {
		return err
	}
	return ctx.Err()
}
