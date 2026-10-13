package transport

import (
	"context"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"devshard/observability"
)

const (
	// RPCZoneShared is the peer weight bucket (unaries except Watch).
	RPCZoneShared = "shared"
	// RPCZoneStreams is Watch / Chat concurrent-stream cap.
	RPCZoneStreams = "streams"
	// RPCZoneAttachFloor is the process-wide Attach floor.
	RPCZoneAttachFloor = "attach_floor"

	rpcTrafficClosedMinutes = 5
	rpcTrafficPeerCap       = 1000
	rpcTrafficIPCap         = 1000
	rpcTrafficOtherKey      = "other"
	rpcTrafficUnknownIP     = "unknown"
	rpcTrafficShards        = 16

	// RPCStatsBannedIdentityLogCap is how many banned peer/IP names fit in
	// one closed-minute warn. Overflow is counted in banned_*_omitted.
	RPCStatsBannedIdentityLogCap = 32

	// RPCStatsLogTag is the closed-minute warn filter tag (Loki/journal).
	// Join Prometheus with rpc_stats_join = host + "/" + minute_unix.
	RPCStatsLogTag = "rpc_stats"

	// ReconnectFirstAttach is the first successful Attach of a PeerConn.
	ReconnectFirstAttach = "first-attach"
	// ReconnectWatch is a Watch stream death that will re-Attach.
	ReconnectWatch = "watch"
	// ReconnectTTL is a token-refresh re-Attach.
	ReconnectTTL = "ttl"
)

// RPCStatsSnapshot is GET /devshard/stats/rpc. Last closed minute.
type RPCStatsSnapshot struct {
	HostAddress     string          `json:"host_address,omitempty"`
	ProtocolVersion string          `json:"protocol_version,omitempty"`
	BinaryVersion   string          `json:"binary_version,omitempty"`
	MinuteUnix      int64           `json:"minute_unix"`
	Partial         bool            `json:"partial,omitempty"`
	Host            RPCStatsHost    `json:"host"`
	Shards          []RPCStatsShard `json:"shards"`
}

// RPCStatsHost is process-wide traffic for one closed minute.
type RPCStatsHost struct {
	Requests   uint64              `json:"requests"`
	Banned     uint64              `json:"banned"`
	Endpoints  []RPCStatsEndpoint  `json:"endpoints"`
	Zones      []RPCStatsZone      `json:"zones"`
	Peers      []RPCStatsPeer      `json:"peers"`
	IPs        []RPCStatsIP        `json:"ips"`
	Reconnects []RPCStatsReconnect `json:"reconnects"`
	Attach     RPCStatsAttach      `json:"attach"`
}

// RPCStatsShard is the same minute sliced by escrow.
type RPCStatsShard struct {
	EscrowID        string             `json:"escrow_id"`
	ProtocolVersion string             `json:"protocol_version,omitempty"`
	Requests        uint64             `json:"requests"`
	Banned          uint64             `json:"banned"`
	Endpoints       []RPCStatsEndpoint `json:"endpoints"`
	Zones           []RPCStatsZone     `json:"zones"`
	Peers           []RPCStatsPeer     `json:"peers"`
}

// RPCStatsEndpoint is per classified Connect method.
type RPCStatsEndpoint struct {
	Endpoint string `json:"endpoint"`
	Zone     string `json:"zone"`
	Requests uint64 `json:"requests"`
	Banned   uint64 `json:"banned"`
}

// RPCStatsZone is per rate-limit zone.
type RPCStatsZone struct {
	Zone     string `json:"zone"`
	Requests uint64 `json:"requests"`
	Banned   uint64 `json:"banned"`
}

// RPCStatsPeer is per authenticated peer (Attach bans use peer "").
type RPCStatsPeer struct {
	Peer     string `json:"peer"`
	Requests uint64 `json:"requests"`
	Banned   uint64 `json:"banned"`
}

