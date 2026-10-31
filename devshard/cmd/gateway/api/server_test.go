package api

import (
	"context"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go.opentelemetry.io/otel/trace"

	"devshard/cmd/gateway/chain"
	"devshard/cmd/gateway/config"
	"devshard/cmd/gateway/engine"
	"devshard/cmd/gateway/escrow"
	"devshard/cmd/gateway/filters"
	"devshard/cmd/gateway/journal"
	"devshard/cmd/gateway/limits"
	"devshard/cmd/gateway/perf"
	"devshard/cmd/gateway/registry"
	"devshard/cmd/gateway/scheduler"
	"devshard/cmd/gateway/store"
)

const (
	adminKey  = "admin-secret-key"
	clientKey = "client-api-key"
	chatBody  = `{"model":"qwen","messages":[{"role":"user","content":"hi"}]}`
)

type fakeRegistry struct {
	models   []string
	escrows  []scheduler.Escrow
	sessions map[string]registry.EscrowSession
	busy     map[string]bool
}

func (f *fakeRegistry) Serves(model string) bool { return slices.Contains(f.models, model) }
func (f *fakeRegistry) Models() []string         { return f.models }

func (f *fakeRegistry) Candidates(model string) []scheduler.Escrow {
	matched := make([]scheduler.Escrow, 0, len(f.escrows))
	for _, candidate := range f.escrows {
		if candidate.Model == model {
			matched = append(matched, candidate)
		}
	}
	return matched
}

func (f *fakeRegistry) Routable(escrowID string) (scheduler.Escrow, bool) {
	for _, candidate := range f.escrows {
		if candidate.ID == escrowID {
			return candidate, true
		}
	}
	return scheduler.Escrow{}, false
}

func (f *fakeRegistry) Snapshot() []registry.EscrowState {
	held := make([]registry.EscrowState, 0, len(f.escrows))
	for _, candidate := range f.escrows {
		held = append(held, registry.EscrowState{ID: candidate.ID, Model: candidate.Model, Accepting: true})
	}
	return held
}

func (f *fakeRegistry) RoutableSession(escrowID string) (registry.EscrowSession, bool) {
	session, held := f.sessions[escrowID]
	return session, held
}

func (f *fakeRegistry) SettlementSession(escrowID string) (registry.EscrowSession, bool) {
	return f.RoutableSession(escrowID)
}

func (f *fakeRegistry) HoldSettlement(escrowID string) (registry.EscrowSession, func(), bool) {
	session, held := f.SettlementSession(escrowID)
	return session, func() {}, held
}

// Inspect mirrors the production contract: a resident escrow answers, an absent one reports the sentinel.
func (f *fakeRegistry) Inspect(_ context.Context, escrowID string) (registry.EscrowSession, func(), error) {
	session, held := f.SettlementSession(escrowID)
	if !held {
		return nil, nil, fmt.Errorf("escrow %s: %w", escrowID, escrow.ErrUnknownEscrow)
	}
	return session, func() {}, nil
}

func (f *fakeRegistry) IsBusy(escrowID string) bool { return f.busy[escrowID] }

// fakeEngine's ledger stands in for the real engine's recording point, which runs before Run returns.
type fakeEngine struct {
	runs    atomic.Int64
	raced   atomic.Pointer[engine.Request]
	racedIn atomic.Pointer[trace.SpanContext]
	reply   string
	chunks  []string
	outcome engine.RaceOutcome
	err     error
	ledger  *RaceLedger
	panics  bool

	finishes   chan func()
	late       []byte
	lateWrites chan func()
}

func (e *fakeEngine) Run(ctx context.Context, request engine.Request, client io.Writer) (engine.RaceOutcome, error) {
	e.runs.Add(1)
	spanContext := trace.SpanContextFromContext(ctx)
	e.racedIn.Store(&spanContext)
	e.raced.Store(&request)
	if e.finishes != nil {
		e.finishes <- request.OnFinished
	} else if request.OnFinished != nil {
		defer request.OnFinished()
	}
	if e.panics {
		panic("race panicked")
	}
	if e.late != nil {
		e.lateWrites <- func() { _, _ = client.Write(e.late) }
	}
	if request.OnEscrow != nil && e.outcome.EscrowID != "" {
		request.OnEscrow(e.outcome.EscrowID)
	}
	for _, chunk := range e.chunks {
		if _, err := client.Write([]byte(chunk)); err != nil {
			return engine.RaceOutcome{}, err
		}
	}
	if e.reply != "" && len(e.chunks) == 0 {
		if _, err := client.Write([]byte(e.reply)); err != nil {
			return engine.RaceOutcome{}, err
		}
	}
	if e.ledger != nil {
		e.ledger.RecordRequest(e.outcome)
	}
	return e.outcome, e.err
}

