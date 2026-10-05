package user

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"

	"devshard/host"
	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/state"
	"devshard/storage"
	"devshard/stub"
	"devshard/transport"
	"devshard/types"
)

const (
	storedCatchUpRoutePrefix = "/devshard/v2"
	storedCatchUpBalance     = 10_000_000
)

// hostRequestTotals is what host-bound requests cost on the wire, as each host's HTTP server read them.
type hostRequestTotals struct {
	largestBodyBytes   int64
	totalBodyBytes     int64
	requestCount       int
	refusedCount       int
	longestHandling    time.Duration
	unmeasuredRequests int
}

// hostRequestLog accumulates hostRequestTotals across the fleet's servers.
type hostRequestLog struct {
	mu              sync.Mutex
	totals          hostRequestTotals
	handlingPerHost map[int]time.Duration
}

func (requestLog *hostRequestLog) record(hostIdx int, bodyBytes int64, status int, handling time.Duration) {
	requestLog.mu.Lock()
	defer requestLog.mu.Unlock()
	if bodyBytes < 0 {
		requestLog.totals.unmeasuredRequests++
	}
	requestLog.totals.largestBodyBytes = max(requestLog.totals.largestBodyBytes, bodyBytes)
	requestLog.totals.totalBodyBytes += max(bodyBytes, 0)
	requestLog.totals.requestCount++
	if status >= http.StatusBadRequest {
		requestLog.totals.refusedCount++
	}
	requestLog.totals.longestHandling = max(requestLog.totals.longestHandling, handling)
	requestLog.handlingPerHost[hostIdx] += handling
}

func (requestLog *hostRequestLog) snapshot() hostRequestTotals {
	requestLog.mu.Lock()
	defer requestLog.mu.Unlock()
	return requestLog.totals
}

func (requestLog *hostRequestLog) handlingOf(hostIdx int) time.Duration {
	requestLog.mu.Lock()
	defer requestLog.mu.Unlock()
	return requestLog.handlingPerHost[hostIdx]
}

func (requestLog *hostRequestLog) reset() {
	requestLog.mu.Lock()
	defer requestLog.mu.Unlock()
	requestLog.totals = hostRequestTotals{}
	clear(requestLog.handlingPerHost)
}

// httpHostFleet is a group of real hosts, each behind its own transport.Server over HTTP with the production body limit.
type httpHostFleet struct {
	hostSigners  []*signing.Secp256k1Signer
	user         *signing.Secp256k1Signer
	group        []types.SlotAssignment
	config       types.SessionConfig
	verifier     signing.Verifier
	queryTimeout time.Duration
	hosts        []*host.Host
	clients      []HostClient
	requests     *hostRequestLog
}

func newHTTPHostFleet(t *testing.T, hostCount int, queryTimeout time.Duration) *httpHostFleet {
	t.Helper()
	fleet := &httpHostFleet{
		hostSigners:  make([]*signing.Secp256k1Signer, hostCount),
		user:         testutil.MustGenerateKey(t),
		verifier:     signing.NewSecp256k1Verifier(),
		queryTimeout: queryTimeout,
		hosts:        make([]*host.Host, hostCount),
		clients:      make([]HostClient, hostCount),
		requests:     &hostRequestLog{handlingPerHost: make(map[int]time.Duration)},
	}
	for hostIdx := range fleet.hostSigners {
		fleet.hostSigners[hostIdx] = testutil.MustGenerateKey(t)
	}
	fleet.group = testutil.MakeGroup(fleet.hostSigners)
	fleet.config = testutil.DefaultConfig(hostCount)
	for hostIdx := range fleet.hostSigners {
		fleet.startHost(t, hostIdx)
	}
	return fleet
}

