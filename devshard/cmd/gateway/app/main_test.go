package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"go.uber.org/goleak"

	"devshard/bridge"
	"devshard/cmd/gateway/api"
	"devshard/cmd/gateway/chain"
	"devshard/cmd/gateway/config"
	"devshard/cmd/gateway/engine"
	"devshard/cmd/gateway/env"
	"devshard/cmd/gateway/escrow"
	"devshard/cmd/gateway/filters"
	"devshard/cmd/gateway/internal/logcapture"
	"devshard/cmd/gateway/journal"
	"devshard/cmd/gateway/limits"
	"devshard/cmd/gateway/metrics"
	"devshard/cmd/gateway/perf"
	"devshard/cmd/gateway/registry"
	"devshard/cmd/gateway/scheduler"
	"devshard/cmd/gateway/store"
	"devshard/heightsync"
	"devshard/transport"
	"devshard/types"
	"devshard/user"
)

const routeTestAdminKey = "route-test-admin-key"

var errNoChainDialed = errors.New("the composed test gateway dials no chain")

// TestMain ignores the goroutines that live for the process: database/sql's cleaner, the sqlite finalizer and the unclosable chain client.
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m,
		goleak.IgnoreTopFunction("database/sql.(*DB).connectionOpener"),
		goleak.IgnoreTopFunction("modernc.org/sqlite.(*conn).interruptOnDone.func1"),
		goleak.IgnoreTopFunction("github.com/desertbit/timer.timerRoutine"),
		goleak.IgnoreTopFunction("google.golang.org/grpc/internal/grpcsync.(*CallbackSerializer).run"),
		goleak.IgnoreTopFunction("google.golang.org/grpc/internal/resolver/dns.(*dnsResolver).watcher"),
		goleak.IgnoreTopFunction("google.golang.org/grpc.(*addrConn).resetTransportAndUnlock"),
		goleak.IgnoreAnyFunction("github.com/godbus/dbus.(*Conn).inWorker"),
	)
}

func freePort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserving port: %v", err)
	}
	defer listener.Close()
	return listener.Addr().(*net.TCPAddr).Port
}

