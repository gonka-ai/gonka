package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"maps"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"common/chain"

	inferencetypes "github.com/productscience/inference/x/inference/types"
)

const (
	defaultChainPhasePollInterval = 5 * time.Second

	// versionsTTLPollMultiplier scales the poll interval into how long a
	// /v1/versions fetch stays fresh, so entries survive a couple of missed polls
	// but never outlive the poller's cadence.
	versionsTTLPollMultiplier = 3

	epochPhaseInference           = "Inference"
	epochPhasePoCGenerate         = "PoCGenerate"
	epochPhasePoCGenerateWindDown = "PoCGenerateWindDown"
	epochPhasePoCValidate         = "PoCValidate"
	epochPhasePoCValidateWindDown = "PoCValidateWindDown"

	confirmationPoCInactive    = "CONFIRMATION_POC_INACTIVE"
	confirmationPoCGracePeriod = "CONFIRMATION_POC_GRACE_PERIOD"
	confirmationPoCGeneration  = "CONFIRMATION_POC_GENERATION"
	confirmationPoCValidation  = "CONFIRMATION_POC_VALIDATION"
	confirmationPoCCompleted   = "CONFIRMATION_POC_COMPLETED"
)

type ChainPhaseSnapshot struct {
	BlockHeight          int64     `json:"block_height,omitempty"`
	EpochIndex           uint64    `json:"epoch_index,omitempty"`
	EpochPhase           string    `json:"chain_phase,omitempty"`
	ConfirmationPoCPhase string    `json:"confirmation_poc_phase,omitempty"`
	RequestsBlocked      bool      `json:"requests_blocked"`
	BlockReason          string    `json:"block_reason,omitempty"`
	LastUpdatedAt        time.Time `json:"last_updated_at,omitempty"`
	LastError            string    `json:"last_error,omitempty"`

	pocStartBlockHeight          int64
	epochSwitchBlockHeight       int64
	confirmationPoCTriggerHeight int64
}

type ChainPhaseGate struct {
	endpoint                      string
	participantsEndpoint          string
	preservedSnapshotEndpoint     string
	client                        *http.Client
	pollInterval                  time.Duration
	defaultMaxSpeculativeAttempts int

	mu       sync.RWMutex
	snapshot ChainPhaseSnapshot

	// capacityState receives per-host weights and the preserved-host
	// set on every refresh so it can keep W_tot, W(e), and the
	// GatewayLimiter scale factor in sync with chain phase transitions.
	// Reactive throttle is consulted live by the state itself via the
	// availability callback wired separately (SetLiveAvailable on the
	// state at gateway construction). Refresh-time scale propagation
	// (e.g. ApplyScaleFactor on the GatewayLimiter) is the
	// scaleApplyHook's responsibility.
	capacityState  *CapacityState
	scaleApplyHook func(scale float64)

	chainQuery chain.InferenceClient

	// versions polls each candidate miner's /v1/versions endpoint
	versions *VersionsCache

	stopCh chan struct{}
	doneCh chan struct{}
}

type chainEpochInfoResponse struct {
	BlockHeight             jsonInt64                         `json:"block_height"`
	Phase                   string                            `json:"phase"`
	LatestEpoch             chainLatestEpoch                  `json:"latest_epoch"`
	EpochStages             chainEpochStages                  `json:"epoch_stages"`
	NextEpochStages         chainEpochStages                  `json:"next_epoch_stages"`
	IsConfirmationPoCActive bool                              `json:"is_confirmation_poc_active"`
	ActiveConfirmationPoC   *chainConfirmationPoCEventPayload `json:"active_confirmation_poc_event,omitempty"`
}

type chainLatestEpoch struct {
	Index               jsonUint64 `json:"index"`
	PocStartBlockHeight jsonInt64  `json:"poc_start_block_height"`
}

type chainEpochStages struct {
	EpochIndex       jsonUint64 `json:"epoch_index"`
	SetNewValidators jsonInt64  `json:"set_new_validators"`
	NextPoCStart     jsonInt64  `json:"next_poc_start"`
}

type chainConfirmationPoCEventPayload struct {
	Phase         confirmationPoCPhaseValue `json:"phase"`
	TriggerHeight jsonInt64                 `json:"trigger_height"`
}

type chainCurrentParticipantsResponse struct {
	ActiveParticipants chainActiveParticipantsGroup `json:"active_participants"`
}

type chainActiveParticipantsGroup struct {
	Participants []chainActiveParticipant `json:"participants"`
}

type chainActiveParticipant struct {
	Index        string              `json:"index"`
	InferenceURL string              `json:"inference_url"`
	Weight       jsonUint64          `json:"weight,omitempty"`
	Models       []string            `json:"models,omitempty"`
	MLNodes      []chainModelMLNodes `json:"ml_nodes"`
}

type chainModelMLNodes struct {
	MLNodes []chainMLNodeInfo `json:"ml_nodes"`
}

type chainMLNodeInfo struct {
	NodeID             string     `json:"node_id"`
	TimeslotAllocation []bool     `json:"timeslot_allocation"`
	PoCWeight          jsonUint64 `json:"poc_weight,omitempty"`
}

type chainPreservedNodesSnapshotResponse struct {
	Snapshot *chainPreservedNodesSnapshot `json:"snapshot,omitempty"`
	Found    bool                         `json:"found"`
}

type chainPreservedNodesSnapshot struct {
	EpisodeAnchorHeight jsonInt64                  `json:"episode_anchor_height"`
	ModelPreservedNodes []chainModelPreservedNodes `json:"model_preserved_nodes"`
}

type chainModelPreservedNodes struct {
	ModelID      string                           `json:"model_id"`
	Participants []chainParticipantPreservedNodes `json:"participants"`
}

type chainParticipantPreservedNodes struct {
	ParticipantID string   `json:"participant_id"`
	NodeIDs       []string `json:"node_ids"`
}

