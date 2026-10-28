package scenarios

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"devshard/cmd/gateway/app"
	"devshard/cmd/gateway/chain"
	"devshard/cmd/gateway/config"
	"devshard/cmd/gateway/engine"
	"devshard/cmd/gateway/env"
	"devshard/cmd/gateway/escrow"
	"devshard/cmd/gateway/internal/logcapture"
	"devshard/cmd/gateway/store"
	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/types"
)

const (
	escrowKeyEnv = "SCENARIO_ESCROW_KEY"
	testModelID  = "scenario-model"
	logTailLines = 400
)

type modelSpec struct {
	id               string
	maxModelLen      uint64
	amount           uint64
	targetCount      int
	tempCount        int
	reserveCount     int
	maxUnsettled     int
	fullContextSlots int
}

type seededEscrow struct {
	amount uint64
	role   string
	parked bool
	model  string
}

type testSpec struct {
	participants   int
	groupSize      int
	model          modelSpec
	seeded         []seededEscrow
	walletBalance  uint64
	epoch          chainEpoch
	validationRate uint32
	behaviours     map[int]participantBehaviour
	environment    map[string]string
	maxUnsettled   int
	allowUnsettled bool
	pins           map[string]string
	steps          []harnessStep
	otherModels    []modelSpec
	slotOwners     []int
	violationSink  *string
}

// gatewayHarness runs one gateway, its chain and its fleet inside a bubble, and remembers what it saw for the invariants.
type gatewayHarness struct {
	t          *testing.T
	spec       testSpec
	chain      *fakeChain
	fleet      *hostFleet
	values     env.Values
	storageDir string
	gateway    *app.Composed
	gatewayMu  sync.Mutex
	background context.CancelFunc
	bootedAt   time.Time
	seededIDs  []string

	requests            sync.WaitGroup
	requestsMu          sync.Mutex
	openRequests        map[int]time.Time
	nextRequest         int
	statuses            []int
	unavailableBalances []uint64

	observationsMu   sync.Mutex
	unreadFullAt     map[string][]string
	createCandidates []createCandidate
	pinsSeen         map[string]bool
	logged           *logcapture.Recorder
	logStart         int

	ledgerComparisons int
	severedSessions   int
	replacedEngines   []*engine.Engine
}

// harnessStep is one thing a scenario does between two invariant checks.
type harnessStep interface {
	apply(harness *gatewayHarness)
}

type sendRequests struct {
	model       string
	promptBytes int
	maxTokens   int
	count       int
	sequential  bool
}

type advance struct{ by time.Duration }

// alignToTick moves time to offset past the manager's next tick.
type alignToTick struct{ offset time.Duration }

type changeParticipants struct {
	indexes []int
	change  func(*participantBehaviour)
}

type moveChain struct{ move func(*fakeChain) }

// restartGateway shuts the gateway down and composes it again on the same store directory, chain and hosts; work the shutdown abandoned dies with the process.
type restartGateway struct {
	whileDown expectThat
}

type reconfigure struct{ change func(*config.Config) }

type reconfigureModels struct {
	change func(entries []map[string]any)
}

// reconfigureModel changes the scenario's model as an operator override would, so the invariants judge later creates by the changed figures.
type reconfigureModel struct{ change func(*modelSpec) }

type operatorDeactivates struct{ escrowID func(*gatewayHarness) string }

type operatorCreates struct{ model string }

type operatorSettles struct {
	escrowID func(*gatewayHarness) string
	force    bool
}

type fundWallet struct{ amount uint64 }

// reportEscrowMissing stands in for a host answering that it no longer holds the escrow.
type reportEscrowMissing struct{ escrowID func(*gatewayHarness) string }

type expectThat struct {
	label  string
	verify func(harness *gatewayHarness) error
}

type violationReport struct{ key, detail string }

func defaultSpec() testSpec {
	return testSpec{
		participants:  4,
		groupSize:     4,
		model:         modelSpec{id: testModelID, maxModelLen: 8192, amount: 1_000_000, targetCount: 1, tempCount: 1, fullContextSlots: 1},
		seeded:        []seededEscrow{{amount: 1_000_000, role: "regular"}},
		walletBalance: 100_000_000,
		epoch:         chainEpoch{latest: 7, effective: 7, blockHeight: 1000, pocStart: 2000, setNewValidators: 2100, phase: chain.EpochPhaseInference},
		maxUnsettled:  64,
	}
}