// startHost serves host hostIdx from empty storage, as a host that never held the escrow or lost it.
func (fleet *httpHostFleet) startHost(t *testing.T, hostIdx int) {
	t.Helper()
	machineStore := testutil.MustMemoryStore(t, "escrow-1", fleet.user.Address(), fleet.config, fleet.group, storedCatchUpBalance)
	machine, err := state.NewStateMachine("escrow-1", fleet.config, fleet.group, storedCatchUpBalance, fleet.user.Address(), fleet.verifier, machineStore,
		state.WithStateRootAndProtocolVersion(testutil.RuntimeTestVersion))
	require.NoError(t, err)
	hostStore := testutil.MustMemoryStore(t, "escrow-1", fleet.user.Address(), fleet.config, fleet.group, storedCatchUpBalance)
	hostNode, err := host.NewHost(machine, fleet.hostSigners[hostIdx], stub.NewInferenceEngine(), "escrow-1", fleet.group, nil,
		host.WithGrace(10), host.WithStorage(hostStore), host.WithVerifier(fleet.verifier))
	require.NoError(t, err)
	server, err := transport.NewServer(hostNode, hostStore, fleet.verifier, fleet.user.Address())
	require.NoError(t, err)

	router := echo.New()
	routes := router.Group(storedCatchUpRoutePrefix)
	routes.Use(fleet.measureRequests(hostIdx))
	routes.Use(server.AuthMiddleware)
	routes.POST("/sessions/:id/chat/completions", server.HandleInference)
	routes.GET("/sessions/:id/signatures", server.HandleGetSignatures)
	routes.GET("/sessions/:id/mempool", server.HandleGetMempool)
	httpServer := httptest.NewServer(router)
	t.Cleanup(httpServer.Close)

	clientConfig := transport.DefaultClientConfig()
	clientConfig.RoutePrefix = storedCatchUpRoutePrefix
	clientConfig.QueryTimeout = fleet.queryTimeout
	fleet.hosts[hostIdx] = hostNode
	fleet.clients[hostIdx] = transport.NewHTTPClient(httpServer.URL, "escrow-1", fleet.user, clientConfig)
}

func (fleet *httpHostFleet) measureRequests(hostIdx int) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			if c.Request().Method != http.MethodPost {
				return next(c)
			}
			started := time.Now()
			err := next(c)
			status := c.Response().Status
			var httpError *echo.HTTPError
			if errors.As(err, &httpError) {
				status = httpError.Code
			} else if err != nil {
				status = http.StatusInternalServerError
			}
			fleet.requests.record(hostIdx, c.Request().ContentLength, status, time.Since(started))
			return err
		}
	}
}

// openSession opens the gateway's session over the fleet, journaling into store.
func (fleet *httpHostFleet) openSession(t *testing.T, store storage.Storage) *Session {
	t.Helper()
	require.NoError(t, store.CreateSession(storage.CreateSessionParams{
		EscrowID:       "escrow-1",
		Version:        testutil.RuntimeTestVersion,
		CreatorAddr:    fleet.user.Address(),
		Config:         fleet.config,
		Group:          fleet.group,
		InitialBalance: storedCatchUpBalance,
	}))
	machine, err := state.NewStateMachine("escrow-1", fleet.config, fleet.group, storedCatchUpBalance, fleet.user.Address(), fleet.verifier, store,
		state.WithStateRootAndProtocolVersion(testutil.RuntimeTestVersion))
	require.NoError(t, err)
	session, err := NewSession(machine, fleet.user, "escrow-1", fleet.group, fleet.clients, fleet.verifier, WithStorage(store))
	require.NoError(t, err)
	return session
}

// recoverSession restarts the gateway's session from store over the same fleet.
func (fleet *httpHostFleet) recoverSession(t *testing.T, store storage.Storage) *Session {
	t.Helper()
	session, _, err := RecoverSession(store, fleet.user, fleet.verifier, "escrow-1", testutil.RuntimeTestVersion, fleet.group, fleet.clients)
	require.NoError(t, err)
	return session
}

// requireHostsMatchTheGateway catches every host up and checks each holds the gateway's nonce and state root.
func (fleet *httpHostFleet) requireHostsMatchTheGateway(t *testing.T, session *Session) {
	t.Helper()
	require.NoError(t, session.CatchUpAllHosts(context.Background()))
	gatewayRoot, err := session.StateMachine().ComputeStateRoot()
	require.NoError(t, err)
	for hostIdx, hostNode := range fleet.hosts {
		require.Equal(t, session.Nonce(), hostNode.LatestNonce(), "host %d LatestNonce() = %d, want the gateway's %d", hostIdx, hostNode.LatestNonce(), session.Nonce())
		hostRoot, err := hostNode.StateRoot()
		require.NoError(t, err)
		require.Equal(t, gatewayRoot, hostRoot, "host %d StateRoot() = %x, want the gateway's %x", hostIdx, hostRoot, gatewayRoot)
	}
}

