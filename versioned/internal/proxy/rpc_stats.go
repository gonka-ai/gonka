package proxy

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	rpcStatsParentTimeout = 2 * time.Second
	rpcStatsChildTimeout  = time.Second
	rpcStatsMergeSlack    = 150 * time.Millisecond
	rpcStatsMergeCacheTTL = 15 * time.Second
	rpcStatsMaxBodyBytes  = 8 << 20
	rpcStatsGzipFloor     = 1 << 10
	rpcStatsGzipEncoding  = "gzip"
)

var errRPCStatsUnavailable = errors.New("rpc stats unavailable")

type rpcStatsSnapshot struct {
	HostAddress     string          `json:"host_address,omitempty"`
	ProtocolVersion string          `json:"protocol_version,omitempty"`
	BinaryVersion   string          `json:"binary_version,omitempty"`
	MinuteUnix      int64           `json:"minute_unix"`
	Partial         bool            `json:"partial,omitempty"`
	Host            rpcStatsHost    `json:"host"`
	Shards          []rpcStatsShard `json:"shards"`
}

type rpcStatsHost struct {
	Requests   uint64              `json:"requests"`
	Banned     uint64              `json:"banned"`
	Endpoints  []rpcStatsEndpoint  `json:"endpoints"`
	Zones      []rpcStatsZone      `json:"zones"`
	Peers      []rpcStatsPeer      `json:"peers"`
	IPs        []rpcStatsIP        `json:"ips"`
	Reconnects []rpcStatsReconnect `json:"reconnects"`
	Attach     rpcStatsAttach      `json:"attach"`
}

type rpcStatsShard struct {
	EscrowID        string             `json:"escrow_id"`
	ProtocolVersion string             `json:"protocol_version,omitempty"`
	Requests        uint64             `json:"requests"`
	Banned          uint64             `json:"banned"`
	Endpoints       []rpcStatsEndpoint `json:"endpoints"`
	Zones           []rpcStatsZone     `json:"zones"`
	Peers           []rpcStatsPeer     `json:"peers"`
}

type rpcStatsEndpoint struct {
	Endpoint string `json:"endpoint"`
	Zone     string `json:"zone"`
	Requests uint64 `json:"requests"`
	Banned   uint64 `json:"banned"`
}

type rpcStatsZone struct {
	Zone     string `json:"zone"`
	Requests uint64 `json:"requests"`
	Banned   uint64 `json:"banned"`
}

type rpcStatsPeer struct {
	Peer     string `json:"peer"`
	Requests uint64 `json:"requests"`
	Banned   uint64 `json:"banned"`
}

type rpcStatsIP struct {
	IP       string `json:"ip"`
	Requests uint64 `json:"requests"`
	Banned   uint64 `json:"banned"`
}

type rpcStatsReconnect struct {
	Peer     string `json:"peer"`
	Reason   string `json:"reason"`
	Attempts uint64 `json:"attempts"`
}

type rpcStatsAttach struct {
	Attempts    uint64 `json:"attempts"`
	Banned      uint64 `json:"banned"`
	BannedFloor uint64 `json:"banned_floor"`
}

func isRPCStatsPath(rest string) bool {
	return strings.TrimPrefix(rest, "/") == "stats/rpc"
}