// fakeChain answers the observer's polls with an empty epoch, so a composed gateway boots without reaching a real node.
func fakeChain(t *testing.T) string {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{}`)
	}))
	t.Cleanup(server.Close)
	return server.URL
}

func gatewayEnvironment(t *testing.T) {
	t.Helper()
	chainURL := fakeChain(t)
	t.Setenv("DEVSHARD_STORAGE_DIR", t.TempDir())
	t.Setenv("DEVSHARD_CHAIN_GRPC", "127.0.0.1:9090")
	t.Setenv("DEVSHARD_PUBLIC_API", chainURL)
}

// chainWithoutADial is a chain.Reader that never dials, so no test here reaches a real node.
type chainWithoutADial struct{}

func (chainWithoutADial) EscrowParams(context.Context) (chain.EscrowParams, bool, error) {
	return chain.EscrowParams{}, false, nil
}

func (chainWithoutADial) Models(context.Context) (map[string]chain.ModelParams, error) {
	return nil, nil
}

func (chainWithoutADial) PreservedNodes(context.Context) (*chain.PreservedNodes, bool, error) {
	return nil, false, nil
}

func (chainWithoutADial) ChainID(context.Context) (string, error) { return "", errNoChainDialed }

func (chainWithoutADial) Account(context.Context, string) (chain.Account, error) {
	return chain.Account{}, errNoChainDialed
}

func (chainWithoutADial) SpendableBalance(context.Context, string, string) (uint64, error) {
	return 0, errNoChainDialed
}

func (chainWithoutADial) Broadcast(context.Context, []byte) (string, error) {
	return "", errNoChainDialed
}

func (chainWithoutADial) Tx(context.Context, string) (chain.TxResult, bool, error) {
	return chain.TxResult{}, false, errNoChainDialed
}

func (chainWithoutADial) Escrow(context.Context, uint64) (chain.EscrowInfo, bool, error) {
	return chain.EscrowInfo{}, false, errNoChainDialed
}

func sessionsReading(records devshardLookup, storageDir string, reader chain.Reader) sessionSources {
	return func(config.Chain, config.HeightSync, string, *transport.PeerRPCAdoption, user.StepTimer) (chainSources, error) {
		return chainSources{
			Serving: func(context.Context, string) (registry.EscrowSession, error) {
				return nil, errNoChainDialed
			},
			ReadOnly:  readOnlySessions(records, storageDir),
			Reader:    reader,
			Transport: chainWithoutADial{},
		}, nil
	}
}

// chainServingModels is a chain.Reader that answers Models with a fixed context-length map.
type chainServingModels struct {
	chainWithoutADial
	models map[string]chain.ModelParams
}

func (c chainServingModels) Models(context.Context) (map[string]chain.ModelParams, error) {
	return c.models, nil
}

// Test flow:
//  1. Compose the gateway with sources that carry their own public-API client.
//  2. Assert the composed gateway keeps that client for the public API.
func TestComposeReadsThePublicAPIThroughTheSourcesClient(t *testing.T) {
	gatewayEnvironment(t)
	values, err := env.Load()
	if err != nil {
		t.Fatalf("env.Load() = %v, want nil", err)
	}
	storageDir, err := resolveStorageDir(values.StorageDir)
	if err != nil {
		t.Fatalf("resolveStorageDir() = %v, want nil", err)
	}
	gatewayStore, err := store.Open(storageDir)
	if err != nil {
		t.Fatalf("store.Open() = %v, want nil", err)
	}
	client := &http.Client{}
	sources := func(config.Chain, config.HeightSync, string, *transport.PeerRPCAdoption, user.StepTimer) (chainSources, error) {
		return chainSources{
			Serving:   func(context.Context, string) (registry.EscrowSession, error) { return nil, errNoChainDialed },
			ReadOnly:  readOnlySessions(gatewayStore, storageDir),
			Reader:    chainWithoutADial{},
			Transport: chainWithoutADial{},
			PublicAPI: client,
		}, nil
	}

	composed, err := compose(t.Context(), values, storageDir, gatewayStore, sources)
	if err != nil {
		gatewayStore.Close()
		t.Fatalf("compose() = %v, want nil", err)
	}
	t.Cleanup(func() { _ = composed.shutdown(shutdownGracePeriod) })

	if composed.publicAPI != client {
		t.Fatalf("compose().publicAPI = %p, want the sources client %p", composed.publicAPI, client)
	}
}

// composedGateway builds exactly what run() builds, through compose().
func composedGateway(t *testing.T) *gateway {
	t.Helper()
	return composedGatewayReading(t, chainWithoutADial{})
}

func composedGatewayReading(t *testing.T, reader chain.Reader) *gateway {
	t.Helper()
	values, err := env.Load()
	if err != nil {
		t.Fatalf("loading environment: %v", err)
	}
	storageDir, err := resolveStorageDir(values.StorageDir)
	if err != nil {
		t.Fatalf("resolving storage dir: %v", err)
	}
	gatewayStore, err := store.Open(storageDir)
	if err != nil {
		t.Fatalf("opening store: %v", err)
	}
	composed, err := compose(context.Background(), values, storageDir, gatewayStore, sessionsReading(gatewayStore, storageDir, reader))
	if err != nil {
		gatewayStore.Close()
		t.Fatalf("compose(): %v", err)
	}
	t.Cleanup(func() {
		if err := composed.shutdown(shutdownGracePeriod); err != nil {
			t.Errorf("shutdown(): %v", err)
		}
	})
	return composed
}

// newTestJournal is a journal a test closes, for wiring that hands facts to one.
func newTestJournal(t *testing.T) *journal.Journal {
	t.Helper()
	events := journal.New(journal.Settings{})
	t.Cleanup(func() { _ = events.Close() })
	return events
}

// Test flow:
//  1. Start run() in the background against a free port with a fake chain and metrics environment.
//  2. Poll /metrics until it responds 200, since go_goroutines from the process collector is present on the very first scrape.
//  3. Assert the scraped body contains go_goroutines.
//  4. Cancel the context and assert run() returns nil within 10s.
func TestRunServesMetricsAndShutsDownGracefully(t *testing.T) {
	port := freePort(t)
	gatewayEnvironment(t)
	t.Setenv("DEVSHARD_PORT", fmt.Sprintf("%d", port))

	ctx, cancel := context.WithCancel(context.Background())
	runResult := make(chan error, 1)
	go func() { runResult <- run(ctx) }()

	baseURL := fmt.Sprintf("http://127.0.0.1:%d", port)
	var body string
	deadline := time.Now().Add(5 * time.Second)
	for {
		response, err := http.Get(baseURL + "/metrics")
		if err == nil {
			raw, readErr := io.ReadAll(response.Body)
			response.Body.Close()
			if readErr == nil && response.StatusCode == http.StatusOK {
				body = string(raw)
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("gateway did not serve /metrics within 5s (last error: %v)", err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !strings.Contains(body, "go_goroutines") {
		t.Fatalf("/metrics exposition is missing the process collector:\n%s", body)
	}

	cancel()
	select {
	case err := <-runResult:
		if err != nil {
			t.Fatalf("run() after cancel: %v, want nil (graceful shutdown)", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("run() did not return within 10s of cancellation")
	}
}

// Test flow:
//  1. Start run() in the background against a free port with a fake chain environment and an admin key.
//  2. Poll POST /v1/chat/completions, as the admin, with a model no escrow serves until it responds.
//  3. Assert the status is 503 rather than 404, since 404 would mean main is serving the metrics mux instead of the api server's routes.
//  4. Cancel the context and assert run() returns nil within 10s.
func TestRunServesTheChatCompletionsRouteFromTheComposedServer(t *testing.T) {
	port := freePort(t)
	gatewayEnvironment(t)
	t.Setenv("DEVSHARD_PORT", fmt.Sprintf("%d", port))
	t.Setenv("DEVSHARD_ADMIN_API_KEY", routeTestAdminKey)

	ctx, cancel := context.WithCancel(context.Background())
	runResult := make(chan error, 1)
	go func() { runResult <- run(ctx) }()

	baseURL := fmt.Sprintf("http://127.0.0.1:%d", port)
	var status int
	deadline := time.Now().Add(5 * time.Second)
	for {
		request, err := http.NewRequest(http.MethodPost, baseURL+"/v1/chat/completions",
			strings.NewReader(`{"model":"nobody-serves-this","messages":[{"role":"user","content":"hi"}]}`))
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Authorization", "Bearer "+routeTestAdminKey)
		response, err := http.DefaultClient.Do(request)
		if err == nil {
			response.Body.Close()
			status = response.StatusCode
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("gateway did not serve /v1/chat/completions within 5s (last error: %v)", err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if status != http.StatusServiceUnavailable {
		t.Fatalf("POST /v1/chat/completions with an empty registry = %d, want %d", status, http.StatusServiceUnavailable)
	}

	cancel()
	select {
	case err := <-runResult:
		if err != nil {
			t.Fatalf("run() after cancel: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("run() did not return within 10s of cancellation")
	}
}

// Test flow:
//  1. Set GATEWAY_MATCH_WAIT_MS, a variable the deployed names never included, to a non-numeric value.
//  2. Call run().
//  3. Assert it returns an error naming GATEWAY_MATCH_WAIT_MS.
func TestRunFailsFastOnInvalidEnvironment(t *testing.T) {
	gatewayEnvironment(t)
	t.Setenv("GATEWAY_MATCH_WAIT_MS", "not-a-number")
	if err := run(context.Background()); err == nil || !strings.Contains(err.Error(), "GATEWAY_MATCH_WAIT_MS") {
		t.Fatalf("run() with bad env = %v, want error naming GATEWAY_MATCH_WAIT_MS", err)
	}
}

// Test flow:
//  1. Set GATEWAY_MAX_TOKENS_CAP above the largest output any request may ask for.
//  2. Call run().
//  3. Assert it returns an error naming max_tokens_cap.
func TestRunFailsFastOnInvalidMergedConfig(t *testing.T) {
	gatewayEnvironment(t)
	t.Setenv("GATEWAY_MAX_TOKENS_CAP", fmt.Sprintf("%d", filters.MaxOutputTokens+1))
	if err := run(context.Background()); err == nil || !strings.Contains(err.Error(), "max_tokens_cap") {
		t.Fatalf("run() with invalid merged config = %v, want max_tokens_cap error", err)
	}
}

// Test flow:
//  1. Compose a gateway and run one race with no escrows configured.
//  2. Assert Run() fails.
//  3. Poll the store for an accounting row keyed by the request's ID.
//  4. Assert the row records the raced request's model and input tokens.
func TestARacePutsAnAccountingRowInTheStoreTheGatewayOpened(t *testing.T) {
	gatewayEnvironment(t)
	composed := composedGateway(t)

	if _, err := composed.races.Run(context.Background(), engine.Request{
		RequestID:   "accounted-request",
		Model:       "nobody-serves-this",
		InputTokens: 11,
	}, io.Discard); err == nil {
		t.Fatal("Run() with no escrows = nil, want a failure")
	}

	record := waitForAccountingRow(t, composed.store, "accounted-request")
	if record.Model != "nobody-serves-this" || record.InputTokens != 11 {
		t.Fatalf("accounting row = %+v, want the raced request's own model and input tokens", record)
	}
}

func waitForAccountingRow(t *testing.T, records *store.Store, requestID string) store.RequestRecord {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		record, found, err := records.FindRequest(context.Background(), requestID)
		if err != nil {
			t.Fatalf("FindRequest(%q): %v", requestID, err)
		}
		if found {
			return record
		}
		if time.Now().After(deadline) {
			t.Fatalf("no accounting row for %q within 5s: the engine's ledger is not wired to this store", requestID)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// Test flow:
//  1. Compose a gateway and scrape /metrics before any race runs.
//  2. Run one race with no escrows configured, which fails.
//  3. Scrape /metrics again.
//  4. Assert the race-total metric family is absent before the race and present after.
func TestARaceMovesTheMetricsTheGatewayExposes(t *testing.T) {
	gatewayEnvironment(t)
	composed := composedGateway(t)
	exposition := httptest.NewServer(composed.server.Handler)
	defer exposition.Close()

	before := scrape(t, exposition.URL)
	if _, err := composed.races.Run(context.Background(), engine.Request{
		RequestID:   "measured-request",
		Model:       "nobody-serves-this",
		InputTokens: 7,
	}, io.Discard); err == nil {
		t.Fatal("Run() with no escrows = nil, want a failure")
	}
	after := scrape(t, exposition.URL)

	const raceFamily = "devshard_gateway_requests_total"
	if strings.Contains(before, raceFamily) {
		t.Fatalf("%s was already exposed before any race ran", raceFamily)
	}
	if !strings.Contains(after, raceFamily) {
		t.Fatalf("%s is absent after a race ran: the engine's race recorder is not wired\n%s", raceFamily, after)
	}
}

func scrape(t *testing.T, baseURL string) string {
	t.Helper()
	response, err := http.Get(baseURL + "/metrics")
	if err != nil {
		t.Fatalf("scraping /metrics: %v", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("reading /metrics: %v", err)
	}
	return string(body)
}

// Test flow:
//  1. Compose a gateway.
//  2. For each owner (limits, perf, registry, chain, transport, accounting, journal, funding), build a fresh collector describing the same families.
//  3. Try registering it on the gateway's own telemetry registry.
//  4. Assert registration is refused, since a refusal proves compose already registered that owner's collector.
func TestEveryOwnerCollectorIsRegisteredOnTheGatewaysRegistry(t *testing.T) {
	gatewayEnvironment(t)
	composed := composedGateway(t)

	testCases := []struct {
		name      string
		collector prometheus.Collector
	}{
		{name: "limits", collector: metrics.NewLimitsCollector(metrics.LimitsSources{})},
		{name: "perf", collector: metrics.NewPerfCollector(nil)},
		{name: "registry", collector: metrics.NewRegistryCollector(metrics.RegistrySources{})},
		{name: "chain", collector: metrics.NewChainCollector(nil, time.Now)},
		{name: "transport", collector: metrics.NewTransportCollector(nil)},
		{name: "accounting", collector: metrics.NewAccountingCollector(nil)},
		{name: "journal", collector: metrics.NewJournalCollector(nil)},
		{name: "nonce ledger", collector: metrics.NewNonceLedgerCollector(nil)},
		{name: "funding", collector: metrics.NewFundingCollector(nil, nil)},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			if err := composed.telemetry.Registry().Register(testCase.collector); err == nil {
				t.Fatalf("the %s collector was not registered by compose()", testCase.name)
			}
		})
	}
}

// Test flow:
//  1. Build a depletion notice no manager has been bound to yet.
//  2. Send it every routing notice.
//  3. Assert none of them panics.
func TestADepletionNoticeWithoutAManagerDropsItsNotices(t *testing.T) {
	notice := &depletionNotice{}

	notice.OnBalanceExhausted("5", scheduler.ExhaustionBalanceFloor)
	notice.OnReserveTaken("5")
	notice.OnMoneyShort("qwen")
}

// Test flow:
//  1. Compose a gateway with accounting enabled.
//  2. Gather every metric family from the telemetry registry.
//  3. Assert no family name carries the nonce-accounting prefix or names, since the ledger is read through its own API, not a Prometheus scrape.
func TestTheAccountingLedgerExportsNothingToPrometheus(t *testing.T) {
	gatewayEnvironment(t)
	t.Setenv("DEVSHARD_STATS_ENABLED", "true")
	composed := composedGateway(t)

	families, err := composed.telemetry.Registry().Gather()
	if err != nil {
		t.Fatalf("Gather(): %v", err)
	}
	for _, family := range families {
		name := family.GetName()
		if strings.HasPrefix(name, "devshard_gateway_nonces_") || name == "devshard_gateway_nonce_facts_rejected_total" || name == "devshard_gateway_nonce_finding" {
			t.Errorf("the accounting ledger still exports %s", name)
		}
	}
}

// Test flow:
//  1. For each builder limit (1, 4, 16, 64), build a boot budget.
//  2. Assert its client transport is an *http.Transport.
//  3. Assert the budget's builder count matches the limit.
//  4. Assert MaxIdleConnsPerHost and MaxIdleConns both match the limit.
func TestBootBudgetSizesTheIdlePoolToTheBuilderLimit(t *testing.T) {
	for _, builders := range []int{1, 4, 16, 64} {
		t.Run(fmt.Sprintf("%d builders", builders), func(t *testing.T) {
			budget := newBootBudget(builders)
			pooled, isTransport := budget.client.Transport.(*http.Transport)
			if !isTransport {
				t.Fatalf("boot client transport is %T, want *http.Transport", budget.client.Transport)
			}
			if budget.builders != builders {
				t.Fatalf("builders = %d, want %d", budget.builders, builders)
			}
			if pooled.MaxIdleConnsPerHost != builders {
				t.Fatalf("MaxIdleConnsPerHost = %d, want %d (the semaphore bound); an unpaired pool churns connections",
					pooled.MaxIdleConnsPerHost, builders)
			}
			if pooled.MaxIdleConns != builders {
				t.Fatalf("MaxIdleConns = %d, want %d", pooled.MaxIdleConns, builders)
			}
		})
	}
}

// Test flow:
//  1. Build a boot budget with a builder limit of 0.
//  2. Assert the budget falls back to 1 builder.
func TestBootBudgetRefusesANonPositiveBuilderLimit(t *testing.T) {
	if budget := newBootBudget(0); budget.builders != 1 {
		t.Fatalf("newBootBudget(0).builders = %d, want 1", budget.builders)
	}
}

type escrowPublisher struct {
	mu          sync.Mutex
	added       []string
	retired     []string
	deactivated []string
	unserved    []string
	unopened    []string
	causes      map[string]error

	failures map[string]error

	inFlight atomic.Int64
	peak     atomic.Int64
	hold     chan struct{}
}

func (p *escrowPublisher) add(_ context.Context, record store.DevshardRecord) error {
	escrowID := record.EscrowID
	inFlight := p.inFlight.Add(1)
	for {
		peak := p.peak.Load()
		if inFlight <= peak || p.peak.CompareAndSwap(peak, inFlight) {
			break
		}
	}
	if p.hold != nil {
		<-p.hold
	}
	p.inFlight.Add(-1)

	p.mu.Lock()
	defer p.mu.Unlock()
	p.added = append(p.added, escrowID)
	return p.failures[escrowID]
}

func (p *escrowPublisher) retire(escrowID string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.retired = append(p.retired, escrowID)
	return nil
}

func (p *escrowPublisher) deactivate(escrowID string, cause error) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.deactivated = append(p.deactivated, escrowID)
	if p.causes == nil {
		p.causes = map[string]error{}
	}
	p.causes[escrowID] = cause
	return nil
}

func (p *escrowPublisher) unservable(escrowID string, _ error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.unserved = append(p.unserved, escrowID)
}

func (p *escrowPublisher) unopenedEscrow(escrowID string, _ error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.unopened = append(p.unopened, escrowID)
}

func devshard(escrowID string, active bool) store.DevshardRecord {
	return store.DevshardRecord{EscrowID: escrowID, Model: "model-a", Active: active}
}

// Test flow:
//  1. Build a table of devshard records and per-escrow build failures, varying whether the record is inactive, missing from the chain, missing its key, failing for another reason (left unpublished for a retry), or healthy.
//  2. Run publishEscrows over each case's records with a fake publisher.
//  3. Assert the returned error matches the case's expectation.
//  4. Assert the publisher's added, retired, deactivated, unserved and unopened lists match the case's expectations.
//  5. Assert each deactivation carried the build failure that caused it.
func TestPublishEscrowsWalksTheThreeArmedBuildLadder(t *testing.T) {
	buildFailure := errors.New("chain REST unreachable")
	testCases := []struct {
		name            string
		records         []store.DevshardRecord
		failures        map[string]error
		wantAdded       []string
		wantRetired     []string
		wantDeactivated []string
		wantUnopened    []string
		wantCauses      map[string]error
		wantError       error
	}{
		{
			name:        "an inactive devshard is never built and is retired from routing",
			records:     []store.DevshardRecord{devshard("dormant", false)},
			wantRetired: []string{"dormant"},
		},
		{
			name:            "an escrow the chain does not have is marked inactive and skipped",
			records:         []store.DevshardRecord{devshard("gone", true)},
			failures:        map[string]error{"gone": fmt.Errorf("opening session: %w", bridge.ErrEscrowNotFound)},
			wantAdded:       []string{"gone"},
			wantDeactivated: []string{"gone"},
			wantCauses:      map[string]error{"gone": bridge.ErrEscrowNotFound},
		},
		{
			name:            "an escrow whose key the environment lacks is marked inactive and skipped",
			records:         []store.DevshardRecord{devshard("keyless", true)},
			failures:        map[string]error{"keyless": fmt.Errorf("opening session: %w", env.ErrPrivateKeyMissing)},
			wantAdded:       []string{"keyless"},
			wantDeactivated: []string{"keyless"},
			wantCauses:      map[string]error{"keyless": env.ErrPrivateKeyMissing},
		},
		{
			name:         "any other failure leaves the escrow unpublished for a retry and marks nothing inactive",
			records:      []store.DevshardRecord{devshard("unreachable", true), devshard("serving", true)},
			failures:     map[string]error{"unreachable": buildFailure},
			wantAdded:    []string{"serving", "unreachable"},
			wantUnopened: []string{"unreachable"},
		},
		{
			name:      "a healthy devshard is published",
			records:   []store.DevshardRecord{devshard("serving", true)},
			wantAdded: []string{"serving"},
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			publisher := &escrowPublisher{failures: testCase.failures}

			unopened, err := publishEscrows(context.Background(), testCase.records, 4,
				publisher.add, publisher.retire, publisher.deactivate, publisher.unservable, publisher.unopenedEscrow)

			if testCase.wantError == nil && err != nil {
				t.Fatalf("publishEscrows() = %v, want nil", err)
			}
			if testCase.wantError != nil && !errors.Is(err, testCase.wantError) {
				t.Fatalf("publishEscrows() = %v, want %v", err, testCase.wantError)
			}
			assertSame(t, "added", publisher.added, testCase.wantAdded)
			assertSame(t, "retired", publisher.retired, testCase.wantRetired)
			assertSame(t, "deactivated", publisher.deactivated, testCase.wantDeactivated)
			assertSame(t, "unserved", publisher.unserved, testCase.wantDeactivated)
			assertSame(t, "unopened", publisher.unopened, testCase.wantUnopened)
			if unopened != len(testCase.wantUnopened) {
				t.Fatalf("publishEscrows() reported %d unopened, want %d", unopened, len(testCase.wantUnopened))
			}
			for escrowID, wantCause := range testCase.wantCauses {
				if !errors.Is(publisher.causes[escrowID], wantCause) {
					t.Fatalf("deactivate(%s) cause = %v, want %v", escrowID, publisher.causes[escrowID], wantCause)
				}
			}
		})
	}
}

// Test flow:
//  1. Build a registry.
//  2. Call addServing for one row in the reserve role and one regular row.
//  3. Assert the registry publishes the first as a reserve candidate and the second as a regular one.
func TestAddingAReserveRowPublishesItAsAReserve(t *testing.T) {
	escrows := registry.New(registry.Deps{
		ServingSessions: func(context.Context, string) (registry.EscrowSession, error) {
			return weightlessSession{participants: []string{"validator-a"}}, nil
		},
		Now: time.Now,
	})
	t.Cleanup(func() { escrows.Close() })

	if err := addServing(context.Background(), escrows, store.DevshardRecord{EscrowID: "reserve", Model: "model-a", Active: true, RotationRole: escrow.RoleReserve}); err != nil {
		t.Fatalf("addServing(reserve) = %v", err)
	}
	if err := addServing(context.Background(), escrows, store.DevshardRecord{EscrowID: "serving", Model: "model-a", Active: true, RotationRole: "regular"}); err != nil {
		t.Fatalf("addServing(serving) = %v", err)
	}

	reserves := map[string]bool{}
	for _, candidate := range escrows.Candidates("model-a") {
		reserves[candidate.ID] = candidate.IsReserve
	}
	if !reserves["reserve"] || reserves["serving"] || len(reserves) != 2 {
		t.Fatalf("candidate reserve flags = %v, want only the reserve row flagged", reserves)
	}
}

// Test flow:
//  1. For each table case, build the escrowExhaustion probe over a registry holding one escrow: funded, with a balance of one, or with a balance of one in a finalizing session; or ask for an escrow the registry does not hold.
//  2. Ask its Exhaustion for the escrow.
//  3. Assert no case reads a reason: a balance never retires an escrow, and only the nonce cap does (no case here reaches it).
func TestTheExhaustionProbeReadsTheSchedulersVerdict(t *testing.T) {
	testCases := []struct {
		name     string
		session  registry.EscrowSession
		escrowID string
		want     scheduler.ExhaustionReason
	}{
		{name: "funded", session: weightlessSession{participants: []string{"validator-a"}}, escrowID: "escrow-1"},
		{name: "balance_of_one", session: poorSession{weightlessSession{participants: []string{"validator-a"}}}, escrowID: "escrow-1"},
		{name: "finalizing", session: finalizingPoorSession{poorSession{weightlessSession{participants: []string{"validator-a"}}}}, escrowID: "escrow-1"},
		{name: "unknown", session: poorSession{weightlessSession{participants: []string{"validator-a"}}}, escrowID: "unknown"},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			escrows, router := composedRoutingOver(t, limits.NewCapacity(func(string, string) bool { return true }), testCase.session)
			exhaustion := escrowExhaustion{escrows: escrows, router: router}

			if reason := exhaustion.Exhaustion(testCase.escrowID); reason != testCase.want {
				t.Fatalf("Exhaustion(%s) = %q, want %q", testCase.escrowID, reason, testCase.want)
			}
		})
	}
}

func assertSame(t *testing.T, label string, got, want []string) {
	t.Helper()
	got, want = slices.Sorted(slices.Values(got)), slices.Sorted(slices.Values(want))
	if len(got) != len(want) {
		t.Fatalf("%s = %v, want %v", label, got, want)
	}
	for index := range got {
		if got[index] != want[index] {
			t.Fatalf("%s = %v, want %v", label, got, want)
		}
	}
}

// Test flow:
//  1. Run publishEscrows over 12 records with a builder limit of 3 and a publisher that blocks until released.
//  2. Wait until the publisher's in-flight count reaches the builder limit.
//  3. Release the hold and let publishEscrows finish.
//  4. Assert the peak in-flight count never exceeded the builder limit.
func TestPublishEscrowsBuildsNoMoreThanTheBuilderLimitAtOnce(t *testing.T) {
	const builders = 3
	records := make([]store.DevshardRecord, 0, 12)
	for index := range 12 {
		records = append(records, devshard(fmt.Sprintf("escrow-%d", index), true))
	}
	publisher := &escrowPublisher{hold: make(chan struct{})}

	done := make(chan error, 1)
	go func() {
		_, err := publishEscrows(context.Background(), records, builders,
			publisher.add, publisher.retire, publisher.deactivate, publisher.unservable, publisher.unopenedEscrow)
		done <- err
	}()

	deadline := time.Now().Add(5 * time.Second)
	for publisher.inFlight.Load() < builders {
		if time.Now().After(deadline) {
			t.Fatalf("only %d builders ever ran at once, want %d", publisher.peak.Load(), builders)
		}
		time.Sleep(time.Millisecond)
	}
	close(publisher.hold)
	if err := <-done; err != nil {
		t.Fatalf("publishEscrows(): %v", err)
	}
	if peak := publisher.peak.Load(); peak > builders {
		t.Fatalf("%d builders ran at once, want at most %d", peak, builders)
	}
}

type shutdownRecorder struct {
	mu       sync.Mutex
	sequence *[]string
	name     string
	err      error
}

func (r *shutdownRecorder) note() {
	r.mu.Lock()
	defer r.mu.Unlock()
	*r.sequence = append(*r.sequence, r.name)
}

func (r *shutdownRecorder) Shutdown(context.Context) error { r.note(); return r.err }
func (r *shutdownRecorder) Stop()                          { r.note() }
func (r *shutdownRecorder) Close() error                   { r.note(); return r.err }
func (r *shutdownRecorder) CloseIdleConnections()          { r.note() }

func recordedShutdownParts(listener *shutdownRecorder, recorder func(name string) *shutdownRecorder) shutdownParts {
	return shutdownParts{
		listener:        listener,
		endRequests:     func() {},
		races:           recorder("races"),
		dispatchers:     recorder("dispatchers"),
		escrowLifecycle: recorder("escrow lifecycle"),
		chainObserver:   recorder("chain observer"),
		nonceSweep:      recorder("nonce sweep"),
		sessions:        recorder("escrow sessions"),
		events:          recorder("journal"),
		nonceLedger:     recorder("nonce accounting"),
		governanceFeed:  recorder("runtime params"),
		heightFollower:  recorder("height follower"),
		storage:         recorder("store"),
		publicAPI:       recorder("public api connections"),
	}
}

// Test flow:
//  1. Build a shutdown step list from a set of recorders wired through shutdownOrder.
//  2. Run stopAll.
//  3. Assert it succeeds.
//  4. Assert the recorded stop order runs from the http server through to the store and public API connections.
func TestShutdownStopsAcceptingFirstAndClosesTheStoreLast(t *testing.T) {
	var sequence []string
	recorder := func(name string) *shutdownRecorder {
		return &shutdownRecorder{sequence: &sequence, name: name}
	}

	steps := shutdownOrder(recordedShutdownParts(recorder("http server"), recorder))
	if err := stopAll(context.Background(), steps); err != nil {
		t.Fatalf("stopAll(): %v", err)
	}

	want := []string{"http server", "races", "dispatchers", "escrow lifecycle", "chain observer", "nonce sweep", "escrow sessions", "journal", "nonce accounting", "runtime params", "height follower", "store", "public api connections"}
	assertSame(t, "shutdown sequence", sequence, want)
}

// Test flow:
//  1. Build a step list where "races" fails to drain and "escrow sessions" needs a quiesced drain.
//  2. Run stopAll.
//  3. Assert it returns an error.
//  4. Assert "escrow sessions" never ran and only "store" was reached.
func TestShutdownSkipsEscrowSessionsWhenADrainOverran(t *testing.T) {
	var sequence []string
	record := func(name string) func(context.Context) error {
		return func(context.Context) error { sequence = append(sequence, name); return nil }
	}
	steps := []shutdownStep{
		{name: "races", stop: func(context.Context) error { return errors.New("abandoned with work still running") }},
		{name: "escrow sessions", stop: record("escrow sessions"), needsQuiesced: true},
		{name: "store", stop: record("store")},
	}

	err := stopAll(context.Background(), steps)

	if err == nil {
		t.Fatal("stopAll reported success after abandoning a drain")
	}
	for _, name := range sequence {
		if name == "escrow sessions" {
			t.Fatal("escrow sessions were closed while a drain was still running")
		}
	}
	if len(sequence) != 1 || sequence[0] != "store" {
		t.Fatalf("sequence = %v, want the store still reached", sequence)
	}
}

// Test flow:
//  1. Build a shutdown step list where the http server step fails.
//  2. Run stopAll.
//  3. Assert the returned error names the listener failure.
//  4. Assert the store still appears in the recorded stop sequence.
func TestShutdownReachesTheStoreEvenWhenAnEarlierStepFails(t *testing.T) {
	var sequence []string
	recorder := func(name string) *shutdownRecorder {
		return &shutdownRecorder{sequence: &sequence, name: name}
	}
	failing := &shutdownRecorder{sequence: &sequence, name: "http server", err: errors.New("listener stuck")}

	steps := shutdownOrder(recordedShutdownParts(failing, recorder))
	err := stopAll(context.Background(), steps)

	if err == nil || !strings.Contains(err.Error(), "listener stuck") {
		t.Fatalf("stopAll() = %v, want the listener failure reported", err)
	}
	if !slices.Contains(sequence, "store") {
		t.Fatalf("shutdown sequence = %v, want the store closed despite the earlier failure", sequence)
	}
}

// blockingCloser is a close that returns only once released, like a journal whose sink is stuck.
type blockingCloser struct {
	entered  chan struct{}
	released chan struct{}
	release  func()
	result   error
}

// newBlockingCloser releases the close in cleanup, so the goroutine closeWithin started returns before goleak looks.
func newBlockingCloser(t *testing.T, result error) *blockingCloser {
	t.Helper()
	stuck := &blockingCloser{entered: make(chan struct{}), released: make(chan struct{}), result: result}
	stuck.release = sync.OnceFunc(func() { close(stuck.released) })
	t.Cleanup(stuck.release)
	return stuck
}

func (c *blockingCloser) Close() error {
	close(c.entered)
	<-c.released
	return c.result
}

// Test flow:
//  1. Build a blocking closer that never returns on its own and an already-expired context.
//  2. Run closeWithin with a zero budget against the expired context.
//  3. Assert it returns an error reporting the step abandoned with events still queued.
func TestTheJournalStepGivesUpWhenItsCloseOutlivesTheBudget(t *testing.T) {
	stuck := newBlockingCloser(t, nil)
	expired, cancel := context.WithCancel(t.Context())
	cancel()

	err := closeWithin(stuck, 0)(expired)

	if err == nil || !strings.Contains(err.Error(), "abandoned with events still queued") {
		t.Fatalf("closeWithin() = %v, want the journal step reported as abandoned", err)
	}
}

// Test flow:
//  1. Build a blocking closer that returns a specific refusal error and an already-expired context.
//  2. Run closeWithin with an hour-long floor against the expired context, in the background.
//  3. Wait for the close to start, then release it.
//  4. Assert closeWithin returns the close's own refusal error within 5 seconds.
func TestTheJournalStepWaitsOutItsFloorWhenTheBudgetIsAlreadySpent(t *testing.T) {
	refusal := errors.New("journal refused 1 money-lane events past its ceiling of 1")
	closing := newBlockingCloser(t, refusal)
	expired, cancel := context.WithCancel(t.Context())
	cancel()

	stepResult := make(chan error, 1)
	go func() { stepResult <- closeWithin(closing, time.Hour)(expired) }()
	select {
	case <-closing.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("closeWithin() never started the close")
	}
	closing.release()

	select {
	case err := <-stepResult:
		if !errors.Is(err, refusal) {
			t.Fatalf("closeWithin() = %v, want the close's own result", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("closeWithin() did not return within 5s of the close returning")
	}
}

// blockingStopper is a component whose drain never finishes on its own.
type blockingStopper struct {
	released chan struct{}
	returned chan struct{}
}

func newBlockingStopper(t *testing.T) *blockingStopper {
	t.Helper()
	blocked := &blockingStopper{released: make(chan struct{}), returned: make(chan struct{})}
	t.Cleanup(func() {
		close(blocked.released)
		<-blocked.returned
	})
	return blocked
}

func (b *blockingStopper) Stop() {
	<-b.released
	close(b.returned)
}

// Test flow:
//  1. Build a context with a 50ms timeout and a stopper whose drain never finishes on its own.
//  2. Run waitFor against that context.
//  3. Assert it returns the context's deadline-exceeded error.
func TestWaitForYieldsTheBudgetToTheStepsBelowADrainThatOutlastsIt(t *testing.T) {
	drainCtx, cancelDrain := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancelDrain()

	err := waitFor(newBlockingStopper(t))(drainCtx)

	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("waitFor() over a drain that outlasts the budget = %v, want the exceeded deadline reported", err)
	}
}

// Test flow:
//  1. Run waitFor over a recorder that stops immediately.
//  2. Assert it returns nil.
//  3. Assert the recorder is in the drained sequence.
func TestWaitForReportsNothingWhenTheDrainFinishesInTime(t *testing.T) {
	var sequence []string
	if err := waitFor(&shutdownRecorder{sequence: &sequence, name: "races"})(context.Background()); err != nil {
		t.Fatalf("waitFor() over a drain that finishes = %v, want nil", err)
	}
	assertSame(t, "drained components", sequence, []string{"races"})
}

func openedStore(t *testing.T) *store.Store {
	t.Helper()
	records, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open(): %v", err)
	}
	t.Cleanup(func() {
		if err := records.Close(); err != nil {
			t.Errorf("Close(): %v", err)
		}
	})
	return records
}

func servingRegistry(t *testing.T) *registry.Registry {
	t.Helper()
	escrows := registry.New(registry.Deps{
		ServingSessions: func(context.Context, string) (registry.EscrowSession, error) {
			return weightlessSession{participants: []string{"validator-a"}}, nil
		},
		Now: time.Now,
	})
	t.Cleanup(func() { escrows.Close() })
	return escrows
}

func storedDevshard(t *testing.T, records *store.Store, escrowID string) store.DevshardRecord {
	t.Helper()
	record, err := findDevshard(context.Background(), records, escrowID)
	if err != nil {
		t.Fatalf("findDevshard(%s): %v", escrowID, err)
	}
	return record
}

// Test flow:
//  1. Store an inactive devshard row in the reserve role.
//  2. Call operator.Activate for that escrow.
//  3. Assert the registry publishes it as a reserve, so routing keeps it for the requests no regular escrow can take.
func TestActivatingAReserveRowPublishesItAsAReserve(t *testing.T) {
	ctx := context.Background()
	records := openedStore(t)
	if err := records.UpsertDevshard(ctx, store.DevshardRecord{EscrowID: "escrow-1", Model: "model-a", PrivateKeyEnv: "DEVSHARD_HELD_KEY", RotationRole: escrow.RoleReserve}); err != nil {
		t.Fatalf("UpsertDevshard(): %v", err)
	}
	escrows := servingRegistry(t)
	operator := &operations{store: records, escrows: escrows}

	if err := operator.Activate(ctx, "escrow-1"); err != nil {
		t.Fatalf("Activate() = %v, want nil", err)
	}

	candidates := escrows.Candidates("model-a")
	if len(candidates) != 1 || !candidates[0].IsReserve {
		t.Fatalf("Candidates = %+v, want escrow-1 published as a reserve", candidates)
	}
}

// Test flow:
//  1. Store an active devshard record, park it for settlement, and set its settle transaction hash.
//  2. Call operator.AddDevshard for the same escrow with Activate set.
//  3. Assert it returns ErrDevshardNotActivatable.
//  4. Assert the stored row is still inactive, settlement-pending, and keeps its settle hash.
//  5. Assert the escrow was not published routable.
func TestRegisteringAParkedEscrowIsRefused(t *testing.T) {
	ctx := context.Background()
	t.Setenv("DEVSHARD_PARKED_KEY", "0x01")
	records := openedStore(t)
	if err := records.UpsertDevshard(ctx, store.DevshardRecord{EscrowID: "1", Model: "model-a", PrivateKeyEnv: "DEVSHARD_PARKED_KEY", Active: true}); err != nil {
		t.Fatalf("UpsertDevshard(): %v", err)
	}
	if err := records.ParkForSettlement(ctx, "1"); err != nil {
		t.Fatalf("ParkForSettlement(): %v", err)
	}
	if err := records.SetDevshardSettleTxHash(ctx, "1", "SETTLE-HASH"); err != nil {
		t.Fatalf("SetDevshardSettleTxHash(): %v", err)
	}
	escrows := servingRegistry(t)
	operator := &operations{store: records, escrows: escrows}

	err := operator.AddDevshard(ctx, api.AddDevshardRequest{
		EscrowID: "1", Model: "model-a", PrivateKeyEnv: "DEVSHARD_PARKED_KEY", Activate: true,
	})

	if !errors.Is(err, api.ErrDevshardNotActivatable) {
		t.Fatalf("AddDevshard(parked) = %v, want ErrDevshardNotActivatable", err)
	}
	row := storedDevshard(t, records, "1")
	if row.Active || !row.SettlementPending || row.SettleTxHash != "SETTLE-HASH" {
		t.Errorf("row = active %v, settlement pending %v, settle hash %q; want it still parked with its hash", row.Active, row.SettlementPending, row.SettleTxHash)
	}
	if _, routable := escrows.Routable("1"); routable {
		t.Error("the parked escrow was published routable")
	}
}

// Test flow:
//  1. Call operator.AddDevshard for an escrow the store has never seen, with Activate set.
//  2. Assert it succeeds.
//  3. Assert the stored row is active.
//  4. Assert the escrow is published routable.
func TestRegisteringAnEscrowTheStoreHasNotSeenPublishesIt(t *testing.T) {
	ctx := context.Background()
	t.Setenv("DEVSHARD_FRESH_KEY", "0x01")
	records := openedStore(t)
	escrows := servingRegistry(t)
	operator := &operations{store: records, escrows: escrows}

	if err := operator.AddDevshard(ctx, api.AddDevshardRequest{
		EscrowID: "1", Model: "model-a", PrivateKeyEnv: "DEVSHARD_FRESH_KEY", Activate: true,
	}); err != nil {
		t.Fatalf("AddDevshard(new) = %v, want nil", err)
	}

	if row := storedDevshard(t, records, "1"); !row.Active {
		t.Errorf("row = %+v, want it stored active", row)
	}
	if _, routable := escrows.Routable("1"); !routable {
		t.Error("the new escrow was not published")
	}
}

// Test flow:
//  1. Set the key variable and open an empty store.
//  2. Call operator.AddDevshard with Activate set for each id that is not a canonical decimal chain escrow id.
//  3. Assert each returns ErrInvalidEscrowID.
//  4. Assert no row was stored and nothing was published.
func TestRegisteringAnEscrowWhoseIDIsNotAChainEscrowIDIsRefused(t *testing.T) {
	ctx := t.Context()
	t.Setenv("DEVSHARD_FRESH_KEY", "0x01")
	records := openedStore(t)
	escrows := servingRegistry(t)
	operator := &operations{store: records, escrows: escrows}

	for _, escrowID := range []string{"1/../../outside", "escrow-1", "007", "+7", " 7", "-1", "18446744073709551616"} {
		err := operator.AddDevshard(ctx, api.AddDevshardRequest{EscrowID: escrowID, Model: "model-a", PrivateKeyEnv: "DEVSHARD_FRESH_KEY", Activate: true})
		if !errors.Is(err, api.ErrInvalidEscrowID) {
			t.Errorf("AddDevshard(%q) = %v, want ErrInvalidEscrowID", escrowID, err)
		}
	}

	known, err := records.ListDevshards(ctx)
	if err != nil {
		t.Fatalf("ListDevshards(): %v", err)
	}
	if len(known) != 0 {
		t.Errorf("refused registrations stored %d rows, want 0", len(known))
	}
	if candidates := escrows.Candidates("model-a"); len(candidates) != 0 {
		t.Errorf("Candidates(model-a) = %+v, want none", candidates)
	}
}

// Test flow:
//  1. Build a participant limiter and record one transport-fault result for a known participant.
//  2. Call operator.Unquarantine for a participant key the limiter has never tracked.
//  3. Assert it returns ErrUnknownParticipant.
//  4. Call operator.Unquarantine for the tracked participant and assert it succeeds.
func TestUnquarantiningAnUntrackedParticipantIsNotReportedAsDone(t *testing.T) {
	configuration := config.Defaults()
	limiter := limits.NewParticipantLimiter(limits.ParticipantConfigFromLimits(configuration.Limits), time.Now)
	limiter.OnResult(limits.Result{Participant: "validator-a", Model: "model-a", Verdict: limits.TransportFault})
	operator := &operations{participants: limiter}

	if err := operator.Unquarantine(context.Background(), "validator-typo"); !errors.Is(err, api.ErrUnknownParticipant) {
		t.Fatalf("Unquarantine(unknown) = %v, want ErrUnknownParticipant", err)
	}
	if err := operator.Unquarantine(context.Background(), "validator-a"); err != nil {
		t.Fatalf("Unquarantine(tracked) = %v, want nil", err)
	}
}

// weightlessSession is enough of an escrow session for the registry to publish and route, but cannot commit a nonce.
type weightlessSession struct{ participants []string }

type poorSession struct{ weightlessSession }

func (poorSession) Balance() uint64 { return 1 }

type finalizingPoorSession struct{ poorSession }

func (finalizingPoorSession) Phase() types.SessionPhase { return types.PhaseFinalizing }

func (weightlessSession) Balance() uint64     { return 1 << 40 }
func (weightlessSession) TokenPrice() uint64  { return 1 }
func (weightlessSession) FeePerNonce() uint64 { return 0 }

func (weightlessSession) PendingTxs() []*types.DevshardTx       { return nil }
func (weightlessSession) SendPendingDiff(context.Context) error { return nil }

func (s weightlessSession) ParticipantKeys() []string        { return s.participants }
func (s weightlessSession) HostParticipantKeyList() []string { return s.participants }
func (s weightlessSession) Nonce() uint64                    { return 1 }
func (s weightlessSession) PublishedNonce() uint64           { return 1 }
func (s weightlessSession) Phase() types.SessionPhase        { return types.PhaseActive }
func (s weightlessSession) SettlementSignatures(uint64) (map[uint32][]byte, error) {
	return nil, nil
}

func (s weightlessSession) SignaturesAt(uint64) map[uint32][]byte { return nil }

func (s weightlessSession) SignatureStatus() ([]user.SignatureStatusEntry, uint64, bool) {
	return nil, 0, false
}

func (s weightlessSession) SignedSlots() map[uint64]types.Bitmap128         { return nil }
func (s weightlessSession) SnapshotState() types.EscrowState                { return types.EscrowState{} }
func (s weightlessSession) SealedInferences() int                           { return 0 }
func (s weightlessSession) SetChainMaxNonce(uint32)                         {}
func (s weightlessSession) ArmQuietSettle()                                 {}
func (s weightlessSession) AllInferences() map[uint64]types.InferenceRecord { return nil }
func (s weightlessSession) Finalize(context.Context) error                  { return nil }
func (s weightlessSession) FlushSnapshot() error                            { return nil }
func (s weightlessSession) Close() error                                    { return nil }
func (s weightlessSession) UserSession() *user.Session                      { return nil }
func (s weightlessSession) SweepExecutionTimeouts(context.Context, time.Duration, int) user.SweepReport {
	return user.SweepReport{}
}

func (s weightlessSession) SweepBeforeFinalize(context.Context, int) (user.SweepReport, error) {
	return user.SweepReport{}, nil
}

func (s weightlessSession) ProtocolVersion() string { return "" }

func (s weightlessSession) Inference(uint64) (types.InferenceRecord, bool) {
	return types.InferenceRecord{}, false
}

func (s weightlessSession) SealedInference(uint64) (types.InferenceRecord, bool) {
	return types.InferenceRecord{}, false
}
func (s weightlessSession) HostDials() []registry.HostDial              { return nil }
func (s weightlessSession) ChatBudgets() map[string]registry.ChatBudget { return nil }
func (s weightlessSession) HeightSyncView() heightsync.OperatorView     { return heightsync.OperatorView{} }
func (s weightlessSession) WaitRouterCatalog(context.Context) error     { return nil }
func (s weightlessSession) WaitHeightSeedReady(context.Context) error   { return nil }

func (s weightlessSession) PrepareInferenceFn(user.ParamsForHost) (*user.PreparedInference, error) {
	return nil, nil
}

func (s weightlessSession) LiveInferences() (types.SessionConfig, []types.InferenceRecord) {
	return types.SessionConfig{}, nil
}

// Test flow:
//  1. Build capacity, config and a phase observer, then wire a router through newRouting for two weighted participants.
//  2. Add one escrow to the registry.
//  3. Assert the escrow's capacity weight is greater than zero.
//  4. Call router.Pick with a bounded timeout, since the fake session cannot hand back a committed nonce.
//  5. Assert the pick does not fail with ErrNoEscrowCapacity, meaning the escrow was selected rather than skipped as weightless.
func TestPublishingAnEscrowGivesItWeightAndMakesItPickable(t *testing.T) {
	participants := []string{"validator-a", "validator-b"}
	capacity := limits.NewCapacity(func(string, string) bool { return true })
	capacity.Update(chain.PhaseSnapshot{
		CurrentWeights: map[string]float64{"validator-a": 1000, "validator-b": 1000},
		FullWeights:    map[string]float64{"validator-a": 1000, "validator-b": 1000},
	})
	configuration := config.Defaults()
	configHolder := config.NewHolder(&configuration)

	observer, err := chain.NewPhaseObserver(chain.ObserverConfig{PublicAPIBaseURL: "http://127.0.0.1:1"})
	if err != nil {
		t.Fatalf("NewPhaseObserver(): %v", err)
	}

	escrows, router, _, routingErr := newRouting(routingDeps{
		Sessions: func(context.Context, string) (registry.EscrowSession, error) {
			return weightlessSession{participants: participants}, nil
		},
		Capacity:     capacity,
		Participants: limits.NewParticipantLimiter(limits.ParticipantConfigFromLimits(configuration.Limits), time.Now),
		Hosts:        perf.NewTracker(configHolder, time.Now),
		Snapshots:    observer,
		Config:       configHolder,
		Depletion:    &depletionNotice{},
		Finishes:     &finishNotice{},
		Journal:      newTestJournal(t),
		Now:          time.Now,
	})
	if routingErr != nil {
		t.Fatalf("newRouting() = %v, want a wired router", routingErr)
	}
	t.Cleanup(func() { escrows.Close() })
	t.Cleanup(router.Stop)

	if err := escrows.Add(context.Background(), "escrow-1", "model-a"); err != nil {
		t.Fatalf("Add(): %v", err)
	}

	if weight := capacity.EscrowWeight("escrow-1", "model-a"); weight <= 0 {
		t.Fatalf("EscrowWeight() = %v, want > 0; membership never reached the capacity model, so every candidate scores as weightless", weight)
	}
	pickCtx, cancelPick := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancelPick()
	_, err = router.Pick(pickCtx, scheduler.RequestProfile{Model: "model-a", InputTokens: 4})
	if errors.Is(err, scheduler.ErrNoEscrowCapacity) {
		t.Fatalf("Pick() = %v, want the escrow to be selected; a weightless escrow is skipped and the gateway serves nothing", err)
	}
}

// Test flow:
//  1. Start a blocked phase observer and a capacity model with a partial weight cut.
//  2. For PoC mode relaxed and off, build a modelCapacity wrapping the same capacity and observer.
//  3. Read ModelWeights("model-a").ScaleFactor.
//  4. Assert relaxed mode keeps a positive scale factor and off leaves it at zero.
func TestModelCapacityScaleFactorFoldsRelaxedModeOverTheBlockedChainState(t *testing.T) {
	observer := blockedPhaseObserverForTest(t)
	capacity := limits.NewCapacity(func(string, string) bool { return true })
	capacity.Update(chain.PhaseSnapshot{
		CurrentWeights: map[string]float64{"host-a": 50},
		FullWeights:    map[string]float64{"host-a": 100},
	})

	testCases := []struct {
		name       string
		pocMode    string
		wantScaled bool
	}{
		{name: "relaxed mode keeps the weight-derived cap alive", pocMode: config.PoCModeRelaxed, wantScaled: true},
		{name: "off leaves the chain's block in effect", pocMode: config.PoCModeOff, wantScaled: false},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			configuration := config.Defaults()
			configuration.Modes.PoCMode = testCase.pocMode
			capacityForMode := modelCapacity{capacity: capacity, snapshots: observer, config: config.NewHolder(&configuration)}

			scale := capacityForMode.ModelWeights("model-a").ScaleFactor
			if testCase.wantScaled && scale <= 0 {
				t.Fatalf("ModelWeights().ScaleFactor = %v, want > 0", scale)
			}
			if !testCase.wantScaled && scale != 0 {
				t.Fatalf("ModelWeights().ScaleFactor = %v, want 0", scale)
			}
		})
	}
}

// blockedPhaseObserverForTest starts a PhaseObserver against a stub PoC-generation epoch and waits for its first snapshot, so RequestsBlocked is already true when it returns.
func blockedPhaseObserverForTest(t *testing.T) *chain.PhaseObserver {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/epochs/latest":
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{"block_height": 100, "phase": "PoCGenerate", "latest_epoch": {"index": 1, "poc_start_block_height": 0}, "is_confirmation_poc_active": false}`))
		case "/v1/epochs/current/participants":
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{"active_participants": {"participants": []}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)

	observer, err := chain.NewPhaseObserver(chain.ObserverConfig{
		PublicAPIBaseURL: server.URL,
		PollInterval:     time.Hour,
		HTTPClient:       server.Client(),
	})
	if err != nil {
		t.Fatalf("NewPhaseObserver(): %v", err)
	}
	t.Cleanup(observer.Stop)

	published := make(chan chain.PhaseSnapshot, 1)
	cancelSubscription := observer.Subscribe(func(snapshot chain.PhaseSnapshot) {
		select {
		case published <- snapshot:
		default:
		}
	})
	defer cancelSubscription()

	observer.Start(context.Background())
	select {
	case snapshot := <-published:
		if !snapshot.RequestsBlocked {
			t.Fatalf("RequestsBlocked = false, want true for a PoCGenerate epoch phase")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for the observer's first snapshot")
	}
	return observer
}