// requireRequestsWithinTheHostLimit checks no request was refused and none carried more than a host accepts.
func requireRequestsWithinTheHostLimit(t *testing.T, requests hostRequestTotals) {
	t.Helper()
	require.Zero(t, requests.unmeasuredRequests, "requests without a Content-Length = %d, want 0", requests.unmeasuredRequests)
	require.Zero(t, requests.refusedCount, "requests a host refused = %d, want 0", requests.refusedCount)
	require.LessOrEqual(t, requests.largestBodyBytes, transport.DefaultMaxBodySize,
		"largest request body = %d bytes, want at most the host limit %d", requests.largestBodyBytes, transport.DefaultMaxBodySize)
}

// diffWindowWatch records the most diffs a session held in memory after any compose.
type diffWindowWatch struct {
	mostDiffsHeld atomic.Int64
}

func (watch *diffWindowWatch) attach(session *Session) {
	session.mu.Lock()
	defer session.mu.Unlock()
	session.diffObserver = func(types.Diff) {
		watch.mostDiffsHeld.Store(max(watch.mostDiffsHeld.Load(), int64(len(session.diffs))))
	}
}

func storedCatchUpInference() InferenceParams {
	return InferenceParams{
		Model: "llama", Prompt: testutil.TestPrompt,
		InputLength: 100, MaxTokens: testutil.TestMaxTokens, StartedAt: 1000,
	}
}

// serveOneInferencePerHost sends one inference to every host in turn, round robin from the next nonce.
func serveOneInferencePerHost(t *testing.T, session *Session) {
	t.Helper()
	for range len(session.group) {
		_, err := session.SendInference(context.Background(), storedCatchUpInference())
		require.NoError(t, err, "SendInference(nonce %d) = %v, want nil", session.Nonce(), err)
	}
}

// composeUnsentDiffs advances the session by count signed diffs that reach no host, so every host falls behind.
func composeUnsentDiffs(t *testing.T, session *Session, count int) {
	t.Helper()
	session.mu.Lock()
	defer session.mu.Unlock()
	for range count {
		_, _, err := session.composeDiffLocked(nil)
		require.NoError(t, err)
	}
}

// composeUntilHostIsNext composes unsent diffs until the next nonce is dispatched to hostIdx.
func composeUntilHostIsNext(t *testing.T, session *Session, hostIdx int) {
	t.Helper()
	for int((session.Nonce()+1)%uint64(len(session.group))) != hostIdx {
		composeUnsentDiffs(t, session, 1)
	}
}

func hostCursor(session *Session, hostIdx int) uint64 {
	session.mu.Lock()
	defer session.mu.Unlock()
	return session.hostSyncNonce[hostIdx]
}

// faultyDiffStore delays or fails the store's multi-nonce diff reads while armed; single-nonce reads pass.
type faultyDiffStore struct {
	storage.Storage
	mu           sync.Mutex
	delay        time.Duration
	failure      error
	readsFailed  int
	slowReadSeen chan struct{}
}

func (store *faultyDiffStore) arm(delay time.Duration, failure error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.delay = delay
	store.failure = failure
	store.slowReadSeen = make(chan struct{}, 1)
}

func (store *faultyDiffStore) disarm() {
	store.arm(0, nil)
}

func (store *faultyDiffStore) GetDiffs(escrowID string, fromNonce, toNonce uint64) ([]types.DiffRecord, error) {
	store.mu.Lock()
	delay, failure, slowReadSeen := store.delay, store.failure, store.slowReadSeen
	if failure != nil && toNonce > fromNonce {
		store.readsFailed++
	}
	store.mu.Unlock()
	if toNonce > fromNonce {
		if delay > 0 {
			select {
			case slowReadSeen <- struct{}{}:
			default:
			}
			time.Sleep(delay)
		}
		if failure != nil {
			return nil, failure
		}
	}
	return store.Storage.GetDiffs(escrowID, fromNonce, toNonce)
}

func (store *faultyDiffStore) failedReads() int {
	store.mu.Lock()
	defer store.mu.Unlock()
	return store.readsFailed
}

// longLagNonces is three times the gateway's nonce ceiling per escrow (19 800), and the smallest round
// count whose unsent diffs, about 196 wire bytes each, add up to more than the 10 MB a host accepts.
const longLagNonces = 60_000

