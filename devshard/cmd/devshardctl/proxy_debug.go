//go:build dev || debug || development

package main

import (
	"encoding/hex"
	"net/http"
	"strconv"
	"strings"

	"devshard/heightsync"
	"devshard/transport"
)

// handleDebugCheatAnchor arms a one-shot hook that flips the first byte of the
// outbound Anchor mainnet_block_hash_hex for the target inference nonce (query
// ?nonce=N, default next session nonce).
func (p *Proxy) handleDebugCheatAnchor(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	// Session.Nonce is last applied; the hook runs on the next PrepareInference (last+1).
	target := p.session.Nonce() + 1
	if q := r.URL.Query().Get("nonce"); q != "" {
		v, err := strconv.ParseUint(q, 10, 64)
		if err != nil {
			http.Error(w, "invalid nonce query", http.StatusBadRequest)
			return
		}
		target = v
	}
	p.session.SetOneShotHeightSyncRequestMutateHookForDebug(func(sec *heightsync.HeightSyncSection, nonce uint64) {
		if nonce != target || sec == nil {
			return
		}
		b, err := hex.DecodeString(strings.TrimPrefix(strings.TrimPrefix(sec.MainnetBlockHashHex, "0x"), "0X"))
		if err != nil || len(b) == 0 {
			return
		}
		bogus := append([]byte(nil), b...)
		bogus[0] ^= 0xff
		sec.MainnetBlockHashHex = hex.EncodeToString(bogus)
	})
	w.WriteHeader(http.StatusNoContent)
}

// handleDebugArmHostHold forwards to POST /v1/debug/arm-hold-inference-response on a host
// (query host_idx=0..3).
func (p *Proxy) handleDebugArmHostHold(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	idxStr := r.URL.Query().Get("host_idx")
	if idxStr == "" {
		http.Error(w, "missing host_idx query", http.StatusBadRequest)
		return
	}
	idx, err := strconv.Atoi(idxStr)
	if err != nil || idx < 0 {
		http.Error(w, "invalid host_idx", http.StatusBadRequest)
		return
	}
	clients := p.session.Clients()
	if idx >= len(clients) {
		http.Error(w, "host_idx out of range", http.StatusBadRequest)
		return
	}
	hc, ok := clients[idx].(*transport.HTTPClient)
	if !ok {
		http.Error(w, "host client is not HTTP", http.StatusInternalServerError)
		return
	}
	if err := hc.ArmHoldInferenceResponse(r.Context()); err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
