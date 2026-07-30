package devshardobs

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"sort"
	"time"
)

// statsShardsListJSON mirrors devshardd session.statsShardsResponse for merge.
type statsShardsListJSON struct {
	ProtocolVersion string              `json:"protocol_version,omitempty"`
	BinaryVersion   string              `json:"binary_version,omitempty"`
	CurrentEpochID  uint64              `json:"current_epoch_id"`
	CachedAt        int64               `json:"cached_at"`
	CacheTTLSeconds int64               `json:"cache_ttl_seconds"`
	ActiveEscrows   []string            `json:"active_escrows"`
	Shards          []statsShardSummary `json:"shards"`
}

type statsShardSummary struct {
	EscrowID string `json:"escrow_id"`
	EpochID  uint64 `json:"epoch_id"`
}

func isStatsShardsList(rest string) bool {
	return rest == "/stats/shards"
}

// serveStatsShardsAggregate fetches /stats/shards from every running version and
// unions escrows/shards. Top-level protocol/binary versions prefer primary.
func (h *Handler) serveStatsShardsAggregate(w http.ResponseWriter, r *http.Request, versions []string) {
	noteStatsAggregate()
	primary := PrimaryVersion(versions)

	type partOK struct {
		ver  string
		body statsShardsListJSON
	}
	parts := make([]partOK, 0, len(versions))

	for i := len(versions) - 1; i >= 0; i-- {
		ver := versions[i]
		rec := httptest.NewRecorder()
		h.serveVersion(rec, r.Clone(r.Context()), ver, "/stats/shards")
		if rec.Code != http.StatusOK {
			continue
		}
		var part statsShardsListJSON
		if err := json.Unmarshal(rec.Body.Bytes(), &part); err != nil {
			continue
		}
		parts = append(parts, partOK{ver: ver, body: part})
	}
	if len(parts) == 0 {
		http.NotFound(w, r)
		return
	}

	merged := statsShardsListJSON{
		Shards: make([]statsShardSummary, 0),
	}
	seenShard := make(map[string]struct{})
	seenActive := make(map[string]struct{})

	for _, p := range parts {
		if p.ver == primary {
			merged.ProtocolVersion = p.body.ProtocolVersion
			merged.BinaryVersion = p.body.BinaryVersion
		}
		if p.body.CurrentEpochID > merged.CurrentEpochID {
			merged.CurrentEpochID = p.body.CurrentEpochID
		}
		if p.body.CacheTTLSeconds > 0 && (merged.CacheTTLSeconds == 0 || p.body.CacheTTLSeconds < merged.CacheTTLSeconds) {
			merged.CacheTTLSeconds = p.body.CacheTTLSeconds
		}
		for _, s := range p.body.Shards {
			if s.EscrowID == "" {
				continue
			}
			if _, ok := seenShard[s.EscrowID]; ok {
				continue
			}
			seenShard[s.EscrowID] = struct{}{}
			merged.Shards = append(merged.Shards, s)
		}
		for _, id := range p.body.ActiveEscrows {
			if id == "" {
				continue
			}
			seenActive[id] = struct{}{}
		}
	}
	// If primary did not respond, take metadata from newest successful part.
	if merged.ProtocolVersion == "" && merged.BinaryVersion == "" {
		merged.ProtocolVersion = parts[0].body.ProtocolVersion
		merged.BinaryVersion = parts[0].body.BinaryVersion
	}
	for id := range seenShard {
		seenActive[id] = struct{}{}
	}
	merged.ActiveEscrows = make([]string, 0, len(seenActive))
	for id := range seenActive {
		merged.ActiveEscrows = append(merged.ActiveEscrows, id)
	}
	sort.Strings(merged.ActiveEscrows)
	slices.SortFunc(merged.Shards, func(a, b statsShardSummary) int {
		switch {
		case a.EscrowID < b.EscrowID:
			return -1
		case a.EscrowID > b.EscrowID:
			return 1
		default:
			return 0
		}
	})
	merged.CachedAt = time.Now().Unix()
	if merged.CacheTTLSeconds == 0 {
		merged.CacheTTLSeconds = 60
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(merged)
}