type countingLimiter struct {
	acquires  atomic.Int64
	releases  atomic.Int64
	tokens    atomic.Int64
	acquired  atomic.Int64
	err       error
	onAcquire func()
}

func (l *countingLimiter) AcquireForModel(_ context.Context, _ string, inputTokens int64, _ limits.ModelCapacity) error {
	if l.err != nil {
		return l.err
	}
	if l.onAcquire != nil {
		l.onAcquire()
	}
	l.acquires.Add(1)
	l.tokens.Add(inputTokens)
	l.acquired.Add(inputTokens)
	return nil
}

func (l *countingLimiter) ReleaseForModel(_ string, inputTokens int64) {
	l.releases.Add(1)
	l.tokens.Add(-inputTokens)
}

type fakeCapacity struct{ capacity limits.ModelCapacity }

func (f *fakeCapacity) ForModel(string) limits.ModelCapacity { return f.capacity }

type fakeControl struct {
	devshards []store.DevshardRecord
	overrides config.Overrides
	rotation  []store.RotationStatus
	listErr   error
}

func (f *fakeControl) ListDevshards(context.Context) ([]store.DevshardRecord, error) {
	return f.devshards, f.listErr
}

func (f *fakeControl) LoadOverrides(context.Context) (config.Overrides, error) {
	return f.overrides, nil
}

func (f *fakeControl) LoadRotationStatuses(context.Context) ([]store.RotationStatus, error) {
	return f.rotation, nil
}

type fakeOperations struct {
	mu          sync.Mutex
	calls       []string
	err         error
	create      chain.CreateEscrowResult
	settle      chain.SettleEscrowResult
	onSettle    func(escrowID string)
	settleCtx   context.Context
	created     []CreateEscrowRequest
	added       []AddDevshardRequest
	imported    []ImportDevshardRequest
	deleted     []string
	deactivated []string
	applied     config.Overrides
}

func (f *fakeOperations) record(name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, name)
	return f.err
}

// recordedCalls exists because a batch route calls these from several goroutines at once.
func (f *fakeOperations) recordedCalls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.calls)
}

// deactivatedEscrows is sorted because a batch route deactivates from several goroutines in no fixed order.
func (f *fakeOperations) deactivatedEscrows() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Sorted(slices.Values(f.deactivated))
}

func (f *fakeOperations) CreateEscrow(_ context.Context, request CreateEscrowRequest) (chain.CreateEscrowResult, error) {
	f.mu.Lock()
	f.created = append(f.created, request)
	f.mu.Unlock()
	return f.create, f.record("create")
}

func (f *fakeOperations) AddDevshard(_ context.Context, request AddDevshardRequest) error {
	f.mu.Lock()
	f.added = append(f.added, request)
	f.mu.Unlock()
	return f.record("add")
}

func (f *fakeOperations) ImportDevshard(_ context.Context, request ImportDevshardRequest) error {
	f.mu.Lock()
	f.imported = append(f.imported, request)
	f.mu.Unlock()
	return f.record("import")
}

func (f *fakeOperations) DeleteDevshard(_ context.Context, escrowID string) error {
	f.mu.Lock()
	f.deleted = append(f.deleted, escrowID)
	f.mu.Unlock()
	return f.record("delete")
}

func (f *fakeOperations) Activate(context.Context, string) error { return f.record("activate") }
func (f *fakeOperations) Deactivate(_ context.Context, escrowID string) error {
	f.mu.Lock()
	f.deactivated = append(f.deactivated, escrowID)
	f.mu.Unlock()
	return f.record("deactivate")
}

func (f *fakeOperations) Settle(ctx context.Context, escrowID string, _ bool) (chain.SettleEscrowResult, error) {
	f.mu.Lock()
	f.settleCtx = ctx
	hold := f.onSettle
	f.mu.Unlock()
	if hold != nil {
		hold(escrowID)
	}
	return f.settle, f.record("settle")
}

func (f *fakeOperations) ResetAccountingEpoch(context.Context, uint64) (int, error) {
	return 0, f.record("reset_accounting_epoch")
}

func (f *fakeOperations) Unquarantine(context.Context, string) error {
	return f.record("unquarantine")
}

func (f *fakeOperations) Reconfigure(_ context.Context, overrides config.Overrides) error {
	f.mu.Lock()
	f.applied = overrides
	f.mu.Unlock()
	return f.record("reconfigure")
}

func (f *fakeOperations) appliedOverrides() config.Overrides {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.applied
}