type preservedSnapshotState struct {
	byModel map[string]map[string]map[string]struct{}
}

type preservedSnapshotStatus int

const (
	preservedSnapshotUnavailable preservedSnapshotStatus = iota
	preservedSnapshotCurrent
	preservedSnapshotMissingCurrent
)

type preservationMode int

const (
	preservationModeLegacy preservationMode = iota
	preservationModeSnapshot
	preservationModeAll
)

type jsonInt64 int64

func (n *jsonInt64) UnmarshalJSON(data []byte) error {
	parsed, err := parseFlexibleInt64(data)
	if err != nil {
		return err
	}
	*n = jsonInt64(parsed)
	return nil
}

type jsonUint64 uint64

func (n *jsonUint64) UnmarshalJSON(data []byte) error {
	parsed, err := parseFlexibleUint64(data)
	if err != nil {
		return err
	}
	*n = jsonUint64(parsed)
	return nil
}

type confirmationPoCPhaseValue string

func (p *confirmationPoCPhaseValue) UnmarshalJSON(data []byte) error {
	var asString string
	if err := json.Unmarshal(data, &asString); err == nil {
		*p = confirmationPoCPhaseValue(asString)
		return nil
	}

	var asInt int
	if err := json.Unmarshal(data, &asInt); err == nil {
		switch asInt {
		case 0:
			*p = confirmationPoCPhaseValue(confirmationPoCInactive)
		case 1:
			*p = confirmationPoCPhaseValue(confirmationPoCGracePeriod)
		case 2:
			*p = confirmationPoCPhaseValue(confirmationPoCGeneration)
		case 3:
			*p = confirmationPoCPhaseValue(confirmationPoCValidation)
		case 4:
			*p = confirmationPoCPhaseValue(confirmationPoCCompleted)
		default:
			*p = confirmationPoCPhaseValue(strconv.Itoa(asInt))
		}
		return nil
	}

	return fmt.Errorf("unsupported confirmation PoC phase %s", string(data))
}

type RequestAdmissionError struct {
	Reason  string
	Message string
}

func (e *RequestAdmissionError) Error() string {
	if e == nil {
		return ""
	}
	if strings.TrimSpace(e.Message) != "" {
		return e.Message
	}
	if strings.TrimSpace(e.Reason) != "" {
		return e.Reason
	}
	return "request admission blocked"
}

func NewChainPhaseGate(baseURL string, pollInterval time.Duration) *ChainPhaseGate {
	baseURL = strings.TrimSpace(baseURL)
	if baseURL == "" {
		return nil
	}
	if pollInterval <= 0 {
		pollInterval = defaultChainPhasePollInterval
	}
	client := &http.Client{Timeout: 5 * time.Second}
	return &ChainPhaseGate{
		endpoint:                      strings.TrimRight(baseURL, "/") + "/v1/epochs/latest",
		participantsEndpoint:          strings.TrimRight(baseURL, "/") + "/v1/epochs/current/participants",
		client:                        client,
		pollInterval:                  pollInterval,
		defaultMaxSpeculativeAttempts: CurrentMaxSpeculativeAttempts(),
		versions:                      NewVersionsCache(client, versionsTTLPollMultiplier*pollInterval),
		stopCh:                        make(chan struct{}),
		doneCh:                        make(chan struct{}),
	}
}