func (spec testSpec) allModels() []modelSpec {
	return append([]modelSpec{spec.model}, spec.otherModels...)
}

func (spec testSpec) modelByID(modelID string) modelSpec {
	for _, model := range spec.allModels() {
		if model.id == modelID {
			return model
		}
	}
	return spec.model
}

func (spec testSpec) modelIDs() []string {
	ids := make([]string, 0, len(spec.otherModels)+1)
	for _, model := range spec.allModels() {
		ids = append(ids, model.id)
	}
	return ids
}

func (model modelSpec) guaranteeSlots() int {
	if model.fullContextSlots > 0 {
		return model.fullContextSlots
	}
	return 2
}

func modelEntry(model modelSpec) map[string]any {
	entry := map[string]any{
		"model_id":        model.id,
		"target_count":    model.targetCount,
		"temp_count":      model.tempCount,
		"reserve_count":   model.reserveCount,
		"amount":          model.amount,
		"private_key_env": escrowKeyEnv,
	}
	if model.maxUnsettled > 0 {
		entry["max_unsettled"] = model.maxUnsettled
	}
	if model.fullContextSlots > 0 {
		entry["full_context_slots"] = model.fullContextSlots
	}
	return entry
}

func modelsJSON(t *testing.T, models []modelSpec, change func(entries []map[string]any)) string {
	t.Helper()
	entries := make([]map[string]any, 0, len(models))
	for _, model := range models {
		entries = append(entries, modelEntry(model))
	}
	if change != nil {
		change(entries)
	}
	encoded, err := json.Marshal(entries)
	if err != nil {
		t.Fatalf("json.Marshal(models) = %v, want nil", err)
	}
	return string(encoded)
}

func runSteps(t *testing.T, spec testSpec) {
	t.Helper()
	runStepsWith(t, spec, nil)
}

// runStepsWith sets the environment outside the bubble, lets beforeBoot script the chain, then boots, steps, checks and stops inside it.
func runStepsWith(t *testing.T, spec testSpec, beforeBoot func(*fakeChain)) {
	t.Helper()
	creator := testutil.MustGenerateKey(t)
	participants := make([]*signing.Secp256k1Signer, spec.participants)
	for index := range participants {
		participants[index] = testutil.MustGenerateKey(t)
	}
	setGatewayEnvironment(t, spec, creator)
	logged := logcapture.Shared(t)
	t.Cleanup(func() { printCapturedLogTail(t, logged) })
	synctest.Test(t, func(t *testing.T) {
		harness := newGatewayHarness(t, spec, creator, participants)
		harness.logged = logged
		harness.logStart = len(logged.All())
		defer harness.finish()
		if beforeBoot != nil {
			beforeBoot(harness.chain)
		}
		harness.start()
		for index, step := range spec.steps {
			step.apply(harness)
			synctest.Wait()
			harness.judge(fmt.Sprintf("step %d (%T)", index, step), harness.violations())
		}
		harness.judge("ledger at rest", harness.ledgerAtRest())
		harness.finish()
		harness.judge("scenario end", harness.takeDeferredCreateViolations())
		harness.requirePinsSeen()
	})
}

// printCapturedLogTail shows a failed scenario the gateway's last lines, which the shared recorder otherwise keeps from stderr.
func printCapturedLogTail(t *testing.T, logged *logcapture.Recorder) {
	if !t.Failed() {
		return
	}
	entries := logged.All()
	shown := entries[max(0, len(entries)-logTailLines):]
	t.Logf("last %d of %d gateway log lines:", len(shown), len(entries))
	for _, entry := range shown {
		t.Logf("%s %s %v", entry.Level, entry.Msg, entry.Fields)
	}
}

func openModelAccess(models []modelSpec) map[string]string {
	access := make(map[string]string, len(models))
	for _, model := range models {
		access[model.id] = config.ModelAccessOpen
	}
	return access
}