type fakeSuspicious struct{ hosts []string }

func (f *fakeSuspicious) List() []string { return f.hosts }

func (f *fakeSuspicious) Add(_ context.Context, participantKey string) error {
	f.hosts = append(f.hosts, participantKey)
	return nil
}

func (f *fakeSuspicious) Remove(_ context.Context, participantKey string) error {
	f.hosts = slices.DeleteFunc(f.hosts, func(host string) bool { return host == participantKey })
	return nil
}

type fakeAccounting struct {
	mu       sync.Mutex
	rows     map[string]store.RequestRecord
	recorded []store.RequestRecord
	findErr  error
}

func (a *fakeAccounting) Record(record store.RequestRecord) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.recorded = append(a.recorded, record)
	a.rows[record.RequestID] = record
}

func (a *fakeAccounting) Find(_ context.Context, requestID string) (store.RequestRecord, bool, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.findErr != nil {
		return store.RequestRecord{}, false, a.findErr
	}
	record, found := a.rows[requestID]
	return record, found, nil
}

func (a *fakeAccounting) rowsRecorded() []store.RequestRecord {
	a.mu.Lock()
	defer a.mu.Unlock()
	return slices.Clone(a.recorded)
}

// fakeTelemetry records the labels registered at build time and the label the last served request was counted under.
type fakeTelemetry struct {
	labels []string
	served string
}

func (t *fakeTelemetry) InstrumentRoute(routeLabel string, next http.Handler) http.Handler {
	t.labels = append(t.labels, routeLabel)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.served = routeLabel
		next.ServeHTTP(&instrumentedWriter{ResponseWriter: w}, r)
	})
}

// instrumentedWriter mirrors the wrapper metrics.InstrumentRoute puts in front of every route.
type instrumentedWriter struct{ http.ResponseWriter }

func (w *instrumentedWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *instrumentedWriter) Flush() {
	if flusher, ok := w.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

func (t *fakeTelemetry) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("# exposition\n"))
	})
}

var harnessClock = time.Unix(1700000000, 0)

type fixedSnapshots struct{ snapshot chain.PhaseSnapshot }

func (f fixedSnapshots) Snapshot() chain.PhaseSnapshot {
	snapshot := f.snapshot
	if snapshot.LastHealthyAt.IsZero() {
		snapshot.LastUpdatedAt = harnessClock
		snapshot.LastHealthyAt = harnessClock
	}
	return snapshot
}

type harness struct {
	cached      *spyCacheResults
	server      *Server
	config      *config.Holder
	escrows     *fakeRegistry
	inference   *fakeEngine
	limiter     *countingLimiter
	capacity    *fakeCapacity
	snapshots   *fixedSnapshots
	control     *fakeControl
	accounting  *fakeAccounting
	operations  *fakeOperations
	suspicious  *fakeSuspicious
	filters     *fakeModelFilters
	telemetry   *fakeTelemetry
	rejections  *spyRejections
	hosts       *fakeHosts
	events      *journal.Journal
	comparisons atomic.Int64
	storageDir  string
}

