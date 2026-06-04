package transport

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"sort"
	"strings"
	"sync"
	"time"
)

const defaultClosedConnectionHold = 60 * time.Second

type connLifecycleState uint8

const (
	connStateActive connLifecycleState = iota + 1
	connStateIdle
	connStateClosed
)

type HostConnectionSnapshot struct {
	Address        string
	Active         int64
	Idle           int64
	HoldAfterClose int64
	OpenTotal      int64
}

type hostConnectionCounts struct {
	active         int64
	idle           int64
	holdAfterClose int64
	openTotal      int64
}

type trackedConnState struct {
	address string
	state   connLifecycleState
}

type HostConnectionTracker struct {
	mu           sync.Mutex
	holdDuration time.Duration
	counts       map[string]*hostConnectionCounts
	conns        map[*trackedConn]*trackedConnState
}

type instrumentedRoundTripper struct {
	base    http.RoundTripper
	tracker *HostConnectionTracker
}

type trackedBody struct {
	io.ReadCloser
	conn    net.Conn
	tracker *HostConnectionTracker
	once    sync.Once
}

type trackedConn struct {
	net.Conn
	tracker *HostConnectionTracker
	address string
	once    sync.Once
}

var defaultHostConnectionTracker = NewHostConnectionTracker(defaultClosedConnectionHold)

func NewHostConnectionTracker(holdDuration time.Duration) *HostConnectionTracker {
	return &HostConnectionTracker{
		holdDuration: holdDuration,
		counts:       make(map[string]*hostConnectionCounts),
		conns:        make(map[*trackedConn]*trackedConnState),
	}
}

func DefaultHostConnectionTracker() *HostConnectionTracker {
	return defaultHostConnectionTracker
}

func (t *HostConnectionTracker) WrapRoundTripper(base http.RoundTripper) http.RoundTripper {
	if t == nil || base == nil {
		return base
	}
	return &instrumentedRoundTripper{
		base:    base,
		tracker: t,
	}
}

func (t *HostConnectionTracker) TrackDialContext(
	base func(context.Context, string, string) (net.Conn, error),
	fallbackAddress string,
) func(context.Context, string, string) (net.Conn, error) {
	if t == nil || base == nil {
		return base
	}
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		conn, err := base(ctx, network, addr)
		if err != nil {
			return nil, err
		}
		return t.trackConn(conn, fallbackAddress), nil
	}
}

