package warmup

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"devshard/cmd/gateway/accounting"
	"devshard/cmd/gateway/chain"
	"devshard/cmd/gateway/config"
	"devshard/cmd/gateway/internal/leakcheck"
	"devshard/cmd/gateway/registry"
	"devshard/host"
	"devshard/state"
	"devshard/types"
	"devshard/user"
)

type recordedProbe struct {
	escrowID string
	attempt  accounting.Attempt
}

// spyLedger is both of the warmup's ledger paths: the escrow it opens directly and the probe it hands the journal.
type spyLedger struct {
	opened      []accounting.EscrowMetadata
	probes      []recordedProbe
	openRefusal error
}

func (s *spyLedger) OpenEscrow(metadata accounting.EscrowMetadata) error {
	s.opened = append(s.opened, metadata)
	return s.openRefusal
}

func (s *spyLedger) ProbeRecorded(escrowID string, attempt accounting.Attempt) {
	s.probes = append(s.probes, recordedProbe{escrowID: escrowID, attempt: attempt})
}

// stubEpochs stamps a fixed epoch, so a test that cares about the stamp states it rather than the chain.
type stubEpochs struct{ epoch uint64 }

func (s stubEpochs) Snapshot() chain.PhaseSnapshot {
	return chain.PhaseSnapshot{EpochIndex: s.epoch}
}

type stubEscrows struct {
	session  registry.EscrowSession
	live     bool
	released int
}

func (s *stubEscrows) Acquire(string) (registry.EscrowSession, func(), bool) {
	if !s.live {
		return nil, nil, false
	}
	return s.session, func() { s.released++ }, true
}

func warmupClock() func() time.Time {
	moment := time.Date(2026, 8, 21, 10, 0, 0, 0, time.UTC)
	return func() time.Time { return moment }
}

// Test flow:
//  1. Build a config holder with warming left off.
//  2. Call `New`.
//  3. Assert it returns nil, so nothing observes publications.
func TestWarmupIsSkippedWhenTheOperatorTurnedItOff(t *testing.T) {
	holder := config.NewHolder(&config.Config{})

	if warmup := New(holder, nil, stubEpochs{}, warmupClock()); warmup != nil {
		t.Errorf("New() = %v with warming off, want nil so nothing observes publications", warmup)
	}
}

// Test flow:
//  1. Build a config holder with `Scheduler.WarmNewEscrows` on.
//  2. Call `New`.
//  3. Assert it returns a non-nil warmup.
func TestWarmupIsBuiltWhenWarmingIsOn(t *testing.T) {
	holder := config.NewHolder(&config.Config{Scheduler: config.Scheduler{WarmNewEscrows: true}})

	if warmup := New(holder, nil, stubEpochs{}, warmupClock()); warmup == nil {
		t.Error("New() = nil with warming on, want a warmup")
	}
}

// Test flow:
//  1. Build a warmup with warming on and no ledger passed in.
//  2. Assert its `ledger` field is a nil interface, not a typed nil that would panic on the first record.
func TestWarmupKeepsATypedNilLedgerOutOfItsInterface(t *testing.T) {
	holder := config.NewHolder(&config.Config{Scheduler: config.Scheduler{WarmNewEscrows: true}})

	warmup := New(holder, nil, stubEpochs{}, warmupClock())

	if warmup.ledger != nil {
		t.Error("ledger is non-nil for a nil *Book: a typed nil there panics on the first record")
	}
}

// Test flow:
//  1. Build a `Prober` whose escrow registry reports the escrow as no longer live.
//  2. Warm that escrow.
//  3. Assert no probe was recorded.
func TestWarmupSkipsAnEscrowThatIsAlreadyGone(t *testing.T) {
	escrows := &stubEscrows{live: false}
	ledger := &spyLedger{}
	warmup := &Prober{escrows: escrows, ledger: ledger, probes: ledger, now: warmupClock()}

	warmup.warm("escrow-1", "test-model")

	if len(ledger.probes) != 0 {
		t.Errorf("recorded %d probes for a retired escrow, want 0", len(ledger.probes))
	}
}

