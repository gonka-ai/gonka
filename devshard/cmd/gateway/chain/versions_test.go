package chain

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"common/httpguard"

	"devshard/cmd/gateway/internal/leakcheck"
)

const capableNodeReply = `{"mlnodes":[{"node_id":"node-1","poc_validation_inference":true}]}`

// fakeClock is a mutex-guarded injectable clock so TTL/staleness tests never
// depend on the real wall clock.
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func newFakeClock(start time.Time) *fakeClock {
	return &fakeClock{now: start}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// newVersionsStub starts an httptest server serving a fixed /v1/versions
// response; it is closed automatically on test cleanup.
func newVersionsStub(t *testing.T, status int, body string) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		if body != "" {
			w.Write([]byte(body))
		}
	}))
	t.Cleanup(server.Close)
	return server
}

// Test flow:
//  1. Start a versions stub reporting one PoC-validation-capable node and a fake clock at time zero.
//  2. Create a VersionsCache with a one-minute TTL against the stub and register the candidate miner.
//  3. Poll the cache.
//  4. Assert the node reads as validation-capable within the TTL.
func TestVersionsCache_PollSucceeds_NodeCapableWithinTTL(t *testing.T) {
	server := newVersionsStub(t, http.StatusOK, capableNodeReply)
	clock := newFakeClock(time.Unix(0, 0))
	cache := NewVersionsCache(server.Client(), time.Minute, clock.Now)
	cache.SetCandidates(map[string]string{"miner-1": server.URL})

	cache.Poll(context.Background())

	if !cache.IsNodeValidationCapable("miner-1", "node-1") {
		t.Fatal("capable node within ttl should be validation-capable")
	}
}

// Test flow:
//  1. Create a VersionsCache with a fake clock and no registered candidates.
//  2. Assert IsNodeValidationCapable for a miner that was never polled returns false (fails closed).
func TestVersionsCache_UnknownMiner_FailsClosed(t *testing.T) {
	clock := newFakeClock(time.Unix(0, 0))
	cache := NewVersionsCache(http.DefaultClient, time.Minute, clock.Now)

	if cache.IsNodeValidationCapable("never-a-candidate", "node-1") {
		t.Fatal("miner never polled should fail closed")
	}
}

// Test flow:
//  1. Start a versions stub returning HTTP 500 and a fake clock at time zero.
//  2. Create a VersionsCache against the stub and register the candidate miner.
//  3. Poll the cache.
//  4. Assert the node reads as not validation-capable (fails closed) after the failed poll.
func TestVersionsCache_ServerError_FailsClosed(t *testing.T) {
	server := newVersionsStub(t, http.StatusInternalServerError, "")
	clock := newFakeClock(time.Unix(0, 0))
	cache := NewVersionsCache(server.Client(), time.Minute, clock.Now)
	cache.SetCandidates(map[string]string{"miner-1": server.URL})

	cache.Poll(context.Background())

	if cache.IsNodeValidationCapable("miner-1", "node-1") {
		t.Fatal("500 response should fail closed")
	}
}

// Test flow:
//  1. Start a versions stub reporting one node with poc_validation_inference=false, and a fake clock.
//  2. Create a VersionsCache against the stub and register the candidate miner.
//  3. Poll the cache.
//  4. Assert the node reads as not validation-capable.
func TestVersionsCache_NodeExplicitlyIncapable_ReturnsFalse(t *testing.T) {
	server := newVersionsStub(t, http.StatusOK, `{"mlnodes":[{"node_id":"node-1","poc_validation_inference":false}]}`)
	clock := newFakeClock(time.Unix(0, 0))
	cache := NewVersionsCache(server.Client(), time.Minute, clock.Now)
	cache.SetCandidates(map[string]string{"miner-1": server.URL})

	cache.Poll(context.Background())

	if cache.IsNodeValidationCapable("miner-1", "node-1") {
		t.Fatal("node explicitly flagged poc_validation_inference=false should not be capable")
	}
}

