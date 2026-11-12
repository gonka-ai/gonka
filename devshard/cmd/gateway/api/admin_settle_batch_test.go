package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"devshard/cmd/gateway/chain"
	"devshard/cmd/gateway/escrow"
	"devshard/cmd/gateway/store"
)

type batchSettleResult struct {
	EscrowID string `json:"escrow_id"`
	TxHash   string `json:"tx_hash"`
	Settler  string `json:"settler"`
	Status   int    `json:"status"`
	Error    string `json:"error"`
}

type batchSettleAnswer struct {
	Settled int
	Failed  int
	Results []batchSettleResult
}

// decodeBatchSettle reads one result per line and the closing {settled, failed} line.
func decodeBatchSettle(t *testing.T, body []byte) batchSettleAnswer {
	t.Helper()
	lines := strings.Split(strings.TrimSpace(string(body)), "\n")
	var summary struct {
		Settled int `json:"settled"`
		Failed  int `json:"failed"`
	}
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &summary); err != nil {
		t.Fatalf("decoding the summary line of %s: %v", body, err)
	}
	answer := batchSettleAnswer{Settled: summary.Settled, Failed: summary.Failed}
	for _, line := range lines[:len(lines)-1] {
		var result batchSettleResult
		if err := json.Unmarshal([]byte(line), &result); err != nil {
			t.Fatalf("decoding result line %s: %v", line, err)
		}
		answer.Results = append(answer.Results, result)
	}
	return answer
}

// flushProbe hands the test the body written so far at every flush, the way a streaming client would see it.
type flushProbe struct {
	*httptest.ResponseRecorder
	flushed chan string
}

func newFlushProbe() *flushProbe {
	return &flushProbe{ResponseRecorder: httptest.NewRecorder(), flushed: make(chan string, 64)}
}

func (p *flushProbe) Flush() {
	p.ResponseRecorder.Flush()
	select {
	case p.flushed <- p.Body.String():
	default:
	}
}

func waitForFlushedLine(t *testing.T, probe *flushProbe, fragment string) {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case body := <-probe.flushed:
			if strings.Contains(body, fragment) {
				return
			}
		case <-deadline:
			t.Fatalf("no flushed line carried %s before the batch finished", fragment)
		}
	}
}

// Test flow:
//  1. Register two devshards (escrows "7" and "9"), mark "9" busy, and set the settle result for escrow 7.
//  2. POST a batch settle for escrows "7", "9", and an unregistered "404".
//  3. Assert the response is 200 with settled=1 and failed=2 across 3 results.
//  4. Assert escrow 7's result carries its transaction hash and settler with no failure status.
//  5. Assert escrow 9 answers 409 (busy) and escrow 404 answers 404 (unregistered).
func TestABatchSettleAnswersForEveryEscrowItWasGiven(t *testing.T) {
	live := newHarness(t)
	live.control.devshards = []store.DevshardRecord{
		{EscrowID: "7", Model: "qwen"},
		{EscrowID: "9", Model: "qwen"},
	}
	live.escrows.busy["9"] = true
	live.operations.settle = chain.SettleEscrowResult{EscrowID: 7, TxHash: "TX7", Settler: "gonka1settler"}

	response := live.request(t, http.MethodPost, "/v1/admin/devshards/settle",
		`{"escrow_ids":["7","9","404"]}`, adminHeaders())

	if response.Code != http.StatusOK {
		t.Fatalf("status: got %d (%s), want 200", response.Code, response.Body.String())
	}
	answer := decodeBatchSettle(t, response.Body.Bytes())
	if answer.Settled != 1 || answer.Failed != 2 {
		t.Fatalf("counts: got settled=%d failed=%d, want 1 and 2", answer.Settled, answer.Failed)
	}
	if len(answer.Results) != 3 {
		t.Fatalf("results: got %d entries, want 3", len(answer.Results))
	}
	byEscrow := map[string]batchSettleResult{}
	for _, result := range answer.Results {
		byEscrow[result.EscrowID] = result
	}
	if settled := byEscrow["7"]; settled.TxHash != "TX7" || settled.Settler != "gonka1settler" {
		t.Fatalf("the settled escrow lost its transaction: %+v", settled)
	}
	if byEscrow["7"].Status != 0 {
		t.Fatalf("a settled escrow carries a failure status: %d", byEscrow["7"].Status)
	}
	if byEscrow["9"].Status != http.StatusConflict {
		t.Fatalf("a draining escrow: got %d, want 409", byEscrow["9"].Status)
	}
	if byEscrow["404"].Status != http.StatusNotFound {
		t.Fatalf("an unregistered escrow: got %d, want 404", byEscrow["404"].Status)
	}
}

