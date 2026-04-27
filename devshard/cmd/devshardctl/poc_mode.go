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
	currentPoCPreservedLoaded bool
	currentPoCPreservedKeys   = map[string]struct{}{}
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
		currentPoCPreservedLoaded = false
		currentPoCPreservedKeys = map[string]struct{}{}
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
	if active {
		currentPoCReason = strings.TrimSpace(reason)
		return
	}
	currentPoCReason = ""
	currentPoCPreservedLoaded = false
	currentPoCPreservedKeys = map[string]struct{}{}
}

func setPoCPreservedParticipants(keys []string) {
	next := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		next[key] = struct{}{}
	}
	pocModeMu.Lock()
	defer pocModeMu.Unlock()
	currentPoCPreservedKeys = next
	currentPoCPreservedLoaded = true
}

func poCPreservedParticipantsLoaded() bool {
	pocModeMu.RLock()
	defer pocModeMu.RUnlock()
	return currentPoCPreservedLoaded
}

func isPoCPreservedParticipant(key string) bool {
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
	_, ok := currentPoCPreservedKeys[key]
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

// shouldUseProbeForParticipant is the PoC-bypass policy decision keyed on
// a participant identifier. Callers in the Session.PrepareInferenceFn
// chooser pass the key from the HostBinding (the chooser runs under
// Session.mu, so calling Session.HostParticipantKey there would deadlock).
//
// Placed here rather than on *Redundancy because it consults only PoC
// globals defined in this file -- the receiver-as-namespace pattern was
// misleading.
func shouldUseProbeForParticipant(participantKey string) bool {
	if !relaxedPoCBypassActive() {
		return false
	}
	if !poCPreservedParticipantsLoaded() {
		return false
	}
	return !isPoCPreservedParticipant(participantKey)
}