// Test flow:
//  1. Record a served probe's nonce through `warmup.record`.
//  2. Assert one probe attempt was recorded for that nonce.
//  3. Assert it is marked sent, with usage `UsageLoser`, since nobody consumed a warmup answer.
func TestWarmupSettlesItsNonceAsWorkNobodyUsed(t *testing.T) {
	ledger := &spyLedger{}
	warmup := &Prober{ledger: ledger, probes: ledger, now: warmupClock()}

	warmup.record("escrow-1", 7, true, nil)

	if len(ledger.probes) != 1 {
		t.Fatalf("recorded %d probes, want 1", len(ledger.probes))
	}
	attempt := ledger.probes[0].attempt
	switch {
	case attempt.Nonce != 7:
		t.Errorf("nonce = %d, want 7", attempt.Nonce)
	case !attempt.Sent:
		t.Error("Sent = false, want true: the gateway dispatched this nonce")
	case attempt.Usage != accounting.UsageLoser:
		t.Errorf("Usage = %q, want %q: nobody consumed a warmup answer", attempt.Usage, accounting.UsageLoser)
	}
}

// Test flow:
//  1. Record a refused probe's nonce and error through `warmup.record`.
//  2. Assert the recorded attempt is marked sent but not acknowledged.
func TestARefusedProbeIsNotAcknowledged(t *testing.T) {
	ledger := &spyLedger{}
	warmup := &Prober{ledger: ledger, probes: ledger, now: warmupClock()}

	warmup.record("escrow-1", 7, false, errors.New("host refused"))

	attempt := ledger.probes[0].attempt
	switch {
	case !attempt.Sent:
		t.Error("Sent = false, want true: the nonce is spent either way")
	case attempt.Acknowledged:
		t.Error("Acknowledged = true, want false: no receipt came back")
	}
}

// Test flow:
//  1. Build a warmup with warming on and no ledger, so the ledger field holds a nil interface.
//  2. Call `openLedger` for one escrow.
//  3. Assert the ledger field is still nil rather than a typed nil `openLedger` would dereference.
func TestWarmupWithoutALedgerRecordsNothingAndDoesNotPanic(t *testing.T) {
	holder := config.NewHolder(&config.Config{Scheduler: config.Scheduler{WarmNewEscrows: true}})

	warmup := New(holder, nil, stubEpochs{}, warmupClock())
	warmup.openLedger("escrow-1", "test-model", stubSession{})

	if warmup.ledger != nil {
		t.Fatalf("ledger = %#v, want a nil interface: openLedger dereferences a typed nil instead of skipping", warmup.ledger)
	}
}

// Test flow:
//  1. Parse the probe's fixed prompt body.
//  2. Assert its declared `max_tokens` matches `probeMaxTokens`, the amount the warmup actually reserves.
//  3. Assert the probe's params reserve exactly `state.MinReservationTokens`, the same output budget devshardctl's PoC probe reserves.
func TestABurnedNonceAndAWarmupProbeAgreeOnTheirTokenFloor(t *testing.T) {
	var body struct {
		MaxTokens uint64 `json:"max_tokens"`
	}
	if err := json.Unmarshal(probePrompt, &body); err != nil {
		t.Fatalf("parsing probePrompt: %v", err)
	}
	if body.MaxTokens != probeMaxTokens {
		t.Errorf("probePrompt max_tokens = %d, reservation = %d: a host refuses a probe declaring less than it reserved",
			body.MaxTokens, probeMaxTokens)
	}
	if params := probeParams("qwen", 1); params.MaxTokens != state.MinReservationTokens {
		t.Errorf("probe MaxTokens = %d, want state.MinReservationTokens %d", params.MaxTokens, state.MinReservationTokens)
	}
}

// Test flow:
//  1. Build a `Prober` whose probe commits a nonce and whose catch-up signals completion.
//  2. Publish one escrow through `EscrowPublished`.
//  3. Assert the catch-up runs within 5 seconds.
//  4. Assert no goroutine the warmup started is still running afterward.
func TestEscrowPublishedLeavesNoGoroutineBehind(t *testing.T) {
	defer leakcheck.VerifyNoneStarted(t)()

	caughtUp := make(chan struct{})
	warmup := &Prober{
		escrows: &stubEscrows{session: stubSession{}, live: true},
		ledger:  &spyLedger{},
		probe: func(_ context.Context, _ registry.EscrowSession, _ user.InferenceParams, nonceCommitted func()) (uint64, bool, error) {
			nonceCommitted()
			return 1, true, nil
		},
		catchUp: func(context.Context, registry.EscrowSession) error {
			close(caughtUp)
			return nil
		},
		stop: make(chan struct{}),
		now:  warmupClock(),
	}

	warmup.EscrowPublished("escrow-1", "model-a")

	select {
	case <-caughtUp:
	case <-time.After(5 * time.Second):
		t.Fatal("warm never reached its catch-up: the probe path did not run")
	}
}

