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
	s := &Server{fault: cfg.Faults}
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
	e.POST("/api/v1/models/status", s.handleModelStatus)
	e.POST("/testenv/fault", s.handleFaultPatch)
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
	s.patchFault(p)
	return c.JSON(http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleStats(c echo.Context) error {
	return c.JSON(http.StatusOK, Stats{
		RequestsReceived:    s.stats.requestsReceived.Load(),
		SuccessfulResponses: s.stats.successfulResponses.Load(),
		FailedResponses:     s.stats.failedResponses.Load(),
		Timeouts:            s.stats.timeouts.Load(),
		ReplayHits:          s.stats.replayHits.Load(),
		ReplayMisses:        s.stats.replayMisses.Load(),
	})
}

func (s *Server) recordFailure(ctx context.Context) {
	if ctx.Err() != nil {
		s.stats.timeouts.Add(1)
		return
	}
	s.stats.failedResponses.Add(1)
}

func (s *Server) handleChatCompletions(c echo.Context) error {
	s.stats.requestsReceived.Add(1)
	if !s.acquire(c.Request().Context()) {
		s.recordFailure(c.Request().Context())
		return c.JSON(http.StatusServiceUnavailable, map[string]string{"error": "mock-openai capacity exhausted"})
	}
	defer s.release()

	f, streamGate := s.streamFaults()
	if f.Hang {
		<-c.Request().Context().Done()
		s.stats.timeouts.Add(1)
		return c.Request().Context().Err()
	}
	if f.StreamErrorEnvelope {
		s.stats.failedResponses.Add(1)
		return s.streamErrorEnvelope(c)
	}
	if f.HTTPStatus >= 400 {
		s.stats.failedResponses.Add(1)
		return c.JSON(f.HTTPStatus, OpenAIErrorBody(f.HTTPStatus, "mock-openai fault injection"))
	}

	body, err := readBody(c.Request())
	if err != nil {
		s.recordFailure(c.Request().Context())
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}

	var req ChatRequest
	if err := json.Unmarshal(body, &req); err != nil {
		s.recordFailure(c.Request().Context())
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	if req.Model == "" {
		req.Model = "test-model"
	}
	f = s.withSeedUsage(f, req.Seed)

	// Latency delays Validate (non-stream JSON or an enforced-token replay) only.
	// Stream first-token delay is StreamFirstTokenDelay inside streamCompletion;
	// StreamChunkDelay sleeps after each flushed chunk.
	validate := !req.Stream || isValidationReplay(body)
	if validate {
		f.StreamFirstTokenDelay = 0
		f.StreamChunkDelay = 0
		f.DropFirstChunk = false
		f.PartialStream = false
		f.SSEErrorMessage = ""
		f.PauseStream = false
		f.UsageCompletionTokens = 0
		f.UsagePromptTokens = 0
		f.PadEmptyChunks = 0
		f.DropTokens = false
	}
	if f.Latency > 0 && validate {
		if err := waitForLatency(c.Request().Context(), f.Latency); err != nil {
			s.recordFailure(c.Request().Context())
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
			return c.JSON(status, map[string]string{"error": "mock-openai fault injection"})
		}
	}

	text := completionText(body)
	var recorded *replay.Recorded
	if s.replay != nil {
		sample, ok := s.lookupReplay(req, body)
		if !ok {
			s.stats.replayMisses.Add(1)
			return c.JSON(http.StatusNotFound, map[string]string{"error": "mock-openai replay sample not found"})
		}
		s.stats.replayHits.Add(1)
		text = sample.Response.Content
		recorded = sample.Recorded
	}
	if req.Stream {
		var err error
		if recorded != nil {
			err = s.streamRecorded(c, recorded.Stream, f, streamGate)
		} else {
			err = s.streamCompletion(c, req, text, body, f, streamGate)
		}
		if err != nil || f.PartialStream || f.DropFirstChunk {
			s.recordFailure(c.Request().Context())
			return err
		}
		s.stats.successfulResponses.Add(1)
		return nil
	}
	if recorded != nil {
		err = c.Blob(http.StatusOK, echo.MIMEApplicationJSON, recorded.NonStream)
	} else {
		err = s.jsonCompletion(c, req, text, body)
	}
	if err != nil {
		s.recordFailure(c.Request().Context())
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

func (s *Server) jsonCompletion(c echo.Context, req ChatRequest, text string, body []byte) error {
	promptTok := promptTokenEstimate(body)
	completionTok := len(text) / 4
	if completionTok < 1 {
		completionTok = 1
	}
	choice := map[string]any{
		"index": 0,
		"message": map[string]any{
			"role":    "assistant",
			"content": text,
		},
		"finish_reason": "stop",
	}
	if req.Logprobs {
		choice["logprobs"] = map[string]any{
			"content": buildLogprobContent(text, req.TopLogprobs),
		}
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

func (s *Server) streamCompletion(c echo.Context, req ChatRequest, text string, body []byte, f FaultConfig, streamGate <-chan struct{}) error {
	if err := waitForLatency(c.Request().Context(), f.StreamFirstTokenDelay); err != nil {
		return err
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
		if f.StreamChunkDelay > 0 {
			time.Sleep(f.StreamChunkDelay)
		}
		return nil
	}

	tokens := []rune(text)
	if len(tokens) == 0 {
		tokens = []rune(" ")
	}
	// vLLM-style streaming error: optional content prefix, then error event + [DONE].
	if msg := strings.TrimSpace(f.SSEErrorMessage); msg != "" {
		// Emit a short content prefix so partial body capture has bytes before the error.
		prefix := tokens
		if len(prefix) > 4 {
			prefix = prefix[:4]
		}
		for _, r := range prefix {
			if err := writeChunk(map[string]any{"content": string(r)}, nil, nil); err != nil {
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
	first := true
	paused := false
	for i, r := range tokens {
		if first && f.DropFirstChunk {
			first = false
			continue
		}
		first = false
		tok := string(r)
		var lp any
		if req.Logprobs {
			lp = map[string]any{
				"content": buildLogprobContent(tok, req.TopLogprobs),
			}
		}
		if err := writeChunk(map[string]any{"content": tok}, nil, lp); err != nil {
			return err
		}
		if !paused && f.PauseStream && streamGate != nil {
			paused = true
			select {
			case <-streamGate:
			case <-c.Request().Context().Done():
				return c.Request().Context().Err()
			}
		}
		if f.PartialStream && i == len(tokens)/2 {
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
	if completionTok < 1 {
		completionTok = 1
	}
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
func (s *Server) streamRecorded(c echo.Context, chunks []json.RawMessage, f FaultConfig, streamGate <-chan struct{}) error {
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