func (g *ChainPhaseGate) SetChainQueryClient(client chain.InferenceClient) {
	if g == nil {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.chainQuery = client
}

func (g *ChainPhaseGate) SetPreservedSnapshotBaseURL(baseURL string) {
	_ = baseURL
}

func (g *ChainPhaseGate) Start() {
	if g == nil {
		return
	}
		if g.versions != nil {
		// Derive a context whose lifetime matches the gate's stopCh so the
		// versions poller stops cleanly without introducing a second lifecycle.
		ctx, cancel := context.WithCancel(context.Background())
		go func() {
			<-g.stopCh
			cancel()
		}()
		go g.versions.Run(ctx, g.pollInterval)
	}
	go g.run()
}

func (g *ChainPhaseGate) Stop() {
	if g == nil {
		return
	}
	select {
	case <-g.doneCh:
		return
	default:
	}
	close(g.stopCh)
	<-g.doneCh
	setPoCPhaseState(false, "")
	g.restoreDefaultSpeculativeAttempts()
}

func (g *ChainPhaseGate) Snapshot() ChainPhaseSnapshot {
	if g == nil {
		return ChainPhaseSnapshot{}
	}
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.snapshot
}

// SetCapacityState attaches the capacity state that should receive
// per-host weights and the preserved-host set on every refresh.
// scaleHook is invoked after each refresh with the new W_tot/W_ref
// scale factor; pass nil to skip scale propagation. Reactive throttle
// is consulted live by the state itself via the availability callback
// wired separately (SetLiveAvailable on the state at gateway
// construction), so we no longer push throttle snapshots through the
// refresh loop.
func (g *ChainPhaseGate) SetCapacityState(state *CapacityState, scaleHook func(scale float64)) {
	if g == nil {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.capacityState = state
	g.scaleApplyHook = scaleHook
}

func (g *ChainPhaseGate) capacitySinks() (*CapacityState, func(float64)) {
	if g == nil {
		return nil, nil
	}
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.capacityState, g.scaleApplyHook
}

func (g *ChainPhaseGate) AdmissionError() error {
	if g == nil {
		return nil
	}
	snapshot := g.Snapshot()
	if !snapshot.RequestsBlocked {
		return nil
	}
	return &RequestAdmissionError{
		Reason:  snapshot.BlockReason,
		Message: fmt.Sprintf("devshard temporarily unavailable during %s", humanizePhaseBlockReason(snapshot)),
	}
}

func (g *ChainPhaseGate) run() {
	defer close(g.doneCh)
	g.refresh()

	ticker := time.NewTicker(g.pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			g.refresh()
		case <-g.stopCh:
			return
		}
	}
}

func (g *ChainPhaseGate) refresh() {
	if g == nil {
		return
	}
	previous := g.Snapshot()
	resp, err := g.fetchEpochInfo()
	if err != nil {
		g.recordError(err)
		log.Printf("chain phase poll failed: %v", err)
		return
	}
	snapshot := deriveChainPhaseSnapshot(resp)
	active, _ := rawPoCBlockingState(snapshot.EpochPhase, snapshot.ConfirmationPoCPhase)

	capacityState, scaleHook := g.capacitySinks()

	// We need participants info for two purposes:
	//   (a) the relaxed-PoC preserved-set update (on active edge and
	//       active phase/anchor changes)
	//   (b) feeding CapacityState weights + preserved set (every refresh,
	//       when attached) so W(e)/W_tot/W_ref reflect the latest chain
	//       observation.
	// Combine the fetches so we never double-poll the chain.
	needPreservedFetch := active && relaxedPoCModeEnabled() && shouldRefreshPoCPreservedParticipants(previous, snapshot)
	if needPreservedFetch || capacityState != nil {
		state, perr := g.fetchParticipantsState(
			active,
			preservedSnapshotAnchor(snapshot),
			allowAllParticipantsUntilSnapshot(snapshot),
		)
		if perr != nil {
			g.recordError(perr)
			g.logPreservedParticipantFetchFailure(snapshot, perr)
		} else {
			// Keep the versions poller pointed at the current candidate set.
			if g.versions != nil {
				g.versions.SetCandidates(state.inferenceURLs)
			}

			isValidation := rawPoCValidationState(snapshot.EpochPhase, snapshot.ConfirmationPoCPhase)
			if active && relaxedPoCModeEnabled() && isValidation && g.versions != nil {
				preserved, preservedByModel, curWeights, curWeightsByModel :=
					mergePreservedWithValidationCapable(state, g.versions.IsNodeValidationCapable)
				if capacityState != nil {
					capacityState.SetHostWeightViews(curWeights, state.fullWeights, curWeightsByModel, state.fullWeightsByModel)
					capacityState.SetPoCPreserved(preserved)
				}
				if needPreservedFetch {
					setPoCPreservedParticipantsByModel(preservedByModel)
					g.logPreservedParticipantsLoaded(snapshot, preserved, state.excluded)
				}
			} else {
				if needPreservedFetch {
					setPoCPreservedParticipantsByModel(state.preservedByModel)
					g.logPreservedParticipantsLoaded(snapshot, state.preserved, state.excluded)
				}
				if capacityState != nil {
					// The participants response has enough ML-node data to compute
					// both views on every poll: current is PoC/CPoC-filtered,
					// full is the all-node steady-state baseline. Updating both
					// prevents cold starts during PoC from falling back to unknown
					// baseline capacity.
					capacityState.SetHostWeightViews(state.weights, state.fullWeights, state.weightsByModel, state.fullWeightsByModel)
					if active && relaxedPoCModeEnabled() {
						capacityState.SetPoCPreserved(state.preserved)
					} else {
						// Outside relaxed PoC every host is "preserved"
						// from the limiter's perspective; nil tells the
						// state to treat the preserved set as not-yet-loaded
						// and therefore not block anyone on PoC grounds.
						capacityState.SetPoCPreserved(nil)
					}
				}
			}
		}
	}
	if !active {
		setPoCPreservedParticipantsByModel(nil)
	}

	if capacityState != nil && scaleHook != nil {
		scaleHook(capacityState.ScaleFactorAcrossModels())
	}

	g.applySpeculativeAttemptPolicy(snapshot)
	g.logSnapshotTransition(previous, snapshot)
	g.storeSnapshot(snapshot)
}

func (g *ChainPhaseGate) fetchEpochInfo() (*chainEpochInfoResponse, error) {
	resp, err := g.client.Get(g.endpoint)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		io.Copy(io.Discard, resp.Body)
		return nil, fmt.Errorf("epoch info status %d", resp.StatusCode)
	}

	var payload chainEpochInfoResponse
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, err
	}
	return &payload, nil
}

// participantNode holds the per-node data for one ML node belonging to a participant
type participantNode struct {
	model  string
	nodeID string
	weight float64
}

// participantsState is the parsed form of the chain participants
// endpoint used by both the relaxed-PoC preserved-set logic and the
// CapacityState weight ingestion. We collect everything in one fetch
// so the gate doesn't double-poll. All keys (preserved, excluded,
// weights map keys) are the participant's gonka address (chain
// `Index`); the InferenceURL is intentionally NOT used as an identity
// -- the chain address is the canonical participant key everywhere
// downstream (CapacityState, ParticipantRequestLimiter, Session,
// transport admission). Weights are derived from raw ML-node poc_weight
// values rather than the chain's coefficient-adjusted participant.Weight:
// outside PoC every ML node contributes, while during active PoC only
// preserved ML nodes contribute.
type participantsState struct {
	preserved          []string
	preservedByModel   map[string][]string
	excluded           []string
	weights            map[string]float64
	weightsByModel     map[string]map[string]float64
	fullWeights        map[string]float64
	fullWeightsByModel map[string]map[string]float64
	inferenceURLs      map[string]string
	nodesByParticipant map[string][]participantNode
}

func (g *ChainPhaseGate) fetchPreservedParticipantKeys() ([]string, []string, error) {
	state, err := g.fetchParticipantsState(false, 0, false)
	if err != nil {
		return nil, nil, err
	}
	return state.preserved, state.excluded, nil
}