// Test flow:
//  1. Register escrow "7" and make its settle report that the chain pruned it.
//  2. POST a single settle and a batch settle for it.
//  3. Assert both answer 410 Gone rather than a not-found failure.
func TestASettleOfAnEscrowTheChainPrunedAnswersGone(t *testing.T) {
	live := newHarness(t)
	live.control.devshards = []store.DevshardRecord{{EscrowID: "7", Model: "qwen"}}
	live.operations.err = fmt.Errorf("settling escrow 7: %w", escrow.ErrEscrowPruned)

	single := live.request(t, http.MethodPost, "/v1/admin/devshards/7/settle", "", adminHeaders())
	batch := live.request(t, http.MethodPost, "/v1/admin/devshards/settle", `{"escrow_ids":["7"]}`, adminHeaders())

	if single.Code != http.StatusGone {
		t.Fatalf("single settle: got %d (%s), want 410", single.Code, single.Body.String())
	}
	if answer := decodeBatchSettle(t, batch.Body.Bytes()); answer.Results[0].Status != http.StatusGone {
		t.Fatalf("batch settle: got %+v, want status 410", answer.Results[0])
	}
}

// settleBarrierGrace lets a sequential run finish and be judged on its peak rather than hang.
const settleBarrierGrace = 200 * time.Millisecond

// settleBarrier tracks how many settles are in flight at once, to tell a parallel run from a sequential one.
type settleBarrier struct {
	mu        sync.Mutex
	inFlight  int
	peak      int
	target    int
	reached   chan struct{}
	closeOnce sync.Once
}

func newSettleBarrier(target int) *settleBarrier {
	return &settleBarrier{target: target, reached: make(chan struct{})}
}

func (b *settleBarrier) hold() {
	b.mu.Lock()
	b.inFlight++
	if b.inFlight > b.peak {
		b.peak = b.inFlight
	}
	if b.inFlight >= b.target {
		b.closeOnce.Do(func() { close(b.reached) })
	}
	b.mu.Unlock()

	select {
	case <-b.reached:
	case <-time.After(settleBarrierGrace):
	}

	b.mu.Lock()
	b.inFlight--
	b.mu.Unlock()
}

func (b *settleBarrier) peakInFlight() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.peak
}

func registeredDevshardRows(escrowIDs ...string) []store.DevshardRecord {
	records := make([]store.DevshardRecord, 0, len(escrowIDs))
	for _, escrowID := range escrowIDs {
		records = append(records, store.DevshardRecord{EscrowID: escrowID, Model: "qwen"})
	}
	return records
}

// Test flow:
//  1. Register 4 devshards and wire a `settleBarrier` that tracks concurrent settles into the operations' onSettle hook.
//  2. POST a batch settle naming all 4 escrows.
//  3. Assert the response is 200.
//  4. Assert the barrier's peak in-flight count reached 4, so the batch settled them concurrently rather than one after another.
func TestABatchSettlesItsEscrowsAtTheSameTime(t *testing.T) {
	live := newHarness(t)
	live.control.devshards = registeredDevshardRows("1", "2", "3", "4")
	barrier := newSettleBarrier(4)
	live.operations.onSettle = func(string) { barrier.hold() }

	response := live.request(t, http.MethodPost, "/v1/admin/devshards/settle",
		`{"escrow_ids":["1","2","3","4"]}`, adminHeaders())

	if response.Code != http.StatusOK {
		t.Fatalf("status: got %d (%s), want 200", response.Code, response.Body.String())
	}
	if peak := barrier.peakInFlight(); peak != 4 {
		t.Fatalf("settles in flight at once: got %d, want 4 -- the batch is settling one escrow after another", peak)
	}
}