// Test flow:
//  1. Start the warmup against a cancellable context.
//  2. Cancel that context.
//  3. Assert the warmup's stop channel closes within a second.
func TestTheWarmupStopsWithTheGateway(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	warmup := &Prober{now: warmupClock()}

	warmup.Start(ctx)
	cancel()

	select {
	case <-warmup.stop:
	case <-time.After(time.Second):
		t.Error("the warmup did not see the gateway stop: it would keep spending nonces after shutdown")
	}
}

// stubSession implements only what the warmup reads; anything else panics.
type stubSession struct {
	registry.EscrowSession
	nonce          uint64
	quietSettleArm *int
}

func (s stubSession) ArmQuietSettle() {
	if s.quietSettleArm != nil {
		*s.quietSettleArm++
	}
}

func (s stubSession) Nonce() uint64 { return s.nonce }

func (s stubSession) WaitRouterCatalog(context.Context) error   { return nil }
func (s stubSession) WaitHeightSeedReady(context.Context) error { return nil }

func (s stubSession) SnapshotState() types.EscrowState {
	return types.EscrowState{Group: []types.SlotAssignment{{SlotID: 0, ValidatorAddress: "host-a"}}}
}

func newWarmupUnderTest(session registry.EscrowSession, probeErr error) (*Prober, *spyLedger, *int) {
	caughtUp := 0
	ledger := &spyLedger{}
	warmup := &Prober{
		escrows: &stubEscrows{session: session, live: true},
		ledger:  ledger,
		probes:  ledger,
		probe: func(_ context.Context, _ registry.EscrowSession, _ user.InferenceParams, nonceCommitted func()) (uint64, bool, error) {
			nonceCommitted()
			return 1, probeErr == nil, probeErr
		},
		catchUp: func(context.Context, registry.EscrowSession) error {
			caughtUp++
			return nil
		},
		now: warmupClock(),
	}
	return warmup, ledger, &caughtUp
}

// Test flow:
//  1. Warm an escrow whose probe succeeds.
//  2. Assert the catch-up ran exactly once, so every host the dispatch might have missed still comes to hold the escrow.
func TestEveryHostLearnsTheEscrowAfterTheProbe(t *testing.T) {
	warmup, _, caughtUp := newWarmupUnderTest(stubSession{}, nil)

	warmup.warm("escrow-1", "test-model")

	if *caughtUp != 1 {
		t.Errorf("catch-up ran %d times, want 1: without it a host the dispatch missed never holds the escrow", *caughtUp)
	}
}

// Test flow:
//  1. Warm an escrow whose probe succeeds, on a session that counts the quiet-settle arms.
//  2. Assert the session's quiet settle was armed, so the probe's Finish settles even when no request ever follows it on this escrow.
func TestTheProbeArmsTheQuietSettleSoItsFinishSettlesWithoutTraffic(t *testing.T) {
	arms := 0
	warmup, _, _ := newWarmupUnderTest(stubSession{quietSettleArm: &arms}, nil)

	warmup.warm("escrow-1", "test-model")

	if arms != 1 {
		t.Errorf("quiet settle armed %d times, want 1: a reserve escrow takes no request, so its probe's Finish would stay pending until retirement", arms)
	}
}

// Test flow:
//  1. Warm an escrow whose probe is refused.
//  2. Assert the catch-up still ran exactly once, since the diff is persisted before the send regardless of the answer.
func TestTheGroupLearnsTheEscrowEvenWhenTheProbeWasRefused(t *testing.T) {
	warmup, _, caughtUp := newWarmupUnderTest(stubSession{}, errors.New("host refused"))

	warmup.warm("escrow-1", "test-model")

	if *caughtUp != 1 {
		t.Error("catch-up was skipped after a refused probe: the diff is persisted before the send, so there is state to replay")
	}
}

// Test flow:
//  1. Warm an escrow whose session already reports a non-zero nonce.
//  2. Assert no probe was recorded and no catch-up ran, since its hosts already hold the escrow.
func TestAnEscrowThatAlreadyServedIsNeitherProbedNorCaughtUp(t *testing.T) {
	warmup, ledger, caughtUp := newWarmupUnderTest(stubSession{nonce: 917}, nil)

	warmup.warm("escrow-1", "test-model")

	if len(ledger.probes) != 0 || *caughtUp != 0 {
		t.Errorf("probes = %d, catch-ups = %d, want 0 and 0: its hosts already hold the escrow", len(ledger.probes), *caughtUp)
	}
}