// RPCStatsIP is per trusted client IP (post-handshake). Attach is not keyed here.
type RPCStatsIP struct {
	IP       string `json:"ip"`
	Requests uint64 `json:"requests"`
	Banned   uint64 `json:"banned"`
}

// RPCStatsReconnect is outbound Attach / re-attach from this child.
type RPCStatsReconnect struct {
	Peer     string `json:"peer"`
	Reason   string `json:"reason"`
	Attempts uint64 `json:"attempts"`
}

// RPCStatsAttach is the process-wide Attach throttle block.
type RPCStatsAttach struct {
	Attempts    uint64 `json:"attempts"`
	Banned      uint64 `json:"banned"`
	BannedFloor uint64 `json:"banned_floor"`
}

// RPCSample is one classified inbound RPC (or Attach).
type RPCSample struct {
	Procedure   string
	Peer        string
	Escrow      string
	IP          string
	Banned      bool
	Attach      bool
	AttachFloor bool
	StreamCap   bool
}

// RPCRateLimitZone is the bucket that can ban this procedure.
// streamCap is true when the refuse is the concurrent-stream cap (Watch/Chat).
func RPCRateLimitZone(procedure string, streamCap bool) string {
	if procedure == "" {
		return RPCZoneShared
	}
	name := RPCEndpointName(procedure)
	switch name {
	case "Attach":
		return RPCZoneAttachFloor
	case "Watch":
		return RPCZoneStreams
	case "Chat":
		if streamCap {
			return RPCZoneStreams
		}
		return RPCZoneShared
	default:
		return RPCZoneShared
	}
}

type counts struct {
	requests uint64
	banned   uint64
}

type minuteBucket struct {
	endpoints map[string]*counts // endpoint\x00zone
	zones     map[string]*counts
	peers     map[string]*counts
	ips       map[string]*counts
	escrows   map[string]*escrowMinute
	attach    RPCStatsAttach
}

type escrowMinute struct {
	total     counts
	endpoints map[string]*counts
	zones     map[string]*counts
	peers     map[string]*counts
}

type reconnectMinute struct {
	byKey map[string]uint64 // peer\x00reason
}

type trafficShard struct {
	mu      sync.Mutex
	hits    atomic.Uint64
	open    int64
	current *minuteBucket
	closed  map[int64]*minuteBucket
}

// minuteKeyBudget is the process-wide set of named keys for one dimension
// (peers or IPs) in one minute. The mutex is taken only when a shard has
// not seen the key yet. An IP is stored on the peer's shard, so the same
// address must not consume a slot per shard.
type minuteKeyBudget struct {
	mu      sync.Mutex
	minutes map[int64]map[string]struct{}
}

// RPCTraffic is the in-memory minute ring for one child mux. Observe shards
// by peer (or IP) so inbound RPCs do not share one process-wide mutex.
// Attach has no key, so it round-robins across shards.
type RPCTraffic struct {
	now  func() time.Time
	warn func(ctx context.Context, minute int64, host RPCStatsHost)

	shards [rpcTrafficShards]trafficShard

	sweepMu    sync.Mutex
	warned     map[int64]struct{}
	peerBudget minuteKeyBudget
	ipBudget   minuteKeyBudget
	attachSeq  atomic.Uint64
}

// NewRPCTraffic records inbound classified RPCs. now nil uses time.Now.
func NewRPCTraffic(now func() time.Time) *RPCTraffic {
	if now == nil {
		now = time.Now
	}
	t := &RPCTraffic{now: now}
	for i := range t.shards {
		t.shards[i].closed = make(map[int64]*minuteBucket)
	}
	t.warn = t.defaultWarn
	return t
}

// SetWarn replaces the closed-minute host warn (tests).
func (t *RPCTraffic) SetWarn(fn func(ctx context.Context, minute int64, host RPCStatsHost)) {
	if t == nil {
		return
	}
	t.warn = fn
}