func newHarness(t *testing.T, tune ...func(*config.Config)) *harness {
	t.Helper()
	configuration := config.Defaults()
	storageDir := t.TempDir()
	configuration.Server.StorageDir = storageDir
	configuration.Server.AdminAPIKey = adminKey
	configuration.Server.APIKeys = []string{clientKey}
	configuration.Limits.ModelAccess = map[string]string{"qwen": config.ModelAccessOpen}
	for _, adjust := range tune {
		adjust(&configuration)
	}

	live := &harness{
		config:     config.NewHolder(&configuration),
		escrows:    &fakeRegistry{models: []string{"qwen"}, sessions: map[string]registry.EscrowSession{}, busy: map[string]bool{}},
		inference:  &fakeEngine{reply: `{"id":"resp","choices":[]}`, outcome: engine.RaceOutcome{EscrowID: "7", Succeeded: true}},
		limiter:    &countingLimiter{},
		rejections: &spyRejections{},
		cached:     &spyCacheResults{},
		capacity:   &fakeCapacity{capacity: limits.ModelCapacity{ScaleFactor: 1}},
		snapshots:  &fixedSnapshots{},
		control: &fakeControl{devshards: []store.DevshardRecord{
			{EscrowID: "7", Model: "qwen", Active: false},
			{EscrowID: "9", Model: "qwen", Active: true},
		}},
		accounting: &fakeAccounting{rows: map[string]store.RequestRecord{}},
		operations: &fakeOperations{},
		suspicious: &fakeSuspicious{},
		filters:    newFakeModelFilters(),
		hosts:      &fakeHosts{},
		telemetry:  &fakeTelemetry{},
		events:     journal.New(journal.Settings{}),
		storageDir: storageDir,
	}
	t.Cleanup(func() { _ = live.events.Close() })
	live.escrows.escrows = []scheduler.Escrow{{ID: "7", Model: "qwen", ActiveUsers: 1}}

	server, err := New(Deps{
		Config:       live.config,
		Escrows:      live.escrows,
		Inference:    live.inference,
		Limiter:      live.limiter,
		Capacity:     live.capacity,
		Snapshots:    live.snapshots,
		Control:      live.control,
		Accounting:   live.accounting,
		Operations:   live.operations,
		Suspicious:   live.suspicious,
		ModelFilters: live.filters,
		HostStates:   live.hosts,
		HostWindows:  fakeHostWindows{hosts: live.hosts},
		Telemetry:    live.telemetry,
		Rejections:   live.rejections,
		CacheResults: live.cached,
		Journal:      live.events,
		StorageDir:   storageDir,
		Version:      "test",
		Now:          func() time.Time { return harnessClock },
		RequestIDs:   func() string { return "request-1" },
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	compare := server.verify
	server.verify = func(authorization string) credentials {
		live.comparisons.Add(1)
		return compare(authorization)
	}
	live.server = server
	return live
}

func (h *harness) request(t *testing.T, method, target, body string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	recorder := httptest.NewRecorder()
	h.requestInto(t, recorder, method, target, body, headers)
	return recorder
}

func (h *harness) requestInto(t *testing.T, w http.ResponseWriter, method, target, body string, headers map[string]string) {
	t.Helper()
	h.requestIntoWithContext(t, context.Background(), w, method, target, body, headers)
}

func (h *harness) requestWithContext(t *testing.T, ctx context.Context, method, target, body string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	recorder := httptest.NewRecorder()
	h.requestIntoWithContext(t, ctx, recorder, method, target, body, headers)
	return recorder
}

func (h *harness) requestIntoWithContext(t *testing.T, ctx context.Context, w http.ResponseWriter, method, target, body string, headers map[string]string) {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	request := httptest.NewRequestWithContext(ctx, method, target, reader)
	for name, value := range headers {
		request.Header.Set(name, value)
	}
	h.server.Handler().ServeHTTP(w, request)
}

func adminHeaders() map[string]string {
	return map[string]string{"Authorization": "Bearer " + adminKey}
}

func (h *harness) swapConfig(mutate func(*config.Config)) {
	next := *h.config.Load()
	mutate(&next)
	h.config.Swap(&next)
}

// Test flow:
//  1. Define a table of every route the gateway serves, varying method, target, body and headers across success, wrong-method, unauthenticated, malformed-body, and not-found cases for public, devshard-pinned, and admin routes.
//  2. For each case, send the request through a fresh harness.
//  3. Assert the response status matches the case's documented expectation.
func TestEveryRouteAnswersItsDocumentedStatus(t *testing.T) {
	testCases := []struct {
		name    string
		method  string
		target  string
		body    string
		headers map[string]string
		want    int
	}{
		{name: "models", method: http.MethodGet, target: "/v1/models", want: http.StatusOK},
		{name: "models head", method: http.MethodHead, target: "/v1/models", want: http.StatusOK},
		{name: "models wrong method", method: http.MethodPost, target: "/v1/models", want: http.StatusMethodNotAllowed},
		{name: "chat", method: http.MethodPost, target: "/v1/chat/completions", body: chatBody, want: http.StatusOK},
		{name: "chat wrong method", method: http.MethodGet, target: "/v1/chat/completions", want: http.StatusMethodNotAllowed},
		{name: "chat unparsable body", method: http.MethodPost, target: "/v1/chat/completions", body: "{", want: http.StatusBadRequest},
		{name: "status", method: http.MethodGet, target: "/v1/status", want: http.StatusOK},
		{name: "status wrong method", method: http.MethodDelete, target: "/v1/status", want: http.StatusMethodNotAllowed},
		{name: "metrics", method: http.MethodGet, target: "/metrics", want: http.StatusOK},
		{name: "metrics wrong method", method: http.MethodPost, target: "/metrics", want: http.StatusMethodNotAllowed},
		{name: "healthz", method: http.MethodGet, target: "/healthz", want: http.StatusOK},
		{name: "healthz head", method: http.MethodHead, target: "/healthz", want: http.StatusOK},
		{name: "healthz wrong method", method: http.MethodPost, target: "/healthz", want: http.StatusMethodNotAllowed},

		{name: "devshard models", method: http.MethodGet, target: "/devshard/7/v1/models", want: http.StatusOK},
		{name: "devshard models unknown", method: http.MethodGet, target: "/devshard/404/v1/models", want: http.StatusNotFound},
		{name: "devshard models wrong method", method: http.MethodPost, target: "/devshard/7/v1/models", want: http.StatusMethodNotAllowed},
		{name: "devshard status", method: http.MethodGet, target: "/devshard/7/v1/status", want: http.StatusOK},
		{name: "devshard status unknown", method: http.MethodGet, target: "/devshard/404/v1/status", want: http.StatusNotFound},

		{name: "finalize needs admin", method: http.MethodGet, target: "/devshard/7/v1/finalize", want: http.StatusUnauthorized},
		{name: "finalize unknown", method: http.MethodGet, target: "/devshard/404/v1/finalize", headers: adminHeaders(), want: http.StatusNotFound},

		{name: "admin state", method: http.MethodGet, target: "/v1/admin/state", headers: adminHeaders(), want: http.StatusOK},
		{name: "admin state unauthenticated", method: http.MethodGet, target: "/v1/admin/state", want: http.StatusUnauthorized},
		{name: "admin state wrong method", method: http.MethodPost, target: "/v1/admin/state", headers: adminHeaders(), want: http.StatusMethodNotAllowed},
		{name: "admin settings get", method: http.MethodGet, target: "/v1/admin/settings", headers: adminHeaders(), want: http.StatusOK},
		{name: "admin settings put", method: http.MethodPut, target: "/v1/admin/settings", body: `{"max_tokens_cap":4096}`, headers: adminHeaders(), want: http.StatusOK},
		{name: "admin settings post", method: http.MethodPost, target: "/v1/admin/settings", body: `{"max_tokens_cap":4096}`, headers: adminHeaders(), want: http.StatusOK},
		{name: "admin settings unknown field", method: http.MethodPut, target: "/v1/admin/settings", body: `{"nope":1}`, headers: adminHeaders(), want: http.StatusBadRequest},
		{name: "admin settings wrong method", method: http.MethodDelete, target: "/v1/admin/settings", headers: adminHeaders(), want: http.StatusMethodNotAllowed},

		{name: "admin devshards list", method: http.MethodGet, target: "/v1/admin/devshards", headers: adminHeaders(), want: http.StatusOK},
		{name: "admin devshards add", method: http.MethodPost, target: "/v1/admin/devshards", body: `{"id":"11","model":"qwen","private_key_env":"GATEWAY_KEY_11"}`, headers: adminHeaders(), want: http.StatusOK},
		{name: "admin devshards add without model", method: http.MethodPost, target: "/v1/admin/devshards", body: `{"id":"11"}`, headers: adminHeaders(), want: http.StatusBadRequest},
		{name: "admin devshards add without a key variable", method: http.MethodPost, target: "/v1/admin/devshards", body: `{"id":"11","model":"qwen"}`, headers: adminHeaders(), want: http.StatusBadRequest},
		{name: "admin devshards wrong method", method: http.MethodDelete, target: "/v1/admin/devshards", headers: adminHeaders(), want: http.StatusMethodNotAllowed},

		{name: "admin import", method: http.MethodPost, target: "/v1/admin/devshards/import", body: `{"id":"11","source_path":"/tmp/x","private_key_env":"GATEWAY_KEY_11"}`, headers: adminHeaders(), want: http.StatusOK},
		{name: "admin import without source", method: http.MethodPost, target: "/v1/admin/devshards/import", body: `{"id":"11"}`, headers: adminHeaders(), want: http.StatusBadRequest},
		{name: "admin import without a key variable", method: http.MethodPost, target: "/v1/admin/devshards/import", body: `{"id":"11","source_path":"/tmp/x"}`, headers: adminHeaders(), want: http.StatusBadRequest},

		{name: "admin delete inactive", method: http.MethodDelete, target: "/v1/admin/devshards/7", headers: adminHeaders(), want: http.StatusOK},
		{name: "admin delete unknown", method: http.MethodDelete, target: "/v1/admin/devshards/404", headers: adminHeaders(), want: http.StatusNotFound},
		{name: "admin delete wrong method", method: http.MethodGet, target: "/v1/admin/devshards/7", headers: adminHeaders(), want: http.StatusMethodNotAllowed},

		{name: "admin activate", method: http.MethodPost, target: "/v1/admin/devshards/7/activate", headers: adminHeaders(), want: http.StatusOK},
		{name: "admin activate unknown", method: http.MethodPost, target: "/v1/admin/devshards/404/activate", headers: adminHeaders(), want: http.StatusNotFound},
		{name: "admin deactivate", method: http.MethodPost, target: "/v1/admin/devshards/9/deactivate", headers: adminHeaders(), want: http.StatusOK},
		{name: "admin settle", method: http.MethodPost, target: "/v1/admin/devshards/7/settle", headers: adminHeaders(), want: http.StatusOK},
		{name: "admin settle empty body", method: http.MethodPost, target: "/v1/admin/devshards/7/settle", body: "", headers: adminHeaders(), want: http.StatusOK},
		{name: "admin settle unknown", method: http.MethodPost, target: "/v1/admin/devshards/404/settle", headers: adminHeaders(), want: http.StatusNotFound},
		{name: "admin participants unknown", method: http.MethodGet, target: "/v1/admin/devshards/404/participants", headers: adminHeaders(), want: http.StatusNotFound},

		{name: "admin escrows", method: http.MethodPost, target: "/v1/admin/escrows", body: `{"model_id":"qwen","amount":10,"private_key_env":"KEY"}`, headers: adminHeaders(), want: http.StatusOK},
		{name: "admin escrows without key", method: http.MethodPost, target: "/v1/admin/escrows", body: `{"model_id":"qwen","amount":10}`, headers: adminHeaders(), want: http.StatusBadRequest},
		{name: "admin escrows without amount", method: http.MethodPost, target: "/v1/admin/escrows", body: `{"model_id":"qwen"}`, headers: adminHeaders(), want: http.StatusBadRequest},
		{name: "admin escrows with a raw private key are refused at the boundary, not as a server error", method: http.MethodPost, target: "/v1/admin/escrows", body: `{"model_id":"qwen","amount":10,"private_key":"deadbeef"}`, headers: adminHeaders(), want: http.StatusBadRequest},

		{name: "suspicious list", method: http.MethodGet, target: "/v1/admin/suspicious-hosts", headers: adminHeaders(), want: http.StatusOK},
		{name: "suspicious add", method: http.MethodPost, target: "/v1/admin/suspicious-hosts", body: `{"participant_key":"gonka1abc"}`, headers: adminHeaders(), want: http.StatusOK},
		{name: "suspicious delete", method: http.MethodDelete, target: "/v1/admin/suspicious-hosts", body: `{"participant_key":"gonka1abc"}`, headers: adminHeaders(), want: http.StatusOK},
		{name: "suspicious without key", method: http.MethodPost, target: "/v1/admin/suspicious-hosts", body: `{}`, headers: adminHeaders(), want: http.StatusBadRequest},

		{name: "unquarantine", method: http.MethodPost, target: "/v1/admin/participants/unquarantine", body: `{"participant_key":"gonka1abc"}`, headers: adminHeaders(), want: http.StatusOK},
		{name: "unquarantine without key", method: http.MethodPost, target: "/v1/admin/participants/unquarantine", body: `{}`, headers: adminHeaders(), want: http.StatusBadRequest},

		{name: "reset an accounting epoch", method: http.MethodPost, target: "/v1/admin/accounting/reset/370", headers: adminHeaders(), want: http.StatusOK},
		{name: "reset an accounting epoch that is not a number", method: http.MethodPost, target: "/v1/admin/accounting/reset/latest", headers: adminHeaders(), want: http.StatusBadRequest},
		{name: "reset accounting without naming an epoch", method: http.MethodPost, target: "/v1/admin/accounting/reset", headers: adminHeaders(), want: http.StatusNotFound},
		{name: "reset an accounting epoch wrong method", method: http.MethodGet, target: "/v1/admin/accounting/reset/370", headers: adminHeaders(), want: http.StatusMethodNotAllowed},
		{name: "reset an accounting epoch without the admin key", method: http.MethodPost, target: "/v1/admin/accounting/reset/370", want: http.StatusUnauthorized},

		{name: "debug rotation", method: http.MethodGet, target: "/v1/debug/rotation", headers: adminHeaders(), want: http.StatusOK},
		{name: "debug rotation wrong method", method: http.MethodPost, target: "/v1/debug/rotation", headers: adminHeaders(), want: http.StatusMethodNotAllowed},
		{name: "debug memstats", method: http.MethodGet, target: "/v1/debug/memstats", headers: adminHeaders(), want: http.StatusOK},
		{name: "debug memstats wrong method", method: http.MethodDelete, target: "/v1/debug/memstats", headers: adminHeaders(), want: http.StatusMethodNotAllowed},
		{name: "debug heightsync", method: http.MethodGet, target: "/v1/debug/heightsync", headers: adminHeaders(), want: http.StatusOK},
		{name: "debug heightsync wrong method", method: http.MethodPost, target: "/v1/debug/heightsync", headers: adminHeaders(), want: http.StatusMethodNotAllowed},
		{name: "debug heightsync without the admin key", method: http.MethodGet, target: "/v1/debug/heightsync", want: http.StatusUnauthorized},

		{name: "unmatched path", method: http.MethodGet, target: "/favicon.ico", want: http.StatusNotFound},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			live := newHarness(t)
			recorder := live.request(t, testCase.method, testCase.target, testCase.body, testCase.headers)
			if recorder.Code != testCase.want {
				t.Fatalf("%s %s: got %d, want %d (body %s)", testCase.method, testCase.target, recorder.Code, testCase.want, recorder.Body.String())
			}
		})
	}
}