func (g *ChainPhaseGate) fetchParticipantsState(pocActive bool, expectedSnapshotAnchor int64, allowAllWhenSnapshotMissing bool) (*participantsState, error) {
	resp, err := g.client.Get(g.participantsEndpoint)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		io.Copy(io.Discard, resp.Body)
		return nil, fmt.Errorf("current participants status %d", resp.StatusCode)
	}

	var payload chainCurrentParticipantsResponse
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, err
	}

	var preservedSnapshot *preservedSnapshotState
	preservation := preservationModeLegacy
	if pocActive {
		snapshot, status, err := g.fetchPreservedSnapshotState(expectedSnapshotAnchor)
		if err != nil {
			log.Printf("chain phase gate: preserved snapshot poll failed error=%v", err)
		} else if status == preservedSnapshotCurrent {
			preservedSnapshot = snapshot
			preservation = preservationModeSnapshot
		} else if status == preservedSnapshotMissingCurrent && allowAllWhenSnapshotMissing {
			preservation = preservationModeAll
		}
	}

	state := &participantsState{
		preservedByModel:   make(map[string][]string),
		weights:            make(map[string]float64, len(payload.ActiveParticipants.Participants)),
		weightsByModel:     make(map[string]map[string]float64),
		fullWeights:        make(map[string]float64, len(payload.ActiveParticipants.Participants)),
		fullWeightsByModel: make(map[string]map[string]float64),
		inferenceURLs:      make(map[string]string, len(payload.ActiveParticipants.Participants)),
		nodesByParticipant: make(map[string][]participantNode, len(payload.ActiveParticipants.Participants)),
	}
	seenPreserved := make(map[string]struct{}, len(payload.ActiveParticipants.Participants))
	seenPreservedByModel := make(map[string]map[string]struct{})
	seenExcluded := make(map[string]struct{}, len(payload.ActiveParticipants.Participants))
	for _, participant := range payload.ActiveParticipants.Participants {
		key := strings.TrimSpace(participant.Index)
		if key == "" {
			// A participant with no chain Index is not addressable
			// downstream (Session/CapacityState/limiter all key by
			// gonka address). Drop it -- there's no sensible fallback
			// because the inference URL alone can't sign votes,
			// receive PoC weight, or be matched against group slots.
			continue
		}
		if inferenceURL := strings.TrimSpace(participant.InferenceURL); inferenceURL != "" {
			state.inferenceURLs[key] = inferenceURL
		}
		if nodes := participantNodes(participant); len(nodes) > 0 {
			state.nodesByParticipant[key] = nodes
		}
		state.weights[key] = participantWeight(participant, pocActive, preservedSnapshot, preservation)
		state.fullWeights[key] = participantWeight(participant, false, nil, preservationModeAll)
		for model, weight := range participantWeightsByModel(participant, pocActive, preservedSnapshot, preservation) {
			modelWeights := state.weightsByModel[model]
			if modelWeights == nil {
				modelWeights = map[string]float64{}
				state.weightsByModel[model] = modelWeights
			}
			modelWeights[key] = weight
		}
		for model, weight := range participantWeightsByModel(participant, false, nil, preservationModeAll) {
			modelWeights := state.fullWeightsByModel[model]
			if modelWeights == nil {
				modelWeights = map[string]float64{}
				state.fullWeightsByModel[model] = modelWeights
			}
			modelWeights[key] = weight
		}
		for _, model := range preservedModelsForParticipant(participant, preservedSnapshot, preservation) {
			modelSet := seenPreservedByModel[model]
			if modelSet == nil {
				modelSet = map[string]struct{}{}
				seenPreservedByModel[model] = modelSet
			}
			if _, ok := modelSet[key]; ok {
				continue
			}
			modelSet[key] = struct{}{}
			state.preservedByModel[model] = append(state.preservedByModel[model], key)
		}
		if !participantHasPreservedNode(participant, preservedSnapshot, preservation) {
			if _, ok := seenExcluded[key]; ok {
				continue
			}
			seenExcluded[key] = struct{}{}
			state.excluded = append(state.excluded, key)
			continue
		}
		if _, ok := seenPreserved[key]; ok {
			continue
		}
		seenPreserved[key] = struct{}{}
		state.preserved = append(state.preserved, key)
	}
	for model := range state.preservedByModel {
		sort.Strings(state.preservedByModel[model])
	}
	return state, nil
}

func (g *ChainPhaseGate) preservedSnapshotURL() string {
	if g == nil {
		return ""
	}
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.preservedSnapshotEndpoint
}

func (g *ChainPhaseGate) fetchPreservedSnapshotState(expectedAnchor int64) (*preservedSnapshotState, preservedSnapshotStatus, error) {
	qc := g.chainQueryClient()
	if qc == nil {
		return nil, preservedSnapshotUnavailable, nil
	}
	resp, err := qc.PreservedNodesSnapshot(context.Background(), &inferencetypes.QueryPreservedNodesSnapshotRequest{})
	if err != nil {
		return nil, preservedSnapshotUnavailable, err
	}
	if resp == nil || !resp.Found || resp.Snapshot == nil {
		return nil, preservedSnapshotMissingCurrent, nil
	}
	snapshot := preservedSnapshotFromProto(resp.Snapshot)
	if expectedAnchor > 0 && int64(snapshot.EpisodeAnchorHeight) != expectedAnchor {
		log.Printf(
			"chain phase gate: preserved snapshot anchor mismatch expected=%d actual=%d",
			expectedAnchor,
			snapshot.EpisodeAnchorHeight,
		)
		return nil, preservedSnapshotMissingCurrent, nil
	}
	return newPreservedSnapshotState(snapshot), preservedSnapshotCurrent, nil
}

func (g *ChainPhaseGate) chainQueryClient() chain.InferenceClient {
	if g == nil {
		return nil
	}
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.chainQuery
}