// Test flow:
//  1. Start a versions stub reporting one capable node and a fake clock at time zero.
//  2. Create a VersionsCache with a one-minute TTL, register the candidate miner, and poll.
//  3. Assert the node is capable before the TTL elapses (precondition).
//  4. Advance the clock past the TTL.
//  5. Assert the node now reads as not validation-capable (fails closed).
func TestVersionsCache_EntryOlderThanTTL_FailsClosed(t *testing.T) {
	server := newVersionsStub(t, http.StatusOK, capableNodeReply)
	clock := newFakeClock(time.Unix(0, 0))
	cache := NewVersionsCache(server.Client(), time.Minute, clock.Now)
	cache.SetCandidates(map[string]string{"miner-1": server.URL})
	cache.Poll(context.Background())
	if !cache.IsNodeValidationCapable("miner-1", "node-1") {
		t.Fatal("precondition: node should be capable before the ttl elapses")
	}

	clock.Advance(2 * time.Minute)

	if cache.IsNodeValidationCapable("miner-1", "node-1") {
		t.Fatal("entry older than ttl should fail closed")
	}
}

// Test flow:
//  1. Start a versions stub reporting one capable node and a fake clock, register the candidate miner, and poll.
//  2. Assert the node is capable before its miner is removed (precondition).
//  3. Call SetCandidates again without that miner.
//  4. Assert the node now reads as not validation-capable even though its cached entry is still fresh.
func TestVersionsCache_SetCandidates_DropsRemovedMiner(t *testing.T) {
	server := newVersionsStub(t, http.StatusOK, capableNodeReply)
	clock := newFakeClock(time.Unix(0, 0))
	cache := NewVersionsCache(server.Client(), time.Minute, clock.Now)
	cache.SetCandidates(map[string]string{"miner-1": server.URL})
	cache.Poll(context.Background())
	if !cache.IsNodeValidationCapable("miner-1", "node-1") {
		t.Fatal("precondition: node should be capable before its miner is removed")
	}

	cache.SetCandidates(map[string]string{"miner-2": server.URL})

	if cache.IsNodeValidationCapable("miner-1", "node-1") {
		t.Fatal("miner dropped from candidates must fail closed even with a cached entry")
	}
}

// Test flow:
//  1. Register a leak check deferred first so it runs last, after the server and its client connections are closed.
//  2. Start a versions stub server, a fake clock, and a VersionsCache with one candidate miner.
//  3. Start Run in a goroutine against a cancellable context, then cancel the context.
//  4. Assert Run's goroutine exits within 2 seconds of the cancellation.
func TestVersionsCache_Run_ExitsOnContextCancel(t *testing.T) {
	defer leakcheck.VerifyNone(t)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(capableNodeReply))
	}))
	defer server.Close()

	clock := newFakeClock(time.Unix(0, 0))
	cache := NewVersionsCache(server.Client(), time.Minute, clock.Now)
	cache.SetCandidates(map[string]string{"miner-1": server.URL})

	ctx, cancel := context.WithCancel(context.Background())
	runDone := make(chan struct{})
	go func() {
		defer close(runDone)
		cache.Run(ctx, time.Millisecond)
	}()

	cancel()

	select {
	case <-runDone:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not exit within 2s of context cancellation")
	}
}