func storedGapWireBytes(t *testing.T, store storage.Storage, fromNonce, toNonce uint64) int {
	t.Helper()
	records, err := store.GetDiffs("escrow-1", fromNonce, toNonce)
	require.NoError(t, err)
	diffs := make([]types.Diff, 0, len(records))
	for _, record := range records {
		diffs = append(diffs, record.Diff)
	}
	request, err := transport.HostRequestToJSON(host.HostRequest{Diffs: diffs, Nonce: toNonce})
	require.NoError(t, err)
	body, err := json.Marshal(request)
	require.NoError(t, err)
	return len(body)
}

// Test flow:
//  1. Serve one inference per host, then compose 60 000 signed diffs no host receives.
//  2. Restart the gateway: recover the session from its store over the same hosts.
//  3. Serve one inference per host: each is first taught the 60 000 diffs it lacks.
//  4. No request was refused or past the 10 MB host limit, though the gap alone is past it.
//  5. The session never held more than twice its diff window in memory.
//  6. Every host ends at the gateway's nonce and state root.
func TestRecoveredSession_CatchesUpHostsFarBehindWithinTheBodyLimit(t *testing.T) {
	fleet := newHTTPHostFleet(t, 3, 30*time.Second)
	store := newTestStore(t)
	session := fleet.openSession(t, store)
	window := &diffWindowWatch{}
	window.attach(session)
	serveOneInferencePerHost(t, session)
	laggingFrom := session.Nonce() + 1
	composeUnsentDiffs(t, session, longLagNonces)
	require.NoError(t, session.FlushSnapshot())
	gapBytes := storedGapWireBytes(t, store, laggingFrom, session.Nonce())

	recovered := fleet.recoverSession(t, store)
	window.attach(recovered)
	fleet.requests.reset()
	serveOneInferencePerHost(t, recovered)

	requests := fleet.requests.snapshot()
	t.Logf("gap %d wire bytes, %d requests, largest body %d bytes, host limit %d", gapBytes, requests.requestCount, requests.largestBodyBytes, transport.DefaultMaxBodySize)
	require.Greater(t, int64(gapBytes), transport.DefaultMaxBodySize, "the gap's wire bytes = %d, want past the host limit %d", gapBytes, transport.DefaultMaxBodySize)
	requireRequestsWithinTheHostLimit(t, requests)
	require.LessOrEqual(t, window.mostDiffsHeld.Load(), int64(2*defaultDiffsKeptInMemory),
		"most diffs held in memory = %d, want at most %d", window.mostDiffsHeld.Load(), 2*defaultDiffsKeptInMemory)
	require.LessOrEqual(t, len(recovered.Diffs()), 2*defaultDiffsKeptInMemory)
	fleet.requireHostsMatchTheGateway(t, recovered)
}

// Test flow:
//  1. Serve one inference per host, compose 60 000 unsent diffs, and catch every host up.
//  2. Replace host 1 with one holding nothing, as a host that lost its storage, and rewind its cursor.
//  3. Serve the next inference on host 1: it is taught the whole chain from nonce 1 first.
//  4. No request was refused or past the host limit, and every host matches the gateway's root.
func TestRewoundHost_IsTaughtTheWholeChainWithinTheBodyLimit(t *testing.T) {
	fleet := newHTTPHostFleet(t, 3, 30*time.Second)
	session := fleet.openSession(t, newTestStore(t))
	serveOneInferencePerHost(t, session)
	composeUnsentDiffs(t, session, longLagNonces)
	require.NoError(t, session.CatchUpAllHosts(context.Background()))

	fleet.startHost(t, 1)
	session.clients[1] = fleet.clients[1]
	require.True(t, session.RewindHostCatchUp(1, "host lost the escrow"))
	require.Zero(t, hostCursor(session, 1), "hostCursor(1) after the rewind = %d, want 0", hostCursor(session, 1))
	composeUntilHostIsNext(t, session, 1)
	fleet.requests.reset()
	_, err := session.SendInference(context.Background(), storedCatchUpInference())

	require.NoError(t, err, "SendInference(rewound host) = %v, want nil", err)
	requests := fleet.requests.snapshot()
	t.Logf("%d requests, largest body %d bytes, host limit %d", requests.requestCount, requests.largestBodyBytes, transport.DefaultMaxBodySize)
	requireRequestsWithinTheHostLimit(t, requests)
	require.Greater(t, requests.totalBodyBytes, transport.DefaultMaxBodySize, "bytes sent to the rewound host = %d, want past one body's limit", requests.totalBodyBytes)
	fleet.requireHostsMatchTheGateway(t, session)
}