func setGatewayEnvironment(t *testing.T, spec testSpec, creator *signing.Secp256k1Signer) {
	t.Helper()
	for name, value := range map[string]string{
		"DEVSHARD_STORAGE_DIR":                        t.TempDir(),
		"DEVSHARD_PUBLIC_API":                         "http://observer.scenario",
		"DEVSHARD_CHAIN_GRPC":                         "127.0.0.1:9090",
		"DEVSHARD_ESCROW_ROTATION_ENABLED":            "true",
		"DEVSHARD_ESCROW_ROTATION_SETTLEMENT_ENABLED": "true",
		"DEVSHARD_ESCROW_ROTATION_MODELS_JSON":        modelsJSON(t, spec.allModels(), nil),
		"DEVSHARD_TX_FEE_AMOUNT":                      "1000",
		"DEVSHARD_TX_POLL_INTERVAL_MS":                "500",
		"DEVSHARD_GATEWAY_HOST_PING_DISABLED":         "true",
		"GATEWAY_WARM_NEW_ESCROWS":                    "false",
		"DEVSHARD_STATS_ENABLED":                      "true",
		"DEVSHARD_STATS_PORT":                         "9091",
		"GATEWAY_HEIGHT_SYNC_ENABLED":                 "false",
		"DEVSHARD_REQUIRE_HEIGHT_SEED":                "false",
		escrowKeyEnv:                                  creator.PrivateKeyHex(),
	} {
		t.Setenv(name, value)
	}
	for name, value := range spec.environment {
		t.Setenv(name, value)
	}
}

func newGatewayHarness(t *testing.T, spec testSpec, creator *signing.Secp256k1Signer, participants []*signing.Secp256k1Signer) *gatewayHarness {
	addresses := make([]string, len(participants))
	for index, participant := range participants {
		addresses[index] = participant.Address()
	}
	models := make(map[string]chain.ModelParams, len(spec.otherModels)+1)
	for _, model := range spec.allModels() {
		models[model.id] = chain.ModelParams{ContextWindow: model.maxModelLen, MaxModelLen: model.maxModelLen}
	}
	blockchain := newFakeChain(fakeChainShape{
		participants: addresses,
		groupSize:    spec.groupSize,
		params:       chain.EscrowParams{MaxNonce: 1_000_000, TokenPrice: 1, FeePerNonce: 10, CreateDevshardFee: 100},
		models:       models,
		wallets:      map[string]uint64{creator.Address(): spec.walletBalance},
		txFee:        1000,
		epoch:        spec.epoch,
		slotOwners:   spec.slotOwners,
	})
	fleet := newHostFleet(t, blockchain, creator, participants, spec.validationRate)
	for index, behaviour := range spec.behaviours {
		fleet.changeBehaviour([]int{index}, func(current *participantBehaviour) { *current = behaviour })
	}
	harness := &gatewayHarness{
		t: t, spec: spec, chain: blockchain, fleet: fleet,
		openRequests: map[int]time.Time{}, unreadFullAt: map[string][]string{}, pinsSeen: map[string]bool{},
	}
	blockchain.setCreateHook(harness.observeCreate)
	for _, seeded := range spec.seeded {
		harness.seededIDs = append(harness.seededIDs, formatEscrowID(blockchain.fundEscrow(creator.Address(), cmp.Or(seeded.model, spec.model.id), seeded.amount)))
	}
	return harness
}

func (h *gatewayHarness) start() {
	values, err := env.Load()
	if err != nil {
		h.t.Fatalf("env.Load() = %v, want nil", err)
	}
	if values.StorageDir == nil {
		h.t.Fatalf("env.Load().StorageDir = nil, want the DEVSHARD_STORAGE_DIR the harness set")
	}
	storageDir := *values.StorageDir
	gatewayStore, err := store.Open(storageDir)
	if err != nil {
		h.t.Fatalf("store.Open() = %v, want nil", err)
	}
	if err := gatewayStore.SaveOverrides(context.Background(), config.Overrides{ModelAccess: openModelAccess(h.spec.allModels())}); err != nil {
		h.t.Fatalf("SaveOverrides() = %v, want nil", err)
	}
	for index, escrowID := range h.seededIDs {
		seeded := h.spec.seeded[index]
		if err := gatewayStore.UpsertDevshard(context.Background(), store.DevshardRecord{
			EscrowID: escrowID, PrivateKeyEnv: escrowKeyEnv, Model: cmp.Or(seeded.model, h.spec.model.id), Active: !seeded.parked,
			RotationRole: seeded.role, RotationEpoch: int64(h.spec.epoch.latest), SettlementPending: seeded.parked,
		}); err != nil {
			h.t.Fatalf("UpsertDevshard(%s) = %v, want nil", escrowID, err)
		}
	}
	h.values, h.storageDir = values, storageDir
	h.boot(gatewayStore)
}