// routingFor builds the escrow set, capacity model and picker the way compose does.
func routingFor(t *testing.T, capacity *limits.Capacity, participants []string) *scheduler.Scheduler {
	t.Helper()
	_, router := composedRouting(t, capacity, participants)
	return router
}

func composedRouting(t *testing.T, capacity *limits.Capacity, participants []string) (*registry.Registry, *scheduler.Scheduler) {
	t.Helper()
	return composedRoutingOver(t, capacity, weightlessSession{participants: participants})
}

func composedRoutingOver(t *testing.T, capacity *limits.Capacity, session registry.EscrowSession) (*registry.Registry, *scheduler.Scheduler) {
	t.Helper()
	configuration := config.Defaults()
	configHolder := config.NewHolder(&configuration)
	observer, err := chain.NewPhaseObserver(chain.ObserverConfig{PublicAPIBaseURL: "http://127.0.0.1:1"})
	if err != nil {
		t.Fatalf("NewPhaseObserver(): %v", err)
	}
	escrows, router, _, routingErr := newRouting(routingDeps{
		Sessions: func(context.Context, string) (registry.EscrowSession, error) {
			return session, nil
		},
		Capacity:     capacity,
		Participants: limits.NewParticipantLimiter(limits.ParticipantConfigFromLimits(configuration.Limits), time.Now),
		Hosts:        perf.NewTracker(configHolder, time.Now),
		Snapshots:    observer,
		Config:       configHolder,
		Depletion:    &depletionNotice{},
		Finishes:     &finishNotice{},
		Journal:      newTestJournal(t),
		Now:          time.Now,
	})
	if routingErr != nil {
		t.Fatalf("newRouting() = %v, want a wired router", routingErr)
	}
	t.Cleanup(func() { escrows.Close() })
	t.Cleanup(router.Stop)
	if err := escrows.Add(context.Background(), "escrow-1", "model-a"); err != nil {
		t.Fatalf("Add(): %v", err)
	}
	return escrows, router
}