// Test flow:
//  1. Register 6 devshards and wire the same concurrency-tracking barrier.
//  2. POST a batch settle naming all 6 escrows.
//  3. Assert the response is 200 and the peak in-flight count never exceeded `defaultBatchSize`.
//  4. Assert all 6 escrows were reported settled, so the pool queues the rest rather than dropping them.
func TestABatchKeepsItsSettlementsWithinThePool(t *testing.T) {
	live := newHarness(t)
	escrowIDs := []string{"1", "2", "3", "4", "5", "6"}
	live.control.devshards = registeredDevshardRows(escrowIDs...)
	barrier := newSettleBarrier(len(escrowIDs))
	live.operations.onSettle = func(string) { barrier.hold() }

	response := live.request(t, http.MethodPost, "/v1/admin/devshards/settle",
		`{"escrow_ids":["1","2","3","4","5","6"]}`, adminHeaders())

	if response.Code != http.StatusOK {
		t.Fatalf("status: got %d (%s), want 200", response.Code, response.Body.String())
	}
	if peak := barrier.peakInFlight(); peak > defaultBatchSize {
		t.Fatalf("settles in flight at once: got %d, want at most %d", peak, defaultBatchSize)
	}
	if answer := decodeBatchSettle(t, response.Body.Bytes()); answer.Settled != len(escrowIDs) {
		t.Fatalf("settled: got %d, want %d -- the pool must queue the rest, not drop them", answer.Settled, len(escrowIDs))
	}
}

// Test flow:
//  1. Register a busy escrow "9" and set its settle result.
//  2. POST a batch settle for it with `force: true` in the body, next to `batch_size`.
//  3. Assert the escrow was settled and its result carries the expected transaction hash.
func TestABatchBodyForceCrossesTheBusyCheck(t *testing.T) {
	live := newHarness(t)
	live.control.devshards = registeredDevshardRows("9")
	live.escrows.busy["9"] = true
	live.operations.settle = chain.SettleEscrowResult{EscrowID: 9, TxHash: "TX9"}

	response := live.request(t, http.MethodPost, "/v1/admin/devshards/settle",
		`{"escrow_ids":["9"],"batch_size":2,"force":true}`, adminHeaders())

	answer := decodeBatchSettle(t, response.Body.Bytes())
	if answer.Settled != 1 || len(answer.Results) != 1 || answer.Results[0].TxHash != "TX9" {
		t.Fatalf("a forced settle: got %+v, want escrow 9 settled", answer)
	}
}

// Test flow:
//  1. Register a busy escrow "9" and set its settle result.
//  2. POST a batch settle for it with `force=true` as a query parameter.
//  3. Assert the escrow was settled and its result carries the expected transaction hash.
func TestAForcedBatchCrossesTheBusyCheck(t *testing.T) {
	live := newHarness(t)
	live.control.devshards = registeredDevshardRows("9")
	live.escrows.busy["9"] = true
	live.operations.settle = chain.SettleEscrowResult{EscrowID: 9, TxHash: "TX9"}

	response := live.request(t, http.MethodPost, "/v1/admin/devshards/settle?force=true",
		`{"escrow_ids":["9"]}`, adminHeaders())

	answer := decodeBatchSettle(t, response.Body.Bytes())
	if answer.Settled != 1 || len(answer.Results) != 1 || answer.Results[0].TxHash != "TX9" {
		t.Fatalf("a forced settle: got %+v, want escrow 9 settled", answer)
	}
}

// Test flow:
//  1. Register escrow "7".
//  2. POST a batch settle naming escrow "7" three times, with whitespace variants.
//  3. Assert the response carries exactly one result.
//  4. Assert exactly one "settle" call was recorded.
func TestABatchSettlesARepeatedEscrowOnce(t *testing.T) {
	live := newHarness(t)
	live.control.devshards = registeredDevshardRows("7")

	response := live.request(t, http.MethodPost, "/v1/admin/devshards/settle",
		`{"escrow_ids":["7"," 7 ","7"]}`, adminHeaders())

	answer := decodeBatchSettle(t, response.Body.Bytes())
	if len(answer.Results) != 1 {
		t.Fatalf("results: got %+v, want one entry", answer.Results)
	}
	if settles := slices.Contains(live.operations.recordedCalls(), "settle"); !settles {
		t.Fatal("the escrow was never settled")
	}
	if got := len(live.operations.recordedCalls()); got != 1 {
		t.Fatalf("settle calls: got %d, want 1", got)
	}
}