func (h *gatewayHarness) boot(gatewayStore *store.Store) {
	sources := app.Sources{
		Serving:   h.fleet.serving,
		ReadOnly:  h.fleet.readOnly,
		Reader:    h.chain,
		Transport: h.chain,
		PublicAPI: newFakePublicAPIClient(h.chain, h.spec.modelIDs()),
		Hosts:     h.fleet,
	}
	composed, err := app.Compose(context.Background(), h.values, h.storageDir, gatewayStore, sources)
	if err != nil {
		_ = gatewayStore.Close()
		h.t.Fatalf("Compose() = %v, want nil", err)
	}
	background, cancel := context.WithCancel(context.Background())
	h.setGateway(composed)
	h.background = cancel
	for _, step := range composed.BootSteps(context.Background(), background) {
		if step.Name == "http listener" {
			continue
		}
		if step.Name == "nonce ledger" {
			composed.Nonces().StartSweeping(background, composed.Escrows(), composed.Journal())
			continue
		}
		if step.Name == "escrow lifecycle" {
			h.bootedAt = time.Now()
		}
		if err := step.Start(); err != nil {
			h.t.Fatalf("boot step %q = %v, want nil", step.Name, err)
		}
		if step.Name == "chain observer" {
			synctest.Wait()
		}
	}
	synctest.Wait()
}

// finish stops everything and waits out votes the shutdowns abandoned, the running engine's and every engine a restart replaced (production would exit instead), so a failed step unwinds without a synctest deadlock masking it.
func (h *gatewayHarness) finish() {
	engines := h.replacedEngines
	h.replacedEngines = nil
	if h.gateway != nil {
		engines = append(engines, h.gateway.Races())
		h.stop()
	}
	h.fleet.release()
	h.requests.Wait()
	for _, races := range engines {
		races.Stop()
	}
}

func (h *gatewayHarness) stop() {
	defer h.setGateway(nil)
	h.drainRequests(app.ShutdownGracePeriod)
	h.background()
	h.resolveCandidates()
	if err := h.gateway.Shutdown(); err != nil {
		h.t.Logf("Shutdown() = %v", err)
	}
}

// drainRequests waits for in-flight requests up to grace, as server.Shutdown waits for in-flight handlers.
func (h *gatewayHarness) drainRequests(grace time.Duration) {
	drained := make(chan struct{})
	go func() {
		defer close(drained)
		h.requests.Wait()
	}()
	graceTimer := time.NewTimer(grace)
	defer graceTimer.Stop()
	select {
	case <-drained:
	case <-graceTimer.C:
		h.t.Logf("shutdown began with requests still in flight after %s", grace)
	}
}

func (h *gatewayHarness) setGateway(running *app.Composed) {
	h.gatewayMu.Lock()
	defer h.gatewayMu.Unlock()
	h.gateway = running
}

func (h *gatewayHarness) currentGateway() *app.Composed {
	h.gatewayMu.Lock()
	defer h.gatewayMu.Unlock()
	return h.gateway
}