// Test flow:
//  1. POST to the models route, which only accepts GET and HEAD.
//  2. Assert the Allow header lists "GET, HEAD".
//  3. Assert the Content-Type is application/json.
func TestMethodNotAllowedNamesTheMethodsItAccepts(t *testing.T) {
	live := newHarness(t)
	recorder := live.request(t, http.MethodPost, "/v1/models", "", nil)
	if got := recorder.Header().Get("Allow"); got != "GET, HEAD" {
		t.Fatalf("Allow header: got %q, want %q", got, "GET, HEAD")
	}
	if got := recorder.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("Content-Type: got %q, want application/json", got)
	}
}

// Test flow:
//  1. List the uninstrumented paths: /metrics, /healthz, and the pprof endpoints.
//  2. Assert none of them appear among the telemetry's registered instrumentation labels.
//  3. Assert the number of instrumented labels equals every registered route minus those uninstrumented ones.
func TestMetricsHealthzAndTheProfilerAreNotInstrumented(t *testing.T) {
	live := newHarness(t)
	uninstrumented := []string{"/metrics", "/healthz", "/debug/pprof/", "/debug/pprof/cmdline", "/debug/pprof/profile", "/debug/pprof/symbol", "/debug/pprof/trace"}
	for _, pattern := range uninstrumented {
		if slices.Contains(live.telemetry.labels, pattern) {
			t.Fatalf("%s was registered with an instrumentation label: %v", pattern, live.telemetry.labels)
		}
	}
	if want := len(live.server.routes()) - len(uninstrumented); len(live.telemetry.labels) != want {
		t.Fatalf("instrumented routes: got %d, want %d (every route but %v)", len(live.telemetry.labels), want, uninstrumented)
	}
}

