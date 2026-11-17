package chain

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"devshard/cmd/gateway/internal/leakcheck"
)

// phaseObserverStub serves the public-API and chain-REST endpoints PhaseObserver polls, with mutable status and body for each.
type phaseObserverStub struct {
	mu                 sync.Mutex
	epochStatus        int
	epochBody          string
	participantsStatus int
	participantsBody   string
	preserved          *PreservedNodes
	preservedFound     bool
	preservedErr       error
	preservedHits      int
	maxNonce           uint64
	tokenPrice         uint64
	feePerNonce        uint64
	createDevshardFee  uint64
	maxNonceHeld       bool
	maxNonceErr        error
	maxNonceHits       int
	models             map[string]ModelParams
	modelsErr          error
	versionsStatus     int
	versionsBody       string
}

func newPhaseObserverStub() *phaseObserverStub {
	return &phaseObserverStub{
		epochStatus:        http.StatusOK,
		participantsStatus: http.StatusOK,
		versionsStatus:     http.StatusNotFound,
	}
}

func (s *phaseObserverStub) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		switch r.URL.Path {
		case "/v1/epochs/latest":
			w.WriteHeader(s.epochStatus)
			w.Write([]byte(s.epochBody))
		case "/v1/epochs/current/participants":
			w.WriteHeader(s.participantsStatus)
			w.Write([]byte(s.participantsBody))
		case "/v1/versions":
			w.WriteHeader(s.versionsStatus)
			w.Write([]byte(s.versionsBody))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}
}

func (s *phaseObserverStub) setEpoch(status int, body string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.epochStatus = status
	s.epochBody = body
}

func (s *phaseObserverStub) setParticipants(status int, body string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.participantsStatus = status
	s.participantsBody = body
}

// PreservedNodes and MaxNonce make the stub the observer's chain reader, so a test programs both the
// public API and the chain from one place and still counts what was asked for.
func (s *phaseObserverStub) PreservedNodes(context.Context) (*PreservedNodes, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.preservedHits++
	return s.preserved, s.preservedFound, s.preservedErr
}

func (s *phaseObserverStub) Models(context.Context) (map[string]ModelParams, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.models, s.modelsErr
}

func (s *phaseObserverStub) EscrowParams(context.Context) (EscrowParams, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.maxNonceHits++
	return EscrowParams{MaxNonce: s.maxNonce, TokenPrice: s.tokenPrice, FeePerNonce: s.feePerNonce, CreateDevshardFee: s.createDevshardFee}, s.maxNonceHeld, s.maxNonceErr
}

func (s *phaseObserverStub) setPreservedNodes(snapshot *PreservedNodes, found bool, failure error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.preserved, s.preservedFound, s.preservedErr = snapshot, found, failure
}

func (s *phaseObserverStub) setMaxNonceValue(value uint64, held bool, failure error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.maxNonce, s.maxNonceHeld, s.maxNonceErr = value, held, failure
}

func (s *phaseObserverStub) setEscrowPrice(tokenPrice, feePerNonce, createDevshardFee uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tokenPrice, s.feePerNonce, s.createDevshardFee = tokenPrice, feePerNonce, createDevshardFee
}

func (s *phaseObserverStub) setModels(models map[string]ModelParams, failure error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.models, s.modelsErr = models, failure
}

func (s *phaseObserverStub) setVersions(status int, body string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.versionsStatus = status
	s.versionsBody = body
}

func (s *phaseObserverStub) preservedHitCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.preservedHits
}

func (s *phaseObserverStub) maxNonceHitCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.maxNonceHits
}

func observerEpochJSON(blockHeight int64, epochIndex uint64, phase EpochPhase) string {
	return fmt.Sprintf(`{
		"block_height": %d,
		"phase": %q,
		"latest_epoch": {"index": %d, "poc_start_block_height": 0},
		"is_confirmation_poc_active": false
	}`, blockHeight, string(phase), epochIndex)
}

func observerParticipantsJSON(participantAddr, inferenceURL string, pocWeight uint64) string {
	return fmt.Sprintf(`{
		"active_participants": {
			"participants": [
				{
					"index": %q,
					"inference_url": %q,
					"models": ["model-a"],
					"ml_nodes": [{"ml_nodes": [{"node_id": "node-1", "timeslot_allocation": [true, true], "poc_weight": %d}]}]
				}
			]
		}
	}`, participantAddr, inferenceURL, pocWeight)
}

// observerEpochSwitchJSON is an Inference-phase epoch body with configurable epoch_stages/
// next_epoch_stages set_new_validators heights, used to drive the EpochSwitchBlockHeight ladder.
func observerEpochSwitchJSON(blockHeight, setNewValidators, nextSetNewValidators int64) string {
	return fmt.Sprintf(`{
		"block_height": %d,
		"phase": "Inference",
		"latest_epoch": {"index": 7, "poc_start_block_height": 900},
		"epoch_stages": {"set_new_validators": %d},
		"next_epoch_stages": {"set_new_validators": %d},
		"is_confirmation_poc_active": false
	}`, blockHeight, setNewValidators, nextSetNewValidators)
}

// observerEpochPoCJSON is a PoCGenerate-or-Validate epoch body with an explicit PoC start height.
func observerEpochPoCJSON(phase EpochPhase, pocStartHeight int64) string {
	return fmt.Sprintf(`{
		"block_height": 1000,
		"phase": %q,
		"latest_epoch": {"index": 7, "poc_start_block_height": %d},
		"is_confirmation_poc_active": false
	}`, string(phase), pocStartHeight)
}

// observerEpochBeforePoCJSON is an Inference epoch body whose next PoC starts at nextPoCStart.
func observerEpochBeforePoCJSON(blockHeight, nextPoCStart int64) string {
	return fmt.Sprintf(`{
		"block_height": %d,
		"phase": "Inference",
		"latest_epoch": {"index": 7, "poc_start_block_height": 900},
		"epoch_stages": {"next_poc_start": %d},
		"is_confirmation_poc_active": false
	}`, blockHeight, nextPoCStart)
}

// observerEpochConfirmationJSON is an Inference epoch body with an active confirmation-PoC event.
func observerEpochConfirmationJSON(confirmationPhase ConfirmationPoCPhase, triggerHeight int64) string {
	return fmt.Sprintf(`{
		"block_height": 1000,
		"phase": "Inference",
		"latest_epoch": {"index": 7, "poc_start_block_height": 900},
		"is_confirmation_poc_active": true,
		"active_confirmation_poc_event": {"phase": %q, "trigger_height": %d}
	}`, string(confirmationPhase), triggerHeight)
}

// observerTwoMinerParticipantsJSON has gonka1abc with a timeslot-non-preserved node1 (weight 100)
// and gonka1bcd with a timeslot-preserved node2 (weight 40), both serving model-a from baseURL.
func observerTwoMinerParticipantsJSON(baseURL string) string {
	return fmt.Sprintf(`{
		"active_participants": {
			"participants": [
				{
					"index": "gonka1abc",
					"inference_url": %q,
					"models": ["model-a"],
					"ml_nodes": [{"ml_nodes": [{"node_id": "node1", "timeslot_allocation": [true, false], "poc_weight": 100}]}]
				},
				{
					"index": "gonka1bcd",
					"inference_url": %q,
					"models": ["model-a"],
					"ml_nodes": [{"ml_nodes": [{"node_id": "node2", "timeslot_allocation": [true, true], "poc_weight": 40}]}]
				}
			]
		}
	}`, baseURL, baseURL)
}