func preservedSnapshotFromProto(snapshot *inferencetypes.PreservedNodesSnapshot) *chainPreservedNodesSnapshot {
	if snapshot == nil {
		return nil
	}
	out := &chainPreservedNodesSnapshot{
		EpisodeAnchorHeight: jsonInt64(snapshot.EpisodeAnchorHeight),
	}
	for _, modelNodes := range snapshot.GetModelPreservedNodes() {
		entry := chainModelPreservedNodes{ModelID: modelNodes.GetModelId()}
		for _, participant := range modelNodes.GetParticipants() {
			entry.Participants = append(entry.Participants, chainParticipantPreservedNodes{
				ParticipantID: participant.GetParticipantId(),
				NodeIDs:       append([]string(nil), participant.GetNodeIds()...),
			})
		}
		out.ModelPreservedNodes = append(out.ModelPreservedNodes, entry)
	}
	return out
}

func newPreservedSnapshotState(snapshot *chainPreservedNodesSnapshot) *preservedSnapshotState {
	state := &preservedSnapshotState{byModel: map[string]map[string]map[string]struct{}{}}
	if snapshot == nil {
		return state
	}
	for _, modelNodes := range snapshot.ModelPreservedNodes {
		model := strings.TrimSpace(modelNodes.ModelID)
		if model == "" {
			continue
		}
		byParticipant := state.byModel[model]
		if byParticipant == nil {
			byParticipant = map[string]map[string]struct{}{}
			state.byModel[model] = byParticipant
		}
		for _, participant := range modelNodes.Participants {
			participantID := strings.TrimSpace(participant.ParticipantID)
			if participantID == "" {
				continue
			}
			nodeSet := byParticipant[participantID]
			if nodeSet == nil {
				nodeSet = map[string]struct{}{}
				byParticipant[participantID] = nodeSet
			}
			for _, nodeID := range participant.NodeIDs {
				nodeID = strings.TrimSpace(nodeID)
				if nodeID != "" {
					nodeSet[nodeID] = struct{}{}
				}
			}
		}
	}
	return state
}

func (g *ChainPhaseGate) storeSnapshot(snapshot ChainPhaseSnapshot) {
	setPoCPhaseStateFromSnapshot(snapshot)
	g.mu.Lock()
	defer g.mu.Unlock()
	g.snapshot = snapshot
}

func (g *ChainPhaseGate) logPreservedParticipantFetchFailure(snapshot ChainPhaseSnapshot, err error) {
	if g == nil || err == nil {
		return
	}
	log.Printf(
		"chain phase gate: preserved participant poll failed reason=%s chain_phase=%s confirmation_poc_phase=%s epoch=%d block_height=%d error=%v",
		snapshot.BlockReason,
		snapshot.EpochPhase,
		snapshot.ConfirmationPoCPhase,
		snapshot.EpochIndex,
		snapshot.BlockHeight,
		err,
	)
}

// participantKeyLabels returns a sorted, short-form copy of the
// supplied gonka addresses for log compactness. Short form is the
// last 8 characters of the bech32 string -- enough to disambiguate in
// practice without polluting log lines.
func participantKeyLabels(keys []string) []string {
	labels := make([]string, len(keys))
	for i, k := range keys {
		labels[i] = shortParticipantKey(k)
	}
	sort.Strings(labels)
	return labels
}

func shortParticipantKey(key string) string {
	trimmed := strings.TrimSpace(key)
	if len(trimmed) <= 8 {
		return trimmed
	}
	return trimmed[len(trimmed)-8:]
}

func (g *ChainPhaseGate) logPreservedParticipantsLoaded(snapshot ChainPhaseSnapshot, preserved []string, excluded []string) {
	if g == nil {
		return
	}
	excludedLabels := participantKeyLabels(excluded)
	excludedJoined := strings.Join(excludedLabels, ",")
	if len(preserved) == 0 {
		log.Printf(
			"chain phase gate: preserved participant poll empty reason=%s chain_phase=%s confirmation_poc_phase=%s epoch=%d block_height=%d excluded_count=%d excluded_participants=%s",
			snapshot.BlockReason,
			snapshot.EpochPhase,
			snapshot.ConfirmationPoCPhase,
			snapshot.EpochIndex,
			snapshot.BlockHeight,
			len(excludedLabels),
			excludedJoined,
		)
		return
	}
	preservedLabels := participantKeyLabels(preserved)
	log.Printf(
		"chain phase gate: preserved participants loaded reason=%s chain_phase=%s confirmation_poc_phase=%s epoch=%d block_height=%d count=%d participants=%s excluded_count=%d excluded_participants=%s",
		snapshot.BlockReason,
		snapshot.EpochPhase,
		snapshot.ConfirmationPoCPhase,
		snapshot.EpochIndex,
		snapshot.BlockHeight,
		len(preservedLabels),
		strings.Join(preservedLabels, ","),
		len(excludedLabels),
		excludedJoined,
	)
}

