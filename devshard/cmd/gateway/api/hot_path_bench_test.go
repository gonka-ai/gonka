package api

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"devshard/cmd/gateway/chain"
	"devshard/cmd/gateway/config"
	"devshard/cmd/gateway/engine"
	"devshard/cmd/gateway/filters"
	"devshard/cmd/gateway/journal"
	"devshard/cmd/gateway/limits"
	"devshard/cmd/gateway/registry"
	"devshard/cmd/gateway/scheduler"
	"devshard/cmd/gateway/store"
	"devshard/logging"
)

// sinkWriter stands in for the server's own writer, discarding what it is given.
type sinkWriter struct {
	header http.Header
	status int
}

func newSinkWriter() *sinkWriter { return &sinkWriter{header: http.Header{}} }

func (w *sinkWriter) Header() http.Header    { return w.header }
func (w *sinkWriter) WriteHeader(status int) { w.status = status }
func (w *sinkWriter) Flush()                 {}

func (w *sinkWriter) Write(chunk []byte) (int, error) { return len(chunk), nil }

func (w *sinkWriter) reset() {
	clear(w.header)
	w.status = 0
}

type quietLogger struct{}

func (quietLogger) Info(string, ...any)  {}
func (quietLogger) Error(string, ...any) {}
func (quietLogger) Warn(string, ...any)  {}
func (quietLogger) Debug(string, ...any) {}

func quietLogging(b *testing.B) {
	b.Helper()
	logging.SetLogger(quietLogger{})
	b.Cleanup(func() { logging.SetLogger(logging.NewSlogAdapter()) })
}

type benchSnapshots struct{}

func (benchSnapshots) Snapshot() chain.PhaseSnapshot {
	return chain.PhaseSnapshot{LastUpdatedAt: harnessClock, LastHealthyAt: harnessClock}
}

// benchServer builds a Server wired with fakes, so every iteration measures the same cache miss.
func benchServer(b *testing.B, chunks []string) *Server {
	b.Helper()
	quietLogging(b)
	configuration := config.Defaults()
	configuration.Server.StorageDir = b.TempDir()
	configuration.Server.AdminAPIKey = adminKey
	configuration.Server.APIKeys = []string{clientKey, "second-key", "third-key"}
	configuration.Limits.ModelAccess = map[string]string{"qwen": config.ModelAccessOpen}
	escrows := &fakeRegistry{
		models:   []string{"qwen"},
		sessions: map[string]registry.EscrowSession{},
		busy:     map[string]bool{},
		escrows:  []scheduler.Escrow{{ID: "7", Model: "qwen", ActiveUsers: 1}},
	}
	events := journal.New(journal.Settings{Lines: quietLogger{}})
	b.Cleanup(func() { _ = events.Close() })
	server, err := New(Deps{
		Config:       config.NewHolder(&configuration),
		Escrows:      escrows,
		Inference:    &fakeEngine{chunks: chunks, outcome: benchOutcome()},
		Limiter:      &countingLimiter{},
		Capacity:     &fakeCapacity{capacity: limits.ModelCapacity{ScaleFactor: 1}},
		Snapshots:    benchSnapshots{},
		Control:      &fakeControl{},
		Accounting:   &fakeAccounting{rows: map[string]store.RequestRecord{}},
		Operations:   &fakeOperations{},
		Suspicious:   &fakeSuspicious{},
		ModelFilters: newFakeModelFilters(),
		Telemetry:    &fakeTelemetry{},
		Rejections:   &spyRejections{},
		Journal:      events,
		Buffers:      NewBufferBudget(1 << 30),
		StorageDir:   configuration.Server.StorageDir,
		Version:      "bench",
		Now:          func() time.Time { return harnessClock },
		RequestIDs:   func() string { return "request-1" },
	})
	if err != nil {
		b.Fatalf("New: %v", err)
	}
	return server
}