// observerPreservedSnapshotJSON is a found preserved-nodes snapshot listing only gonka1abc/node1
// under model-a at the given anchor height.
func observerPreservedSnapshot(anchorHeight int64) *PreservedNodes {
	return &PreservedNodes{
		EpisodeAnchorHeight: anchorHeight,
		Models: []PreservedModel{{
			ModelID:      "model-a",
			Participants: []PreservedParticipant{{ParticipantID: "gonka1abc", NodeIDs: []string{"node1"}}},
		}},
	}
}

// newPoCPhaseObserver builds an observer over the stub server; a nil chainReader is the shape of a
// gateway with no chain access, and refresh is driven directly by the tests, never via Start.
func newPoCPhaseObserver(t *testing.T, server *httptest.Server, chainReader Reader) *PhaseObserver {
	t.Helper()
	clock := newFakeClock(time.Unix(100, 0))
	cfg := ObserverConfig{
		PublicAPIBaseURL: server.URL,
		PollInterval:     time.Hour,
		HTTPClient:       server.Client(),
		Now:              clock.Now,
	}
	if chainReader != nil {
		cfg.Chain = chainReader
	}
	observer, err := NewPhaseObserver(cfg)
	if err != nil {
		t.Fatalf("NewPhaseObserver(): %v", err)
	}
	return observer
}

// waitForSnapshot drains snapshots until predicate matches, failing the test if timeout elapses first.
func waitForSnapshot(t *testing.T, snapshots <-chan PhaseSnapshot, timeout time.Duration, predicate func(PhaseSnapshot) bool) PhaseSnapshot {
	t.Helper()
	deadline := time.After(timeout)
	for {
		select {
		case snapshot := <-snapshots:
			if predicate(snapshot) {
				return snapshot
			}
		case <-deadline:
			t.Fatal("timed out waiting for expected snapshot")
			return PhaseSnapshot{}
		}
	}
}

// Test flow:
//  1. Call NewPhaseObserver with an empty PublicAPIBaseURL.
//  2. Assert it returns an error.
func TestNewPhaseObserver_EmptyPublicAPIBaseURL_Errors(t *testing.T) {
	_, err := NewPhaseObserver(ObserverConfig{})
	if err == nil {
		t.Fatal("NewPhaseObserver() error = nil, want error for empty PublicAPIBaseURL")
	}
}

// Test flow:
//  1. Call NewPhaseObserver with only PublicAPIBaseURL set, leaving every other field zero.
//  2. Assert the poll interval, HTTP client (and its timeout), clock and versions cache all fall back to their documented defaults.
func TestNewPhaseObserver_AppliesDefaultsForZeroFields(t *testing.T) {
	observer, err := NewPhaseObserver(ObserverConfig{PublicAPIBaseURL: "http://chain.example.invalid"})
	if err != nil {
		t.Fatalf("NewPhaseObserver(): %v", err)
	}
	if observer.pollInterval != DefaultObserverPollInterval {
		t.Errorf("pollInterval = %v, want %v", observer.pollInterval, DefaultObserverPollInterval)
	}
	if observer.client == nil {
		t.Error("HTTP client default not applied")
	} else if observer.client.Timeout != 5*time.Second {
		t.Errorf("default client timeout = %v, want 5s", observer.client.Timeout)
	}
	if observer.now == nil {
		t.Error("now default not applied")
	}
	if observer.versions == nil {
		t.Error("versions cache not constructed")
	}
}

// Test flow:
//  1. Call NewPhaseObserver once without a versions poll interval and once with 7 seconds.
//  2. Assert the default is DefaultVersionsPollInterval with a TTL of three intervals, and the chain poll stays at its own default.
//  3. Assert the configured interval is used and the TTL is three times it.
func TestNewPhaseObserver_VersionsPollIntervalAndTTLComeFromConfig(t *testing.T) {
	defaulted, err := NewPhaseObserver(ObserverConfig{PublicAPIBaseURL: "http://chain.example.invalid"})
	if err != nil {
		t.Fatalf("NewPhaseObserver(): %v", err)
	}
	if defaulted.versionsPollInterval != DefaultVersionsPollInterval || defaulted.versions.ttl != versionsTTLPollMultiplier*DefaultVersionsPollInterval {
		t.Errorf("default versions interval/ttl = %v/%v, want %v/%v", defaulted.versionsPollInterval, defaulted.versions.ttl, DefaultVersionsPollInterval, versionsTTLPollMultiplier*DefaultVersionsPollInterval)
	}
	if defaulted.pollInterval != DefaultObserverPollInterval {
		t.Errorf("chain pollInterval = %v, want %v", defaulted.pollInterval, DefaultObserverPollInterval)
	}

	const configuredInterval = 7 * time.Second
	configured, err := NewPhaseObserver(ObserverConfig{PublicAPIBaseURL: "http://chain.example.invalid", VersionsPollInterval: configuredInterval})
	if err != nil {
		t.Fatalf("NewPhaseObserver(): %v", err)
	}
	if configured.versionsPollInterval != configuredInterval || configured.versions.ttl != versionsTTLPollMultiplier*configuredInterval {
		t.Errorf("configured versions interval/ttl = %v/%v, want %v/%v", configured.versionsPollInterval, configured.versions.ttl, configuredInterval, versionsTTLPollMultiplier*configuredInterval)
	}
}

// Test flow:
//  1. Configure the stub with one inference-phase epoch and one participant, and create an observer with an hour-long poll interval.
//  2. Subscribe to snapshots, call Start, and wait for the first published snapshot.
//  3. Assert the first snapshot arrives immediately (proving Start doesn't wait for the ticker) with the epoch's block height, index, phase and timestamp.
//  4. Assert requests are unblocked and the current weights and inference URLs are folded in from the participant.
func TestPhaseObserver_StartPublishesFirstSnapshotAndNotifiesSubscribers(t *testing.T) {
	stub := newPhaseObserverStub()
	server := httptest.NewServer(stub.handler())
	defer server.Close()
	stub.setEpoch(http.StatusOK, observerEpochJSON(1000, 7, EpochPhaseInference))
	stub.setParticipants(http.StatusOK, observerParticipantsJSON("gonka1abc", server.URL, 42))

	clock := newFakeClock(time.Unix(100, 0))
	observer, err := NewPhaseObserver(ObserverConfig{
		PublicAPIBaseURL: server.URL,
		PollInterval:     time.Hour,
		HTTPClient:       server.Client(),
		Now:              clock.Now,
	})
	if err != nil {
		t.Fatalf("NewPhaseObserver(): %v", err)
	}

	snapshots := make(chan PhaseSnapshot, 16)
	defer observer.Subscribe(func(s PhaseSnapshot) { snapshots <- s })()

	observer.Start(context.Background())
	defer observer.Stop()

	first := waitForSnapshot(t, snapshots, 2*time.Second, func(PhaseSnapshot) bool { return true })

	if first.BlockHeight != 1000 {
		t.Errorf("BlockHeight = %d, want 1000", first.BlockHeight)
	}
	if first.EpochIndex != 7 {
		t.Errorf("EpochIndex = %d, want 7", first.EpochIndex)
	}
	if first.EpochPhase != EpochPhaseInference {
		t.Errorf("EpochPhase = %q, want %q", first.EpochPhase, EpochPhaseInference)
	}
	if first.RequestsBlocked {
		t.Error("RequestsBlocked = true, want false for Inference phase")
	}
	if first.BlockReason != BlockReasonNone {
		t.Errorf("BlockReason = %q, want empty", first.BlockReason)
	}
	if !first.LastUpdatedAt.Equal(time.Unix(100, 0)) {
		t.Errorf("LastUpdatedAt = %v, want %v", first.LastUpdatedAt, time.Unix(100, 0))
	}
	wantWeights := map[string]float64{"gonka1abc": 42}
	if !reflect.DeepEqual(first.CurrentWeights, wantWeights) {
		t.Errorf("CurrentWeights = %v, want %v", first.CurrentWeights, wantWeights)
	}
	wantURLs := map[string]string{"gonka1abc": server.URL}
	if !reflect.DeepEqual(first.InferenceURLs, wantURLs) {
		t.Errorf("InferenceURLs = %v, want %v", first.InferenceURLs, wantURLs)
	}
}

