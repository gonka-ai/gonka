package main

import (
	"context"
	"encoding/json"
	"io"
	"maps"
	"net/http"
	"strings"
	"sync"
	"time"
)

// mlnodeVersionEntry is one element of the dapi public /v1/versions "mlnodes" list.
type mlnodeVersionEntry struct {
	NodeID                 string `json:"node_id"`
	PoCValidationInference bool   `json:"poc_validation_inference"`
}

type versionsResponse struct {
	MLNodes []mlnodeVersionEntry `json:"mlnodes"`
}

type versionsEntry struct {
	capableNodes map[string]bool // node_id -> validation-inference capable
	fetchedAt    time.Time
}

// VersionsCache polls each candidate miner's dapi /v1/versions in the background
// and answers, per (miner, node_id), whether that node can serve inference during
// PoC validation. Fail-closed: unknown miner/node, error, or stale entry -> false.
type VersionsCache struct {
	client *http.Client
	ttl    time.Duration

	mu         sync.RWMutex
	candidates map[string]string // miner -> dapi base URL (inference_url)
	entries    map[string]versionsEntry
	now        func() time.Time
}

func NewVersionsCache(client *http.Client, ttl time.Duration) *VersionsCache {
	return &VersionsCache{
		client:     client,
		ttl:        ttl,
		candidates: map[string]string{},
		entries:    map[string]versionsEntry{},
		now:        time.Now,
	}
}

// SetCandidates replaces the miner->URL set polled on the next pass and drops
// entries for miners no longer present.
func (c *VersionsCache) SetCandidates(minerURLs map[string]string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	next := make(map[string]string, len(minerURLs))
	for miner, u := range minerURLs {
		if miner == "" || strings.TrimSpace(u) == "" {
			continue
		}
		next[miner] = u
	}
	c.candidates = next
	for miner := range c.entries {
		if _, ok := next[miner]; !ok {
			delete(c.entries, miner)
		}
	}
}

// versionsURL builds the dapi public /v1/versions endpoint from a miner's
// inference base URL (scheme+host[:port]).
func versionsURL(base string) string {
	return strings.TrimSuffix(strings.TrimSpace(base), "/") + "/v1/versions"
}

// Poll runs one pass over the current candidate set.
func (c *VersionsCache) Poll(ctx context.Context) {
	c.mu.RLock()
	candidates := make(map[string]string, len(c.candidates))
	maps.Copy(candidates, c.candidates)
	c.mu.RUnlock()

	for miner, base := range candidates {
		nodes := c.fetchOne(ctx, base)
		c.mu.Lock()
		c.entries[miner] = versionsEntry{capableNodes: nodes, fetchedAt: c.now()}
		c.mu.Unlock()
	}
}

// fetchOne returns the per-node capability map, or nil on any error (fail-closed).
func (c *VersionsCache) fetchOne(ctx context.Context, base string) map[string]bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, versionsURL(base), nil)
	if err != nil {
		return nil
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		io.Copy(io.Discard, resp.Body)
		return nil
	}
	var parsed versionsResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return nil
	}
	nodes := make(map[string]bool, len(parsed.MLNodes))
	for _, n := range parsed.MLNodes {
		id := strings.TrimSpace(n.NodeID)
		if id == "" {
			continue
		}
		nodes[id] = n.PoCValidationInference
	}
	return nodes
}

// IsNodeValidationCapable reports whether the named node of the named miner is
// currently known to be validation-inference capable and the knowledge is fresh.
func (c *VersionsCache) IsNodeValidationCapable(miner, nodeID string) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	entry, ok := c.entries[miner]
	if !ok || entry.capableNodes == nil {
		return false
	}
	if c.now().Sub(entry.fetchedAt) > c.ttl {
		return false
	}
	return entry.capableNodes[nodeID]
}

// Run polls on a fixed interval until ctx is cancelled.
func (c *VersionsCache) Run(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			c.Poll(ctx)
		}
	}
}