// Test flow:
//  1. Build an oversized escrow-ID list one entry past `batchEscrowLimit`.
//  2. Define a table of request bodies, varying across no body at all, an empty list, blanks only, the oversized list, and a batch size outside 0..`maxBatchSize`.
//  3. For each case, POST it to the batch settle route on a fresh harness.
//  4. Assert the response is 400.
//  5. Assert no "settle" call was recorded, so a refused list never reaches the settle path.
func TestABatchRefusesAListItCannotAnswerFor(t *testing.T) {
	oversized := make([]string, batchEscrowLimit+1)
	for index := range oversized {
		oversized[index] = strconv.Itoa(index)
	}
	oversizedBody, err := json.Marshal(SettleDevshardsRequest{EscrowIDs: oversized})
	if err != nil {
		t.Fatalf("building the oversized body: %v", err)
	}
	testCases := []struct {
		name string
		body string
	}{
		{name: "no body at all", body: ""},
		{name: "an empty list", body: `{"escrow_ids":[]}`},
		{name: "blanks only", body: `{"escrow_ids":["  "]}`},
		{name: "more escrows than one call settles", body: string(oversizedBody)},
		{name: "a batch size past the ceiling", body: `{"escrow_ids":["7"],"batch_size":` + strconv.Itoa(maxBatchSize+1) + `}`},
		{name: "a negative batch size", body: `{"escrow_ids":["7"],"batch_size":-1}`},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			live := newHarness(t)
			response := live.request(t, http.MethodPost, "/v1/admin/devshards/settle", testCase.body, adminHeaders())
			if response.Code != http.StatusBadRequest {
				t.Fatalf("status: got %d (%s), want 400", response.Code, response.Body.String())
			}
			if calls := live.operations.recordedCalls(); len(calls) != 0 {
				t.Fatalf("a refused list reached the settle path: %v", calls)
			}
		})
	}
}

// Test flow:
//  1. Register escrows "1" and "2", and hold escrow 2's settle until the test lets it go.
//  2. POST a batch settle for both through a writer that reports every flush.
//  3. Assert escrow 1's result line was flushed while escrow 2 was still settling.
//  4. Release escrow 2 and assert the response is NDJSON that ends with settled=2.
func TestABatchStreamsEachResultAsItsEscrowSettles(t *testing.T) {
	live := newHarness(t)
	live.control.devshards = registeredDevshardRows("1", "2")
	release := make(chan struct{})
	var releaseOnce sync.Once
	letGo := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(letGo)
	live.operations.onSettle = func(escrowID string) {
		if escrowID == "2" {
			<-release
		}
	}
	probe := newFlushProbe()
	finished := make(chan struct{})

	go func() {
		defer close(finished)
		live.requestInto(t, probe, http.MethodPost, "/v1/admin/devshards/settle", `{"escrow_ids":["1","2"]}`, adminHeaders())
	}()
	waitForFlushedLine(t, probe, `"escrow_id":"1"`)
	letGo()
	<-finished

	if contentType := probe.Header().Get("Content-Type"); contentType != "application/x-ndjson" {
		t.Fatalf("content type: got %q, want application/x-ndjson", contentType)
	}
	if answer := decodeBatchSettle(t, probe.Body.Bytes()); answer.Settled != 2 || len(answer.Results) != 2 {
		t.Fatalf("a streamed batch: got %+v, want both escrows settled", answer)
	}
}

// Test flow:
//  1. Register 6 devshards and wire a barrier that only opens at 3 settles in flight.
//  2. POST a batch settle naming all 6 with `batch_size` 2.
//  3. Assert the peak in-flight count was exactly 2, so the caller's size replaced the default of 4.
//  4. Assert all 6 escrows were reported settled.
func TestABatchSettlesNoMoreAtOnceThanItsBatchSize(t *testing.T) {
	live := newHarness(t)
	live.control.devshards = registeredDevshardRows("1", "2", "3", "4", "5", "6")
	barrier := newSettleBarrier(3)
	live.operations.onSettle = func(string) { barrier.hold() }

	response := live.request(t, http.MethodPost, "/v1/admin/devshards/settle",
		`{"escrow_ids":["1","2","3","4","5","6"],"batch_size":2}`, adminHeaders())

	if response.Code != http.StatusOK {
		t.Fatalf("status: got %d (%s), want 200", response.Code, response.Body.String())
	}
	if peak := barrier.peakInFlight(); peak != 2 {
		t.Fatalf("settles in flight at once: got %d, want 2", peak)
	}
	if answer := decodeBatchSettle(t, response.Body.Bytes()); answer.Settled != 6 {
		t.Fatalf("settled: got %d, want 6", answer.Settled)
	}
}

