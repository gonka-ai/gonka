package chain

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	DefaultObserverPollInterval = 5 * time.Second
	DefaultVersionsPollInterval = 15 * time.Second
	refreshDeadlineMultiple     = 6
	versionsTTLPollMultiplier   = 3
	observerHTTPTimeout         = 5 * time.Second

	epochInfoPath    = "/v1/epochs/latest"
	participantsPath = "/v1/epochs/current/participants"
)

// ObserverConfig configures a PhaseObserver; PublicAPIBaseURL is required, and a nil Chain skips the preserved-nodes snapshot and the nonce ceiling. See README.md, "Which nodes count as preserved".
type ObserverConfig struct {
	PublicAPIBaseURL     string
	Chain                Reader
	PollInterval         time.Duration
	VersionsPollInterval time.Duration
	HTTPClient           *http.Client
	Now                  func() time.Time
}

// healthNarrator is satisfied by *journal.Journal; chain never imports it. See README.md, "The poll loop".
type healthNarrator interface {
	ChainSnapshotStale(lastError string, epoch uint64, height int64)
	ChainSnapshotRecovered(epoch uint64, height int64)
}

// PhaseObserver polls chain phase and participant state and publishes an immutable PhaseSnapshot. See README.md, "What the chain observer provides".
type PhaseObserver struct {
	publicAPIBaseURL     string
	chain                Reader
	client               *http.Client
	pollInterval         time.Duration
	versionsPollInterval time.Duration
	now                  func() time.Time
	versions             *VersionsCache

	current  atomic.Pointer[PhaseSnapshot]
	health   snapshotHealth
	narrator healthNarrator

	mu          sync.Mutex
	subscribers map[int]func(PhaseSnapshot)
	nextID      int

	lifecycleMu sync.Mutex
	cancel      context.CancelFunc
	doneCh      chan struct{}
}

// NewPhaseObserver validates cfg and applies defaults for unset fields; it errors only when PublicAPIBaseURL is blank.
func NewPhaseObserver(cfg ObserverConfig) (*PhaseObserver, error) {
	baseURL := strings.TrimRight(strings.TrimSpace(cfg.PublicAPIBaseURL), "/")
	if baseURL == "" {
		return nil, fmt.Errorf("public API base URL is required")
	}
	pollInterval := cfg.PollInterval
	if pollInterval <= 0 {
		pollInterval = DefaultObserverPollInterval
	}
	versionsPollInterval := cfg.VersionsPollInterval
	if versionsPollInterval <= 0 {
		versionsPollInterval = DefaultVersionsPollInterval
	}
	client := cfg.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: observerHTTPTimeout}
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}

	observer := &PhaseObserver{
		publicAPIBaseURL:     baseURL,
		chain:                cfg.Chain,
		client:               client,
		pollInterval:         pollInterval,
		versionsPollInterval: versionsPollInterval,
		now:                  now,
		versions:             NewVersionsCache(newVersionsClient(), versionsTTLPollMultiplier*versionsPollInterval, now),
		subscribers:          make(map[int]func(PhaseSnapshot)),
	}
	observer.current.Store(&PhaseSnapshot{})
	return observer, nil
}

// Start spawns the poll loop and the versions poller, and is idempotent. See README.md, "The poll loop".
func (o *PhaseObserver) Start(ctx context.Context) {
	o.lifecycleMu.Lock()
	defer o.lifecycleMu.Unlock()
	if o.cancel != nil {
		return
	}
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	o.cancel, o.doneCh = cancel, done

	var running sync.WaitGroup
	running.Go(func() { o.run(ctx) })
	running.Go(func() { o.versions.Run(ctx, o.versionsPollInterval) })
	go func() {
		running.Wait()
		close(done)
	}()
}

// Stop blocks until both loops have exited and is a barrier for every caller. See README.md, "The poll loop".
func (o *PhaseObserver) Stop() {
	o.lifecycleMu.Lock()
	cancel, done := o.cancel, o.doneCh
	o.cancel = nil
	o.lifecycleMu.Unlock()
	if cancel != nil {
		cancel()
	}
	if done != nil {
		<-done
	}
}

func (o *PhaseObserver) run(ctx context.Context) {
	o.refreshBounded(ctx)
	ticker := time.NewTicker(o.pollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			o.refreshBounded(ctx)
		}
	}
}

// refreshBounded gives one poll its own deadline. See README.md, "What the chain observer provides".
func (o *PhaseObserver) refreshBounded(ctx context.Context) {
	bounded, cancel := context.WithTimeout(ctx, o.pollInterval*refreshDeadlineMultiple)
	defer cancel()
	o.refresh(bounded)
}