// Test flow:
//  1. Configure the stub with a PoCGenerate-phase epoch and one participant.
//  2. Call refresh directly (no ticker) and read the snapshot.
//  3. Assert requests are blocked with BlockReasonPoC and the epoch phase is PoCGenerate.
func TestPhaseObserver_BlockedPoCPhaseSetsRequestsBlockedAndReason(t *testing.T) {
	stub := newPhaseObserverStub()
	server := httptest.NewServer(stub.handler())
	defer server.Close()
	stub.setEpoch(http.StatusOK, observerEpochJSON(500, 3, EpochPhasePoCGenerate))
	stub.setParticipants(http.StatusOK, observerParticipantsJSON("gonka1abc", server.URL, 10))

	clock := newFakeClock(time.Unix(0, 0))
	observer, err := NewPhaseObserver(ObserverConfig{
		PublicAPIBaseURL: server.URL,
		HTTPClient:       server.Client(),
		Now:              clock.Now,
	})
	if err != nil {
		t.Fatalf("NewPhaseObserver(): %v", err)
	}

	observer.refresh(context.Background())
	snapshot := observer.Snapshot()

	if !snapshot.RequestsBlocked {
		t.Error("RequestsBlocked = false, want true for PoCGenerate phase")
	}
	if snapshot.BlockReason != BlockReasonPoC {
		t.Errorf("BlockReason = %q, want %q", snapshot.BlockReason, BlockReasonPoC)
	}
	if snapshot.EpochPhase != EpochPhasePoCGenerate {
		t.Errorf("EpochPhase = %q, want %q", snapshot.EpochPhase, EpochPhasePoCGenerate)
	}
}

// Test flow:
//  1. Create an observer over the stub with one participant.
//  2. Refresh with an epoch body carrying both the current and next epoch's set_new_validators heights and assert EpochSwitchBlockHeight picks the current epoch's value.
//  3. Refresh again with the current epoch's set_new_validators omitted and assert EpochSwitchBlockHeight falls through to the next epoch's value.
func TestPhaseObserver_EpochSwitchBlockHeightUsesFallbackLadder(t *testing.T) {
	stub := newPhaseObserverStub()
	server := httptest.NewServer(stub.handler())
	defer server.Close()
	stub.setParticipants(http.StatusOK, observerParticipantsJSON("gonka1abc", server.URL, 42))

	observer := newPoCPhaseObserver(t, server, nil)

	stub.setEpoch(http.StatusOK, observerEpochSwitchJSON(1000, 1200, 1300))
	observer.refresh(context.Background())
	if got := observer.Snapshot().EpochSwitchBlockHeight; got != 1200 {
		t.Fatalf("EpochSwitchBlockHeight = %d, want 1200 (current epoch set_new_validators)", got)
	}

	stub.setEpoch(http.StatusOK, observerEpochSwitchJSON(1000, 0, 1300))
	observer.refresh(context.Background())
	if got := observer.Snapshot().EpochSwitchBlockHeight; got != 1300 {
		t.Fatalf("EpochSwitchBlockHeight = %d, want 1300 (next epoch set_new_validators, primary omitted)", got)
	}
}

// Test flow:
//  1. Configure the stub with an inference-phase epoch and one participant, and start an observer with a millisecond poll interval.
//  2. Subscribe to snapshots and wait for the first inference-phase snapshot.
//  3. Change the stub's epoch to a blocking PoCValidate phase.
//  4. Wait for a later snapshot reporting that phase and assert it is blocked with BlockReasonPoC and carries the new block height and epoch index.
func TestPhaseObserver_TickerDrivesRepeatedRefreshOnPhaseChange(t *testing.T) {
	stub := newPhaseObserverStub()
	server := httptest.NewServer(stub.handler())
	defer server.Close()
	stub.setEpoch(http.StatusOK, observerEpochJSON(100, 1, EpochPhaseInference))
	stub.setParticipants(http.StatusOK, observerParticipantsJSON("gonka1abc", server.URL, 5))

	clock := newFakeClock(time.Unix(0, 0))
	observer, err := NewPhaseObserver(ObserverConfig{
		PublicAPIBaseURL: server.URL,
		PollInterval:     time.Millisecond,
		HTTPClient:       server.Client(),
		Now:              clock.Now,
	})
	if err != nil {
		t.Fatalf("NewPhaseObserver(): %v", err)
	}

	snapshots := make(chan PhaseSnapshot, 256)
	defer observer.Subscribe(func(s PhaseSnapshot) { snapshots <- s })()

	observer.Start(context.Background())
	defer observer.Stop()

	waitForSnapshot(t, snapshots, 2*time.Second, func(s PhaseSnapshot) bool { return s.EpochPhase == EpochPhaseInference })

	stub.setEpoch(http.StatusOK, observerEpochJSON(200, 2, EpochPhasePoCValidate))

	changed := waitForSnapshot(t, snapshots, 2*time.Second, func(s PhaseSnapshot) bool { return s.EpochPhase == EpochPhasePoCValidate })
	if !changed.RequestsBlocked || changed.BlockReason != BlockReasonPoC {
		t.Errorf("after phase change: RequestsBlocked=%v BlockReason=%q, want true/%q", changed.RequestsBlocked, changed.BlockReason, BlockReasonPoC)
	}
	if changed.BlockHeight != 200 || changed.EpochIndex != 2 {
		t.Errorf("BlockHeight/EpochIndex = %d/%d, want 200/2", changed.BlockHeight, changed.EpochIndex)
	}
}

// Test flow:
//  1. Create an observer over the stub with one participant and subscribe two counting handlers.
//  2. Call refresh once and assert both subscribers were notified.
//  3. Cancel one subscription, then call refresh twice more.
//  4. Assert the cancelled subscriber's count is unchanged while the remaining subscriber keeps counting.
func TestPhaseObserver_CancelStopsFurtherNotifications(t *testing.T) {
	stub := newPhaseObserverStub()
	server := httptest.NewServer(stub.handler())
	defer server.Close()
	stub.setEpoch(http.StatusOK, observerEpochJSON(1, 1, EpochPhaseInference))
	stub.setParticipants(http.StatusOK, observerParticipantsJSON("gonka1abc", server.URL, 1))

	clock := newFakeClock(time.Unix(0, 0))
	observer, err := NewPhaseObserver(ObserverConfig{
		PublicAPIBaseURL: server.URL,
		HTTPClient:       server.Client(),
		Now:              clock.Now,
	})
	if err != nil {
		t.Fatalf("NewPhaseObserver(): %v", err)
	}

	cancelledCount := 0
	cancel := observer.Subscribe(func(PhaseSnapshot) { cancelledCount++ })
	controlCount := 0
	observer.Subscribe(func(PhaseSnapshot) { controlCount++ })

	observer.refresh(context.Background())
	if cancelledCount != 1 || controlCount != 1 {
		t.Fatalf("after first refresh: cancelledCount=%d controlCount=%d, want 1/1", cancelledCount, controlCount)
	}

	cancel()
	observer.refresh(context.Background())
	observer.refresh(context.Background())

	if cancelledCount != 1 {
		t.Errorf("cancelled subscriber invoked after cancel: count=%d, want 1 (unchanged)", cancelledCount)
	}
	if controlCount != 3 {
		t.Errorf("control subscriber count=%d, want 3", controlCount)
	}
}