// benchOutcome is a race one host won, with the timings and the stamp a finished request is logged from.
func benchOutcome() engine.RaceOutcome {
	sent := harnessClock
	return engine.RaceOutcome{
		Model: "qwen", InputTokens: 1024, ClientStream: true, WinnerNonce: 7, Succeeded: true,
		Attempts: []engine.AttemptOutcome{{
			Participant:           "gonka1qqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqq",
			HostIdx:               1,
			HostLabel:             "host-1",
			Nonce:                 7,
			SendTime:              sent,
			ReceiptTime:           sent.Add(200 * time.Millisecond),
			FirstToken:            sent.Add(400 * time.Millisecond),
			Completed:             sent.Add(3 * time.Second),
			UsageCompletionTokens: 384,
			Confirmed:             true,
			ConfirmedAt:           sent.Add(100 * time.Millisecond).Unix(),
		}},
	}
}

// benchChunks is a host's SSE as it arrives: one framed event per token.
func benchChunks(count, contentBytes int) []string {
	content := strings.Repeat("token ", (contentBytes/6)+1)[:contentBytes]
	chunks := make([]string, 0, count)
	for index := range count {
		chunks = append(chunks, fmt.Sprintf(
			`data: {"id":"chatcmpl-%d","object":"chat.completion.chunk","created":1700000000,"model":"qwen","choices":[{"index":0,"delta":{"content":%q},"finish_reason":null}]}`+"\n\n",
			index, content))
	}
	return append(chunks, "data: [DONE]\n\n")
}

func benchRequest(target, body string) *http.Request {
	request := httptest.NewRequest(http.MethodPost, target, strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+clientKey)
	request.Header.Set("Content-Type", "application/json")
	return request
}

func benchChatBody(streaming bool, promptBytes int) string {
	prompt := strings.Repeat("the quick brown fox jumps over the lazy dog ", (promptBytes/44)+1)[:promptBytes]
	return fmt.Sprintf(`{"model":"qwen","stream":%t,"messages":[{"role":"user","content":%q}]}`, streaming, prompt)
}

// serveRepeatedly drives one prepared request through the handler, restoring the body the handler consumed.
func serveRepeatedly(b *testing.B, handler http.Handler, request *http.Request, body string, wantStatus int) {
	b.Helper()
	writer := newSinkWriter()
	prepared := *request
	b.ReportAllocs()
	for b.Loop() {
		writer.reset()
		next := prepared
		next.Body = io.NopCloser(strings.NewReader(body))
		next.ContentLength = int64(len(body))
		handler.ServeHTTP(writer, &next)
		if writer.status != wantStatus {
			b.Fatalf("status = %d, want %d", writer.status, wantStatus)
		}
	}
}

// A streamed answer of several dozen chunks, which is what a client sees on the common path.
func BenchmarkChatStreamed(b *testing.B) {
	server := benchServer(b, benchChunks(48, 24))
	body := benchChatBody(true, 4<<10)
	serveRepeatedly(b, server.Handler(), benchRequest("/v1/chat/completions", body), body, http.StatusOK)
}

// A non-streaming reply of a few hundred kilobytes, folded as it arrives.
func BenchmarkChatBuffered(b *testing.B) {
	server := benchServer(b, benchChunks(1200, 256))
	body := benchChatBody(false, 4<<10)
	serveRepeatedly(b, server.Handler(), benchRequest("/v1/chat/completions", body), body, http.StatusOK)
}

// The escrow-pinned route resolves the pin before it admits the request.
func BenchmarkDevshardChatStreamed(b *testing.B) {
	server := benchServer(b, benchChunks(48, 24))
	body := benchChatBody(true, 4<<10)
	serveRepeatedly(b, server.Handler(), benchRequest("/devshard/7/v1/chat/completions", body), body, http.StatusOK)
}

func BenchmarkReadChatBody(b *testing.B) {
	body := benchChatBody(false, 64<<10)
	writer := newSinkWriter()
	request := benchRequest("/v1/chat/completions", body)

	b.ReportAllocs()
	for b.Loop() {
		request.Body = io.NopCloser(strings.NewReader(body))
		request.ContentLength = int64(len(body))
		if _, err := readBody(writer, request, chatIngestLimit); err != nil {
			b.Fatalf("readBody: %v", err)
		}
	}
}