func (h *gatewayHarness) sendRequest(model string, promptBytes, maxTokens int, wait bool) {
	h.requestsMu.Lock()
	requestID := h.nextRequest
	h.nextRequest++
	h.openRequests[requestID] = time.Now()
	h.requestsMu.Unlock()
	marker := strconv.Itoa(requestID)
	content := (strings.Repeat("a", promptBytes) + marker)[len(marker):]
	body := fmt.Sprintf(`{"model":%q,"max_tokens":%d,"messages":[{"role":"user","content":%q}]}`,
		cmp.Or(model, h.spec.model.id), maxTokens, content)
	handler := h.currentGateway().Handler()
	serve := func() {
		request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		var largestBalance uint64
		if recorder.Code == http.StatusServiceUnavailable {
			largestBalance = h.largestRoutableBalance(cmp.Or(model, h.spec.model.id))
		}
		h.requestsMu.Lock()
		delete(h.openRequests, requestID)
		h.statuses = append(h.statuses, recorder.Code)
		if recorder.Code == http.StatusServiceUnavailable {
			h.unavailableBalances = append(h.unavailableBalances, largestBalance)
		}
		h.requestsMu.Unlock()
	}
	if wait {
		serve()
		return
	}
	h.requests.Go(serve)
}

func (h *gatewayHarness) answeredStatuses() []int {
	h.requestsMu.Lock()
	defer h.requestsMu.Unlock()
	return append([]int(nil), h.statuses...)
}

func (h *gatewayHarness) openRequestCount() int {
	h.requestsMu.Lock()
	defer h.requestsMu.Unlock()
	return len(h.openRequests)
}

func (h *gatewayHarness) balancesAtUnavailable() []uint64 {
	h.requestsMu.Lock()
	defer h.requestsMu.Unlock()
	return append([]uint64(nil), h.unavailableBalances...)
}

func (h *gatewayHarness) largestRoutableBalance(model string) uint64 {
	running := h.currentGateway()
	if running == nil {
		return 0
	}
	var largest uint64
	for _, escrowID := range h.fleet.builtEscrowIDs() {
		if candidate, routable := running.Escrows().Routable(escrowID); routable && candidate.Model == model {
			largest = max(largest, h.userBalance(escrowID))
		}
	}
	return largest
}

func (h *gatewayHarness) rows() []store.DevshardRecord {
	running := h.currentGateway()
	if running == nil {
		return nil
	}
	rows, err := running.Store().ListDevshards(context.Background())
	if err != nil {
		h.t.Fatalf("ListDevshards() = %v, want nil", err)
	}
	return rows
}

func (h *gatewayHarness) userBalance(escrowID string) uint64 {
	machine, built := h.fleet.userMachine(escrowID)
	if !built {
		return 0
	}
	return machine.Balance()
}

func (step sendRequests) apply(harness *gatewayHarness) {
	for range step.count {
		harness.sendRequest(step.model, step.promptBytes, step.maxTokens, step.sequential)
	}
}

func (step advance) apply(*gatewayHarness) { time.Sleep(step.by) }

func (step alignToTick) apply(harness *gatewayHarness) {
	elapsed := time.Since(harness.bootedAt) % escrow.TickInterval
	time.Sleep((step.offset - elapsed + escrow.TickInterval) % escrow.TickInterval)
}

func (step changeParticipants) apply(harness *gatewayHarness) {
	harness.fleet.changeBehaviour(step.indexes, step.change)
}

func (step moveChain) apply(harness *gatewayHarness) { step.move(harness.chain) }

func (step restartGateway) apply(harness *gatewayHarness) {
	harness.replacedEngines = append(harness.replacedEngines, harness.currentGateway().Races())
	harness.stop()
	if step.whileDown.verify != nil {
		step.whileDown.apply(harness)
	}
	harness.severedSessions += harness.fleet.severOpenSessions()
	gatewayStore, err := store.Open(harness.storageDir)
	if err != nil {
		harness.t.Fatalf("store.Open() on restart = %v, want nil", err)
	}
	harness.boot(gatewayStore)
}

func (step reconfigure) apply(harness *gatewayHarness) { harness.reconfigure(step.change) }

func (step reconfigureModels) apply(harness *gatewayHarness) {
	encoded := modelsJSON(harness.t, harness.spec.allModels(), step.change)
	harness.reconfigure(func(configuration *config.Config) { configuration.Rotation.ModelsJSON = encoded })
}

func (step reconfigureModel) apply(harness *gatewayHarness) {
	step.change(&harness.spec.model)
	reconfigureModels{}.apply(harness)
}