// Test flow:
//  1. Refresh once against a healthy stub and capture the resulting snapshot as the known-good baseline.
//  2. Make the epoch endpoint return HTTP 500 and refresh again.
//  3. Assert LastError is now set.
//  4. Clear LastError from the new snapshot and assert it otherwise equals the known-good baseline.
func TestPhaseObserver_EpochFetchErrorKeepsPreviousSnapshotWithLastError(t *testing.T) {
	stub := newPhaseObserverStub()
	server := httptest.NewServer(stub.handler())
	defer server.Close()
	stub.setEpoch(http.StatusOK, observerEpochJSON(42, 4, EpochPhaseInference))
	stub.setParticipants(http.StatusOK, observerParticipantsJSON("gonka1abc", server.URL, 7))

	clock := newFakeClock(time.Unix(0, 0))
	observer, err := NewPhaseObserver(ObserverConfig{
		PublicAPIBaseURL: server.URL,
		HTTPClient:       server.Client(),
		Now:              clock.Now,
	})
	if err != nil {
		t.Fatalf("NewPhaseObserver(): %v", err)
	}

	observer.refresh(context.Background())
	good := observer.Snapshot()
	if good.LastError != "" {
		t.Fatalf("precondition: LastError = %q, want empty", good.LastError)
	}

	stub.setEpoch(http.StatusInternalServerError, "")
	observer.refresh(context.Background())
	after := observer.Snapshot()

	if after.LastError == "" {
		t.Error("LastError = empty, want set after epoch fetch failure")
	}
	after.LastError = ""
	if !reflect.DeepEqual(after, good) {
		t.Errorf("snapshot after fetch error = %+v, want unchanged from %+v (aside from LastError)", after, good)
	}
}

// Test flow:
//  1. Build a previous snapshot with sample values for every participant-derived field and LastHealthyAt.
//  2. Call publishWithPreviousParticipants with a fresh snapshot carrying only BlockHeight, plus the previous snapshot, simulating a participants-fetch failure.
//  3. Assert every participant-derived field and LastHealthyAt on the published snapshot equal the previous snapshot's values.
//  4. Reflect over every field of PhaseSnapshot and assert each one is accounted for in the participant-derived, phase-derived, or carried-across-failure list, so a newly added field cannot go unclassified.
func TestPhaseObserver_CarriesEveryParticipantDerivedFieldForward(t *testing.T) {
	carriedAcrossFailure := []string{"LastHealthyAt"}
	participantDerived := []string{
		"CurrentWeights", "FullWeights", "CurrentWeightsByModel", "FullWeightsByModel",
		"Preserved", "PreservedByModel", "InferenceURLs",
	}
	phaseDerived := []string{
		"BlockHeight", "EpochSwitchBlockHeight", "EpochIndex", "EffectiveEpochIndex", "EpochPhase", "ConfirmationPoCPhase",
		"RequestsBlocked", "BlockReason", "MaxNonce", "TokenPrice", "FeePerNonce", "CreateDevshardFee", "Models", "LastUpdatedAt", "LastError",
	}

	previous := PhaseSnapshot{
		LastHealthyAt:         time.Unix(1700000000, 0),
		CurrentWeights:        map[string]float64{"participant-a": 1},
		FullWeights:           map[string]float64{"participant-a": 2},
		CurrentWeightsByModel: map[string]map[string]float64{"model-a": {"participant-a": 3}},
		FullWeightsByModel:    map[string]map[string]float64{"model-a": {"participant-a": 4}},
		Preserved:             []string{"participant-a"},
		PreservedByModel:      map[string][]string{"model-a": {"participant-a"}},
		InferenceURLs:         map[string]string{"participant-a": "http://host.invalid"},
	}
	observer := &PhaseObserver{}
	observer.publishWithPreviousParticipants(PhaseSnapshot{BlockHeight: 9}, previous, "fetch failed")
	carried := reflect.ValueOf(observer.Snapshot())
	source := reflect.ValueOf(previous)

	for _, name := range append(append([]string{}, participantDerived...), carriedAcrossFailure...) {
		if got, want := carried.FieldByName(name).Interface(), source.FieldByName(name).Interface(); !reflect.DeepEqual(got, want) {
			t.Errorf("%s = %v, want the previous snapshot's %v", name, got, want)
		}
	}

	known := map[string]bool{}
	for _, name := range append(append(append([]string{}, participantDerived...), phaseDerived...), carriedAcrossFailure...) {
		known[name] = true
	}
	snapshotType := reflect.TypeFor[PhaseSnapshot]()
	for index := range snapshotType.NumField() {
		if name := snapshotType.Field(index).Name; !known[name] {
			t.Errorf("PhaseSnapshot.%s is in neither list: decide whether the failure path must carry it forward", name)
		}
	}
}

// Test flow:
//  1. Refresh once against a healthy stub and capture the resulting snapshot as the known-good baseline.
//  2. Advance the epoch body to a new block height while making the participants endpoint return HTTP 500, then refresh again.
//  3. Assert LastError is now set while BlockHeight still reflects the epoch fetch that succeeded.
//  4. Assert CurrentWeights and InferenceURLs stayed at their stale, known-good values.
func TestPhaseObserver_ParticipantsFetchErrorKeepsPreviousWeightsWithLastError(t *testing.T) {
	stub := newPhaseObserverStub()
	server := httptest.NewServer(stub.handler())
	defer server.Close()
	stub.setEpoch(http.StatusOK, observerEpochJSON(42, 4, EpochPhaseInference))
	stub.setParticipants(http.StatusOK, observerParticipantsJSON("gonka1abc", server.URL, 7))

	clock := newFakeClock(time.Unix(0, 0))
	observer, err := NewPhaseObserver(ObserverConfig{
		PublicAPIBaseURL: server.URL,
		HTTPClient:       server.Client(),
		Now:              clock.Now,
	})
	if err != nil {
		t.Fatalf("NewPhaseObserver(): %v", err)
	}

	observer.refresh(context.Background())
	good := observer.Snapshot()
	if good.LastError != "" {
		t.Fatalf("precondition: LastError = %q, want empty", good.LastError)
	}

	stub.setEpoch(http.StatusOK, observerEpochJSON(43, 4, EpochPhaseInference))
	stub.setParticipants(http.StatusInternalServerError, "")
	observer.refresh(context.Background())
	after := observer.Snapshot()

	if after.LastError == "" {
		t.Error("LastError = empty, want set after participants fetch failure")
	}
	if after.BlockHeight != 43 {
		t.Errorf("BlockHeight = %d, want 43 (epoch fetch still succeeded)", after.BlockHeight)
	}
	if !reflect.DeepEqual(after.CurrentWeights, good.CurrentWeights) {
		t.Errorf("CurrentWeights = %v, want stale %v", after.CurrentWeights, good.CurrentWeights)
	}
	if !reflect.DeepEqual(after.InferenceURLs, good.InferenceURLs) {
		t.Errorf("InferenceURLs = %v, want stale %v", after.InferenceURLs, good.InferenceURLs)
	}
}