// Test flow:
//  1. Call newRouting with an empty routingDeps carrying no journal.
//  2. Assert it returns an error naming the journal as required.
func TestRoutingIsRefusedWithoutAJournal(t *testing.T) {
	_, _, _, err := newRouting(routingDeps{})

	if err == nil || !strings.Contains(err.Error(), "Journal is required") {
		t.Fatalf("newRouting() without a journal = %v, want the missing journal named", err)
	}
}

// Test flow:
//  1. Call newRouting with a journal and no finish notice.
//  2. Assert it returns an error naming the finish notice as required.
func TestRoutingIsRefusedWithoutAFinishNotice(t *testing.T) {
	_, _, _, err := newRouting(routingDeps{Journal: newTestJournal(t)})

	if err == nil || !strings.Contains(err.Error(), "Finishes is required") {
		t.Fatalf("newRouting() without a finish notice = %v, want the missing notice named", err)
	}
}

// Test flow:
//  1. Compose a gateway as run() does.
//  2. Assert the finish notice compose kept holds the composed race engine, so finishes it is told about reach that engine.
func TestComposeAttachesTheRaceEngineToTheFinishNotice(t *testing.T) {
	gatewayEnvironment(t)
	composed := composedGateway(t)

	if attached := composed.finishes.races.Load(); attached == nil || attached != composed.races {
		t.Fatalf("compose() finish notice engine = %p, want the composed engine %p", attached, composed.races)
	}
}