// Test flow:
//  1. Serve one inference per host, compose two diff windows no host receives, and catch hosts 1 and 2 up.
//  2. Make every multi-nonce store read take two seconds.
//  3. Start an inference for host 0, whose gap only the store holds, and wait until its read begins.
//  4. Serve one inference each on hosts 1 and 2 meanwhile: each completes in under 200 ms.
//  5. The inference for host 0 completes once its read does, and every host matches the gateway's root.
func TestStoredCatchUp_DoesNotHoldTheSessionDuringAStoreRead(t *testing.T) {
	fleet := newHTTPHostFleet(t, 3, 30*time.Second)
	store := &faultyDiffStore{Storage: newTestStore(t)}
	session := fleet.openSession(t, store)
	serveOneInferencePerHost(t, session)
	composeUnsentDiffs(t, session, 2*defaultDiffsKeptInMemory)
	require.NoError(t, session.sendCatchUpWith(context.Background(), 1, fleet.clients[1]))
	require.NoError(t, session.sendCatchUpWith(context.Background(), 2, fleet.clients[2]))
	composeUntilHostIsNext(t, session, 0)
	store.arm(2*time.Second, nil)

	laggingHostDone := make(chan error, 1)
	go func() {
		_, err := session.SendInference(context.Background(), storedCatchUpInference())
		laggingHostDone <- err
	}()
	select {
	case <-store.slowReadSeen:
	case <-time.After(10 * time.Second):
		t.Fatal("the lagging host's store read never began")
	}
	var otherHostTimes []time.Duration
	for range 2 {
		started := time.Now()
		_, err := session.SendInference(context.Background(), storedCatchUpInference())
		otherHostTimes = append(otherHostTimes, time.Since(started))
		require.NoError(t, err)
	}
	laggingHostErr := <-laggingHostDone
	store.disarm()

	for hostOffset, elapsed := range otherHostTimes {
		require.Less(t, elapsed, 200*time.Millisecond, "SendInference(host %d) during the slow read took %s, want under 200ms", hostOffset+1, elapsed)
	}
	require.NoError(t, laggingHostErr, "SendInference(lagging host) = %v, want nil", laggingHostErr)
	fleet.requireHostsMatchTheGateway(t, session)
}

// timeoutGapNonces is a gap whose diffs fit one body, about 8.9 MB, so only the time a host takes to apply it
// in one request, not the body limit, would fail a single send.
const timeoutGapNonces = 45_000

// Test flow:
//  1. Give every host client an 800 ms request timeout.
//  2. Serve one inference per host, then compose 45 000 diffs no host receives, a gap that fits one body.
//  3. Serve one inference per host: each is first taught the 45 000 diffs it lacks.
//  4. Every request finished inside the timeout, though teaching host 0 took longer than it in all.
func TestStoredCatchUp_EachRequestFitsTheClientTimeout(t *testing.T) {
	queryTimeout := 800 * time.Millisecond
	fleet := newHTTPHostFleet(t, 3, queryTimeout)
	store := newTestStore(t)
	session := fleet.openSession(t, store)
	serveOneInferencePerHost(t, session)
	laggingFrom := session.Nonce() + 1
	composeUnsentDiffs(t, session, timeoutGapNonces)
	gapBytes := storedGapWireBytes(t, store, laggingFrom, session.Nonce())
	require.Less(t, int64(gapBytes), transport.DefaultMaxBodySize, "the gap's wire bytes = %d, want inside one body", gapBytes)
	fleet.requests.reset()

	serveOneInferencePerHost(t, session)

	requests := fleet.requests.snapshot()
	t.Logf("%d requests, longest %s, host 0 in all %s, timeout %s", requests.requestCount, requests.longestHandling, fleet.requests.handlingOf(0), queryTimeout)
	require.Less(t, requests.longestHandling, queryTimeout, "longest request = %s, want under the %s timeout", requests.longestHandling, queryTimeout)
	require.Greater(t, fleet.requests.handlingOf(0), queryTimeout, "teaching host 0 took %s in all, want past the %s timeout", fleet.requests.handlingOf(0), queryTimeout)
	requireRequestsWithinTheHostLimit(t, requests)
	fleet.requireHostsMatchTheGateway(t, session)
}