// Test flow:
//  1. Configure the stub with an epoch, one participant, and a max-nonce value, token price, fee per nonce and create fee from the chain reader.
//  2. Create an observer with the stub as its chain reader and refresh.
//  3. Assert LastError is empty and MaxNonce, TokenPrice, FeePerNonce and CreateDevshardFee reflect the chain's reported values.
func TestPhaseObserver_DecodesMaxNonceFromDevshardEscrowParams(t *testing.T) {
	stub := newPhaseObserverStub()
	server := httptest.NewServer(stub.handler())
	defer server.Close()
	stub.setEpoch(http.StatusOK, observerEpochJSON(42, 4, EpochPhaseInference))
	stub.setParticipants(http.StatusOK, observerParticipantsJSON("gonka1abc", server.URL, 7))
	stub.setMaxNonceValue(19_800, true, nil)
	stub.setEscrowPrice(7, 3, 11)

	observer := newPoCPhaseObserver(t, server, stub)
	observer.refresh(context.Background())
	snapshot := observer.Snapshot()

	if snapshot.LastError != "" {
		t.Fatalf("LastError = %q, want empty", snapshot.LastError)
	}
	if snapshot.MaxNonce != 19_800 {
		t.Errorf("MaxNonce = %d, want 19800", snapshot.MaxNonce)
	}
	if snapshot.TokenPrice != 7 || snapshot.FeePerNonce != 3 || snapshot.CreateDevshardFee != 11 {
		t.Errorf("TokenPrice, FeePerNonce, CreateDevshardFee = %d, %d, %d; want 7, 3, 11", snapshot.TokenPrice, snapshot.FeePerNonce, snapshot.CreateDevshardFee)
	}
}

// Test flow:
//  1. Configure the stub with an epoch, one participant, and a max-nonce value, but create the observer with no chain reader configured.
//  2. Assert MaxNonce reads 0 before any refresh.
//  3. Refresh and assert LastError stays empty, MaxNonce stays 0, and the stub's max-nonce endpoint was never hit.
func TestPhaseObserver_MaxNonceZeroBeforeFirstSuccessfulFetch(t *testing.T) {
	stub := newPhaseObserverStub()
	server := httptest.NewServer(stub.handler())
	defer server.Close()
	stub.setEpoch(http.StatusOK, observerEpochJSON(42, 4, EpochPhaseInference))
	stub.setParticipants(http.StatusOK, observerParticipantsJSON("gonka1abc", server.URL, 7))
	stub.setMaxNonceValue(19_800, true, nil)

	observer := newPoCPhaseObserver(t, server, nil)
	if observer.Snapshot().MaxNonce != 0 {
		t.Fatalf("MaxNonce before first refresh = %d, want 0", observer.Snapshot().MaxNonce)
	}

	observer.refresh(context.Background())
	snapshot := observer.Snapshot()

	if snapshot.LastError != "" {
		t.Fatalf("LastError = %q, want empty (no chain access is not a failure)", snapshot.LastError)
	}
	if snapshot.MaxNonce != 0 {
		t.Errorf("MaxNonce = %d, want 0 (no chain access, so nothing was fetched)", snapshot.MaxNonce)
	}
	if hits := stub.maxNonceHitCount(); hits != 0 {
		t.Errorf("params reads = %d, want 0 when no chain access is configured", hits)
	}
}

// Test flow:
//  1. Refresh once against a stub reporting a max-nonce value and a price and capture the snapshot as the known-good baseline.
//  2. Make the max-nonce read fail and refresh again.
//  3. Assert LastError is now set while MaxNonce still holds the prior value.
//  4. Clear LastError from the new snapshot and assert it otherwise equals the known-good baseline, price included.
func TestPhaseObserver_MaxNonceFetchErrorKeepsPriorValueAndRestOfSnapshot(t *testing.T) {
	stub := newPhaseObserverStub()
	server := httptest.NewServer(stub.handler())
	defer server.Close()
	stub.setEpoch(http.StatusOK, observerEpochJSON(42, 4, EpochPhaseInference))
	stub.setParticipants(http.StatusOK, observerParticipantsJSON("gonka1abc", server.URL, 7))
	stub.setMaxNonceValue(19_800, true, nil)
	stub.setEscrowPrice(7, 3, 11)

	observer := newPoCPhaseObserver(t, server, stub)
	observer.refresh(context.Background())
	good := observer.Snapshot()
	if good.LastError != "" {
		t.Fatalf("precondition: LastError = %q, want empty", good.LastError)
	}
	if good.MaxNonce != 19_800 {
		t.Fatalf("precondition: MaxNonce = %d, want 19800", good.MaxNonce)
	}

	stub.setMaxNonceValue(0, false, errors.New("chain unreachable"))
	observer.refresh(context.Background())
	after := observer.Snapshot()

	if after.LastError == "" {
		t.Error("LastError = empty, want set after max_nonce fetch failure")
	}
	if after.MaxNonce != 19_800 {
		t.Errorf("MaxNonce = %d, want prior value 19800 preserved (fail-open)", after.MaxNonce)
	}
	after.LastError = ""
	if !reflect.DeepEqual(after, good) {
		t.Errorf("snapshot after max_nonce fetch error = %+v, want unchanged from %+v (aside from LastError)", after, good)
	}
}

// Test flow:
//  1. Register a leak check deferred first so it runs last, after the server is closed.
//  2. Configure the stub, subscribe to snapshots, and call Start with a cancellable context.
//  3. Wait for the first snapshot, then cancel the context without ever calling Stop.
//  4. Assert the observer's done channel closes within 2 seconds of the cancellation.
func TestPhaseObserver_ContextCancelAloneStopsAllGoroutines(t *testing.T) {
	defer leakcheck.VerifyNone(t)

	stub := newPhaseObserverStub()
	server := httptest.NewServer(stub.handler())
	defer server.Close()
	stub.setEpoch(http.StatusOK, observerEpochJSON(1, 1, EpochPhaseInference))
	stub.setParticipants(http.StatusOK, observerParticipantsJSON("gonka1abc", server.URL, 1))

	clock := newFakeClock(time.Unix(0, 0))
	observer, err := NewPhaseObserver(ObserverConfig{
		PublicAPIBaseURL: server.URL,
		PollInterval:     time.Millisecond,
		HTTPClient:       server.Client(),
		Now:              clock.Now,
	})
	if err != nil {
		t.Fatalf("NewPhaseObserver(): %v", err)
	}

	snapshots := make(chan PhaseSnapshot, 16)
	defer observer.Subscribe(func(s PhaseSnapshot) { snapshots <- s })()

	ctx, cancel := context.WithCancel(context.Background())
	observer.Start(ctx)
	waitForSnapshot(t, snapshots, 2*time.Second, func(PhaseSnapshot) bool { return true })

	cancel()
	select {
	case <-observer.doneCh:
	case <-time.After(2 * time.Second):
		t.Fatal("goroutines did not exit within 2s of context cancel")
	}
}