func (t *RPCTraffic) defaultWarn(ctx context.Context, minute int64, host RPCStatsHost) {
	kv := []any{"minute_unix", minute, "banned", host.Banned, "banned_floor", host.Attach.BannedFloor}
	kv = AppendRPCStatsLogTag(kv, "", minute)
	for _, z := range host.Zones {
		if z.Banned > 0 {
			kv = append(kv, "zone_"+z.Zone, z.Banned)
		}
	}
	for _, e := range host.Endpoints {
		if e.Banned > 0 {
			kv = append(kv, "endpoint_"+e.Endpoint, e.Banned)
		}
	}
	kv = AppendBannedIdentityLog(kv, host)
	observability.Log(ctx, observability.LevelWarn, "rpc rate limit closed minute",
		observability.StageReceived, observability.WhereTransportRateLimit,
		"", observability.ReasonRateLimited, nil, kv...)
}

// RPCStatsJoin is host/minute_unix. Same host as Prometheus {host=...}.
func RPCStatsJoin(host string, minuteUnix int64) string {
	host = strings.TrimSpace(host)
	if host == "" {
		host = "unknown"
	}
	return host + "/" + strconv.FormatInt(minuteUnix, 10)
}

// AppendRPCStatsLogTag adds tag=rpc_stats and rpc_stats_join for log search.
func AppendRPCStatsLogTag(kv []any, host string, minuteUnix int64) []any {
	return append(kv,
		"tag", RPCStatsLogTag,
		"rpc_stats_join", RPCStatsJoin(host, minuteUnix),
	)
}

// AppendBannedIdentityLog adds compact peer/IP fields for rows with banned>0.
// Join Prometheus to this line with minute_unix + host_address (not a unique log_id label).
func AppendBannedIdentityLog(kv []any, host RPCStatsHost) []any {
	peers, peerRows, peerOmit := topBannedPeers(host.Peers, RPCStatsBannedIdentityLogCap)
	ips, ipRows, ipOmit := topBannedIPs(host.IPs, RPCStatsBannedIdentityLogCap)
	return append(kv,
		"banned_peers", joinBannedPeers(peers),
		"banned_peer_rows", peerRows,
		"banned_peer_omitted", peerOmit,
		"banned_ips", joinBannedIPs(ips),
		"banned_ip_rows", ipRows,
		"banned_ip_omitted", ipOmit,
	)
}

func topBannedPeers(rows []RPCStatsPeer, capN int) (out []RPCStatsPeer, rowsN, omitted int) {
	var hit []RPCStatsPeer
	for _, p := range rows {
		if p.Banned == 0 {
			continue
		}
		hit = append(hit, p)
	}
	sort.Slice(hit, func(i, j int) bool {
		if hit[i].Banned != hit[j].Banned {
			return hit[i].Banned > hit[j].Banned
		}
		return hit[i].Peer < hit[j].Peer
	})
	return clipBanned(hit, capN)
}

func topBannedIPs(rows []RPCStatsIP, capN int) (out []RPCStatsIP, rowsN, omitted int) {
	var hit []RPCStatsIP
	for _, p := range rows {
		if p.Banned == 0 {
			continue
		}
		hit = append(hit, p)
	}
	sort.Slice(hit, func(i, j int) bool {
		if hit[i].Banned != hit[j].Banned {
			return hit[i].Banned > hit[j].Banned
		}
		return hit[i].IP < hit[j].IP
	})
	return clipBanned(hit, capN)
}

func clipBanned[T any](hit []T, capN int) (out []T, rowsN, omitted int) {
	rowsN = len(hit)
	if capN < 0 {
		capN = 0
	}
	if len(hit) > capN {
		omitted = len(hit) - capN
		hit = hit[:capN]
	}
	return hit, rowsN, omitted
}

func joinBannedPeers(rows []RPCStatsPeer) string {
	var b strings.Builder
	for i, p := range rows {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(p.Peer)
		b.WriteByte('=')
		b.WriteString(strconv.FormatUint(p.Banned, 10))
	}
	return b.String()
}

