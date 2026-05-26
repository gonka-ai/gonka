package poc

import (
	"context"
	"crypto/sha256"
	"errors"
	"sort"
	"sync"
	"time"

	"decentralized-api/broker"
	"decentralized-api/chainphase"
	"decentralized-api/cosmosclient"
	"decentralized-api/logging"
	"decentralized-api/mlnodeclient"

	inferencetypes "github.com/productscience/inference/x/inference/types"
)

const (
	teeIdentityGatherTimeout = 10 * time.Second
	teeParamsQueryTimeout    = 5 * time.Second
)

type TeeChainParamsProvider interface {
	GetTeeAttestationParams(ctx context.Context) (*inferencetypes.TeeAttestationParams, error)
}

func NewRecorderTeeChainParams(recorder cosmosclient.CosmosMessageClient) TeeChainParamsProvider {
	return &recorderTeeChainParams{recorder: recorder}
}

type recorderTeeChainParams struct {
	recorder cosmosclient.CosmosMessageClient
}

func (p *recorderTeeChainParams) GetTeeAttestationParams(ctx context.Context) (*inferencetypes.TeeAttestationParams, error) {
	qc := p.recorder.NewInferenceQueryClient()
	resp, err := qc.Params(ctx, &inferencetypes.QueryParamsRequest{})
	if err != nil {
		return nil, err
	}
	if resp == nil {
		return nil, nil
	}
	return resp.Params.TeeAttestationParams, nil
}

type TeeAttestationWorker struct {
	recorder    cosmosclient.CosmosMessageClient
	nodeBroker  TeeNodeBroker
	tracker     *chainphase.ChainPhaseTracker
	chainParams TeeChainParamsProvider

	interval time.Duration
	stop     chan struct{}
	done     chan struct{}

	mu                  sync.Mutex
	enabled             bool
	disabledReason      string
	lastSubmittedHeight int64
	lastSubmittedCount  int
	lastAttemptAt       time.Time
	lastAttemptHeight   int64
	lastError           string

	stagePocHeight int64
	stageSubmitted map[string]struct{}
	stageCompleted bool
}

type TeeWorkerStatus struct {
	Enabled             bool      `json:"enabled"`
	DisabledReason      string    `json:"disabled_reason,omitempty"`
	IntervalSeconds     float64   `json:"interval_seconds"`
	LastSubmittedHeight int64     `json:"last_submitted_height"`
	LastSubmittedCount  int       `json:"last_submitted_count"`
	LastAttemptHeight   int64     `json:"last_attempt_height"`
	LastAttemptAt       time.Time `json:"last_attempt_at"`
	LastError           string    `json:"last_error"`
}

func (w *TeeAttestationWorker) Status() TeeWorkerStatus {
	w.mu.Lock()
	defer w.mu.Unlock()
	return TeeWorkerStatus{
		Enabled:             w.enabled,
		DisabledReason:      w.disabledReason,
		IntervalSeconds:     w.interval.Seconds(),
		LastSubmittedHeight: w.lastSubmittedHeight,
		LastSubmittedCount:  w.lastSubmittedCount,
		LastAttemptHeight:   w.lastAttemptHeight,
		LastAttemptAt:       w.lastAttemptAt,
		LastError:           w.lastError,
	}
}

type TeeNodeBroker interface {
	GetNodes() ([]broker.NodeResponse, error)
	NewNodeClient(node *broker.Node) mlnodeclient.MLNodeClient
}

func NewTeeAttestationWorker(
	recorder cosmosclient.CosmosMessageClient,
	nodeBroker TeeNodeBroker,
	tracker *chainphase.ChainPhaseTracker,
	chainParams TeeChainParamsProvider,
	interval time.Duration,
) *TeeAttestationWorker {
	w := &TeeAttestationWorker{
		recorder:    recorder,
		nodeBroker:  nodeBroker,
		tracker:     tracker,
		chainParams: chainParams,
		interval:    interval,
		stop:        make(chan struct{}),
		done:        make(chan struct{}),
		enabled:     true,
	}
	disable := func(reason string) *TeeAttestationWorker {
		w.enabled = false
		w.disabledReason = reason
		close(w.done)
		logging.Warn("TeeAttestationWorker disabled", inferencetypes.PoC,
			"reason", w.disabledReason,
			"tee_attestation_unavailable", true)
		return w
	}
	if recorder == nil {
		return disable("no recorder")
	}
	if nodeBroker == nil {
		return disable("no node broker")
	}
	if tracker == nil {
		return disable("no chain phase tracker")
	}
	if interval <= 0 {
		return disable("interval<=0")
	}
	go w.run()
	logging.Info("TeeAttestationWorker started", inferencetypes.PoC, "interval", interval)
	return w
}