// Test flow:
//  1. Build a table of phase snapshots: a fresh poll naming no participants, a stale poll with last-known weights, and a fresh poll naming every participant at zero weight.
//  2. Update capacity from each snapshot and route one request through a picker wired for two participants.
//  3. Assert whether the pick reaches ErrNoEscrowCapacity matches the case's expected routed outcome.
func TestChainWeightsTheGatewayHasNotObservedStillRouteARequest(t *testing.T) {
	cases := []struct {
		name       string
		snapshot   chain.PhaseSnapshot
		wantRouted bool
	}{
		{
			name:       "a fresh poll that named no participants routes on membership alone",
			snapshot:   chain.PhaseSnapshot{LastUpdatedAt: time.Now()},
			wantRouted: true,
		},
		{
			name: "a stale poll routes on the weights it last observed",
			snapshot: chain.PhaseSnapshot{
				CurrentWeights: map[string]float64{"validator-a": 1000, "validator-b": 1000},
				FullWeights:    map[string]float64{"validator-a": 1000, "validator-b": 1000},
				LastError:      "fetch participants: connection refused",
			},
			wantRouted: true,
		},
		{
			name: "a fresh poll that named every participant at zero weight is honoured",
			snapshot: chain.PhaseSnapshot{
				CurrentWeights: map[string]float64{"validator-a": 0, "validator-b": 0},
				FullWeights:    map[string]float64{"validator-a": 0, "validator-b": 0},
				LastUpdatedAt:  time.Now(),
			},
			wantRouted: false,
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			capacity := limits.NewCapacity(func(string, string) bool { return true })
			capacity.Update(testCase.snapshot)
			router := routingFor(t, capacity, []string{"validator-a", "validator-b"})

			pickCtx, cancelPick := context.WithTimeout(context.Background(), 250*time.Millisecond)
			defer cancelPick()
			_, err := router.Pick(pickCtx, scheduler.RequestProfile{Model: "model-a", InputTokens: 4})

			if routed := !errors.Is(err, scheduler.ErrNoEscrowCapacity); routed != testCase.wantRouted {
				t.Fatalf("Pick() = %v, routed = %v, want routed = %v", err, routed, testCase.wantRouted)
			}
		})
	}
}