// Test flow:
//  1. Assert the import route never registered its own instrumentation label.
//  2. Assert the templated devshards label is registered instead, so import shares its series rather than splitting it.
func TestImportKeepsTheTemplatedMetricLabel(t *testing.T) {
	live := newHarness(t)
	if slices.Contains(live.telemetry.labels, "/v1/admin/devshards/import") {
		t.Fatalf("import route split the devshards series: %v", live.telemetry.labels)
	}
	if count := slices.IndexFunc(live.telemetry.labels, func(label string) bool { return label == "/v1/admin/devshards/{id}" }); count < 0 {
		t.Fatalf("templated devshards label missing: %v", live.telemetry.labels)
	}
}

// Test flow:
//  1. Disable the gateway with a custom disabled message.
//  2. Assert a client route now answers 503.
//  3. Assert /metrics, /healthz, /v1/status, and an admin-keyed operator route still answer 200.
func TestKillSwitchSparesMetricsHealthzAndTheOperatorRoutes(t *testing.T) {
	live := newHarness(t)
	live.swapConfig(func(next *config.Config) {
		next.Modes.Disabled = true
		next.Modes.DisabledMessage = "moved"
	})
	if got := live.request(t, http.MethodGet, "/v1/models", "", nil).Code; got != http.StatusServiceUnavailable {
		t.Fatalf("disabled client route: got %d, want 503", got)
	}
	if got := live.request(t, http.MethodGet, "/metrics", "", nil).Code; got != http.StatusOK {
		t.Fatalf("disabled gateway must still serve /metrics: got %d", got)
	}
	if got := live.request(t, http.MethodGet, "/healthz", "", nil).Code; got != http.StatusOK {
		t.Fatalf("disabled gateway must still serve /healthz, or its container healthcheck reports a gateway disabled on purpose as unhealthy: got %d", got)
	}
	if got := live.request(t, http.MethodGet, "/v1/status", "", nil).Code; got != http.StatusOK {
		t.Fatalf("disabled gateway must still serve /v1/status, which the deployed compose healthcheck reads: got %d", got)
	}
	if got := live.request(t, http.MethodGet, "/v1/admin/state", "", adminHeaders()).Code; got != http.StatusOK {
		t.Fatalf("disabled gateway must still serve the operator routes: got %d", got)
	}
}