// Test flow:
//  1. Serve one inference per host, then compose two diff windows no host receives.
//  2. Make every multi-nonce store read fail, and serve an inference on host 0: it fails with the store's error.
//  3. Host 0 was sent nothing, and the gateway's root still equals the one its store journaled.
//  4. Let the store recover and serve one inference per host: each catches up and serves.
//  5. Every host matches the gateway's root, and finalize settles the escrow.
func TestStoredCatchUp_StoreReadFailureSendsNothingAndRecovers(t *testing.T) {
	fleet := newHTTPHostFleet(t, 3, 30*time.Second)
	store := &faultyDiffStore{Storage: newTestStore(t)}
	session := fleet.openSession(t, store)
	serveOneInferencePerHost(t, session)
	composeUnsentDiffs(t, session, 2*defaultDiffsKeptInMemory)
	composeUntilHostIsNext(t, session, 0)
	storeFailure := errors.New("injected store read failure")
	store.arm(0, storeFailure)
	hostNonceBefore := fleet.hosts[0].LatestNonce()
	fleet.requests.reset()

	_, err := session.SendInference(context.Background(), storedCatchUpInference())

	require.ErrorIs(t, err, storeFailure, "SendInference(store failing) = %v, want %v", err, storeFailure)
	require.Positive(t, store.failedReads())
	require.Zero(t, fleet.requests.snapshot().requestCount, "requests sent while the store failed = %d, want 0", fleet.requests.snapshot().requestCount)
	require.Equal(t, hostNonceBefore, fleet.hosts[0].LatestNonce())
	journaled, err := store.GetDiffs("escrow-1", session.Nonce(), session.Nonce())
	require.NoError(t, err)
	gatewayRoot, err := session.StateMachine().ComputeStateRoot()
	require.NoError(t, err)
	require.Equal(t, journaled[0].PostStateRoot, gatewayRoot)

	store.disarm()
	serveOneInferencePerHost(t, session)
	requireRequestsWithinTheHostLimit(t, fleet.requests.snapshot())
	fleet.requireHostsMatchTheGateway(t, session)
	require.NoError(t, session.Finalize(context.Background()))
	require.Equal(t, types.PhaseSettlement, session.StateMachine().Phase(), "Phase() after Finalize = %v, want settlement", session.StateMachine().Phase())
	require.True(t, session.HasQuorumAt(session.Nonce()), "HasQuorumAt(%d) = false, want a settled quorum", session.Nonce())
}

// Test flow:
//  1. Serve one inference per host, then compose two diff windows no host receives.
//  2. Send host 0 the next diff the way a heartbeat, a pending-diff flush and a timeout vote each do.
//  3. Host 0 reaches the diff's nonce with nothing refused; a vote carries the in-memory tail only.
func TestStoredCatchUp_EveryPathClosesTheGapBeforeItsDiff(t *testing.T) {
	sendPaths := map[string]func(t *testing.T, session *Session){
		"heartbeat": func(t *testing.T, session *Session) {
			session.mu.Lock()
			diff, hostIdx, err := session.composeDiffLocked(nil)
			session.mu.Unlock()
			require.NoError(t, err)
			require.NoError(t, session.sendComposedDiff(context.Background(), composedDiff{diff: diff, hostIdx: hostIdx}))
		},
		"pending diff": func(t *testing.T, session *Session) {
			require.NoError(t, session.SendPendingDiff(context.Background()))
		},
		"timeout vote": func(t *testing.T, session *Session) {
			catchUp := session.catchUpDiffsForVerifier(context.Background(), 0)
			session.mu.Lock()
			firstInMemory := session.diffs[0].Nonce
			session.mu.Unlock()
			requireContiguousDiffs(t, catchUp, firstInMemory, session.Nonce())
		},
	}
	for pathName, sendNextDiff := range sendPaths {
		t.Run(pathName, func(t *testing.T) {
			fleet := newHTTPHostFleet(t, 3, 30*time.Second)
			session := fleet.openSession(t, newTestStore(t))
			serveOneInferencePerHost(t, session)
			composeUnsentDiffs(t, session, 2*defaultDiffsKeptInMemory)
			composeUntilHostIsNext(t, session, 0)
			fleet.requests.reset()

			sendNextDiff(t, session)

			requireRequestsWithinTheHostLimit(t, fleet.requests.snapshot())
			session.mu.Lock()
			_, _, hasGap := session.storedGapLocked(0)
			session.mu.Unlock()
			require.False(t, hasGap, "host 0 still has a stored gap after the %s path", pathName)
			fleet.requireHostsMatchTheGateway(t, session)
		})
	}
}