func (g *ChainPhaseGate) logSnapshotTransition(previous, next ChainPhaseSnapshot) {
	if g == nil {
		return
	}

	currentAttempts := CurrentMaxSpeculativeAttempts()
	previousActive, previousReason := rawPoCBlockingState(previous.EpochPhase, previous.ConfirmationPoCPhase)
	nextActive, nextReason := rawPoCBlockingState(next.EpochPhase, next.ConfirmationPoCPhase)
	if !previousActive && nextActive {
		log.Printf(
			"chain phase gate: phase active reason=%s chain_phase=%s confirmation_poc_phase=%s epoch=%d block_height=%d requests_blocked=%t max_attempts=%d",
			nextReason,
			next.EpochPhase,
			next.ConfirmationPoCPhase,
			next.EpochIndex,
			next.BlockHeight,
			next.RequestsBlocked,
			currentAttempts,
		)
	}
	if previousActive && !nextActive {
		log.Printf(
			"chain phase gate: phase inactive previous_reason=%s chain_phase=%s confirmation_poc_phase=%s epoch=%d block_height=%d requests_blocked=%t max_attempts=%d",
			previousReason,
			next.EpochPhase,
			next.ConfirmationPoCPhase,
			next.EpochIndex,
			next.BlockHeight,
			next.RequestsBlocked,
			currentAttempts,
		)
	}
	if previousActive && nextActive &&
		(previousReason != nextReason ||
			previous.EpochPhase != next.EpochPhase ||
			previous.ConfirmationPoCPhase != next.ConfirmationPoCPhase) {
		log.Printf(
			"chain phase gate: phase updated reason=%s chain_phase=%s confirmation_poc_phase=%s epoch=%d block_height=%d requests_blocked=%t max_attempts=%d",
			nextReason,
			next.EpochPhase,
			next.ConfirmationPoCPhase,
			next.EpochIndex,
			next.BlockHeight,
			next.RequestsBlocked,
			currentAttempts,
		)
	}
	if !previous.RequestsBlocked && next.RequestsBlocked {
		log.Printf(
			"chain phase gate: blocking new requests reason=%s chain_phase=%s confirmation_poc_phase=%s epoch=%d block_height=%d max_attempts=%d",
			next.BlockReason,
			next.EpochPhase,
			next.ConfirmationPoCPhase,
			next.EpochIndex,
			next.BlockHeight,
			currentAttempts,
		)
		return
	}

	if previous.RequestsBlocked && !next.RequestsBlocked {
		log.Printf(
			"chain phase gate: request blocking cleared previous_reason=%s chain_phase=%s confirmation_poc_phase=%s epoch=%d block_height=%d max_attempts=%d",
			previous.BlockReason,
			next.EpochPhase,
			next.ConfirmationPoCPhase,
			next.EpochIndex,
			next.BlockHeight,
			currentAttempts,
		)
		return
	}

	if next.RequestsBlocked &&
		(previous.BlockReason != next.BlockReason ||
			previous.EpochPhase != next.EpochPhase ||
			previous.ConfirmationPoCPhase != next.ConfirmationPoCPhase) {
		log.Printf(
			"chain phase gate: blocking mode updated reason=%s chain_phase=%s confirmation_poc_phase=%s epoch=%d block_height=%d max_attempts=%d",
			next.BlockReason,
			next.EpochPhase,
			next.ConfirmationPoCPhase,
			next.EpochIndex,
			next.BlockHeight,
			currentAttempts,
		)
	}
}

func (g *ChainPhaseGate) applySpeculativeAttemptPolicy(snapshot ChainPhaseSnapshot) {
	if g == nil {
		return
	}
	active, _ := rawPoCBlockingState(snapshot.EpochPhase, snapshot.ConfirmationPoCPhase)
	if active && !relaxedPoCModeEnabled() {
		SetMaxSpeculativeAttempts(1)
		return
	}
	g.restoreDefaultSpeculativeAttempts()
}

func (g *ChainPhaseGate) restoreDefaultSpeculativeAttempts() {
	if g == nil {
		return
	}
	SetMaxSpeculativeAttempts(g.defaultMaxSpeculativeAttempts)
}

func (g *ChainPhaseGate) recordError(err error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.snapshot.LastError = err.Error()
}

func deriveChainPhaseSnapshot(resp *chainEpochInfoResponse) ChainPhaseSnapshot {
	if resp == nil {
		return ChainPhaseSnapshot{}
	}

	snapshot := ChainPhaseSnapshot{
		BlockHeight:            int64(resp.BlockHeight),
		EpochIndex:             uint64(resp.LatestEpoch.Index),
		EpochPhase:             deriveEpochPhase(resp),
		LastUpdatedAt:          time.Now().UTC(),
		pocStartBlockHeight:    int64(resp.LatestEpoch.PocStartBlockHeight),
		epochSwitchBlockHeight: deriveEpochSwitchBlockHeight(resp),
	}

	if resp.ActiveConfirmationPoC != nil {
		snapshot.ConfirmationPoCPhase = string(resp.ActiveConfirmationPoC.Phase)
		snapshot.confirmationPoCTriggerHeight = int64(resp.ActiveConfirmationPoC.TriggerHeight)
	}
	rawBlocked, reason := rawPoCBlockingState(snapshot.EpochPhase, snapshot.ConfirmationPoCPhase)
	snapshot.BlockReason = reason
	snapshot.RequestsBlocked = rawBlocked && !relaxedPoCModeEnabled()
	return snapshot
}

func deriveEpochSwitchBlockHeight(resp *chainEpochInfoResponse) int64 {
	if resp == nil {
		return 0
	}
	blockHeight := int64(resp.BlockHeight)
	if resp.EpochStages.SetNewValidators > 0 && int64(resp.EpochStages.SetNewValidators) >= blockHeight {
		return int64(resp.EpochStages.SetNewValidators)
	}
	if resp.NextEpochStages.SetNewValidators > 0 {
		return int64(resp.NextEpochStages.SetNewValidators)
	}
	if resp.EpochStages.NextPoCStart > 0 {
		return int64(resp.EpochStages.NextPoCStart)
	}
	return int64(resp.LatestEpoch.PocStartBlockHeight)
}

func deriveEpochPhase(resp *chainEpochInfoResponse) string {
	if resp == nil {
		return ""
	}
	return strings.TrimSpace(resp.Phase)
}

func rawPoCBlockingState(epochPhase, confirmationPhase string) (bool, string) {
	switch epochPhase {
	case epochPhasePoCGenerate, epochPhasePoCGenerateWindDown, epochPhasePoCValidate, epochPhasePoCValidateWindDown:
		return true, "poc"
	}
	switch confirmationPhase {
	case confirmationPoCGracePeriod, confirmationPoCGeneration, confirmationPoCValidation:
		return true, "confirmation_poc"
	}
	return false, ""
}


func rawPoCGenerationState(epochPhase, confirmationPhase string) bool {
	switch epochPhase {
	case epochPhasePoCGenerate, epochPhasePoCGenerateWindDown:
		return true
	}
	switch confirmationPhase {
	case confirmationPoCGracePeriod, confirmationPoCGeneration:
		return true
	}
	return false
}