// Test flow:
//  1. Disable the gateway with a redirect URL configured.
//  2. Send a client route request.
//  3. Assert the response is 308 with the Location header set to the configured destination.
func TestKillSwitchRedirectsOnlyWithADestination(t *testing.T) {
	live := newHarness(t)
	live.swapConfig(func(next *config.Config) {
		next.Modes.Disabled = true
		next.Modes.DisabledRedirectURL = "https://elsewhere.example/v1"
	})
	recorder := live.request(t, http.MethodGet, "/v1/models", "", nil)
	if recorder.Code != http.StatusPermanentRedirect {
		t.Fatalf("got %d, want 308", recorder.Code)
	}
	if got := recorder.Header().Get("Location"); got != "https://elsewhere.example/v1" {
		t.Fatalf("Location: got %q", got)
	}
}

// Test flow:
//  1. Build the `http.Server` the gateway serves from.
//  2. Assert WriteTimeout is unset, so an absolute deadline never cuts an SSE stream.
//  3. Assert ReadHeaderTimeout is set, so a slowloris connection is not left open.
func TestHTTPServerLeavesWriteTimeoutUnsetSoStreamsSurvive(t *testing.T) {
	live := newHarness(t)
	server, _ := live.server.HTTPServer(":0")
	if server.WriteTimeout != 0 {
		t.Fatalf("WriteTimeout is set to %v; an absolute deadline cuts every SSE stream", server.WriteTimeout)
	}
	if server.ReadHeaderTimeout == 0 {
		t.Fatal("ReadHeaderTimeout is unset; slowloris is open")
	}
}