func BenchmarkResolveCredentials(b *testing.B) {
	server := benchServer(b, nil)
	request := benchRequest("/v1/chat/completions", "")

	b.ReportAllocs()
	for b.Loop() {
		if identity := server.resolveCredentials(request); !identity.apiKey {
			b.Fatal("the configured key must authenticate")
		}
	}
}

func BenchmarkCacheKeyFor(b *testing.B) {
	body := []byte(benchChatBody(false, 4<<10))
	request := benchRequest("/v1/chat/completions", "")

	b.ReportAllocs()
	for b.Loop() {
		cacheKeyFor(request, filters.Result{Model: "qwen", Body: body})
	}
}

func BenchmarkRoutableEscrow(b *testing.B) {
	server := benchServer(b, nil)
	registered := make([]scheduler.Escrow, 0, 12)
	for index := range 12 {
		registered = append(registered, scheduler.Escrow{ID: fmt.Sprintf("escrow-%d", index), Model: "qwen"})
	}
	server.escrows.(*fakeRegistry).escrows = registered

	b.ReportAllocs()
	for b.Loop() {
		if _, found := server.escrows.Routable("escrow-11"); !found {
			b.Fatal("the registered escrow must resolve")
		}
	}
}

// The per-chunk cost of a streaming reply: the rewriter, the done scan and the write.
func BenchmarkClientStreamWrite(b *testing.B) {
	chunks := benchChunks(48, 24)
	writer := newSinkWriter()

	b.ReportAllocs()
	for b.Loop() {
		writer.reset()
		stream := newClientStream(writer, "request-1", true, false, filters.LogprobIntent{}, nil)
		for _, chunk := range chunks {
			if _, err := stream.Write([]byte(chunk)); err != nil {
				b.Fatalf("Write: %v", err)
			}
		}
		if err := stream.Close(); err != nil {
			b.Fatalf("Close: %v", err)
		}
	}
}

// Replaying a stored stream: every chunk keeps its own boundary and its own flush.
func BenchmarkServeCached(b *testing.B) {
	var body bytes.Buffer
	chunks := benchChunks(48, 24)
	bounds := make([]int, 0, len(chunks))
	for _, chunk := range chunks {
		body.WriteString(chunk)
		bounds = append(bounds, body.Len())
	}
	entry := cachedResponse{
		escrowID: "7", stream: true, status: http.StatusOK,
		contentType: "text/event-stream", body: body.Bytes(), bounds: bounds,
	}
	writer := newSinkWriter()

	b.ReportAllocs()
	for b.Loop() {
		writer.reset()
		serveCached(writer, "request-1", entry)
	}
}

// The recorder mirrors every byte the client is sent, so the cache can replay them.
func BenchmarkCacheRecorderStream(b *testing.B) {
	chunks := make([][]byte, 0, 49)
	for _, chunk := range benchChunks(48, 24) {
		chunks = append(chunks, []byte(chunk))
	}
	sink := newSinkWriter()

	b.ReportAllocs()
	for b.Loop() {
		recorder := newCacheRecorder(sink, maxBufferedResponseBytes, true, nil)
		for _, chunk := range chunks {
			if _, err := recorder.Write(chunk); err != nil {
				b.Fatalf("Write: %v", err)
			}
		}
		if _, storable := recorder.entry("7", true, nil); !storable {
			b.Fatal("the recorded reply must be storable")
		}
	}
}

// Every refusal a client sees is rendered here, and a busy shard renders a great many.
func BenchmarkWriteError(b *testing.B) {
	writer := newSinkWriter()

	b.ReportAllocs()
	for b.Loop() {
		writer.reset()
		writeError(writer, http.StatusServiceUnavailable, "no devshard has room for this request right now")
	}
}