// Test flow:
//  1. Start a server whose handler reports arrival then blocks until released, and register a release-once deferred before server.Close so it runs first and a failed assertion can't leave handlers parked blocking Close.
//  2. Create a VersionsCache with several candidate miners all pointing at that server.
//  3. Start Poll in a goroutine.
//  4. Within a deadline shorter than one fetch's timeout, wait for every candidate's request to arrive, asserting they could not all have arrived if fetches were serialized one per timeout.
//  5. Release all handlers and assert Poll returns.
func TestVersionsPollFetchesConcurrently(t *testing.T) {
	defer leakcheck.VerifyNone(t)

	const candidateCount = 8
	arrived := make(chan struct{}, candidateCount)
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		arrived <- struct{}{}
		<-release
		w.Write([]byte(`{"ml_nodes":[]}`))
	}))
	defer server.Close()
	var releaseOnce sync.Once
	releaseAll := func() { releaseOnce.Do(func() { close(release) }) }
	defer releaseAll()

	clock := &fakeClock{now: time.Unix(0, 0)}
	cache := NewVersionsCache(server.Client(), time.Minute, clock.Now)
	candidates := make(map[string]string, candidateCount)
	for i := range candidateCount {
		candidates[fmt.Sprintf("miner-%d", i)] = server.URL
	}
	cache.SetCandidates(candidates)

	polled := make(chan struct{})
	go func() { defer close(polled); cache.Poll(context.Background()) }()

	allStarted := time.After(versionsFetchTimeout / 2)
	for i := range candidateCount {
		select {
		case <-arrived:
		case <-allStarted:
			t.Fatalf("only %d of %d fetches had started within %v; the pass is serialized", i, candidateCount, versionsFetchTimeout/2)
		}
	}
	releaseAll()

	select {
	case <-polled:
	case <-time.After(3 * time.Second):
		t.Fatal("Poll did not return after every fetch was released")
	}
}

// Test flow:
//  1. Start a loopback stub that counts hits, build an observer whose chain API client is the stub's unguarded client the versions poll used to share, and close the dial guard.
//  2. Register one miner by the stub's IP and one by the host name localhost as versions candidates.
//  3. Poll the observer's versions cache.
//  4. Assert the stub was never reached.
func TestTheVersionsPollDoesNotDialAMinerOnAPrivateAddress(t *testing.T) {
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.Write([]byte(`{"mlnodes":[]}`))
	}))
	t.Cleanup(server.Close)
	observer := newPoCPhaseObserver(t, server, nil)
	previous := httpguard.AllowPrivate()
	httpguard.SetAllowPrivate(false)
	t.Cleanup(func() { httpguard.SetAllowPrivate(previous) })
	byName := "http://localhost:" + strings.TrimPrefix(server.URL, "http://127.0.0.1:")
	observer.versions.SetCandidates(map[string]string{"miner-by-ip": server.URL, "miner-by-name": byName})

	observer.versions.Poll(t.Context())

	if got := hits.Load(); got != 0 {
		t.Fatalf("hits on the private miner = %d, want 0", got)
	}
}

// Test flow:
//  1. Start versions stubs that answer a body past the size limit, and a body malformed in the shape that makes goccy read past its buffer.
//  2. Poll a cache against each.
//  3. Assert the node reads as not validation-capable and the poll returns: a miner's reply cannot grow the gateway's memory without bound or crash its poller.
func TestVersionsCache_AnOversizedOrMalformedReply_FailsClosed(t *testing.T) {
	oversized := `{"mlnodes":[{"node_id":"node-1","poc_validation_inference":true}],"padding":"` + strings.Repeat("x", versionsResponseMaxBytes) + `"}`
	malformed := `{"mlnodes":1,"` + "\\0\x05\\"
	for name, body := range map[string]string{"oversized": oversized, "malformed": malformed} {
		t.Run(name, func(t *testing.T) {
			server := newVersionsStub(t, http.StatusOK, body)
			cache := NewVersionsCache(server.Client(), time.Minute, newFakeClock(time.Unix(0, 0)).Now)
			cache.SetCandidates(map[string]string{"miner-1": server.URL})

			cache.Poll(context.Background())

			if cache.IsNodeValidationCapable("miner-1", "node-1") {
				t.Fatalf("a %s reply should fail closed", name)
			}
		})
	}
}