// reconfigure swaps the running configuration as an operator override would; scalar fields only, the copy shares maps.
func (h *gatewayHarness) reconfigure(change func(*config.Config)) {
	running := h.currentGateway()
	next := *running.Config().Load()
	change(&next)
	running.Config().Swap(&next)
}

func (step operatorDeactivates) apply(harness *gatewayHarness) {
	escrowID := step.escrowID(harness)
	if err := harness.currentGateway().Deactivate(context.Background(), escrowID); err != nil {
		harness.t.Fatalf("Deactivate(%s) = %v, want nil", escrowID, err)
	}
}

func (step operatorCreates) apply(harness *gatewayHarness) {
	model := harness.spec.modelByID(cmp.Or(step.model, harness.spec.model.id))
	if _, err := harness.currentGateway().Manager().CreateEscrow(context.Background(), escrow.ModelConfig{ModelID: model.id, Amount: model.amount, PrivateKeyEnv: escrowKeyEnv}); err != nil {
		harness.t.Fatalf("CreateEscrow(%s) = %v, want nil", model.id, err)
	}
}

func (step operatorSettles) apply(harness *gatewayHarness) {
	escrowID := step.escrowID(harness)
	if _, err := harness.currentGateway().Settle(context.Background(), escrowID, step.force); err != nil {
		harness.t.Logf("Settle(%s, force %t) = %v", escrowID, step.force, err)
	}
}

func (step fundWallet) apply(harness *gatewayHarness) {
	harness.chain.addToWallet(harness.fleet.creator.Address(), step.amount)
}

func (step reportEscrowMissing) apply(harness *gatewayHarness) {
	harness.currentGateway().Manager().OnEscrowMissing(step.escrowID(harness))
}

func (step expectThat) apply(harness *gatewayHarness) {
	if err := step.verify(harness); err != nil {
		harness.judge("expectation", []violationReport{{key: step.label, detail: err.Error()}})
	}
}

func parseEscrowID(escrowID string) uint64 {
	numeric, _ := strconv.ParseUint(escrowID, 10, 64)
	return numeric
}

// Test flow:
//  1. Boot the real gateway inside a bubble over the fake chain with one funded escrow and four healthy participants.
//  2. Send one chat request and wait for its answer.
//  3. Assert the answer is 200, then shut the gateway down with nothing left running.
func TestHarnessBootsAndAnswersInsideABubble(t *testing.T) {
	spec := defaultSpec()
	spec.steps = []harnessStep{
		sendRequests{promptBytes: 200, maxTokens: 64, count: 1, sequential: true},
		expectThat{label: "the request was answered 200", verify: func(harness *gatewayHarness) error {
			if statuses := harness.answeredStatuses(); len(statuses) != 1 || statuses[0] != 200 {
				return fmt.Errorf("statuses = %v, want [200]", statuses)
			}
			return nil
		}},
	}
	runSteps(t, spec)
}