// Test flow:
//  1. Compose a gateway and swap its config for one with a concurrency cap of 1 and no admission queue wait.
//  2. Acquire once for a model under that cap.
//  3. Assert the first acquire is admitted.
//  4. Assert a second acquire is refused with a rate-limit error.
func TestReconfiguringTheGatewayChangesTheCapTheNextRequestIsJudgedAgainst(t *testing.T) {
	gatewayEnvironment(t)
	composed := composedGateway(t)

	widened := config.Defaults()
	widened.Limits.Concurrency.MaxRequests = 1
	widened.Limits.AdmissionQueueWaitMS = 0
	composed.config.Swap(&widened)

	if err := composed.limiter.AcquireForModel(context.Background(), "model-a", 1, limits.ModelCapacity{ScaleFactor: 1}); err != nil {
		t.Fatalf("first AcquireForModel() under a cap of 1 = %v, want admitted", err)
	}
	var rateLimited *limits.RateLimitError
	if err := composed.limiter.AcquireForModel(context.Background(), "model-a", 1, limits.ModelCapacity{ScaleFactor: 1}); !errors.As(err, &rateLimited) {
		t.Fatalf("second AcquireForModel() under a cap of 1 = %v, want a rate-limit error; the limiter never saw the new configuration", err)
	}
	composed.limiter.ReleaseForModel("model-a", 1)
}

