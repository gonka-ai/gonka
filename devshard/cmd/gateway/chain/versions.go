package chain

import (
	"context"
	"io"
	"maps"
	"net/http"
	"strings"
	"sync"
	"time"

	"common/httpguard"
	"golang.org/x/sync/errgroup"

	"devshard/cmd/gateway/internal/strictjson"
)

// See README.md, "What the chain observer provides".
const (
	versionsPollConcurrency = 16
	versionsFetchTimeout    = 2 * time.Second

	versionsResponseMaxBytes = 1 << 20
)

type mlnodeVersionEntry struct {
	NodeID                 string `json:"node_id"`
	PoCValidationInference bool   `json:"poc_validation_inference"`
}

type versionsResponse struct {
	MLNodes []mlnodeVersionEntry `json:"mlnodes"`
}

// versionsEntry is one miner's poll result: node id to validation-inference capability, as of fetchedAt.
type versionsEntry struct {
	capableNodes map[string]bool
	fetchedAt    time.Time
}

// VersionsCache reports per-node PoC-validation-inference capability; unknown, errored or stale reads as false. See README.md, "PoC validation rejoins capable miners".
type VersionsCache struct {
	client *http.Client
	ttl    time.Duration

	mu         sync.RWMutex
	candidates map[string]string
	entries    map[string]versionsEntry
	unpolled   map[string]string
	added      chan struct{}
	now        func() time.Time
}

func newVersionsClient() *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.DialContext = httpguard.NewDialer().DialContext
	return &http.Client{Transport: transport}
}

// NewVersionsCache builds a cache that polls through client with the given staleness ttl.
func NewVersionsCache(client *http.Client, ttl time.Duration, now func() time.Time) *VersionsCache {
	return &VersionsCache{
		client:     client,
		ttl:        ttl,
		candidates: map[string]string{},
		entries:    map[string]versionsEntry{},
		unpolled:   map[string]string{},
		added:      make(chan struct{}, 1),
		now:        now,
	}
}

// SetCandidates replaces the miner->URL set, drops entries for miners removed or moved to a new URL, and wakes Run to poll new and moved miners.
func (c *VersionsCache) SetCandidates(minerURLs map[string]string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	next := make(map[string]string, len(minerURLs))
	for miner, u := range minerURLs {
		if miner == "" || strings.TrimSpace(u) == "" {
			continue
		}
		next[miner] = u
		if previous, known := c.candidates[miner]; !known || previous != u {
			c.unpolled[miner] = u
			delete(c.entries, miner)
		}
	}
	c.candidates = next
	removed := func(miner string) bool {
		_, kept := next[miner]
		return !kept
	}
	maps.DeleteFunc(c.entries, func(miner string, _ versionsEntry) bool { return removed(miner) })
	maps.DeleteFunc(c.unpolled, func(miner, _ string) bool { return removed(miner) })
	if len(c.unpolled) > 0 {
		select {
		case c.added <- struct{}{}:
		default:
		}
	}
}

// versionsURL builds the dapi /v1/versions endpoint from a miner's inference base URL (scheme+host[:port]).
func versionsURL(base string) string {
	return strings.TrimSuffix(strings.TrimSpace(base), "/") + "/v1/versions"
}

// Poll fetches every candidate.
func (c *VersionsCache) Poll(ctx context.Context) {
	c.mu.Lock()
	candidates := maps.Clone(c.candidates)
	clear(c.unpolled)
	c.mu.Unlock()
	c.poll(ctx, candidates)
}

func (c *VersionsCache) pollUnpolled(ctx context.Context) {
	c.mu.Lock()
	candidates := maps.Clone(c.unpolled)
	clear(c.unpolled)
	c.mu.Unlock()
	c.poll(ctx, candidates)
}

func (c *VersionsCache) poll(ctx context.Context, candidates map[string]string) {
	if len(candidates) == 0 {
		return
	}

	var workers errgroup.Group
	workers.SetLimit(min(versionsPollConcurrency, len(candidates)))
	for miner, base := range candidates {
		workers.Go(func() error {
			if ctx.Err() == nil {
				fetchCtx, cancelFetch := context.WithTimeout(ctx, versionsFetchTimeout)
				nodes := c.fetchOne(fetchCtx, base)
				cancelFetch()
				c.mu.Lock()
				if nodes != nil && c.candidates[miner] == base {
					c.entries[miner] = versionsEntry{capableNodes: nodes, fetchedAt: c.now()}
				}
				c.mu.Unlock()
			}
			return nil
		})
	}
	_ = workers.Wait()
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
	body, err := io.ReadAll(io.LimitReader(resp.Body, versionsResponseMaxBytes))
	if err != nil {
		return nil
	}
	var parsed versionsResponse
	if err := strictjson.Unmarshal(body, &parsed); err != nil {
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

// IsNodeValidationCapable reports whether the node is known validation-inference capable and the knowledge is fresh.
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

// Run polls every candidate every interval, and a new or moved candidate as soon as SetCandidates reports it.
func (c *VersionsCache) Run(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			c.Poll(ctx)
		case <-c.added:
			c.pollUnpolled(ctx)
		}
	}
}