func (w *TeeAttestationWorker) Close() {
	select {
	case <-w.stop:
		return
	default:
		close(w.stop)
	}
	<-w.done
	logging.Info("TeeAttestationWorker stopped", inferencetypes.PoC)
}

func (w *TeeAttestationWorker) run() {
	defer close(w.done)
	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			w.tick()
		case <-w.stop:
			return
		}
	}
}

func (w *TeeAttestationWorker) tick() {
	epochState := w.tracker.GetCurrentEpochState()
	if epochState == nil || !epochState.IsSynced {
		return
	}
	if epochState.CurrentPhase == inferencetypes.InferencePhase && epochState.ActiveConfirmationPoCEvent != nil {
		return
	}
	pocHeight := GetCurrentPocStageHeight(epochState)
	if pocHeight <= 0 {
		return
	}
	if !ShouldAcceptStoreCommit(epochState, pocHeight) {
		return
	}

	w.mu.Lock()
	if w.stagePocHeight != pocHeight {
		w.stagePocHeight = pocHeight
		w.stageSubmitted = make(map[string]struct{})
		w.stageCompleted = false
	}
	if w.stageCompleted {
		w.mu.Unlock()
		return
	}
	w.mu.Unlock()

	gatherStart := time.Now()
	entries, hadGatherErrors := w.gatherAttestations()
	if elapsed := time.Since(gatherStart); elapsed > w.interval {
		logging.Warn("TeeAttestationWorker: gatherAttestations exceeded tick interval",
			inferencetypes.PoC,
			"pocHeight", pocHeight,
			"elapsed_ms", elapsed.Milliseconds(),
			"interval_ms", w.interval.Milliseconds(),
			"nodes", len(entries),
		)
	}
	if len(entries) == 0 {
		logging.Debug("TeeAttestationWorker: no eligible nodes for stage",
			inferencetypes.PoC, "pocHeight", pocHeight)
		return
	}

	pending := w.filterPending(entries)
	if len(pending) == 0 {
		if hadGatherErrors {
			logging.Debug("TeeAttestationWorker: pending is empty but gather had retryable errors; keeping stage open",
				inferencetypes.PoC, "pocHeight", pocHeight)
			return
		}
		w.markStageCompleted(pocHeight)
		return
	}

	sort.Slice(pending, func(i, j int) bool {
		return pending[i].NodeId < pending[j].NodeId
	})

	maxEntries := w.resolveMaxEntries()
	chunks := chunkEntries(pending, maxEntries)
	allChunksSubmitted := true
	for _, chunk := range chunks {
		if err := w.submitChunkWithFallback(pocHeight, chunk); err != nil {
			allChunksSubmitted = false
		}
	}

	if allChunksSubmitted && !hadGatherErrors {
		w.markStageCompleted(pocHeight)
	}
}

func chunkEntries[T any](entries []T, max int) [][]T {
	if max <= 0 || len(entries) <= max {
		return [][]T{entries}
	}
	out := make([][]T, 0, (len(entries)+max-1)/max)
	for i := 0; i < len(entries); i += max {
		end := i + max
		if end > len(entries) {
			end = len(entries)
		}
		out = append(out, entries[i:end])
	}
	return out
}