// Test flow:
//  1. Register escrow "1" and hold its settle until the test lets it go.
//  2. POST a batch settle for it through a writer that reports every flush.
//  3. Assert a flush reached the client with status 200 while the escrow was still settling.
func TestABatchAnswersBeforeItsFirstSettleEnds(t *testing.T) {
	live := newHarness(t)
	live.control.devshards = registeredDevshardRows("1")
	release := make(chan struct{})
	var releaseOnce sync.Once
	letGo := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(letGo)
	live.operations.onSettle = func(string) { <-release }
	probe := newFlushProbe()
	finished := make(chan struct{})

	go func() {
		defer close(finished)
		live.requestInto(t, probe, http.MethodPost, "/v1/admin/devshards/settle", `{"escrow_ids":["1"]}`, adminHeaders())
	}()
	select {
	case <-probe.flushed:
	case <-time.After(5 * time.Second):
		letGo()
		<-finished
		t.Fatal("nothing was flushed while the escrow was still settling")
	}
	letGo()
	<-finished

	if probe.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200", probe.Code)
	}
}

// Test flow:
//  1. Register escrow "7".
//  2. POST a batch settle for it with `batch_size` at `maxBatchSize`.
//  3. Assert the escrow was settled, so the ceiling itself is accepted.
func TestABatchAcceptsTheLargestBatchSize(t *testing.T) {
	live := newHarness(t)
	live.control.devshards = registeredDevshardRows("7")

	response := live.request(t, http.MethodPost, "/v1/admin/devshards/settle",
		`{"escrow_ids":["7"],"batch_size":`+strconv.Itoa(maxBatchSize)+`}`, adminHeaders())

	if response.Code != http.StatusOK {
		t.Fatalf("status: got %d (%s), want 200", response.Code, response.Body.String())
	}
	if answer := decodeBatchSettle(t, response.Body.Bytes()); answer.Settled != 1 {
		t.Fatalf("settled: got %d, want 1", answer.Settled)
	}
}

// Test flow:
//  1. Register devshard 7 and POST its admin settle on a request context its client has already cancelled.
//  2. Assert the settle answered 200 and ran on a context that was not cancelled and carried a deadline.
func TestAnAdminSettleRunsOnAContextItsClientCannotCancel(t *testing.T) {
	live := newHarness(t)
	live.control.devshards = registeredDevshardRows("7")
	var settleErr error
	var bounded bool
	live.operations.onSettle = func(string) {
		live.operations.mu.Lock()
		defer live.operations.mu.Unlock()
		settleErr = live.operations.settleCtx.Err()
		_, bounded = live.operations.settleCtx.Deadline()
	}
	clientGone, cancel := context.WithCancel(context.Background())
	cancel()

	response := live.requestWithContext(t, clientGone, http.MethodPost, "/v1/admin/devshards/7/settle", "", adminHeaders())

	if response.Code != http.StatusOK {
		t.Fatalf("status: got %d (%s), want 200", response.Code, response.Body.String())
	}
	if settleErr != nil {
		t.Fatalf("the settle ran on a context its client cancelled: %v", settleErr)
	}
	if !bounded {
		t.Fatal("the settle ran on a context with no deadline of its own")
	}
}

// Test flow:
//  1. Register devshards 1 and 2 and wire the settle of escrow 1 to cancel the request context, as a client that disconnects does.
//  2. POST a batch settle for both with `batch_size` 1.
//  3. Assert escrow 1 settled and escrow 2 was never started.
func TestABatchStartsNoSettleOnceItsClientIsGone(t *testing.T) {
	live := newHarness(t)
	live.control.devshards = registeredDevshardRows("1", "2")
	clientGone, cancel := context.WithCancel(context.Background())
	defer cancel()
	var started []string
	live.operations.onSettle = func(escrowID string) {
		started = append(started, escrowID)
		cancel()
	}

	response := live.requestWithContext(t, clientGone, http.MethodPost, "/v1/admin/devshards/settle", `{"escrow_ids":["1","2"],"batch_size":1}`, adminHeaders())

	answer := decodeBatchSettle(t, response.Body.Bytes())
	if !slices.Equal(started, []string{"1"}) || answer.Settled != 1 || answer.Failed != 1 {
		t.Fatalf("a batch whose client left: started %v, answer %+v; want escrow 1 settled and escrow 2 never started", started, answer)
	}
}