func (t *HostConnectionTracker) Snapshots() []HostConnectionSnapshot {
	if t == nil {
		return nil
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	addresses := make([]string, 0, len(t.counts))
	for address, counts := range t.counts {
		if counts.active == 0 && counts.idle == 0 && counts.holdAfterClose == 0 && counts.openTotal == 0 {
			continue
		}
		addresses = append(addresses, address)
	}
	sort.Strings(addresses)

	snapshots := make([]HostConnectionSnapshot, 0, len(addresses))
	for _, address := range addresses {
		counts := t.counts[address]
		snapshots = append(snapshots, HostConnectionSnapshot{
			Address:        address,
			Active:         counts.active,
			Idle:           counts.idle,
			HoldAfterClose: counts.holdAfterClose,
			OpenTotal:      counts.openTotal,
		})
	}
	return snapshots
}

func (rt *instrumentedRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	if rt == nil || rt.base == nil {
		return nil, http.ErrUseLastResponse
	}

	var conn net.Conn
	trace := &httptrace.ClientTrace{
		GotConn: func(info httptrace.GotConnInfo) {
			conn = info.Conn
			if rt.tracker != nil {
				rt.tracker.markActive(info.Conn)
			}
		},
	}

	req = req.Clone(httptrace.WithClientTrace(req.Context(), trace))
	resp, err := rt.base.RoundTrip(req)
	if err != nil {
		return nil, err
	}
	if conn != nil && resp.Body != nil && rt.tracker != nil {
		resp.Body = &trackedBody{
			ReadCloser: resp.Body,
			conn:       conn,
			tracker:    rt.tracker,
		}
	}
	return resp, nil
}

func (b *trackedBody) Close() error {
	err := b.ReadCloser.Close()
	b.once.Do(func() {
		if b.tracker != nil {
			b.tracker.markIdle(b.conn)
		}
	})
	return err
}

func (c *trackedConn) Close() error {
	err := c.Conn.Close()
	c.once.Do(func() {
		if c.tracker != nil {
			c.tracker.markClosed(c)
		}
	})
	return err
}

func (t *HostConnectionTracker) trackConn(conn net.Conn, fallbackAddress string) net.Conn {
	if t == nil || conn == nil {
		return conn
	}

	address := normalizeConnectionAddress(conn.RemoteAddr(), fallbackAddress)
	tracked := &trackedConn{
		Conn:    conn,
		tracker: t,
		address: address,
	}

	t.mu.Lock()
	counts := t.ensureCountsLocked(address)
	counts.active++
	counts.openTotal++
	t.conns[tracked] = &trackedConnState{
		address: address,
		state:   connStateActive,
	}
	t.mu.Unlock()

	return tracked
}

func (t *HostConnectionTracker) markActive(conn net.Conn) {
	tracked, ok := conn.(*trackedConn)
	if !ok || t == nil {
		return
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	state, ok := t.conns[tracked]
	if !ok {
		return
	}
	counts := t.ensureCountsLocked(state.address)
	switch state.state {
	case connStateIdle:
		counts.idle--
		counts.active++
		state.state = connStateActive
	case connStateActive, connStateClosed:
	}
}

func (t *HostConnectionTracker) markIdle(conn net.Conn) {
	tracked, ok := conn.(*trackedConn)
	if !ok || t == nil {
		return
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	state, ok := t.conns[tracked]
	if !ok {
		return
	}
	counts := t.ensureCountsLocked(state.address)
	switch state.state {
	case connStateActive:
		counts.active--
		counts.idle++
		state.state = connStateIdle
	case connStateIdle, connStateClosed:
	}
}

func (t *HostConnectionTracker) markClosed(conn *trackedConn) {
	if t == nil || conn == nil {
		return
	}

	t.mu.Lock()
	state, ok := t.conns[conn]
	if !ok {
		t.mu.Unlock()
		return
	}
	counts := t.ensureCountsLocked(state.address)
	switch state.state {
	case connStateActive:
		counts.active--
		counts.openTotal--
	case connStateIdle:
		counts.idle--
		counts.openTotal--
	case connStateClosed:
		t.mu.Unlock()
		return
	}
	counts.holdAfterClose++
	state.state = connStateClosed
	delete(t.conns, conn)
	t.mu.Unlock()

	if t.holdDuration <= 0 {
		t.releaseHold(state.address)
		return
	}
	time.AfterFunc(t.holdDuration, func() {
		t.releaseHold(state.address)
	})
}

func (t *HostConnectionTracker) releaseHold(address string) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()

	counts, ok := t.counts[address]
	if !ok {
		return
	}
	if counts.holdAfterClose > 0 {
		counts.holdAfterClose--
	}
	if counts.active == 0 && counts.idle == 0 && counts.holdAfterClose == 0 && counts.openTotal == 0 {
		delete(t.counts, address)
	}
}

func (t *HostConnectionTracker) ensureCountsLocked(address string) *hostConnectionCounts {
	if address == "" {
		address = "unknown"
	}
	counts, ok := t.counts[address]
	if !ok {
		counts = &hostConnectionCounts{}
		t.counts[address] = counts
	}
	return counts
}

func normalizeConnectionAddress(addr net.Addr, fallback string) string {
	if addr != nil {
		if host, _, err := net.SplitHostPort(strings.TrimSpace(addr.String())); err == nil && host != "" {
			return host
		}
		if raw := strings.TrimSpace(addr.String()); raw != "" {
			return raw
		}
	}
	return normalizeFallbackAddress(fallback)
}

func normalizeFallbackAddress(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "unknown"
	}
	if host, _, err := net.SplitHostPort(raw); err == nil && host != "" {
		return host
	}
	return raw
}