func rawPoCValidationState(epochPhase, confirmationPhase string) bool {
	switch epochPhase {
	case epochPhasePoCValidate, epochPhasePoCValidateWindDown:
		return true
	}
	return confirmationPhase == confirmationPoCValidation
}

func shouldRefreshPoCPreservedParticipants(previous, next ChainPhaseSnapshot) bool {
	previousActive, _ := rawPoCBlockingState(previous.EpochPhase, previous.ConfirmationPoCPhase)
	nextActive, _ := rawPoCBlockingState(next.EpochPhase, next.ConfirmationPoCPhase)
	if !nextActive {
		return false
	}
	if !previousActive {
		return true
	}
	return previous.BlockReason != next.BlockReason ||
		previous.EpochPhase != next.EpochPhase ||
		previous.ConfirmationPoCPhase != next.ConfirmationPoCPhase ||
		previous.confirmationPoCTriggerHeight != next.confirmationPoCTriggerHeight ||
		previous.pocStartBlockHeight != next.pocStartBlockHeight
}

func isPoCGenerationSnapshot(snapshot ChainPhaseSnapshot) bool {
	return snapshot.EpochPhase == epochPhasePoCGenerate ||
		snapshot.ConfirmationPoCPhase == confirmationPoCGeneration
}

func preservedSnapshotAnchor(snapshot ChainPhaseSnapshot) int64 {
	switch snapshot.BlockReason {
	case "confirmation_poc":
		// Confirmation PoC snapshots are sampled when the event leaves
		// grace period, using the event trigger height as the stable
		// episode anchor. During grace period the matching snapshot is
		// intentionally not available yet.
		return snapshot.confirmationPoCTriggerHeight
	case "poc":
		return snapshot.pocStartBlockHeight
	default:
		return 0
	}
}

func allowAllParticipantsUntilSnapshot(snapshot ChainPhaseSnapshot) bool {
	return snapshot.BlockReason == "confirmation_poc" &&
		snapshot.ConfirmationPoCPhase == confirmationPoCGracePeriod
}

func participantHasPreservedNode(participant chainActiveParticipant, snapshot *preservedSnapshotState, preservation preservationMode) bool {
	if preservation == preservationModeAll {
		return true
	}
	for i, modelNodes := range participant.MLNodes {
		model := participantModelAt(participant, i)
		for _, node := range modelNodes.MLNodes {
			if nodePreserved(participant.Index, model, node, snapshot, preservation) {
				return true
			}
		}
	}
	return false
}

func preservedModelsForParticipant(participant chainActiveParticipant, snapshot *preservedSnapshotState, preservation preservationMode) []string {
	seen := make(map[string]struct{}, len(participant.Models))
	var models []string
	for i, rawModel := range participant.Models {
		model := strings.TrimSpace(rawModel)
		if model == "" || i >= len(participant.MLNodes) {
			continue
		}
		for _, node := range participant.MLNodes[i].MLNodes {
			if !nodePreserved(participant.Index, model, node, snapshot, preservation) {
				continue
			}
			if _, ok := seen[model]; ok {
				continue
			}
			seen[model] = struct{}{}
			models = append(models, model)
			break
		}
	}
	sort.Strings(models)
	return models
}

// participantWeight returns the raw poc_weight CapacityState should ingest
// for one participant. Outside PoC every ML node contributes to the
// steady-state baseline. During PoC only preserved ML nodes contribute,
// so a participant with partial preserved hardware does not contribute
// its full raw capacity.
//
// Missing ML-node poc_weight data is propagated as 0. The phase gate does
// not invent capacity from ML-node counts or default weights; the chain
// API's per-node poc_weight values are the source of truth.
func participantWeight(participant chainActiveParticipant, pocActive bool, snapshot *preservedSnapshotState, preservation preservationMode) float64 {
	var weight uint64
	for i, modelNodes := range participant.MLNodes {
		weight += modelNodePoCWeight(
			participant.Index,
			participantModelAt(participant, i),
			modelNodes,
			snapshot,
			effectivePreservationMode(pocActive, preservation),
		)
	}
	return float64(weight)
}

func participantWeightsByModel(participant chainActiveParticipant, pocActive bool, snapshot *preservedSnapshotState, preservation preservationMode) map[string]float64 {
	weights := make(map[string]float64, len(participant.Models))
	preservation = effectivePreservationMode(pocActive, preservation)
	for i, rawModel := range participant.Models {
		model := strings.TrimSpace(rawModel)
		if model == "" {
			continue
		}
		if i >= len(participant.MLNodes) {
			weights[model] = 0
			continue
		}
		weights[model] = float64(modelNodePoCWeight(participant.Index, model, participant.MLNodes[i], snapshot, preservation))
	}
	return weights
}

func effectivePreservationMode(pocActive bool, preservation preservationMode) preservationMode {
	if !pocActive {
		return preservationModeAll
	}
	return preservation
}

func participantModelAt(participant chainActiveParticipant, index int) string {
	if index < 0 || index >= len(participant.Models) {
		return ""
	}
	return strings.TrimSpace(participant.Models[index])
}

func modelNodePoCWeight(participantID, model string, modelNodes chainModelMLNodes, snapshot *preservedSnapshotState, preservation preservationMode) uint64 {
	var weight uint64
	for _, node := range modelNodes.MLNodes {
		if nodePreserved(participantID, model, node, snapshot, preservation) {
			weight += uint64(node.PoCWeight)
		}
	}
	return weight
}

func nodePreserved(participantID, model string, node chainMLNodeInfo, snapshot *preservedSnapshotState, preservation preservationMode) bool {
	if preservation == preservationModeAll {
		return true
	}
	if preservation == preservationModeSnapshot && snapshot != nil {
		return snapshot.Has(model, participantID, node.NodeID)
	}
	return len(node.TimeslotAllocation) > 1 && node.TimeslotAllocation[1]
}