func serveRPCStatsMerge(w http.ResponseWriter, r *http.Request, routes routeTableLoader) {
	routeMap, _ := routes.Load().(RouteTable)
	versions := sortedVersions(routeMap)
	if len(versions) == 0 {
		http.Error(w, "no versions available", http.StatusServiceUnavailable)
		return
	}

	key := rpcStatsMergeCacheKey(routes, time.Now().Unix()/60)
	body, err := rpcStatsCache.do(key, rpcStatsMergeCacheTTL, func() ([]byte, error) {
		// Dedicated timeout, not r.Context(): waiters on this flight (other
		// gateways, a later poll tick) must not inherit a cancelled debug GET
		// or a caller already at deadline.
		mergeCtx, cancel := context.WithTimeout(context.Background(), rpcStatsParentTimeout)
		defer cancel()
		return mergeRPCStatsJSON(mergeCtx, routes, versions)
	})
	if err != nil {
		if errors.Is(err, errRPCStatsUnavailable) {
			http.Error(w, "rpc stats unavailable", http.StatusBadGateway)
			return
		}
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=UTF-8")
	if acceptsGzip(r.Header) && len(body) >= rpcStatsGzipFloor {
		gz, err := gzipBestSpeed(body)
		if err == nil {
			w.Header().Set("Content-Encoding", rpcStatsGzipEncoding)
			body = gz
		}
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

func mergeRPCStatsJSON(ctx context.Context, routes routeTableLoader, versions []string) ([]byte, error) {
	childCtx, cancel := rpcStatsChildContext(ctx)
	defer cancel()

	type childResult struct {
		ver  string
		snap rpcStatsSnapshot
		ok   bool
	}
	results := make([]childResult, len(versions))
	var wg sync.WaitGroup
	for i, ver := range versions {
		wg.Add(1)
		go func(i int, ver string) {
			defer wg.Done()
			target, ok := acquireTarget(routes, ver)
			if !ok {
				return
			}
			defer target.release()
			snap, err := fetchChildRPCStats(childCtx, target.Address())
			if err != nil {
				return
			}
			if snap.ProtocolVersion == "" {
				snap.ProtocolVersion = ver
			}
			results[i] = childResult{ver: ver, snap: snap, ok: true}
		}(i, ver)
	}
	wg.Wait()

	var parts []rpcStatsSnapshot
	failed := 0
	for _, r := range results {
		if r.ok {
			parts = append(parts, r.snap)
			continue
		}
		failed++
	}
	if len(parts) == 0 {
		return nil, errRPCStatsUnavailable
	}
	merged := mergeRPCStats(parts)
	if failed > 0 {
		merged.Partial = true
	}
	return json.Marshal(merged)
}

func rpcStatsChildContext(parent context.Context) (context.Context, context.CancelFunc) {
	budget := rpcStatsChildTimeout
	if dl, ok := parent.Deadline(); ok {
		remain := time.Until(dl) - rpcStatsMergeSlack
		if remain < 50*time.Millisecond {
			remain = 50 * time.Millisecond
		}
		if remain < budget {
			budget = remain
		}
	}
	return context.WithTimeout(parent, budget)
}

func rpcStatsMergeCacheKey(routes routeTableLoader, minute int64) string {
	routeMap, _ := routes.Load().(RouteTable)
	versions := sortedVersions(routeMap)
	parts := make([]string, 0, len(versions)+1)
	parts = append(parts, strconv.FormatInt(minute, 10))
	for _, ver := range versions {
		addr := ""
		if t := routeMap[ver]; t != nil {
			addr = t.Address()
		}
		parts = append(parts, ver+"="+addr)
	}
	return strings.Join(parts, "|")
}

type rpcStatsMergeCall struct {
	done chan struct{}
	body []byte
	err  error
}

type rpcStatsMergeCache struct {
	mu       sync.Mutex
	inFlight map[string]*rpcStatsMergeCall
	body     []byte
	key      string
	at       time.Time
}

var rpcStatsCache = &rpcStatsMergeCache{inFlight: map[string]*rpcStatsMergeCall{}}

func (c *rpcStatsMergeCache) do(key string, ttl time.Duration, fn func() ([]byte, error)) ([]byte, error) {
	c.mu.Lock()
	if c.body != nil && c.key == key && time.Since(c.at) < ttl {
		body := c.body
		c.mu.Unlock()
		return body, nil
	}
	if call := c.inFlight[key]; call != nil {
		c.mu.Unlock()
		<-call.done
		return call.body, call.err
	}
	call := &rpcStatsMergeCall{done: make(chan struct{})}
	c.inFlight[key] = call
	c.mu.Unlock()

	body, err := fn()
	c.mu.Lock()
	delete(c.inFlight, key)
	if err == nil {
		c.body = body
		c.key = key
		c.at = time.Now()
	}
	call.body = body
	call.err = err
	close(call.done)
	c.mu.Unlock()
	return body, err
}

func (c *rpcStatsMergeCache) reset() {
	c.mu.Lock()
	c.body = nil
	c.key = ""
	c.at = time.Time{}
	c.mu.Unlock()
}

func fetchChildRPCStats(ctx context.Context, addr string) (rpcStatsSnapshot, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+addr+"/stats/rpc", nil)
	if err != nil {
		return rpcStatsSnapshot{}, err
	}
	req.Header.Set("Accept-Encoding", rpcStatsGzipEncoding)
	resp, err := rpcStatsHTTPClient.Do(req)
	if err != nil {
		return rpcStatsSnapshot{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, rpcStatsMaxBodyBytes))
		return rpcStatsSnapshot{}, io.ErrUnexpectedEOF
	}
	raw, err := readLimited(resp.Body, rpcStatsMaxBodyBytes)
	if err != nil {
		return rpcStatsSnapshot{}, err
	}
	if strings.EqualFold(resp.Header.Get("Content-Encoding"), rpcStatsGzipEncoding) {
		gr, err := gzip.NewReader(bytes.NewReader(raw))
		if err != nil {
			return rpcStatsSnapshot{}, err
		}
		raw, err = readLimited(gr, rpcStatsMaxBodyBytes)
		_ = gr.Close()
		if err != nil {
			return rpcStatsSnapshot{}, err
		}
	}
	var snap rpcStatsSnapshot
	if err := json.Unmarshal(raw, &snap); err != nil {
		return rpcStatsSnapshot{}, err
	}
	return snap, nil
}

var rpcStatsHTTPClient = &http.Client{
	Timeout: rpcStatsChildTimeout,
	Transport: &http.Transport{
		DisableCompression: true,
		Proxy:              http.ProxyFromEnvironment,
	},
}

func readLimited(r io.Reader, max int64) ([]byte, error) {
	raw, err := io.ReadAll(io.LimitReader(r, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(raw)) > max {
		return nil, errors.New("rpc stats body too large")
	}
	return raw, nil
}

func mergeRPCStats(parts []rpcStatsSnapshot) rpcStatsSnapshot {
	out := rpcStatsSnapshot{
		Host: rpcStatsHost{
			Endpoints:  []rpcStatsEndpoint{},
			Zones:      []rpcStatsZone{},
			Peers:      []rpcStatsPeer{},
			IPs:        []rpcStatsIP{},
			Reconnects: []rpcStatsReconnect{},
		},
		Shards: []rpcStatsShard{},
	}
	if len(parts) == 0 {
		return out
	}
	out.HostAddress = parts[0].HostAddress
	out.ProtocolVersion = parts[len(parts)-1].ProtocolVersion
	out.BinaryVersion = parts[len(parts)-1].BinaryVersion
	minutes := map[int64]struct{}{}
	ep := map[string]*rpcStatsEndpoint{}
	zones := map[string]*rpcStatsZone{}
	peers := map[string]*rpcStatsPeer{}
	ips := map[string]*rpcStatsIP{}
	reconnects := map[string]*rpcStatsReconnect{}
	var shards []rpcStatsShard

	for _, p := range parts {
		if p.HostAddress != "" && out.HostAddress == "" {
			out.HostAddress = p.HostAddress
		}
		minutes[p.MinuteUnix] = struct{}{}
		if p.MinuteUnix > out.MinuteUnix {
			out.MinuteUnix = p.MinuteUnix
		}
		out.Host.Requests += p.Host.Requests
		out.Host.Banned += p.Host.Banned
		out.Host.Attach.Attempts += p.Host.Attach.Attempts
		out.Host.Attach.Banned += p.Host.Attach.Banned
		out.Host.Attach.BannedFloor += p.Host.Attach.BannedFloor
		mergeEndpoints(ep, p.Host.Endpoints)
		mergeZones(zones, p.Host.Zones)
		mergePeers(peers, p.Host.Peers)
		mergeIPs(ips, p.Host.IPs)
		mergeReconnects(reconnects, p.Host.Reconnects)
		for _, sh := range p.Shards {
			if sh.ProtocolVersion == "" {
				sh.ProtocolVersion = p.ProtocolVersion
			}
			shards = append(shards, sh)
		}
	}
	if len(minutes) > 1 {
		out.Partial = true
	}
	out.Host.Endpoints = endpointValues(ep)
	out.Host.Zones = zoneValues(zones)
	out.Host.Peers = peerValues(peers)
	out.Host.IPs = ipValues(ips)
	out.Host.Reconnects = reconnectValues(reconnects)
	out.Shards = shards
	sort.Slice(out.Shards, func(i, j int) bool {
		if out.Shards[i].EscrowID != out.Shards[j].EscrowID {
			return out.Shards[i].EscrowID < out.Shards[j].EscrowID
		}
		return out.Shards[i].ProtocolVersion < out.Shards[j].ProtocolVersion
	})
	return out
}

func mergeEndpoints(dst map[string]*rpcStatsEndpoint, src []rpcStatsEndpoint) {
	for _, e := range src {
		k := e.Endpoint + "\x00" + e.Zone
		cur := dst[k]
		if cur == nil {
			cp := e
			dst[k] = &cp
			continue
		}
		cur.Requests += e.Requests
		cur.Banned += e.Banned
	}
}

func mergeZones(dst map[string]*rpcStatsZone, src []rpcStatsZone) {
	for _, z := range src {
		cur := dst[z.Zone]
		if cur == nil {
			cp := z
			dst[z.Zone] = &cp
			continue
		}
		cur.Requests += z.Requests
		cur.Banned += z.Banned
	}
}

func mergePeers(dst map[string]*rpcStatsPeer, src []rpcStatsPeer) {
	for _, p := range src {
		cur := dst[p.Peer]
		if cur == nil {
			cp := p
			dst[p.Peer] = &cp
			continue
		}
		cur.Requests += p.Requests
		cur.Banned += p.Banned
	}
}

func mergeIPs(dst map[string]*rpcStatsIP, src []rpcStatsIP) {
	for _, p := range src {
		cur := dst[p.IP]
		if cur == nil {
			cp := p
			dst[p.IP] = &cp
			continue
		}
		cur.Requests += p.Requests
		cur.Banned += p.Banned
	}
}

func mergeReconnects(dst map[string]*rpcStatsReconnect, src []rpcStatsReconnect) {
	for _, r := range src {
		k := r.Peer + "\x00" + r.Reason
		cur := dst[k]
		if cur == nil {
			cp := r
			dst[k] = &cp
			continue
		}
		cur.Attempts += r.Attempts
	}
}

func endpointValues(m map[string]*rpcStatsEndpoint) []rpcStatsEndpoint {
	out := make([]rpcStatsEndpoint, 0, len(m))
	for _, v := range m {
		out = append(out, *v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Endpoint < out[j].Endpoint })
	return out
}

func zoneValues(m map[string]*rpcStatsZone) []rpcStatsZone {
	out := make([]rpcStatsZone, 0, len(m))
	for _, v := range m {
		out = append(out, *v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Zone < out[j].Zone })
	return out
}

func peerValues(m map[string]*rpcStatsPeer) []rpcStatsPeer {
	out := make([]rpcStatsPeer, 0, len(m))
	for _, v := range m {
		out = append(out, *v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Peer < out[j].Peer })
	return out
}

func ipValues(m map[string]*rpcStatsIP) []rpcStatsIP {
	out := make([]rpcStatsIP, 0, len(m))
	for _, v := range m {
		out = append(out, *v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].IP < out[j].IP })
	return out
}

func reconnectValues(m map[string]*rpcStatsReconnect) []rpcStatsReconnect {
	out := make([]rpcStatsReconnect, 0, len(m))
	for _, v := range m {
		out = append(out, *v)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Peer != out[j].Peer {
			return out[i].Peer < out[j].Peer
		}
		return out[i].Reason < out[j].Reason
	})
	return out
}

func acceptsGzip(h http.Header) bool {
	if h == nil {
		return false
	}
	for _, part := range strings.Split(h.Get("Accept-Encoding"), ",") {
		encoding := strings.TrimSpace(strings.Split(part, ";")[0])
		if strings.EqualFold(encoding, rpcStatsGzipEncoding) {
			return true
		}
	}
	return false
}

func gzipBestSpeed(src []byte) ([]byte, error) {
	var buf bytes.Buffer
	w, err := gzip.NewWriterLevel(&buf, gzip.BestSpeed)
	if err != nil {
		return nil, err
	}
	if _, err := w.Write(src); err != nil {
		_ = w.Close()
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