func joinBannedIPs(rows []RPCStatsIP) string {
	var b strings.Builder
	for i, p := range rows {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(p.IP)
		b.WriteByte('=')
		b.WriteString(strconv.FormatUint(p.Banned, 10))
	}
	return b.String()
}

func newMinuteBucket() *minuteBucket {
	return &minuteBucket{
		endpoints: make(map[string]*counts),
		zones:     make(map[string]*counts),
		peers:     make(map[string]*counts),
		ips:       make(map[string]*counts),
		escrows:   make(map[string]*escrowMinute),
	}
}

func addCounts(m map[string]*counts, key string, banned bool) {
	c := m[key]
	if c == nil {
		c = &counts{}
		m[key] = c
	}
	c.requests++
	if banned {
		c.banned++
	}
}

func addCountsN(m map[string]*counts, key string, req, ban uint64) {
	if req == 0 && ban == 0 {
		return
	}
	c := m[key]
	if c == nil {
		c = &counts{}
		m[key] = c
	}
	c.requests += req
	c.banned += ban
}

// allow reports whether key may be stored for minute. A key already named
// this minute is allowed on every shard. A new key past cap folds into "other".
func (b *minuteKeyBudget) allow(minute int64, key string, cap int) bool {
	if b == nil || cap < 1 || key == "" {
		return true
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.minutes == nil {
		b.minutes = make(map[int64]map[string]struct{})
	}
	names := b.minutes[minute]
	if names == nil {
		names = make(map[string]struct{})
		b.minutes[minute] = names
		for m := range b.minutes {
			if m < minute-int64(rpcTrafficClosedMinutes)-1 {
				delete(b.minutes, m)
			}
		}
	}
	if _, ok := names[key]; ok {
		return true
	}
	if len(names) >= cap {
		return false
	}
	names[key] = struct{}{}
	return true
}

func (t *RPCTraffic) shardIndex(s RPCSample) int {
	if t != nil && s.Attach {
		n := t.attachSeq.Add(1)
		return int((n - 1) % uint64(rpcTrafficShards))
	}
	key := s.Peer
	if key == "" {
		key = s.IP
	}
	if key == "" {
		return 0
	}
	var h uint32 = 2166136261
	for i := 0; i < len(key); i++ {
		h ^= uint32(key[i])
		h *= 16777619
	}
	return int(h % uint32(rpcTrafficShards))
}

func mergeMinute(dst, src *minuteBucket) {
	if dst == nil || src == nil {
		return
	}
	for k, c := range src.endpoints {
		if c != nil {
			addCountsN(dst.endpoints, k, c.requests, c.banned)
		}
	}
	for k, c := range src.zones {
		if c != nil {
			addCountsN(dst.zones, k, c.requests, c.banned)
		}
	}
	for k, c := range src.peers {
		if c != nil {
			addCountsN(dst.peers, k, c.requests, c.banned)
		}
	}
	for k, c := range src.ips {
		if c != nil {
			addCountsN(dst.ips, k, c.requests, c.banned)
		}
	}
	dst.attach.Attempts += src.attach.Attempts
	dst.attach.Banned += src.attach.Banned
	dst.attach.BannedFloor += src.attach.BannedFloor
	for id, em := range src.escrows {
		if em == nil {
			continue
		}
		dm := dst.escrows[id]
		if dm == nil {
			dm = &escrowMinute{
				endpoints: make(map[string]*counts),
				zones:     make(map[string]*counts),
				peers:     make(map[string]*counts),
			}
			dst.escrows[id] = dm
		}
		dm.total.requests += em.total.requests
		dm.total.banned += em.total.banned
		for k, c := range em.endpoints {
			if c != nil {
				addCountsN(dm.endpoints, k, c.requests, c.banned)
			}
		}
		for k, c := range em.zones {
			if c != nil {
				addCountsN(dm.zones, k, c.requests, c.banned)
			}
		}
		for k, c := range em.peers {
			if c != nil {
				addCountsN(dm.peers, k, c.requests, c.banned)
			}
		}
	}
}

func endpointKey(endpoint, zone string) string {
	return endpoint + "\x00" + zone
}

func splitEndpointKey(k string) (endpoint, zone string) {
	for i := 0; i < len(k); i++ {
		if k[i] == 0 {
			return k[:i], k[i+1:]
		}
	}
	return k, ""
}

func foldNewKey(m map[string]*counts, key string, allow func() bool) string {
	if key == "" {
		return ""
	}
	if _, ok := m[key]; ok {
		return key
	}
	if allow != nil && !allow() {
		return rpcTrafficOtherKey
	}
	return key
}

// Observe records one classified inbound RPC. Unimplemented / unauthenticated
// paths must not call this. Escrow creates a shard row: observeRPC only
// passes a live local session id, never a raw URL :id.
func (t *RPCTraffic) Observe(ctx context.Context, s RPCSample) {
	if t == nil {
		return
	}
	now := t.now()
	minute := now.Unix() / 60
	endpoint := RPCEndpointName(s.Procedure)
	if endpoint == "" {
		endpoint = "other"
	}
	zone := RPCRateLimitZone(s.Procedure, s.StreamCap)
	if s.Attach {
		zone = RPCZoneAttachFloor
		endpoint = "Attach"
	}

	advanced := t.shards[t.shardIndex(s)].observe(minute, endpoint, zone, s, &t.peerBudget, &t.ipBudget)
	if advanced {
		t.sweepClosed(ctx, minute)
	}

	result := "ok"
	if s.Banned {
		result = "banned"
	}
	observability.IncPeerRPCRequests(endpoint, result)
	if s.Banned {
		observability.IncPeerRPCBanned(endpoint, zone)
		if s.Attach {
			reason := "other"
			if s.AttachFloor {
				reason = "floor"
			}
			observability.IncPeerRPCAttachBanned(reason)
		}
	}
}

func (s *trafficShard) observe(minute int64, endpoint, zone string, sample RPCSample, peers, ips *minuteKeyBudget) (advanced bool) {
	s.hits.Add(1)
	s.mu.Lock()
	defer s.mu.Unlock()
	advanced = s.rollLocked(minute)
	b := s.current
	addCounts(b.endpoints, endpointKey(endpoint, zone), sample.Banned)
	addCounts(b.zones, zone, sample.Banned)
	peer := sample.Peer
	budgetMinute := s.open
	if !sample.Attach {
		peer = foldNewKey(b.peers, sample.Peer, func() bool {
			return peers.allow(budgetMinute, sample.Peer, rpcTrafficPeerCap)
		})
	}
	addCounts(b.peers, peer, sample.Banned)
	if sample.Attach {
		b.attach.Attempts++
		if sample.Banned {
			b.attach.Banned++
		}
		if sample.AttachFloor {
			b.attach.BannedFloor++
		}
		return advanced
	}
	ip := sample.IP
	if ip == "" {
		ip = rpcTrafficUnknownIP
	}
	ip = foldNewKey(b.ips, ip, func() bool {
		return ips.allow(budgetMinute, ip, rpcTrafficIPCap)
	})
	addCounts(b.ips, ip, sample.Banned)
	if sample.Escrow != "" && sample.Escrow != HostRPCEscrowID {
		em := b.escrows[sample.Escrow]
		if em == nil {
			em = &escrowMinute{
				endpoints: make(map[string]*counts),
				zones:     make(map[string]*counts),
				peers:     make(map[string]*counts),
			}
			b.escrows[sample.Escrow] = em
		}
		em.total.requests++
		if sample.Banned {
			em.total.banned++
		}
		addCounts(em.endpoints, endpointKey(endpoint, zone), sample.Banned)
		addCounts(em.zones, zone, sample.Banned)
		addCounts(em.peers, peer, sample.Banned)
	}
	return advanced
}

// rollLocked advances the open minute. It does not prune: sweepClosed merges
// a closed minute before the ring drops it. Callers must hold s.mu.
func (s *trafficShard) rollLocked(minute int64) (advanced bool) {
	if s.current == nil {
		s.open = minute
		s.current = newMinuteBucket()
		return false
	}
	if minute <= s.open {
		return false
	}
	s.closed[s.open] = s.current
	s.current = newMinuteBucket()
	s.open = minute
	return true
}

func zoneBanned(b *minuteBucket) uint64 {
	if b == nil {
		return 0
	}
	var n uint64
	for _, c := range b.zones {
		if c != nil {
			n += c.banned
		}
	}
	return n
}

// sweepClosed rolls every shard to nowMinute, warns once per closed minute
// with the merged ban count, then prunes. The warn runs without shard locks.
// warned is a set so a late shard cannot consume a newer minute's slot.
func (t *RPCTraffic) sweepClosed(ctx context.Context, nowMinute int64) {
	if t == nil {
		return
	}
	t.sweepMu.Lock()
	for i := range t.shards {
		sh := &t.shards[i]
		sh.mu.Lock()
		sh.rollLocked(nowMinute)
		sh.mu.Unlock()
	}
	seen := make(map[int64]struct{})
	mins := make([]int64, 0, rpcTrafficClosedMinutes+1)
	for i := range t.shards {
		sh := &t.shards[i]
		sh.mu.Lock()
		for m := range sh.closed {
			if m >= nowMinute {
				continue
			}
			if _, ok := seen[m]; ok {
				continue
			}
			seen[m] = struct{}{}
			mins = append(mins, m)
		}
		sh.mu.Unlock()
	}
	sort.Slice(mins, func(i, j int) bool { return mins[i] < mins[j] })

	type pendingWarn struct {
		minute int64
		host   RPCStatsHost
	}
	var pending []pendingWarn
	if t.warned == nil {
		t.warned = make(map[int64]struct{})
	}
	for _, m := range mins {
		if _, ok := t.warned[m]; ok {
			continue
		}
		t.warned[m] = struct{}{}
		b := t.mergeClosedMinute(m)
		if zoneBanned(b) == 0 || t.warn == nil {
			continue
		}
		pending = append(pending, pendingWarn{minute: m, host: snapshotHost(b)})
	}
	cutoff := nowMinute - rpcTrafficClosedMinutes
	for i := range t.shards {
		sh := &t.shards[i]
		sh.mu.Lock()
		for m := range sh.closed {
			if m < cutoff {
				delete(sh.closed, m)
			}
		}
		sh.mu.Unlock()
	}
	for m := range t.warned {
		if m < cutoff {
			delete(t.warned, m)
		}
	}
	warn := t.warn
	t.sweepMu.Unlock()

	if warn == nil {
		return
	}
	for _, p := range pending {
		warn(ctx, p.minute*60, p.host)
	}
}

func (t *RPCTraffic) mergeClosedMinute(minute int64) *minuteBucket {
	merged := newMinuteBucket()
	for i := range t.shards {
		sh := &t.shards[i]
		sh.mu.Lock()
		mergeMinute(merged, sh.closed[minute])
		sh.mu.Unlock()
	}
	return merged
}

// Snapshot is the last closed minute (now/60 - 1). Empty if nothing recorded.
func (t *RPCTraffic) Snapshot(now time.Time) RPCStatsSnapshot {
	if t == nil {
		return emptySnapshot(now)
	}
	nowMinute := now.Unix() / 60
	closedMinute := nowMinute - 1
	t.sweepClosed(context.Background(), nowMinute)
	merged := t.mergeClosedMinute(closedMinute)
	out := emptySnapshot(now)
	if len(merged.endpoints) == 0 && merged.attach.Attempts == 0 {
		return out
	}
	out.Host = snapshotHost(merged)
	out.Shards = snapshotShards(merged)
	return out
}

func emptySnapshot(now time.Time) RPCStatsSnapshot {
	closedMinute := now.Unix()/60 - 1
	return RPCStatsSnapshot{
		MinuteUnix: closedMinute * 60,
		Host: RPCStatsHost{
			Endpoints:  []RPCStatsEndpoint{},
			Zones:      []RPCStatsZone{},
			Peers:      []RPCStatsPeer{},
			IPs:        []RPCStatsIP{},
			Reconnects: []RPCStatsReconnect{},
		},
		Shards: []RPCStatsShard{},
	}
}

func snapshotHost(b *minuteBucket) RPCStatsHost {
	h := RPCStatsHost{
		Endpoints:  []RPCStatsEndpoint{},
		Zones:      []RPCStatsZone{},
		Peers:      []RPCStatsPeer{},
		IPs:        []RPCStatsIP{},
		Reconnects: []RPCStatsReconnect{},
		Attach:     b.attach,
	}
	for k, c := range b.endpoints {
		if c == nil {
			continue
		}
		ep, zone := splitEndpointKey(k)
		h.Endpoints = append(h.Endpoints, RPCStatsEndpoint{
			Endpoint: ep, Zone: zone, Requests: c.requests, Banned: c.banned,
		})
		h.Requests += c.requests
		h.Banned += c.banned
	}
	for zone, c := range b.zones {
		if c == nil {
			continue
		}
		h.Zones = append(h.Zones, RPCStatsZone{Zone: zone, Requests: c.requests, Banned: c.banned})
	}
	for peer, c := range b.peers {
		if c == nil {
			continue
		}
		h.Peers = append(h.Peers, RPCStatsPeer{Peer: peer, Requests: c.requests, Banned: c.banned})
	}
	for ip, c := range b.ips {
		if c == nil {
			continue
		}
		h.IPs = append(h.IPs, RPCStatsIP{IP: ip, Requests: c.requests, Banned: c.banned})
	}
	sort.Slice(h.Endpoints, func(i, j int) bool { return h.Endpoints[i].Endpoint < h.Endpoints[j].Endpoint })
	sort.Slice(h.Zones, func(i, j int) bool { return h.Zones[i].Zone < h.Zones[j].Zone })
	sort.Slice(h.Peers, func(i, j int) bool { return h.Peers[i].Peer < h.Peers[j].Peer })
	sort.Slice(h.IPs, func(i, j int) bool { return h.IPs[i].IP < h.IPs[j].IP })
	return h
}

func snapshotShards(b *minuteBucket) []RPCStatsShard {
	out := make([]RPCStatsShard, 0, len(b.escrows))
	for id, em := range b.escrows {
		if em == nil {
			continue
		}
		sh := RPCStatsShard{
			EscrowID:  id,
			Requests:  em.total.requests,
			Banned:    em.total.banned,
			Endpoints: []RPCStatsEndpoint{},
			Zones:     []RPCStatsZone{},
			Peers:     []RPCStatsPeer{},
		}
		for k, c := range em.endpoints {
			if c == nil {
				continue
			}
			ep, zone := splitEndpointKey(k)
			sh.Endpoints = append(sh.Endpoints, RPCStatsEndpoint{
				Endpoint: ep, Zone: zone, Requests: c.requests, Banned: c.banned,
			})
		}
		for zone, c := range em.zones {
			if c == nil {
				continue
			}
			sh.Zones = append(sh.Zones, RPCStatsZone{Zone: zone, Requests: c.requests, Banned: c.banned})
		}
		for peer, c := range em.peers {
			if c == nil {
				continue
			}
			sh.Peers = append(sh.Peers, RPCStatsPeer{Peer: peer, Requests: c.requests, Banned: c.banned})
		}
		sort.Slice(sh.Endpoints, func(i, j int) bool { return sh.Endpoints[i].Endpoint < sh.Endpoints[j].Endpoint })
		sort.Slice(sh.Zones, func(i, j int) bool { return sh.Zones[i].Zone < sh.Zones[j].Zone })
		sort.Slice(sh.Peers, func(i, j int) bool { return sh.Peers[i].Peer < sh.Peers[j].Peer })
		out = append(out, sh)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].EscrowID < out[j].EscrowID })
	return out
}