func (w *TeeAttestationWorker) submitChunkWithFallback(pocHeight int64, chunk []*inferencetypes.TeeAttestationEntry) error {
	if len(chunk) == 0 {
		return nil
	}
	if err := w.submitChunk(pocHeight, chunk); err == nil {
		return nil
	} else if len(chunk) == 1 {
		return err
	}

	logging.Warn("TeeAttestationWorker: chunk failed, retrying entries one-by-one",
		inferencetypes.PoC,
		"pocHeight", pocHeight,
		"chunk_entries", len(chunk),
	)
	var firstErr error
	for _, entry := range chunk {
		if entry == nil {
			continue
		}
		if err := w.submitChunk(pocHeight, []*inferencetypes.TeeAttestationEntry{entry}); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func (w *TeeAttestationWorker) submitChunk(pocHeight int64, chunk []*inferencetypes.TeeAttestationEntry) error {
	msg := &inferencetypes.MsgSubmitTeeAttestation{
		PocStageStartBlockHeight: pocHeight,
		Attestations:             chunk,
	}
	if err := w.recorder.SubmitTeeAttestation(msg); err != nil {
		w.recordChunkError(pocHeight, err)
		logging.Warn("TeeAttestationWorker: chunk submission failed; will resume on next tick",
			inferencetypes.PoC,
			"pocHeight", pocHeight,
			"chunk_entries", len(chunk),
			"error", err,
		)
		return err
	}
	w.recordChunkSubmitted(pocHeight, chunk)
	return nil
}

func (w *TeeAttestationWorker) filterPending(entries []*inferencetypes.TeeAttestationEntry) []*inferencetypes.TeeAttestationEntry {
	w.mu.Lock()
	submitted := w.stageSubmitted
	w.mu.Unlock()
	if len(submitted) == 0 {
		return entries
	}
	pending := make([]*inferencetypes.TeeAttestationEntry, 0, len(entries))
	for _, e := range entries {
		if _, done := submitted[e.NodeId]; done {
			continue
		}
		pending = append(pending, e)
	}
	return pending
}

func (w *TeeAttestationWorker) resolveMaxEntries() int {
	n := int(inferencetypes.DefaultMaxEntriesPerTeeAttestationMessage)
	if w.chainParams == nil {
		return n
	}
	ctx, cancel := context.WithTimeout(context.Background(), teeParamsQueryTimeout)
	params, err := w.chainParams.GetTeeAttestationParams(ctx)
	cancel()
	if err != nil {
		logging.Warn("TeeAttestationWorker: failed to read tee_attestation_params, using default",
			inferencetypes.PoC, "default", n, "error", err)
		return n
	}
	if params != nil && params.MaxEntriesPerMessage > 0 {
		n = int(params.MaxEntriesPerMessage)
	}
	return n
}

func (w *TeeAttestationWorker) recordChunkSubmitted(pocHeight int64, chunk []*inferencetypes.TeeAttestationEntry) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.stagePocHeight != pocHeight {
		return
	}
	if w.stageSubmitted == nil {
		w.stageSubmitted = make(map[string]struct{}, len(chunk))
	}
	for _, e := range chunk {
		w.stageSubmitted[e.NodeId] = struct{}{}
	}
	w.lastSubmittedCount = len(w.stageSubmitted)
	w.lastAttemptAt = time.Now().UTC()
	w.lastAttemptHeight = pocHeight
	w.lastSubmittedHeight = pocHeight
	w.lastError = ""
}

func (w *TeeAttestationWorker) recordChunkError(pocHeight int64, err error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.lastAttemptAt = time.Now().UTC()
	w.lastAttemptHeight = pocHeight
	w.lastError = err.Error()
}

func (w *TeeAttestationWorker) markStageCompleted(pocHeight int64) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.stagePocHeight != pocHeight {
		return
	}
	w.stageCompleted = true
	w.lastSubmittedHeight = pocHeight
	logging.Info("TeeAttestationWorker: stage submitted", inferencetypes.PoC,
		"pocHeight", pocHeight, "entries", w.lastSubmittedCount)
}

func (w *TeeAttestationWorker) gatherAttestations() ([]*inferencetypes.TeeAttestationEntry, bool) {
	nodes, err := w.nodeBroker.GetNodes()
	if err != nil {
		logging.Warn("TeeAttestationWorker: failed to fetch broker nodes",
			inferencetypes.PoC, "error", err)
		return nil, true
	}
	if len(nodes) == 0 {
		return nil, false
	}

	entries := make([]*inferencetypes.TeeAttestationEntry, 0, len(nodes))
	hadRetryableErrors := false
	for i := range nodes {
		node := nodes[i].Node
		identity, err := w.fetchIdentity(&node)
		if err != nil {
			if errors.Is(err, mlnodeclient.ErrEncryptedIdentityDisabled) {
				logging.Debug("TeeAttestationWorker: node has no encrypted identity",
					inferencetypes.PoC, "node_id", node.Id)
				continue
			}
			logging.Warn("TeeAttestationWorker: failed to fetch identity",
				inferencetypes.PoC, "node_id", node.Id, "error", err)
			hadRetryableErrors = true
			continue
		}
		if identity == nil || len(identity.PublicKey) == 0 || identity.Provider == "" {
			logging.Warn("TeeAttestationWorker: node returned empty identity, skipping",
				inferencetypes.PoC, "node_id", node.Id)
			hadRetryableErrors = true
			continue
		}
		if len(identity.Quote) == 0 {
			logging.Warn("TeeAttestationWorker: node returned empty quote, skipping",
				inferencetypes.PoC, "node_id", node.Id, "provider", identity.Provider)
			hadRetryableErrors = true
			continue
		}

		quoteHash := sha256.Sum256(identity.Quote)
		entries = append(entries, &inferencetypes.TeeAttestationEntry{
			NodeId:          node.Id,
			PublicKey:       identity.PublicKey,
			Provider:        identity.Provider,
			Measurement:     identity.Measurement,
			QuoteHash:       quoteHash[:],
			EnvelopeVersion: identity.EnvelopeVersion,
			Quote:           identity.Quote,
		})
	}
	return entries, hadRetryableErrors
}

func (w *TeeAttestationWorker) fetchIdentity(node *broker.Node) (*mlnodeclient.EncryptedIdentity, error) {
	ctx, cancel := context.WithTimeout(context.Background(), teeIdentityGatherTimeout)
	defer cancel()
	client := w.nodeBroker.NewNodeClient(node)
	return client.GetEncryptedIdentity(ctx)
}