// Test flow:
//  1. Make every participant stall after its receipt, so no answer and no further request composes a diff on the seeded escrow.
//  2. Send one request and advance one minute.
//  3. Assert every attempt the session started is Started with its executor's stamp: the heartbeat's drain sequenced the last confirm-start, as production's heartbeat does on a quiet escrow.
//  4. Advance past the execution timeout, so both attempts' timeout votes post while the gateway still runs.
//  5. Assert every attempt Started in step 3 is now TimedOut or sealed, and its executor slot's Missed count rose by one per such attempt.
func TestAQuietEscrowSequencesItsReceipts(t *testing.T) {
	startedExecutorSlots := map[uint64]uint32{}
	missedBefore := map[uint32]uint32{}
	spec := defaultSpec()
	spec.steps = []harnessStep{
		changeParticipants{change: func(behaviour *participantBehaviour) { behaviour.stall = true }},
		sendRequests{promptBytes: 200, maxTokens: 64, count: 1},
		advance{by: time.Minute},
		expectThat{label: "every receipted attempt is Started", verify: func(harness *gatewayHarness) error {
			machine, built := harness.fleet.userMachine(harness.seededIDs[0])
			if !built {
				return fmt.Errorf("the seeded escrow was never served")
			}
			pending, started := 0, 0
			for inferenceID, inference := range machine.SnapshotState().Inferences {
				switch {
				case inference.Status == types.StatusPending:
					pending++
				case inference.Status == types.StatusStarted && inference.ConfirmedAt > 0:
					started++
					startedExecutorSlots[inferenceID] = inference.ExecutorSlot
				}
			}
			if pending > 0 || started == 0 {
				return fmt.Errorf("%d Pending and %d stamped Started records after a minute, want none Pending", pending, started)
			}
			for slot, stats := range machine.SnapshotState().HostStats {
				missedBefore[slot] = stats.Missed
			}
			return nil
		}},
		advance{by: 35 * time.Minute},
		expectThat{label: "every Started attempt timed out and was counted Missed on its executor", verify: func(harness *gatewayHarness) error {
			machine, _ := harness.fleet.userMachine(harness.seededIDs[0])
			state := machine.SnapshotState()
			timedOutPerSlot := map[uint32]uint32{}
			for inferenceID, executorSlot := range startedExecutorSlots {
				if record, live := state.Inferences[inferenceID]; live && record.Status != types.StatusTimedOut {
					return fmt.Errorf("inference %d status = %v after the execution timeout, want TimedOut or sealed", inferenceID, record.Status)
				}
				timedOutPerSlot[executorSlot]++
			}
			for executorSlot, timedOut := range timedOutPerSlot {
				var missedAfter uint32
				if stats := state.HostStats[executorSlot]; stats != nil {
					missedAfter = stats.Missed
				}
				if missedAfter-missedBefore[executorSlot] < timedOut {
					return fmt.Errorf("slot %d Missed rose by %d, want at least %d", executorSlot, missedAfter-missedBefore[executorSlot], timedOut)
				}
			}
			return nil
		}},
	}
	runSteps(t, spec)
}

// Test flow:
//  1. Boot the gateway with one funded escrow, send one request and wait for it.
//  2. Restart the gateway on the same store directory, chain and hosts.
//  3. Send one more request and assert both were answered 200 and the seeded row is still active.
func TestHarnessRestartsTheGatewayOnTheSameStore(t *testing.T) {
	spec := defaultSpec()
	spec.steps = []harnessStep{
		sendRequests{promptBytes: 200, maxTokens: 64, count: 1, sequential: true},
		restartGateway{},
		sendRequests{promptBytes: 200, maxTokens: 64, count: 1, sequential: true},
		expectThat{label: "both requests answered across the restart", verify: func(harness *gatewayHarness) error {
			if statuses := harness.answeredStatuses(); len(statuses) != 2 || statuses[0] != 200 || statuses[1] != 200 {
				return fmt.Errorf("statuses = %v, want [200 200]", statuses)
			}
			for _, row := range harness.rows() {
				if row.EscrowID == harness.seededIDs[0] && row.Active {
					return nil
				}
			}
			return fmt.Errorf("escrow %s no longer active after the restart", harness.seededIDs[0])
		}},
	}
	runSteps(t, spec)
}

// Test flow:
//  1. Boot with a guarantee of one, take participant 0 offline, send eight requests one after another and assert the seeded escrow holds a Pending record, whose refusal vote the gateway owes at +65 s.
//  2. Restart the gateway at once and assert the restart severed the session the abandoned shutdown left open.
//  3. Send one more request, advance two minutes, and assert every request was answered 200 and the Pending record is still Pending: the owed vote died with the old process instead of holding the restart.
func TestHarnessRestartCutsTheVotesTheOldGatewayOwed(t *testing.T) {
	spec := plannerSpec()
	spec.model.fullContextSlots = 1
	spec.behaviours = map[int]participantBehaviour{0: {offline: true}}
	pendingOwed := expectThat{label: "a Pending record whose refusal vote is owed", verify: func(harness *gatewayHarness) error {
		if !hasRecord(harness, harness.seededIDs[0], types.StatusPending) {
			return fmt.Errorf("hasRecord(%s, Pending) = false, want true", harness.seededIDs[0])
		}
		return nil
	}}
	spec.steps = []harnessStep{
		sendRequests{promptBytes: 200, maxTokens: 64, count: 8, sequential: true},
		pendingOwed,
		restartGateway{},
		expectThat{label: "the restart severed the open session", verify: func(harness *gatewayHarness) error {
			if harness.severedSessions == 0 {
				return fmt.Errorf("severedSessions = 0, want the session the abandoned shutdown left open")
			}
			return nil
		}},
		sendRequests{promptBytes: 200, maxTokens: 64, count: 1, sequential: true},
		advance{by: 2 * time.Minute},
		expectThat{label: "the restarted gateway answers", verify: everyAnswer(9, http.StatusOK)},
		pendingOwed,
	}
	runSteps(t, spec)
}