// Test flow:
//  1. Define a table of host responses, varying across an executor receipt, a state signature only, an empty response, and no response at all.
//  2. For each case, call `executorAcknowledged`.
//  3. Assert the result matches the case's expectation, true only for the executor's own receipt.
func TestOnlyAnExecutorReceiptCountsAsAnAnsweredProbe(t *testing.T) {
	for _, testCase := range []struct {
		name     string
		response *host.HostResponse
		want     bool
	}{
		{name: "executor receipt", response: &host.HostResponse{Receipt: []byte("sig")}, want: true},
		{name: "state signature only", response: &host.HostResponse{StateSig: []byte("sig")}, want: false},
		{name: "nothing", response: &host.HostResponse{}, want: false},
		{name: "no response", response: nil, want: false},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			if got := executorAcknowledged(testCase.response); got != testCase.want {
				t.Errorf("executorAcknowledged() = %t, want %t: every host signs state, only the executor answers",
					got, testCase.want)
			}
		})
	}
}

// Test flow:
//  1. Build a `Prober` whose probe blocks until it observes the catch-up complete, and whose catch-up closes immediately.
//  2. Warm one escrow.
//  3. Assert the probe observed the catch-up finish before the probe itself returned, so the group is taught while the probe is still streaming.
func TestTheGroupIsTaughtWhileTheProbeIsStillStreaming(t *testing.T) {
	caughtUp := make(chan struct{})
	sawCatchUp := make(chan struct{})
	warmup := &Prober{
		escrows: &stubEscrows{session: stubSession{}, live: true},
		ledger:  &spyLedger{},
		probe: func(_ context.Context, _ registry.EscrowSession, _ user.InferenceParams, nonceCommitted func()) (uint64, bool, error) {
			nonceCommitted()
			select {
			case <-caughtUp:
				close(sawCatchUp)
			case <-time.After(2 * time.Second):
			}
			return 1, true, nil
		},
		catchUp: func(context.Context, registry.EscrowSession) error {
			close(caughtUp)
			return nil
		},
		now: warmupClock(),
	}

	warmup.warm("escrow-1", "test-model")

	select {
	case <-sawCatchUp:
	default:
		t.Error("the catch-up waited for the probe's answer: the group holds nothing for that whole inference")
	}
}

// Test flow:
//  1. Warm an escrow whose probe succeeds.
//  2. Assert exactly one attempt was recorded for the one nonce the warmup spends.
//  3. Assert its terminal is `accounting.TerminalWarmupProbe`.
func TestTheWarmupNonceIsSettledAsAProbe(t *testing.T) {
	warmup, ledger, _ := newWarmupUnderTest(stubSession{}, nil)

	warmup.warm("escrow-1", "test-model")

	if len(ledger.probes) != 1 {
		t.Fatalf("recorded %+v, want one attempt for the one nonce the warmup spends", ledger.probes)
	}
	if got := ledger.probes[0].attempt.Terminal; got != accounting.TerminalWarmupProbe {
		t.Errorf("terminal = %q, want %q", got, accounting.TerminalWarmupProbe)
	}
}

// Test flow:
//  1. Stamp the chain snapshot with a known epoch and warm an escrow whose probe succeeds.
//  2. Assert the ledger opened exactly one escrow, stamped with the escrow ID, model, and the epoch it was created in.
//  3. Assert the opened escrow carries the slots the session's group reports.
func TestTheProbeOpensTheEscrowItIsAboutToRecordAgainst(t *testing.T) {
	warmup, ledger, _ := newWarmupUnderTest(stubSession{}, nil)
	warmup.epochs = stubEpochs{epoch: 42}

	warmup.warm("escrow-1", "test-model")

	if len(ledger.opened) != 1 {
		t.Fatalf("opened %d escrows in the ledger, want the one it recorded against", len(ledger.opened))
	}
	opened := ledger.opened[0]
	if opened.EscrowID != "escrow-1" || opened.Model != "test-model" || opened.CreationEpoch != 42 {
		t.Errorf("opened %+v, want escrow-1/test-model stamped with the epoch it was created in", opened)
	}
	if len(opened.Slots) != 1 {
		t.Errorf("opened with %d slots, want the group the session reports: a slotless escrow is refused", len(opened.Slots))
	}
}

// Test flow:
//  1. Stamp the chain snapshot with epoch 0 and warm an escrow whose probe succeeds.
//  2. Assert the ledger opened no escrow, since an unknown epoch would pin it there for good.
func TestTheProbeOpensNoEscrowBeforeTheChainHasNamedAnEpoch(t *testing.T) {
	warmup, ledger, _ := newWarmupUnderTest(stubSession{}, nil)
	warmup.epochs = stubEpochs{epoch: 0}

	warmup.warm("escrow-1", "test-model")

	if len(ledger.opened) != 0 {
		t.Fatalf("opened %+v under an unknown epoch, want none", ledger.opened)
	}
}
