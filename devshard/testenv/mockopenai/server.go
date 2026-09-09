package mockopenai

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/labstack/echo/v4"
)

// Server serves OpenAI-compatible /v1/chat/completions.
type Server struct {
	echo  *echo.Echo
	mu    sync.RWMutex
	fault FaultConfig
}

// NewServer builds the HTTP server.
func NewServer(cfg Config) *Server {
	s := &Server{fault: cfg.Faults}
	if s.fault.StreamChunkDelay <= 0 {
		s.fault.StreamChunkDelay = 5 * time.Millisecond
	}
	e := echo.New()
	e.HideBanner = true
	e.POST("/v1/chat/completions", s.handleChatCompletions)
	e.GET("/healthz", func(c echo.Context) error { return c.String(http.StatusOK, "ok") })
	e.POST("/testenv/fault", s.handleFaultPatch)
	s.echo = e
	return s
}

func (s *Server) faults() FaultConfig {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.fault
}

func (s *Server) patchFault(p FaultPatch) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p.apply(&s.fault)
}

func (s *Server) handleFaultPatch(c echo.Context) error {
	var p FaultPatch
	if err := c.Bind(&p); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	s.patchFault(p)
	return c.JSON(http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleChatCompletions(c echo.Context) error {
	f := s.faults()
	if f.Latency > 0 {
		time.Sleep(f.Latency)
	}
	if f.HTTPStatus >= 400 {
		return c.JSON(f.HTTPStatus, map[string]string{"error": "mock-openai fault injection"})
	}

	body, err := readBody(c.Request())
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}

	var req ChatRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	if req.Model == "" {
		req.Model = "test-model"
	}

	text := completionText(body)
	if req.Stream {
		return s.streamCompletion(c, req.Model, text, body, f)
	}
	return s.jsonCompletion(c, req.Model, text, body)
}

func (s *Server) jsonCompletion(c echo.Context, model, text string, body []byte) error {
	promptTok := promptTokenEstimate(body)
	completionTok := len(text) / 4
	if completionTok < 1 {
		completionTok = 1
	}
	resp := map[string]any{
		"id":      "chatcmpl-mockopenai",
		"object":  "chat.completion",
		"created": time.Now().Unix(),
		"model":   model,
		"choices": []map[string]any{{
			"index": 0,
			"message": map[string]any{
				"role":    "assistant",
				"content": text,
			},
			"logprobs":       nil,
			"finish_reason":  "stop",
		}},
		"usage": map[string]any{
			"prompt_tokens":     promptTok,
			"completion_tokens": completionTok,
			"total_tokens":      promptTok + completionTok,
		},
	}
	return c.JSON(http.StatusOK, resp)
}

func (s *Server) streamCompletion(c echo.Context, model, text string, body []byte, f FaultConfig) error {
	c.Response().Header().Set(echo.HeaderContentType, "text/event-stream")
	c.Response().Header().Set("Cache-Control", "no-cache")
	c.Response().Header().Set("Connection", "keep-alive")
	c.Response().WriteHeader(http.StatusOK)

	w := c.Response().Writer
	flusher, _ := w.(http.Flusher)
	created := time.Now().Unix()
	id := "chatcmpl-mockopenai"

	writeChunk := func(delta map[string]any, finish *string) error {
		choice := map[string]any{"index": 0, "delta": delta, "logprobs": nil}
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
	first := true
	for i, r := range tokens {
		if first && f.DropFirstChunk {
			first = false
			continue
		}
		first = false
		if err := writeChunk(map[string]any{"content": string(r)}, nil); err != nil {
			return err
		}
		if f.PartialStream && i == len(tokens)/2 {
			return nil
		}
	}
	if f.PartialStream {
		return nil
	}
	stop := "stop"
	if err := writeChunk(map[string]any{"content": ""}, &stop); err != nil {
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
