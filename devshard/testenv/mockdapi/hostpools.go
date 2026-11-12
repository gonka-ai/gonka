package mockdapi

import (
	"context"
	"net"
	"sort"
	"sync"
	"time"

	"common/nodemanager/gen"

	"google.golang.org/grpc/peer"
	"google.golang.org/protobuf/proto"
)

// unmappedHost is the stats key for callers whose peer IP has no host ID.
const unmappedHost = "unmapped"

const (
	// nameRefreshEvery throttles DNS re-resolution triggered by one unknown peer.
	nameRefreshEvery  = time.Second
	nameLookupTimeout = 2 * time.Second
)

// HostStats is one host's row in GET /testenv/host-stats.
type HostStats struct {
	Challenged          bool              `json:"challenged"`
	Nodes               []string          `json:"nodes,omitempty"`
	Allocations         uint64            `json:"allocations"`
	NodeAllocations     map[string]uint64 `json:"node_allocations,omitempty"`
	ChallengeRejections uint64            `json:"challenge_rejections"`
	AcquireErrors       uint64            `json:"acquire_errors"`
}

// hostPools binds ML nodes to hosts so an ML fault lands on one participant,
// like production where each participant's dapi owns its nodes.
type hostPools struct {
	addrs     map[string]string   // peer IP -> host ID
	names     map[string]string   // DNS name (compose service) -> host ID
	pools     map[string][]string // host ID -> bound node IDs
	shared    []string            // fallback pool for unmapped or unbound hosts
	allNodes  []string
	mu        sync.Mutex
	cursor    map[string]int // host ID -> next preferred node index
	challenge map[string]bool
	stats     map[string]*HostStats
	// lookup resolves names; resolved maps their current IPs to hosts.
	lookup     func(ctx context.Context, name string) ([]string, error)
	nameIPs    map[string][]string
	resolved   map[string]string
	lastTry    map[string]time.Time // unknown peer IP -> last refresh it triggered
	minRefresh time.Duration
}

// newHostPools builds the binding. hostAddrs keys are peer IPs or DNS names,
// resolved lazily since hosts start later with dynamic IPs. Unbound nodes are the
// fallback pool, or every node when all are bound, so no caller is starved.
func newHostPools(nodes []MLNode, nodeHosts, hostAddrs map[string]string) *hostPools {
	p := &hostPools{
		addrs:      make(map[string]string, len(hostAddrs)),
		names:      make(map[string]string),
		pools:      make(map[string][]string),
		cursor:     make(map[string]int),
		challenge:  make(map[string]bool),
		stats:      make(map[string]*HostStats),
		lookup:     net.DefaultResolver.LookupHost,
		nameIPs:    make(map[string][]string),
		resolved:   make(map[string]string),
		lastTry:    make(map[string]time.Time),
		minRefresh: nameRefreshEvery,
	}
	for addr, host := range hostAddrs {
		if net.ParseIP(addr) != nil {
			p.addrs[addr] = host
			continue
		}
		p.names[addr] = host
	}
	for _, node := range nodes {
		p.allNodes = append(p.allNodes, node.ID)
		if host := nodeHosts[node.ID]; host != "" {
			p.pools[host] = append(p.pools[host], node.ID)
			continue
		}
		p.shared = append(p.shared, node.ID)
	}
	if len(p.shared) == 0 {
		p.shared = p.allNodes
	}
	return p
}

// hostFor maps the gRPC caller to a host ID, or "" when its IP is not mapped.
// An unknown IP re-resolves the host names, at most once per minRefresh per IP.
func (p *hostPools) hostFor(ctx context.Context) string {
	pr, ok := peer.FromContext(ctx)
	if !ok || pr.Addr == nil {
		return ""
	}
	ip := pr.Addr.String()
	if host, _, err := net.SplitHostPort(ip); err == nil {
		ip = host
	}
	if host := p.addrs[ip]; host != "" {
		return host
	}
	if len(p.names) == 0 {
		return ""
	}
	p.mu.Lock()
	host := p.resolved[ip]
	due := host == "" && time.Since(p.lastTry[ip]) >= p.minRefresh
	if due {
		p.lastTry[ip] = time.Now()
	}
	p.mu.Unlock()
	if due {
		host = p.refresh(ip)
	}
	return host
}