// EnsureShard adds a zero shard row so a served escrow with no RPC this
// minute still appears.
func EnsureShard(snap *RPCStatsSnapshot, escrowID, protocolVersion string) {
	if snap == nil || escrowID == "" || escrowID == HostRPCEscrowID {
		return
	}
	for i := range snap.Shards {
		if snap.Shards[i].EscrowID == escrowID {
			if snap.Shards[i].ProtocolVersion == "" {
				snap.Shards[i].ProtocolVersion = protocolVersion
			}
			return
		}
	}
	snap.Shards = append(snap.Shards, RPCStatsShard{
		EscrowID:        escrowID,
		ProtocolVersion: protocolVersion,
		Endpoints:       []RPCStatsEndpoint{},
		Zones:           []RPCStatsZone{},
		Peers:           []RPCStatsPeer{},
	})
	sort.Slice(snap.Shards, func(i, j int) bool { return snap.Shards[i].EscrowID < snap.Shards[j].EscrowID })
}

type reconnectStore struct {
	mu     sync.Mutex
	open   int64
	cur    *reconnectMinute
	closed map[int64]*reconnectMinute
}

var processReconnects = newReconnectStore()

func newReconnectStore() *reconnectStore {
	return &reconnectStore{closed: make(map[int64]*reconnectMinute)}
}