// Test flow:
//  1. Build a VersionsCache over in-memory hosts with a fake clock, a 45-second TTL and one candidate miner.
//  2. Poll while the host answers 200 with a capable node and assert the node is capable.
//  3. Make the host answer 503, advance the clock 30 seconds, poll again, and assert the node is still capable.
//  4. Advance the clock past the TTL of the last successful fetch and assert the node reads as not capable.
func TestVersionsCache_AFailedFetchKeepsTheLastGoodAnswerUntilTTL(t *testing.T) {
	hosts := newVersionsHosts()
	clock := newFakeClock(time.Unix(0, 0))
	cache := NewVersionsCache(&http.Client{Transport: hosts}, 45*time.Second, clock.Now)
	cache.SetCandidates(map[string]string{"miner-1": "http://miner-1.invalid"})
	cache.Poll(t.Context())
	if !cache.IsNodeValidationCapable("miner-1", "node-1") {
		t.Fatal("IsNodeValidationCapable(miner-1, node-1) after a successful fetch = false, want true")
	}

	hosts.answer("miner-1.invalid", http.StatusServiceUnavailable)
	clock.Advance(30 * time.Second)
	cache.Poll(t.Context())
	if !cache.IsNodeValidationCapable("miner-1", "node-1") {
		t.Fatal("IsNodeValidationCapable(miner-1, node-1) after a failed fetch within ttl = false, want true")
	}

	clock.Advance(16 * time.Second)
	if cache.IsNodeValidationCapable("miner-1", "node-1") {
		t.Fatal("IsNodeValidationCapable(miner-1, node-1) past the ttl of the last good answer = true, want false")
	}
}

// versionsHosts answers /v1/versions in memory per host, counts requests, and holds a host's reply while its gate is open.
type versionsHosts struct {
	mu       sync.Mutex
	statuses map[string]int
	hits     map[string]int
	gates    map[string]chan struct{}
}

func newVersionsHosts() *versionsHosts {
	return &versionsHosts{statuses: map[string]int{}, hits: map[string]int{}, gates: map[string]chan struct{}{}}
}

func (h *versionsHosts) RoundTrip(request *http.Request) (*http.Response, error) {
	h.mu.Lock()
	host := request.URL.Host
	h.hits[host]++
	status, hasStatus := h.statuses[host]
	gate := h.gates[host]
	h.mu.Unlock()
	if !hasStatus {
		status = http.StatusOK
	}
	if gate != nil {
		select {
		case <-gate:
		case <-request.Context().Done():
			return nil, request.Context().Err()
		}
	}
	return &http.Response{StatusCode: status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(capableNodeReply)), Request: request}, nil
}

func (h *versionsHosts) hitsOf(host string) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.hits[host]
}

func (h *versionsHosts) answer(host string, status int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.statuses[host] = status
}

func (h *versionsHosts) hold(host string) chan struct{} {
	h.mu.Lock()
	defer h.mu.Unlock()
	gate := make(chan struct{})
	h.gates[host] = gate
	return gate
}

// runVersionsCache runs cache.Run until the test ends.
func runVersionsCache(t *testing.T, cache *VersionsCache, interval time.Duration) {
	ctx, cancel := context.WithCancel(t.Context())
	runDone := make(chan struct{})
	go func() {
		defer close(runDone)
		cache.Run(ctx, interval)
	}()
	t.Cleanup(func() { cancel(); <-runDone })
}

// Test flow:
//  1. In a synctest bubble, build a VersionsCache over in-memory hosts and set one candidate miner before Run starts.
//  2. Run it with an hour-long interval and wait until it is idle.
//  3. Assert the miner was fetched once and its node reads as capable, long before the first tick.
func TestVersionsCache_Run_PollsTheCandidatesSetBeforeItStartsWithoutWaitingForATick(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		hosts := newVersionsHosts()
		cache := NewVersionsCache(&http.Client{Transport: hosts}, time.Hour, time.Now)
		cache.SetCandidates(map[string]string{"miner-1": "http://miner-1.invalid"})

		runVersionsCache(t, cache, time.Hour)
		synctest.Wait()

		if got := hosts.hitsOf("miner-1.invalid"); got != 1 {
			t.Errorf("hits(miner-1) = %d, want 1", got)
		}
		if !cache.IsNodeValidationCapable("miner-1", "node-1") {
			t.Error("IsNodeValidationCapable(miner-1, node-1) = false, want true")
		}
	})
}