// Test flow:
//  1. Compose a gateway and install a log capture.
//  2. Feed the configured number of transport-fault results into the composed participant limiter for one host.
//  3. Flush the journal.
//  4. Assert the log carries a "host cut off after transport faults" line with the expected fields.
func TestTheComposedParticipantLimiterNarratesACutOffThroughTheJournal(t *testing.T) {
	gatewayEnvironment(t)
	logged := logcapture.Install(t)
	composed := composedGateway(t)

	for range composed.config.Load().Limits.HostCutoff.AfterFailures {
		composed.participants.OnResult(limits.Result{
			Participant: "gonka1aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			Model:       "model-a",
			Verdict:     limits.TransportFault,
		})
	}
	composed.events.Flush()

	cutOff, found := logged.Find("host cut off after transport faults")
	if !found {
		t.Fatalf("no cut-off line among %+v: main.go did not bind the participant limiter's narrator to the journal", logged.All())
	}
	logged.RequireLine(t, logcapture.Entry{Level: "warn", Msg: "host cut off after transport faults", Fields: []any{
		"host", "aaaaaaaa", "model", "model-a", "reason", "consecutive_transport_faults", "backoff_count", 1,
		"cut_off_for_ms", logcapture.Field(cutOff, "cut_off_for_ms"),
	}})
}