func firstCreated(harness *gatewayHarness) string {
	creates := harness.chain.createsOf(testModelID)
	if len(creates) == 0 {
		harness.t.Fatalf("createsOf(%s) = none, want the operator's create", testModelID)
	}
	return formatEscrowID(creates[0].escrowID)
}

// Test flow:
//  1. Boot one seeded escrow over four participants billing 300 input tokens each, send two requests, and assert both answered 200.
//  2. Add 5000 to the wallet; assert the creator's wallet grew by exactly 5000.
//  3. Create an escrow as the operator; assert it is the one create narrated, with the reason operator.
//  4. Deactivate it as the operator; assert its row is inactive.
//  5. Report the seeded escrow missing while the chain still holds it and advance one tick; assert its row still serves.
//  6. Settle the seeded escrow as the operator and advance one tick; assert the chain holds it settled.
func TestHarnessOperatorStepsActOnTheRunningGateway(t *testing.T) {
	var walletBefore uint64
	spec := defaultSpec()
	spec.behaviours = everyParticipant(spec.participants, participantBehaviour{inputTokens: 300})
	spec.steps = []harnessStep{
		sendRequests{model: testModelID, promptBytes: 200, maxTokens: 64, count: 2, sequential: true},
		expectThat{label: "both requests answered 200", verify: everyAnswer(2, http.StatusOK)},
		expectThat{label: "wallet read", verify: func(harness *gatewayHarness) error {
			walletBefore = harness.creatorWallet()
			return nil
		}},
		fundWallet{amount: 5_000},
		expectThat{label: "the wallet was funded", verify: func(harness *gatewayHarness) error {
			if wallet := harness.creatorWallet(); wallet != walletBefore+5_000 {
				return fmt.Errorf("creatorWallet() = %d, want %d", wallet, walletBefore+5_000)
			}
			return nil
		}},
		operatorCreates{},
		expectThat{label: "the operator's create is narrated", verify: func(harness *gatewayHarness) error {
			if operatorCreated, narrated := harness.createdWithReason(testModelID, "operator"), harness.logLines("escrow created"); operatorCreated != 1 || narrated != 1 {
				return fmt.Errorf("createdWithReason(operator) = %d, logLines(escrow created) = %d, want 1 and 1", operatorCreated, narrated)
			}
			return nil
		}},
		operatorDeactivates{escrowID: firstCreated},
		expectThat{label: "the operator's escrow is inactive", verify: func(harness *gatewayHarness) error {
			if row, stored := rowOf(harness, firstCreated(harness)); !stored || row.Active {
				return fmt.Errorf("row %s = %+v, want stored and inactive", firstCreated(harness), row)
			}
			return nil
		}},
		reportEscrowMissing{escrowID: seededID(0)},
		advance{by: escrow.TickInterval},
		expectThat{label: "a report the chain contradicts deactivates nothing", verify: func(harness *gatewayHarness) error {
			if row, stored := rowOf(harness, harness.seededIDs[0]); !stored || !row.Active {
				return fmt.Errorf("row %s = %+v, want still active", harness.seededIDs[0], row)
			}
			return nil
		}},
		operatorSettles{escrowID: seededID(0)},
		advance{by: escrow.TickInterval},
		expectThat{label: "the seeded escrow settled", verify: func(harness *gatewayHarness) error {
			if record, known := harness.chain.escrowRecord(parseEscrowID(harness.seededIDs[0])); !known || !record.settled {
				return fmt.Errorf("escrowRecord(%s) = %+v, want settled", harness.seededIDs[0], record)
			}
			return nil
		}},
	}
	runSteps(t, spec)
}