func (s *reconnectStore) rollLocked(minute int64) {
	if s.cur == nil {
		s.open = minute
		s.cur = &reconnectMinute{byKey: make(map[string]uint64)}
		return
	}
	if minute == s.open {
		return
	}
	s.closed[s.open] = s.cur
	for m := range s.closed {
		if m < minute-rpcTrafficClosedMinutes {
			delete(s.closed, m)
		}
	}
	s.open = minute
	s.cur = &reconnectMinute{byKey: make(map[string]uint64)}
}

func (s *reconnectStore) add(now time.Time, peer, reason string) {
	if s == nil || peer == "" || reason == "" {
		return
	}
	minute := now.Unix() / 60
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rollLocked(minute)
	s.cur.byKey[peer+"\x00"+reason]++
}

func (s *reconnectStore) snapshot(now time.Time) []RPCStatsReconnect {
	closed := now.Unix()/60 - 1
	s.mu.Lock()
	s.rollLocked(now.Unix() / 60)
	b := s.closed[closed]
	s.mu.Unlock()
	if b == nil {
		return []RPCStatsReconnect{}
	}
	out := make([]RPCStatsReconnect, 0, len(b.byKey))
	for k, n := range b.byKey {
		peer, reason := splitEndpointKey(k)
		out = append(out, RPCStatsReconnect{Peer: peer, Reason: reason, Attempts: n})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Peer != out[j].Peer {
			return out[i].Peer < out[j].Peer
		}
		return out[i].Reason < out[j].Reason
	})
	return out
}

// RecordPeerReconnect counts an outbound Attach / re-attach on this child.
func RecordPeerReconnect(peer, reason string) {
	processReconnects.add(time.Now(), peer, reason)
}

// SnapshotPeerReconnects is the last closed minute of outbound reconnects.
func SnapshotPeerReconnects(now time.Time) []RPCStatsReconnect {
	return processReconnects.snapshot(now)
}

// ResetPeerReconnectsForTest clears outbound reconnect buckets.
func ResetPeerReconnectsForTest() {
	processReconnects = newReconnectStore()
}