// Test flow:
//  1. Register a leak check deferred first so it runs last, after the server is closed.
//  2. Configure the stub, subscribe to snapshots, and call Start.
//  3. Wait for the first snapshot, then call Stop.
//  4. Rely on the deferred leak check to assert both the poll loop and the versions loop exited.
func TestPhaseObserver_StopTerminatesLoopAndVersionsLoop(t *testing.T) {
	defer leakcheck.VerifyNone(t)

	stub := newPhaseObserverStub()
	server := httptest.NewServer(stub.handler())
	defer server.Close()
	stub.setEpoch(http.StatusOK, observerEpochJSON(1, 1, EpochPhaseInference))
	stub.setParticipants(http.StatusOK, observerParticipantsJSON("gonka1abc", server.URL, 1))

	clock := newFakeClock(time.Unix(0, 0))
	observer, err := NewPhaseObserver(ObserverConfig{
		PublicAPIBaseURL: server.URL,
		PollInterval:     time.Millisecond,
		HTTPClient:       server.Client(),
		Now:              clock.Now,
	})
	if err != nil {
		t.Fatalf("NewPhaseObserver(): %v", err)
	}

	snapshots := make(chan PhaseSnapshot, 16)
	defer observer.Subscribe(func(s PhaseSnapshot) { snapshots <- s })()

	observer.Start(context.Background())
	waitForSnapshot(t, snapshots, 2*time.Second, func(PhaseSnapshot) bool { return true })

	observer.Stop()
}

// Test flow:
//  1. Register a leak check deferred first so it runs last, after the server is closed.
//  2. Configure the stub, subscribe to snapshots, and call Start; wait for the first snapshot.
//  3. Call Stop concurrently from four goroutines, then call Stop once more from the main goroutine.
//  4. Rely on the deferred leak check to assert no goroutine is left behind and no call panicked.
func TestPhaseObserver_StopIsIdempotentAndConcurrentSafe(t *testing.T) {
	defer leakcheck.VerifyNone(t)

	stub := newPhaseObserverStub()
	server := httptest.NewServer(stub.handler())
	defer server.Close()
	stub.setEpoch(http.StatusOK, observerEpochJSON(1, 1, EpochPhaseInference))
	stub.setParticipants(http.StatusOK, observerParticipantsJSON("gonka1abc", server.URL, 1))

	clock := newFakeClock(time.Unix(0, 0))
	observer, err := NewPhaseObserver(ObserverConfig{
		PublicAPIBaseURL: server.URL,
		PollInterval:     time.Millisecond,
		HTTPClient:       server.Client(),
		Now:              clock.Now,
	})
	if err != nil {
		t.Fatalf("NewPhaseObserver(): %v", err)
	}

	snapshots := make(chan PhaseSnapshot, 16)
	defer observer.Subscribe(func(s PhaseSnapshot) { snapshots <- s })()

	observer.Start(context.Background())
	waitForSnapshot(t, snapshots, 2*time.Second, func(PhaseSnapshot) bool { return true })

	var stops sync.WaitGroup
	for range 4 {
		stops.Go(func() {
			observer.Stop()
		})
	}
	stops.Wait()
	observer.Stop()
}

// Test flow:
//  1. Create an observer that is never started.
//  2. Call Stop and assert it returns immediately instead of blocking.
func TestPhaseObserver_StopBeforeStartIsNoOp(t *testing.T) {
	observer, err := NewPhaseObserver(ObserverConfig{PublicAPIBaseURL: "http://chain.example.invalid"})
	if err != nil {
		t.Fatalf("NewPhaseObserver(): %v", err)
	}
	observer.Stop()
}

// Test flow:
//  1. Configure the stub with a PoCGenerate-phase epoch, two miners, and a preserved-nodes snapshot found at the expected anchor.
//  2. Create an observer with the stub as its chain reader and refresh.
//  3. Assert LastError is empty and Preserved reflects the snapshot's membership rather than the timeslot flags.
//  4. Assert CurrentWeights, FullWeights and PreservedByModel all follow the snapshot in both directions (adding and removing weight).
func TestPhaseObserver_PoCCurrentPreservedSnapshotReplacesTimeslotRule(t *testing.T) {
	stub := newPhaseObserverStub()
	server := httptest.NewServer(stub.handler())
	defer server.Close()
	stub.setEpoch(http.StatusOK, observerEpochPoCJSON(EpochPhasePoCGenerate, 950))
	stub.setParticipants(http.StatusOK, observerTwoMinerParticipantsJSON(server.URL))
	stub.setPreservedNodes(observerPreservedSnapshot(950), true, nil)

	observer := newPoCPhaseObserver(t, server, stub)
	observer.refresh(context.Background())
	snapshot := observer.Snapshot()

	if snapshot.LastError != "" {
		t.Fatalf("LastError = %q, want empty", snapshot.LastError)
	}
	if want := []string{"gonka1abc"}; !reflect.DeepEqual(snapshot.Preserved, want) {
		t.Errorf("Preserved = %v, want %v (snapshot membership, not timeslots)", snapshot.Preserved, want)
	}
	if want := map[string]float64{"gonka1abc": 100, "gonka1bcd": 0}; !reflect.DeepEqual(snapshot.CurrentWeights, want) {
		t.Errorf("CurrentWeights = %v, want %v", snapshot.CurrentWeights, want)
	}
	if want := map[string]float64{"gonka1abc": 100, "gonka1bcd": 40}; !reflect.DeepEqual(snapshot.FullWeights, want) {
		t.Errorf("FullWeights = %v, want %v", snapshot.FullWeights, want)
	}
	if want := map[string][]string{"model-a": {"gonka1abc"}}; !reflect.DeepEqual(snapshot.PreservedByModel, want) {
		t.Errorf("PreservedByModel = %v, want %v", snapshot.PreservedByModel, want)
	}
}

// Test flow:
//  1. Configure the stub with a confirmation-PoC grace-period epoch, two miners, and no preserved-nodes snapshot.
//  2. Create an observer with the stub as its chain reader and refresh.
//  3. Assert requests are blocked with BlockReasonConfirmationPoC and LastError is empty.
//  4. Assert every participant stays preserved with its full, all-node weight.
func TestPhaseObserver_ConfirmationGraceMissingSnapshotPreservesAll(t *testing.T) {
	stub := newPhaseObserverStub()
	server := httptest.NewServer(stub.handler())
	defer server.Close()
	stub.setEpoch(http.StatusOK, observerEpochConfirmationJSON(ConfirmationPoCGracePeriod, 555))
	stub.setParticipants(http.StatusOK, observerTwoMinerParticipantsJSON(server.URL))
	stub.setPreservedNodes(nil, false, nil)

	observer := newPoCPhaseObserver(t, server, stub)
	observer.refresh(context.Background())
	snapshot := observer.Snapshot()

	if !snapshot.RequestsBlocked || snapshot.BlockReason != BlockReasonConfirmationPoC {
		t.Fatalf("RequestsBlocked/BlockReason = %v/%q, want true/%q", snapshot.RequestsBlocked, snapshot.BlockReason, BlockReasonConfirmationPoC)
	}
	if snapshot.LastError != "" {
		t.Fatalf("LastError = %q, want empty", snapshot.LastError)
	}
	if want := []string{"gonka1abc", "gonka1bcd"}; !reflect.DeepEqual(snapshot.Preserved, want) {
		t.Errorf("Preserved = %v, want all participants %v", snapshot.Preserved, want)
	}
	if want := map[string]float64{"gonka1abc": 100, "gonka1bcd": 40}; !reflect.DeepEqual(snapshot.CurrentWeights, want) {
		t.Errorf("CurrentWeights = %v, want all-node %v", snapshot.CurrentWeights, want)
	}
}