func (s *preservedSnapshotState) Has(model, participantID, nodeID string) bool {
	if s == nil {
		return false
	}
	model = strings.TrimSpace(model)
	participantID = strings.TrimSpace(participantID)
	nodeID = strings.TrimSpace(nodeID)
	if model == "" || participantID == "" || nodeID == "" {
		return false
	}
	byParticipant := s.byModel[model]
	if byParticipant == nil {
		return false
	}
	nodes := byParticipant[participantID]
	if nodes == nil {
		return false
	}
	_, ok := nodes[nodeID]
	return ok
}

func (g *ChainPhaseGate) PoCActive() bool {
	if g == nil {
		return false
	}
	snapshot := g.Snapshot()
	active, _ := rawPoCBlockingState(snapshot.EpochPhase, snapshot.ConfirmationPoCPhase)
	return active
}

func humanizePhaseBlockReason(snapshot ChainPhaseSnapshot) string {
	if snapshot.BlockReason == "poc" {
		switch snapshot.EpochPhase {
		case epochPhasePoCGenerate:
			return "PoC generation"
		case epochPhasePoCGenerateWindDown:
			return "PoC generation wind down"
		case epochPhasePoCValidate:
			return "PoC validation"
		case epochPhasePoCValidateWindDown:
			return "PoC validation wind down"
		}
		return "PoC"
	}
	if snapshot.BlockReason == "confirmation_poc" {
		switch snapshot.ConfirmationPoCPhase {
		case confirmationPoCGracePeriod:
			return "confirmation PoC grace period"
		case confirmationPoCGeneration:
			return "confirmation PoC generation"
		case confirmationPoCValidation:
			return "confirmation PoC validation"
		}
		return "confirmation PoC"
	}
	if strings.TrimSpace(snapshot.BlockReason) != "" {
		return strings.ReplaceAll(snapshot.BlockReason, "_", " ")
	}
	if strings.TrimSpace(snapshot.EpochPhase) != "" {
		return snapshot.EpochPhase
	}
	return "chain admission controls"
}

func parseFlexibleInt64(data []byte) (int64, error) {
	var asInt int64
	if err := json.Unmarshal(data, &asInt); err == nil {
		return asInt, nil
	}

	var asString string
	if err := json.Unmarshal(data, &asString); err == nil {
		var parsed int64
		if _, err := fmt.Sscan(asString, &parsed); err != nil {
			return 0, err
		}
		return parsed, nil
	}

	return 0, fmt.Errorf("unsupported int64 value %s", string(data))
}

func parseFlexibleUint64(data []byte) (uint64, error) {
	var asUint uint64
	if err := json.Unmarshal(data, &asUint); err == nil {
		return asUint, nil
	}

	var asString string
	if err := json.Unmarshal(data, &asString); err == nil {
		var parsed uint64
		if _, err := fmt.Sscan(asString, &parsed); err != nil {
			return 0, err
		}
		return parsed, nil
	}

	return 0, fmt.Errorf("unsupported uint64 value %s", string(data))
}

// participantNodes extracts the flat list of (model, nodeID, weight)
func participantNodes(participant chainActiveParticipant) []participantNode {
	var nodes []participantNode
	for i, rawModel := range participant.Models {
		model := strings.TrimSpace(rawModel)
		if model == "" || i >= len(participant.MLNodes) {
			continue
		}
		for _, node := range participant.MLNodes[i].MLNodes {
			nodeID := strings.TrimSpace(node.NodeID)
			if nodeID == "" {
				continue
			}
			nodes = append(nodes, participantNode{
				model:  model,
				nodeID: nodeID,
				weight: float64(node.PoCWeight),
			})
		}
	}
	return nodes
}

// Returns per model chain weight of the participant's PoC-validation-inference-capable nodes.
func participantValidationInferenceWeights(nodes []participantNode, miner string, capable func(miner, nodeID string) bool) (map[string]float64, float64) {
	weights := map[string]float64{}
	if capable == nil {
		return weights, 0
	}
	var total float64
	for _, node := range nodes {
		if capable(miner, node.nodeID) {
			weights[node.model] += node.weight
			total += node.weight
		}
	}
	return weights, total
}

// Returns the validation-phase availability and weight views:
// preserved miners keep their PoC-filtered current weight, and each non-preserved ("excluded") miner
// that has a validation-inference-capable node is added with the summed weight of those capable nodes (per model).
func mergePreservedWithValidationCapable(
	state *participantsState,
	capable func(miner, nodeID string) bool,
) (preserved []string, preservedByModel map[string][]string, currentWeights map[string]float64, currentWeightsByModel map[string]map[string]float64) {
	preserved = append(preserved, state.preserved...)

	preservedByModel = make(map[string][]string, len(state.preservedByModel))
	for model, miners := range state.preservedByModel {
		preservedByModel[model] = append([]string(nil), miners...)
	}

	currentWeights = make(map[string]float64, len(state.weights))
	maps.Copy(currentWeights, state.weights)
	currentWeightsByModel = cloneModelWeights(state.weightsByModel)

	for _, miner := range state.excluded {
		nodes := state.nodesByParticipant[miner]
		weightsByModel, total := participantValidationInferenceWeights(nodes, miner, capable)
		if total <= 0 {
			continue
		}
		preserved = append(preserved, miner)
		currentWeights[miner] = total
		for model, weight := range weightsByModel {
			if currentWeightsByModel[model] == nil {
				currentWeightsByModel[model] = map[string]float64{}
			}
			currentWeightsByModel[model][miner] = weight
			preservedByModel[model] = append(preservedByModel[model], miner)
		}
	}
	for model := range preservedByModel {
		sort.Strings(preservedByModel[model])
	}
	return preserved, preservedByModel, currentWeights, currentWeightsByModel
}