// Test flow:
//  1. Call `New` with an empty `Deps`.
//  2. Assert it returns an error.
func TestNewRejectsAnIncompleteWiring(t *testing.T) {
	if _, err := New(Deps{}); err == nil {
		t.Fatal("New accepted an empty Deps")
	}
}

type spyCacheResults struct {
	mu      sync.Mutex
	results []string
}

func (s *spyCacheResults) CacheResult(model, result string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.results = append(s.results, model+"/"+result)
}

type spyRejections struct {
	mu    sync.Mutex
	seen  []string
	model string
}

func (s *spyRejections) Rejected(model, reason string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.model, s.seen = model, append(s.seen, reason)
}

func (s *spyRejections) reasons() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.seen...)
}

// fakeHosts stands in for the performance tracker and the participant limiter at once.
type fakeHosts struct {
	states   []perf.HostState
	windows  []limits.HostWindow
	degraded map[string]bool
}

func (f *fakeHosts) Snapshot() []perf.HostState { return f.states }

func (f *fakeHosts) Degraded(participant, model string) bool {
	return f.degraded[participant+"|"+model]
}

func (f *fakeHosts) Capability(string, string) (uint64, uint64, uint64, uint64) { return 0, 0, 0, 0 }

type fakeHostWindows struct{ hosts *fakeHosts }

func (f fakeHostWindows) Snapshot() []limits.HostWindow { return f.hosts.windows }

type fakeModelFilters struct {
	mu       sync.Mutex
	records  map[string]ModelFilterRecord
	byModel  map[string]*filters.Profile
	profiles *filters.ProfileSet
}

func newFakeModelFilters() *fakeModelFilters {
	return &fakeModelFilters{records: map[string]ModelFilterRecord{}, byModel: map[string]*filters.Profile{}}
}

func (f *fakeModelFilters) Profiles() *filters.ProfileSet {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.profiles
}

func (f *fakeModelFilters) Snapshot() (map[string]ModelFilterRecord, *filters.ProfileSet) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return maps.Clone(f.records), f.profiles
}

func (f *fakeModelFilters) Records() map[string]ModelFilterRecord {
	records, _ := f.Snapshot()
	return records
}

func (f *fakeModelFilters) Put(_ context.Context, model string, setting filters.ModelSetting) error {
	profile, err := filters.ComposeProfile(model, setting)
	if err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.records[model] = ModelFilterRecord{Setting: setting, UpdatedAt: harnessClock.UTC().Format(time.RFC3339)}
	f.byModel[model] = profile
	f.profiles = filters.NewProfileSet(f.byModel)
	return nil
}

func (f *fakeModelFilters) Delete(_ context.Context, model string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.records, model)
	delete(f.byModel, model)
	f.profiles = filters.NewProfileSet(f.byModel)
	return nil
}