// refresh re-resolves every host name and returns the host now owning ip.
// A name that fails to resolve keeps its previous IPs.
func (p *hostPools) refresh(ip string) string {
	ctx, cancel := context.WithTimeout(context.Background(), nameLookupTimeout)
	defer cancel()
	fresh := make(map[string][]string, len(p.names))
	for name := range p.names {
		if ips, err := p.lookup(ctx, name); err == nil {
			fresh[name] = ips
		}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	for name, ips := range fresh {
		p.nameIPs[name] = ips
	}
	p.resolved = make(map[string]string)
	for name, ips := range p.nameIPs {
		for _, addr := range ips {
			p.resolved[addr] = p.names[name]
		}
	}
	return p.resolved[ip]
}

// scope narrows req to host's pool by excluding every other node, so the
// params server keeps its lock table and stage logs. preferred allows only the
// host's next node in its own rotation, because the params round-robin cursor
// is shared by all hosts and would skew a small pool; the caller retries with
// the full pool (preferred=nil) when that node is busy. Both are nil when the
// pool is the whole node list or p is nil.
func (p *hostPools) scope(host string, req *gen.AcquireMLNodeRequest) (preferred, full *gen.AcquireMLNodeRequest) {
	if p == nil {
		return nil, nil
	}
	pool := p.pools[host]
	if len(pool) == 0 {
		pool = p.shared
	}
	if len(pool) == len(p.allNodes) {
		return nil, nil
	}
	full = excludeAllBut(req, p.allNodes, pool)
	excluded := make(map[string]struct{}, len(req.GetExcludedNodes()))
	for _, id := range req.GetExcludedNodes() {
		excluded[id] = struct{}{}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	for i := 0; i < len(pool); i++ {
		next := pool[p.cursor[host]%len(pool)]
		p.cursor[host]++
		if _, skip := excluded[next]; !skip {
			return excludeAllBut(req, p.allNodes, []string{next}), full
		}
	}
	return full, full
}

func excludeAllBut(req *gen.AcquireMLNodeRequest, all, keep []string) *gen.AcquireMLNodeRequest {
	allowed := make(map[string]struct{}, len(keep))
	for _, id := range keep {
		allowed[id] = struct{}{}
	}
	scoped := proto.Clone(req).(*gen.AcquireMLNodeRequest)
	for _, id := range all {
		if _, ok := allowed[id]; !ok {
			scoped.ExcludedNodes = append(scoped.ExcludedNodes, id)
		}
	}
	return scoped
}

func (p *hostPools) setChallenged(host string, on bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.challenge[host] = on
}

func (p *hostPools) challenged(host string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.challenge[host]
}

func (p *hostPools) rowLocked(host string) *HostStats {
	if host == "" {
		host = unmappedHost
	}
	row, ok := p.stats[host]
	if !ok {
		row = &HostStats{NodeAllocations: make(map[string]uint64)}
		p.stats[host] = row
	}
	return row
}

func (p *hostPools) record(host, nodeID string, err error, rejected bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	row := p.rowLocked(host)
	switch {
	case rejected:
		row.ChallengeRejections++
	case err != nil:
		row.AcquireErrors++
	default:
		row.Allocations++
		row.NodeAllocations[nodeID]++
	}
}

// snapshot lists every configured host, plus any host or unmapped caller seen.
func (p *hostPools) snapshot() map[string]HostStats {
	p.mu.Lock()
	defer p.mu.Unlock()
	hosts := make(map[string]struct{}, len(p.pools)+len(p.addrs))
	for host := range p.pools {
		hosts[host] = struct{}{}
	}
	for _, host := range p.addrs {
		hosts[host] = struct{}{}
	}
	for _, host := range p.names {
		hosts[host] = struct{}{}
	}
	for host := range p.challenge {
		hosts[host] = struct{}{}
	}
	for host := range p.stats {
		hosts[host] = struct{}{}
	}
	out := make(map[string]HostStats, len(hosts))
	for host := range hosts {
		row := *p.rowLocked(host)
		row.Challenged = p.challenge[host]
		row.Nodes = append([]string(nil), p.pools[host]...)
		sort.Strings(row.Nodes)
		counts := make(map[string]uint64, len(row.NodeAllocations))
		for id, n := range row.NodeAllocations {
			counts[id] = n
		}
		row.NodeAllocations = counts
		out[host] = row
	}
	return out
}