// Test flow:
//  1. Open a store and build a suspicious-hosts tracker over it.
//  2. Add one host and assert it is reported suspicious.
//  3. Rebuild the tracker from the same store and assert the pin survived the restart.
//  4. Remove the host, rebuild again, and assert the unpin survived too.
func TestSuspiciousHostsAreWrittenThroughToTheStoreTheEngineReadsFrom(t *testing.T) {
	records, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open(): %v", err)
	}
	t.Cleanup(func() {
		if err := records.Close(); err != nil {
			t.Errorf("Close(): %v", err)
		}
	})
	ctx := context.Background()

	pins, err := newSuspiciousHosts(ctx, records)
	if err != nil {
		t.Fatalf("newSuspiciousHosts(): %v", err)
	}
	if err := pins.Add(ctx, "validator-a"); err != nil {
		t.Fatalf("Add(): %v", err)
	}
	if !pins.Suspicious("validator-a") {
		t.Error("the predicate the engine is given does not see a host the admin route just pinned")
	}

	restarted, err := newSuspiciousHosts(ctx, records)
	if err != nil {
		t.Fatalf("newSuspiciousHosts() after a restart: %v", err)
	}
	if !restarted.Suspicious("validator-a") {
		t.Error("the pin did not reach the store: a restart forgets it")
	}

	if err := restarted.Remove(ctx, "validator-a"); err != nil {
		t.Fatalf("Remove(): %v", err)
	}
	reloaded, err := newSuspiciousHosts(ctx, records)
	if err != nil {
		t.Fatalf("newSuspiciousHosts() after an unpin: %v", err)
	}
	if reloaded.Suspicious("validator-a") {
		t.Error("the unpin did not reach the store")
	}
}

// Test flow:
//  1. Compose a gateway reading a chain that serves a fixed context window for one model, and start its observer.
//  2. Poll the participant limiter's prefill window for that model.
//  3. Assert it converges to the initial-requests count times the governed context window.
func TestTheComposedParticipantLimiterPricesAModelByTheContextGovernanceReports(t *testing.T) {
	gatewayEnvironment(t)
	const governedContext = 4_096
	composed := composedGatewayReading(t, chainServingModels{
		models: map[string]chain.ModelParams{"model-a": {ContextWindow: governedContext}},
	})
	composed.observer.Start(context.Background())

	prefill := float64(composed.config.Load().Limits.HostWindows.Input.InitialRequests * governedContext)
	waitForPrefillWindow(t, composed, "model-a", prefill)
}

func waitForPrefillWindow(t *testing.T, composed *gateway, model string, want float64) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for attempt := 0; ; attempt++ {
		participant := fmt.Sprintf("gonka1host%d", attempt)
		if release, admitted := composed.participants.Acquire(participant, model, limits.TokenCost{Input: 1, Output: 1}); admitted == limits.AdmissionOpen {
			release()
		}
		observed := prefillWindowFor(composed, participant)
		if observed == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("prefill window for %s = %v, want %v: main.go never pushes governance's context windows into the limiter",
				model, observed, want)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func prefillWindowFor(composed *gateway, participant string) float64 {
	for _, window := range composed.participants.Snapshot() {
		if window.Participant == participant {
			return window.InputWindowTokens
		}
	}
	return 0
}

// Test flow:
//  1. Call contextWindowsOf with a snapshot naming a served model, one with a zero context window, one above the max, and one at the max.
//  2. Assert the result keeps only the served model and the one at the cap, each with its own length.
func TestContextWindowsOfKeepsOnlyTheLengthsAHostCouldServe(t *testing.T) {
	t.Parallel()

	windows := contextWindowsOf(chain.PhaseSnapshot{Models: map[string]chain.ModelParams{
		"served":     {ContextWindow: 32_768},
		"unreported": {ContextWindow: 0},
		"absurd":     {ContextWindow: config.MaxContextTokens + 1},
		"at the cap": {ContextWindow: config.MaxContextTokens},
	}})

	want := map[string]int64{"served": 32_768, "at the cap": config.MaxContextTokens}
	if len(windows) != len(want) {
		t.Fatalf("context windows = %v, want only %v: a length no host could serve is not a unit to count prefill in", windows, want)
	}
	for model, tokens := range want {
		if windows[model] != tokens {
			t.Errorf("context window for %q = %d, want %d", model, windows[model], tokens)
		}
	}
}
