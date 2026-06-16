package main

import (
	"log"
	"strings"
	"sync"
)

const (
	pocRequestModeOff     = "off"
	pocRequestModeRelaxed = "relaxed"

	pocProbeMaxTokens = uint64(1)
)

var (
	pocModeMu                 sync.RWMutex
	currentPoCMode            = pocRequestModeOff
	currentPoCActive          bool
	currentPoCReason          string
	currentPoCGeneration      bool
	currentPoCPreservedLoaded bool
	currentPoCPreservedModels = map[string]map[string]struct{}{}
	pocProbePromptBody        = []byte(`{"messages":[{"role":"user","content":"."}],"max_tokens":1}`)
)

func ConfigurePoCRequestMode(raw string) {
	mode := strings.ToLower(strings.TrimSpace(raw))
	switch mode {
	case "", pocRequestModeOff:
		mode = pocRequestModeOff
	case pocRequestModeRelaxed:
	default:
		log.Printf("invalid DEVSHARD_POC_REQUEST_MODE=%q, using %q", raw, pocRequestModeOff)
		mode = pocRequestModeOff
	}

	pocModeMu.Lock()
	defer pocModeMu.Unlock()
	currentPoCMode = mode
	if mode == pocRequestModeOff {
		currentPoCActive = false
		currentPoCReason = ""
		currentPoCGeneration = false
		currentPoCPreservedLoaded = false
		currentPoCPreservedModels = map[string]map[string]struct{}{}
	}
}

func currentPoCModeValue() string {
	pocModeMu.RLock()
	defer pocModeMu.RUnlock()
	return currentPoCMode
}

func relaxedPoCModeEnabled() bool {
	return currentPoCModeValue() == pocRequestModeRelaxed
}

func setPoCPhaseState(active bool, reason string) {
	pocModeMu.Lock()
	defer pocModeMu.Unlock()
	currentPoCActive = active
	currentPoCGeneration = false
	if active {
		currentPoCReason = strings.TrimSpace(reason)
		return
	}
	currentPoCReason = ""
	currentPoCPreservedLoaded = false
	currentPoCPreservedModels = map[string]map[string]struct{}{}
}

func setPoCPhaseStateFromSnapshot(snapshot ChainPhaseSnapshot) {
	active, reason := rawPoCBlockingState(snapshot.EpochPhase, snapshot.ConfirmationPoCPhase)
	pocModeMu.Lock()
	defer pocModeMu.Unlock()
	currentPoCActive = active
	currentPoCGeneration = active && isPoCGenerationSnapshot(snapshot)
	if active {
		currentPoCReason = strings.TrimSpace(reason)
		return
	}
	currentPoCReason = ""
	currentPoCPreservedLoaded = false
	currentPoCPreservedModels = map[string]map[string]struct{}{}
}

func setPoCPreservedParticipantsByModel(byModel map[string][]string) {
	nextByModel := make(map[string]map[string]struct{}, len(byModel))
	for model, modelKeys := range byModel {
		model = strings.TrimSpace(model)
		if model == "" {
			continue
		}
		modelSet := make(map[string]struct{}, len(modelKeys))
		for _, key := range modelKeys {
			key = strings.TrimSpace(key)
			if key == "" {
				continue
			}
			modelSet[key] = struct{}{}
		}
		nextByModel[model] = modelSet
	}
	pocModeMu.Lock()
	defer pocModeMu.Unlock()
	currentPoCPreservedModels = nextByModel
	currentPoCPreservedLoaded = true
}

func poCPreservedParticipantsLoaded() bool {
	pocModeMu.RLock()
	defer pocModeMu.RUnlock()
	return currentPoCPreservedLoaded
}

func isPoCPreservedParticipantForModel(model, key string) bool {
	model = strings.TrimSpace(model)
	key = strings.TrimSpace(key)
	pocModeMu.RLock()
	defer pocModeMu.RUnlock()
	if key == "" {
		return true
	}
	if currentPoCMode != pocRequestModeRelaxed || !currentPoCActive {
		return true
	}
	if !currentPoCPreservedLoaded {
		return true
	}
	if model == "" {
		return false
	}
	modelSet, ok := currentPoCPreservedModels[model]
	if !ok {
		return false
	}
	_, ok = modelSet[key]
	return ok
}

func relaxedPoCBypassActive() bool {
	pocModeMu.RLock()
	defer pocModeMu.RUnlock()
	return currentPoCMode == pocRequestModeRelaxed && currentPoCActive
}

func currentPoCPhaseReason() string {
	pocModeMu.RLock()
	defer pocModeMu.RUnlock()
	return currentPoCReason
}

func currentPoCGenerationActive() bool {
	pocModeMu.RLock()
	defer pocModeMu.RUnlock()
	return currentPoCMode == pocRequestModeRelaxed && currentPoCGeneration
}

// shouldUseProbeForParticipant is the PoC-bypass policy decision keyed on
// the request model and participant identifier. Callers in the
// Session.PrepareInferenceFn chooser pass the key from the HostBinding (the
// chooser runs under Session.mu, so calling Session.HostParticipantKey there
// would deadlock).
//
// Placed here rather than on *Redundancy because it consults only PoC
// globals defined in this file -- the receiver-as-namespace pattern was
// misleading.
func shouldUseProbeForParticipant(model, participantKey string) bool {
	if !relaxedPoCBypassActive() {
		return false
	}
	if !poCPreservedParticipantsLoaded() {
		return false
	}
	return !isPoCPreservedParticipantForModel(model, participantKey)
}