// refresh runs one poll and publishes a snapshot. See README.md, "What the chain observer provides".
func (o *PhaseObserver) refresh(ctx context.Context) {
	previous := o.Snapshot()

	epoch, err := o.fetchEpochInfo(ctx)
	if err != nil {
		previous.LastError = fmt.Sprintf("fetch epoch info: %v", err)
		o.publish(previous)
		return
	}

	blocked, reason := rawPoCBlockingState(epoch.Phase, epoch.ConfirmationPoCPhase)
	snapshot := PhaseSnapshot{
		BlockHeight:            epoch.BlockHeight,
		EpochSwitchBlockHeight: epoch.EpochSwitchBlockHeight,
		EpochIndex:             epoch.EpochIndex,
		EffectiveEpochIndex:    epoch.EffectiveEpochIndex,
		EpochPhase:             epoch.Phase,
		ConfirmationPoCPhase:   epoch.ConfirmationPoCPhase,
		RequestsBlocked:        blocked,
		BlockReason:            reason,
		LastUpdatedAt:          o.now(),
	}

	if models, fetched, modelsErr := o.fetchModels(ctx); fetched {
		snapshot.Models = models
	} else {
		snapshot.Models = previous.Models
		if modelsErr != nil {
			snapshot.LastError = joinSnapshotError(snapshot.LastError, fmt.Sprintf("fetch governance models: %v", modelsErr))
		}
	}

	if params, fetched, paramsErr := o.fetchEscrowParams(ctx); fetched {
		snapshot.MaxNonce, snapshot.TokenPrice, snapshot.FeePerNonce, snapshot.CreateDevshardFee = params.MaxNonce, params.TokenPrice, params.FeePerNonce, params.CreateDevshardFee
	} else {
		snapshot.MaxNonce, snapshot.TokenPrice, snapshot.FeePerNonce, snapshot.CreateDevshardFee = previous.MaxNonce, previous.TokenPrice, previous.FeePerNonce, previous.CreateDevshardFee
		if paramsErr != nil {
			snapshot.LastError = joinSnapshotError(snapshot.LastError, fmt.Sprintf("fetch devshard escrow params: %v", paramsErr))
		}
	}

	participantsBody, err := o.getBody(ctx, participantsPath)
	if err != nil {
		o.publishWithPreviousParticipants(snapshot, previous, fmt.Sprintf("fetch participants: %v", err))
		return
	}

	preservedEpoch, preservedBlocked, preservedReason := preservationEpoch(epoch, blocked, reason)
	preservation, preservedNodes, preservationErr := o.resolvePreservation(ctx, preservedEpoch, preservedBlocked, preservedReason)
	if preservationErr != nil {
		// Non-fatal: the preserved-set view falls back to the legacy rule for this poll.
		snapshot.LastError = joinSnapshotError(snapshot.LastError, fmt.Sprintf("fetch preserved snapshot: %v", preservationErr))
	}

	participants, err := parseParticipants(participantsBody, preservation, preservedNodes)
	if err != nil {
		o.publishWithPreviousParticipants(snapshot, previous, fmt.Sprintf("fetch participants: %v", err))
		return
	}

	o.versions.SetCandidates(participants.InferenceURLs)

	preserved := participants.Preserved
	preservedByModel := participants.PreservedByModel
	currentWeights := participants.Weights
	currentWeightsByModel := participants.WeightsByModel
	if rawPoCValidationState(epoch.Phase, epoch.ConfirmationPoCPhase) {
		// See README.md, "PoC validation rejoins capable miners".
		preserved, preservedByModel, currentWeights, currentWeightsByModel = mergePreservedWithValidationCapable(participants, o.versions.IsNodeValidationCapable)
	}

	snapshot.CurrentWeights = currentWeights
	snapshot.FullWeights = participants.FullWeights
	snapshot.CurrentWeightsByModel = currentWeightsByModel
	snapshot.FullWeightsByModel = participants.FullWeightsByModel
	snapshot.Preserved = preserved
	snapshot.PreservedByModel = preservedByModel
	snapshot.InferenceURLs = participants.InferenceURLs
	snapshot.LastHealthyAt = snapshot.LastUpdatedAt

	o.publish(snapshot)
}

// fetchModels reads what governance says about each model; fetched=false with a nil error means no chain access.
func (o *PhaseObserver) fetchModels(ctx context.Context) (models map[string]ModelParams, fetched bool, err error) {
	if o.chain == nil {
		return nil, false, nil
	}
	models, err = o.chain.Models(ctx)
	return models, err == nil && len(models) > 0, err
}

// fetchEscrowParams reads the nonce ceiling and the escrow price; fetched=false with a nil error means the chain carries no devshard escrow params.
func (o *PhaseObserver) fetchEscrowParams(ctx context.Context) (params EscrowParams, fetched bool, err error) {
	if o.chain == nil {
		return EscrowParams{}, false, nil
	}
	return o.chain.EscrowParams(ctx)
}

func (o *PhaseObserver) fetchEpochInfo(ctx context.Context) (epochInfo, error) {
	body, err := o.getBody(ctx, epochInfoPath)
	if err != nil {
		return epochInfo{}, err
	}
	return parseEpochInfo(body)
}

func (o *PhaseObserver) getBody(ctx context.Context, path string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, o.publicAPIBaseURL+path, nil)
	if err != nil {
		return nil, err
	}
	resp, err := o.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		io.Copy(io.Discard, resp.Body)
		return nil, fmt.Errorf("%s status %d", path, resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
}