// Test flow:
//  1. In a synctest bubble, Run a VersionsCache over in-memory hosts with an hour-long interval and one candidate miner, and wait for its start poll.
//  2. Call SetCandidates with that miner and a new one, twice, and wait until Run is idle.
//  3. Assert the new miner was fetched exactly once and reads as capable, and the first miner was not fetched again.
func TestVersionsCache_SetCandidates_PollsOnlyANewMinerAndOnlyOnce(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		hosts := newVersionsHosts()
		cache := NewVersionsCache(&http.Client{Transport: hosts}, time.Hour, time.Now)
		cache.SetCandidates(map[string]string{"miner-1": "http://miner-1.invalid"})
		runVersionsCache(t, cache, time.Hour)
		synctest.Wait()

		both := map[string]string{"miner-1": "http://miner-1.invalid", "miner-2": "http://miner-2.invalid"}
		cache.SetCandidates(both)
		cache.SetCandidates(both)
		synctest.Wait()

		if got := hosts.hitsOf("miner-2.invalid"); got != 1 {
			t.Errorf("hits(miner-2) = %d, want 1", got)
		}
		if !cache.IsNodeValidationCapable("miner-2", "node-1") {
			t.Error("IsNodeValidationCapable(miner-2, node-1) = false, want true")
		}
		if got := hosts.hitsOf("miner-1.invalid"); got != 1 {
			t.Errorf("hits(miner-1) = %d, want 1", got)
		}
	})
}

// Test flow:
//  1. In a synctest bubble, Run a VersionsCache over in-memory hosts with an hour-long interval and one capable miner, and wait for its first poll.
//  2. Make the miner's new host answer 503, move the miner to it with SetCandidates, and wait until Run is idle.
//  3. Assert the new host was fetched once, the old one not again, and the miner reads as not capable: the old host's answer went with its URL.
func TestVersionsCache_AMinerMovedToANewURLIsPolledThereRightAwayAndForgetsItsOldAnswer(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		hosts := newVersionsHosts()
		cache := NewVersionsCache(&http.Client{Transport: hosts}, time.Hour, time.Now)
		cache.SetCandidates(map[string]string{"miner-1": "http://old-host.invalid"})
		runVersionsCache(t, cache, time.Hour)
		synctest.Wait()

		hosts.answer("new-host.invalid", http.StatusServiceUnavailable)
		cache.SetCandidates(map[string]string{"miner-1": "http://new-host.invalid"})
		synctest.Wait()

		if got := hosts.hitsOf("new-host.invalid"); got != 1 {
			t.Errorf("hits(new-host) = %d, want 1", got)
		}
		if got := hosts.hitsOf("old-host.invalid"); got != 1 {
			t.Errorf("hits(old-host) = %d, want 1", got)
		}
		if cache.IsNodeValidationCapable("miner-1", "node-1") {
			t.Error("IsNodeValidationCapable(miner-1, node-1) = true, want false")
		}
	})
}

// Test flow:
//  1. In a synctest bubble, build a VersionsCache over in-memory hosts with one candidate miner whose reply is held.
//  2. Start Poll, wait until the fetch is in flight, and remove the miner with SetCandidates.
//  3. Release the reply, wait for Poll to return, and add the miner back.
//  4. Assert the miner reads as not capable: the answer fetched before its removal was not stored.
func TestVersionsCache_AFetchInFlightWhenItsMinerIsRemovedStoresNothing(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		hosts := newVersionsHosts()
		gate := hosts.hold("miner-1.invalid")
		cache := NewVersionsCache(&http.Client{Transport: hosts}, time.Hour, time.Now)
		candidates := map[string]string{"miner-1": "http://miner-1.invalid"}
		cache.SetCandidates(candidates)

		polled := make(chan struct{})
		go func() {
			defer close(polled)
			cache.Poll(t.Context())
		}()
		synctest.Wait()
		cache.SetCandidates(map[string]string{})
		close(gate)
		<-polled
		cache.SetCandidates(candidates)

		if cache.IsNodeValidationCapable("miner-1", "node-1") {
			t.Error("IsNodeValidationCapable(miner-1, node-1) = true, want false")
		}
	})
}