// Test flow:
//  1. Configure an Inference epoch whose next PoC starts at 950, with two miners only one of which keeps a node through PoC, and no chain reader.
//  2. Refresh at height 948, two blocks before the start, and again at 940.
//  3. Assert at 948 only the preserved miner is left serving while requests are not marked blocked, and at 940 nothing is narrowed: a request sent in the last poll before PoC would land on a host already generating.
func TestPhaseObserver_TheHostsPoCWillTakeStopServingTwoBlocksBeforeIt(t *testing.T) {
	stub := newPhaseObserverStub()
	server := httptest.NewServer(stub.handler())
	defer server.Close()
	stub.setParticipants(http.StatusOK, observerTwoMinerParticipantsJSON(server.URL))
	observer := newPoCPhaseObserver(t, server, nil)

	stub.setEpoch(http.StatusOK, observerEpochBeforePoCJSON(948, 950))
	observer.refresh(context.Background())
	imminent := observer.Snapshot()
	stub.setEpoch(http.StatusOK, observerEpochBeforePoCJSON(940, 950))
	observer.refresh(context.Background())
	early := observer.Snapshot()

	if want := []string{"gonka1bcd"}; !reflect.DeepEqual(imminent.Preserved, want) {
		t.Errorf("Preserved two blocks before PoC = %v, want %v", imminent.Preserved, want)
	}
	if imminent.RequestsBlocked {
		t.Error("RequestsBlocked two blocks before PoC = true, want false: only routing looks ahead, the chain phase is still Inference")
	}
	if slices.Equal(early.Preserved, []string{"gonka1bcd"}) {
		t.Errorf("Preserved ten blocks before PoC = %v, want every miner still serving", early.Preserved)
	}
}

// Test flow:
//  1. Build a table of snapshot-miss scenarios: table varies between no snapshot on chain, a failed read, an anchor mismatch, and no chain access configured at all.
//  2. Configure the stub with a PoCGenerate-phase epoch and two miners, and set the case's preserved-nodes response and chain reader.
//  3. Refresh and assert Preserved and CurrentWeights fall back to the participants-endpoint timeslot rule in every case.
//  4. Assert LastError matches the case's expected substring (or stays empty), and that no chain access means the preserved-nodes endpoint was never hit.
func TestPhaseObserver_PoCPreservedSnapshotMissesFallBackToLegacyRule(t *testing.T) {
	cases := []struct {
		name          string
		withChain     bool
		preserved     *PreservedNodes
		preservedHeld bool
		preservedErr  error
		wantErrSubstr string
		wantZeroHits  bool
	}{
		{name: "chain has no snapshot", withChain: true},
		{name: "read failed", withChain: true, preservedErr: errors.New("chain unreachable"), wantErrSubstr: "chain unreachable"},
		{name: "anchor mismatch", withChain: true, preserved: observerPreservedSnapshot(900), preservedHeld: true},
		{name: "no chain access", preserved: observerPreservedSnapshot(950), preservedHeld: true, wantZeroHits: true},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			stub := newPhaseObserverStub()
			server := httptest.NewServer(stub.handler())
			defer server.Close()
			stub.setEpoch(http.StatusOK, observerEpochPoCJSON(EpochPhasePoCGenerate, 950))
			stub.setParticipants(http.StatusOK, observerTwoMinerParticipantsJSON(server.URL))
			stub.setPreservedNodes(testCase.preserved, testCase.preservedHeld, testCase.preservedErr)

			var chainReader Reader
			if testCase.withChain {
				chainReader = stub
			}
			observer := newPoCPhaseObserver(t, server, chainReader)
			observer.refresh(context.Background())
			snapshot := observer.Snapshot()

			if want := []string{"gonka1bcd"}; !reflect.DeepEqual(snapshot.Preserved, want) {
				t.Errorf("Preserved = %v, want legacy %v", snapshot.Preserved, want)
			}
			if want := map[string]float64{"gonka1abc": 0, "gonka1bcd": 40}; !reflect.DeepEqual(snapshot.CurrentWeights, want) {
				t.Errorf("CurrentWeights = %v, want legacy %v", snapshot.CurrentWeights, want)
			}
			if testCase.wantErrSubstr == "" && snapshot.LastError != "" {
				t.Errorf("LastError = %q, want empty", snapshot.LastError)
			}
			if testCase.wantErrSubstr != "" && !strings.Contains(snapshot.LastError, testCase.wantErrSubstr) {
				t.Errorf("LastError = %q, want substring %q", snapshot.LastError, testCase.wantErrSubstr)
			}
			if testCase.wantZeroHits && stub.preservedHitCount() != 0 {
				t.Errorf("preserved reads = %d, want 0 when no chain access is configured", stub.preservedHitCount())
			}
		})
	}
}

// observerVersionsJSON builds a /v1/versions body advertising node1/node2 capability flags.
func observerVersionsJSON(node1Capable, node2Capable bool) string {
	return fmt.Sprintf(`{"mlnodes": [
		{"node_id": "node1", "poc_validation_inference": %t},
		{"node_id": "node2", "poc_validation_inference": %t}
	]}`, node1Capable, node2Capable)
}

// Test flow:
//  1. Configure the stub with a PoCValidate-phase epoch, two miners, and a versions response marking both nodes capable.
//  2. Refresh before polling versions and assert the cold snapshot stays conservative: the legacy-excluded miner has zero weight and is absent from Preserved.
//  3. Poll versions, refresh again, and assert the merged snapshot's Preserved, CurrentWeights, CurrentWeightsByModel, PreservedByModel and FullWeights all now include the capable miner's weight.
func TestPhaseObserver_ValidationMergeAddsCapableExcludedMiner(t *testing.T) {
	stub := newPhaseObserverStub()
	server := httptest.NewServer(stub.handler())
	defer server.Close()
	stub.setEpoch(http.StatusOK, observerEpochPoCJSON(EpochPhasePoCValidate, 950))
	stub.setParticipants(http.StatusOK, observerTwoMinerParticipantsJSON(server.URL))
	stub.setVersions(http.StatusOK, observerVersionsJSON(true, true))

	observer := newPoCPhaseObserver(t, server, stub)

	observer.refresh(context.Background())
	cold := observer.Snapshot()
	if want := []string{"gonka1bcd"}; !reflect.DeepEqual(cold.Preserved, want) {
		t.Fatalf("cold Preserved = %v, want %v (capability unknown => fail closed)", cold.Preserved, want)
	}
	if cold.CurrentWeights["gonka1abc"] != 0 {
		t.Fatalf("cold CurrentWeights[gonka1abc] = %v, want 0", cold.CurrentWeights["gonka1abc"])
	}

	observer.versions.Poll(context.Background())
	observer.refresh(context.Background())
	merged := observer.Snapshot()

	if want := []string{"gonka1abc", "gonka1bcd"}; !reflect.DeepEqual(merged.Preserved, want) {
		t.Errorf("merged Preserved = %v, want %v", merged.Preserved, want)
	}
	if want := map[string]float64{"gonka1abc": 100, "gonka1bcd": 40}; !reflect.DeepEqual(merged.CurrentWeights, want) {
		t.Errorf("merged CurrentWeights = %v, want %v", merged.CurrentWeights, want)
	}
	if want := map[string]map[string]float64{"model-a": {"gonka1abc": 100, "gonka1bcd": 40}}; !reflect.DeepEqual(merged.CurrentWeightsByModel, want) {
		t.Errorf("merged CurrentWeightsByModel = %v, want %v", merged.CurrentWeightsByModel, want)
	}
	if want := map[string][]string{"model-a": {"gonka1abc", "gonka1bcd"}}; !reflect.DeepEqual(merged.PreservedByModel, want) {
		t.Errorf("merged PreservedByModel = %v, want %v", merged.PreservedByModel, want)
	}
	if want := map[string]float64{"gonka1abc": 100, "gonka1bcd": 40}; !reflect.DeepEqual(merged.FullWeights, want) {
		t.Errorf("merged FullWeights = %v, want %v", merged.FullWeights, want)
	}
}

// Test flow:
//  1. Configure the stub with a PoCValidate-phase epoch, two miners, and a versions response marking only the second miner's node capable.
//  2. Poll versions and refresh.
//  3. Assert Preserved and PreservedByModel exclude the non-capable miner and its current weight stays zero.
func TestPhaseObserver_ValidationMergeExcludesNonCapableNode(t *testing.T) {
	stub := newPhaseObserverStub()
	server := httptest.NewServer(stub.handler())
	defer server.Close()
	stub.setEpoch(http.StatusOK, observerEpochPoCJSON(EpochPhasePoCValidate, 950))
	stub.setParticipants(http.StatusOK, observerTwoMinerParticipantsJSON(server.URL))
	stub.setVersions(http.StatusOK, observerVersionsJSON(false, true))

	observer := newPoCPhaseObserver(t, server, stub)
	observer.refresh(context.Background())
	observer.versions.Poll(context.Background())
	observer.refresh(context.Background())
	snapshot := observer.Snapshot()

	if want := []string{"gonka1bcd"}; !reflect.DeepEqual(snapshot.Preserved, want) {
		t.Errorf("Preserved = %v, want %v (non-capable node1 excluded)", snapshot.Preserved, want)
	}
	if snapshot.CurrentWeights["gonka1abc"] != 0 {
		t.Errorf("CurrentWeights[gonka1abc] = %v, want 0", snapshot.CurrentWeights["gonka1abc"])
	}
	if want := map[string][]string{"model-a": {"gonka1bcd"}}; !reflect.DeepEqual(snapshot.PreservedByModel, want) {
		t.Errorf("PreservedByModel = %v, want %v", snapshot.PreservedByModel, want)
	}
}

// Test flow:
//  1. Configure the stub with a PoCGenerate-phase epoch, two miners, and a versions response marking both nodes capable.
//  2. Poll versions and refresh.
//  3. Assert the excluded miner stays out of Preserved and its current weight stays zero, since the merge only applies during a validation stage.
func TestPhaseObserver_GenerationPhaseSkipsValidationMerge(t *testing.T) {
	stub := newPhaseObserverStub()
	server := httptest.NewServer(stub.handler())
	defer server.Close()
	stub.setEpoch(http.StatusOK, observerEpochPoCJSON(EpochPhasePoCGenerate, 950))
	stub.setParticipants(http.StatusOK, observerTwoMinerParticipantsJSON(server.URL))
	stub.setVersions(http.StatusOK, observerVersionsJSON(true, true))

	observer := newPoCPhaseObserver(t, server, stub)
	observer.refresh(context.Background())
	observer.versions.Poll(context.Background())
	observer.refresh(context.Background())
	snapshot := observer.Snapshot()

	if want := []string{"gonka1bcd"}; !reflect.DeepEqual(snapshot.Preserved, want) {
		t.Errorf("Preserved = %v, want %v (no merge during generation)", snapshot.Preserved, want)
	}
	if snapshot.CurrentWeights["gonka1abc"] != 0 {
		t.Errorf("CurrentWeights[gonka1abc] = %v, want 0", snapshot.CurrentWeights["gonka1abc"])
	}
}

// Test flow:
//  1. Register a leak check deferred first so it runs last, after the server is closed.
//  2. Configure the stub and create an observer with the stub as its chain reader.
//  3. Call Start twice in a row without panicking, then Stop.
//  4. Call Start and Stop once more, relying on the deferred leak check to assert no poller from either round was orphaned.
func TestPhaseObserver_StartTwiceIsANoOpAndRestartWorks(t *testing.T) {
	defer leakcheck.VerifyNone(t)
	stub := newPhaseObserverStub()
	server := httptest.NewServer(stub.handler())
	defer server.Close()
	stub.setEpoch(http.StatusOK, observerEpochJSON(1000, 7, EpochPhaseInference))
	stub.setParticipants(http.StatusOK, observerParticipantsJSON("gonka1abc", server.URL, 42))
	observer := newPoCPhaseObserver(t, server, stub)

	ctx := context.Background()
	observer.Start(ctx)
	observer.Start(ctx)
	observer.Stop()

	observer.Start(ctx)
	observer.Stop()
}

// Test flow:
//  1. Build a sequence of steps for a shared snapshotHealth: table varies through a healthy start, a first failure, the same failure repeated, a different failure, recovery, and staying healthy.
//  2. Advance the snapshotHealth through each step in order.
//  3. Assert each step reports degraded or recovered only on the steps that actually turn, and stays silent on every repeat.
func TestSnapshotHealth_AdvanceSpeaksOnlyOnChange(t *testing.T) {
	t.Parallel()
	steps := []struct {
		name          string
		lastError     string
		wantDegraded  bool
		wantRecovered bool
	}{
		{name: "first healthy poll says nothing", lastError: ""},
		{name: "first failure degrades", lastError: "fetch epoch info: 503", wantDegraded: true},
		{name: "same failure again stays silent", lastError: "fetch epoch info: 503"},
		{name: "a different failure stays silent", lastError: "fetch participants: parse"},
		{name: "success recovers", lastError: "", wantRecovered: true},
		{name: "still healthy stays silent", lastError: ""},
	}
	var health snapshotHealth
	for _, step := range steps {
		t.Run(step.name, func(t *testing.T) {
			change := health.advance(step.lastError)
			if change.degraded != step.wantDegraded {
				t.Errorf("degraded = %v, want %v", change.degraded, step.wantDegraded)
			}
			if change.recovered != step.wantRecovered {
				t.Errorf("recovered = %v, want %v", change.recovered, step.wantRecovered)
			}
		})
	}
}

// Test flow:
//  1. Configure the stub with an epoch, one participant, and a governance model parameter, then refresh and capture the snapshot as the known-good baseline.
//  2. Make the models read fail and refresh again.
//  3. Assert the model's context window still reflects the prior value.
//  4. Assert LastError names the failed governance-models read.
func TestPhaseObserver_ModelsFetchErrorKeepsPriorValue(t *testing.T) {
	stub := newPhaseObserverStub()
	server := httptest.NewServer(stub.handler())
	defer server.Close()
	stub.setEpoch(http.StatusOK, observerEpochJSON(42, 4, EpochPhaseInference))
	stub.setParticipants(http.StatusOK, observerParticipantsJSON("gonka1abc", server.URL, 7))
	stub.setModels(map[string]ModelParams{"model-a": {ContextWindow: 400_000}}, nil)

	observer := newPoCPhaseObserver(t, server, stub)
	observer.refresh(context.Background())
	good := observer.Snapshot()
	if got := good.Models["model-a"].ContextWindow; got != 400_000 {
		t.Fatalf("precondition: context window = %d, want the 400000 governance reported", got)
	}

	stub.setModels(nil, errors.New("chain unreachable"))
	observer.refresh(context.Background())
	after := observer.Snapshot()

	if got := after.Models["model-a"].ContextWindow; got != 400_000 {
		t.Errorf("context window after a failed read = %d, want the prior 400000 preserved", got)
	}
	if !strings.Contains(after.LastError, "fetch governance models") {
		t.Errorf("LastError = %q, want it to name the failed models read", after.LastError)
	}
}
