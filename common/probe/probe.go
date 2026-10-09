// Package probe is a registry-free HTTP probe primitive for RTT, reachability,
// and clock-divergence estimates. Callers own Prometheus instruments.
package probe

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptrace"
	"time"
)

// Wire headers for the normative clock contract (unix nanoseconds).
const (
	HeaderServerRecvNs = "X-Server-Recv-Ns"
	HeaderServerSendNs = "X-Server-Send-Ns"
)

// Kind classifies how a probe was performed / where divergence came from.
type Kind uint8

const (
	KindNone Kind = iota
	KindClock
	KindDate
	KindHealth
)

func (k Kind) String() string {
	switch k {
	case KindClock:
		return "clock"
	case KindDate:
		return "date"
	case KindHealth:
		return "health"
	default:
		return "none"
	}
}

// Target is one probe destination.
type Target struct {
	Key         string // metric label value: dial host (gateway) or node_id (dapi)
	ClockURL    string // "" disables clock discovery for this target
	FallbackURL string // cheap liveness; required
}

// Result is one observation. HasDivergence false means emit no divergence sample.
type Result struct {
	Key        string
	Up         bool
	Kind       Kind
	RTT        time.Duration // delay (server processing subtracted when KindClock)
	ConnReused bool          // false => cold dial; exclude from warm RTT series
	At         time.Time     // prober wall clock for freshness gauges

	Divergence       time.Duration
	HasDivergence    bool
	DivergenceSource Kind // KindClock or KindDate

	Err error
}

// Sink is the caller's metric adapter.
type Sink interface {
	Observe(Result)
	Forget(key string)
}

// TargetSource is snapshotted once per scheduler tick.
type TargetSource interface {
	Targets() []Target
}

// Config tunes a Prober. Interval must be >= 2*Timeout.
type Config struct {
	Interval      time.Duration
	Timeout       time.Duration
	Concurrency   int
	Jitter        time.Duration
	CapabilityTTL time.Duration
	Transport     http.RoundTripper
	Clock         func() time.Time // injectable wall clock for tests
}

// Prober executes probes. It holds no Prometheus state.
type Prober struct {
	client   *http.Client
	timeout  time.Duration
	capTTL   time.Duration
	clock    func() time.Time
	caps     *capabilityCache
	interval time.Duration
	conc     int
	jitter   time.Duration
}

// New validates cfg and returns a Prober.
func New(cfg Config) (*Prober, error) {
	if cfg.Interval <= 0 {
		return nil, errors.New("probe: Interval must be > 0")
	}
	if cfg.Timeout <= 0 {
		return nil, errors.New("probe: Timeout must be > 0")
	}
	if cfg.Interval < 2*cfg.Timeout {
		return nil, fmt.Errorf("probe: Interval (%s) must be >= 2*Timeout (%s)", cfg.Interval, cfg.Timeout)
	}
	tr := cfg.Transport
	if tr == nil {
		tr = http.DefaultTransport
	}
	clock := cfg.Clock
	if clock == nil {
		clock = time.Now
	}
	conc := cfg.Concurrency
	if conc <= 0 {
		conc = 8
	}
	ttl := cfg.CapabilityTTL
	if ttl <= 0 {
		ttl = 10 * time.Minute
	}
	return &Prober{
		client:   &http.Client{Transport: tr, Timeout: 0}, // per-request context enforces timeout
		timeout:  cfg.Timeout,
		capTTL:   ttl,
		clock:    clock,
		caps:     newCapabilityCache(),
		interval: cfg.Interval,
		conc:     conc,
		jitter:   cfg.Jitter,
	}, nil
}

// Invalidate clears sticky capability for key so the next probe rediscovers clock.
func (p *Prober) Invalidate(key string) {
	p.caps.invalidate(key)
}

// ProbeOnce probes t once according to the capability cache.
func (p *Prober) ProbeOnce(ctx context.Context, t Target) Result {
	res := Result{Key: t.Key}
	if t.Key == "" {
		res.Err = errors.New("probe: empty target key")
		res.At = p.clock()
		return res
	}
	if t.FallbackURL == "" && t.ClockURL == "" {
		res.Err = errors.New("probe: FallbackURL or ClockURL required")
		res.At = p.clock()
		return res
	}

	useClock := t.ClockURL != "" && p.caps.shouldTryClock(t.Key, p.capTTL, p.clock())
	if useClock {
		wasDemoted := p.caps.isDemoted(t.Key)
		r := p.doHTTP(ctx, t.Key, t.ClockURL, true)
		switch {
		case r.up && (r.status == http.StatusOK || r.status == http.StatusNoContent):
			p.caps.markClock(t.Key)
			return p.finishPing(t.Key, r)
		case r.status == http.StatusNotFound || r.status == http.StatusMethodNotAllowed:
			p.caps.demote(t.Key, p.clock())
			// fall through to fallback
		default:
			// Timeout / refused / 5xx: do not demote. If this was a TTL
			// rediscovery while demoted, refresh demotedAt so we do not flap.
			if wasDemoted {
				p.caps.demote(t.Key, p.clock())
			}
			res.Up = false
			res.Kind = KindClock
			res.RTT = r.rtt
			res.ConnReused = r.connReused
			res.At = r.tDone
			res.Err = r.err
			if res.Err == nil && r.status != 0 {
				res.Err = fmt.Errorf("probe: unexpected status %d", r.status)
			}
			return res
		}
	}

	if t.FallbackURL == "" {
		res.Up = false
		res.Kind = KindClock
		res.Err = errors.New("probe: clock unavailable and no FallbackURL")
		return res
	}
	r := p.doHTTP(ctx, t.Key, t.FallbackURL, false)
	return p.finishFallback(t.Key, r)
}

type rawResult struct {
	up         bool
	status     int
	rtt        time.Duration
	connReused bool
	tProbe     time.Time
	tDone      time.Time
	recvNS     int64
	sendNS     int64
	hasRecv    bool
	hasSend    bool
	date       time.Time
	hasDate    bool
	parseErr   error
	err        error
	body       []byte
	header     http.Header
}

func (p *Prober) doHTTP(ctx context.Context, key, url string, wantPingBody bool) rawResult {
	_ = key
	out := rawResult{}
	monoStart := time.Now()
	out.tProbe = p.clock()

	reqCtx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, url, nil)
	if err != nil {
		out.err = err
		out.tDone = p.clock()
		out.rtt = time.Since(monoStart)
		return out
	}

	var reused bool
	trace := &httptrace.ClientTrace{
		GotConn: func(info httptrace.GotConnInfo) {
			reused = info.Reused
		},
	}
	req = req.WithContext(httptrace.WithClientTrace(req.Context(), trace))

	resp, err := p.client.Do(req)
	out.rtt = time.Since(monoStart)
	out.tDone = p.clock()
	out.connReused = reused
	if err != nil {
		out.err = err
		return out
	}
	defer resp.Body.Close()

	out.status = resp.StatusCode
	out.header = resp.Header.Clone()
	// Cap body; ping/fallback are tiny.
	const maxBody = 4096
	body, readErr := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if readErr != nil && !errors.Is(readErr, io.EOF) {
		out.err = readErr
		return out
	}
	out.body = body

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		out.up = true
	} else {
		out.err = fmt.Errorf("probe: status %d", resp.StatusCode)
	}

	recv, send, hasRecv, hasSend, perr := parsePingTimestamps(resp.Header, body)
	out.recvNS, out.sendNS = recv, send
	out.hasRecv, out.hasSend = hasRecv, hasSend
	if perr != nil {
		out.parseErr = perr
	}
	if d, ok := parseDateHeader(resp.Header); ok {
		out.date = d
		out.hasDate = true
	}
	_ = wantPingBody
	return out
}

func (p *Prober) finishPing(key string, r rawResult) Result {
	res := Result{
		Key:        key,
		Up:         r.up,
		Kind:       KindClock,
		ConnReused: r.connReused,
		At:         r.tDone,
		Err:        firstErr(r.parseErr, r.err),
	}
	if !r.up {
		res.RTT = r.rtt
		return res
	}
	if r.hasRecv && r.hasSend {
		delay, offset := ntpDelayOffset(r.tProbe, r.recvNS, r.sendNS, r.tDone, r.rtt)
		res.RTT = delay
		res.Divergence = offset
		res.HasDivergence = true
		res.DivergenceSource = KindClock
		return res
	}
	// Ping responded but timestamps unusable — still up with wall RTT.
	res.RTT = r.rtt
	if r.parseErr != nil {
		res.Err = r.parseErr
	}
	if r.hasDate {
		res.Divergence, res.HasDivergence = dateDivergence(r.tProbe, r.rtt, r.date)
		if res.HasDivergence {
			res.DivergenceSource = KindDate
			// Kind stays KindClock (how we probed); source label carries date.
		}
	}
	return res
}

func (p *Prober) finishFallback(key string, r rawResult) Result {
	res := Result{
		Key:        key,
		Up:         r.up,
		Kind:       KindHealth,
		RTT:        r.rtt,
		ConnReused: r.connReused,
		At:         r.tDone,
		Err:        r.err,
	}
	if !r.up {
		return res
	}
	if r.hasDate {
		res.Kind = KindDate
		res.Divergence, res.HasDivergence = dateDivergence(r.tProbe, r.rtt, r.date)
		if res.HasDivergence {
			res.DivergenceSource = KindDate
		}
	}
	return res
}

func firstErr(errs ...error) error {
	for _, e := range errs {
		if e != nil {
			return e
		}
	}
	return nil
}

// ntpDelayOffset implements the four-timestamp NTP form.
// tRecvNS/tSendNS are unix nanoseconds on the server.
// monoRTT is the prober's monotonic elapsed (time.Since); delay must not use
// wall-clock T4-T1, or an NTP step zeroes / poisons RTT.
func ntpDelayOffset(tProbe time.Time, tRecvNS, tSendNS int64, tDone time.Time, monoRTT time.Duration) (delay, offset time.Duration) {
	t1 := tProbe.UnixNano()
	t4 := tDone.UnixNano()
	// delay = mono(T4 - T1) - (T3 - T2)
	delayNS := monoRTT.Nanoseconds() - (tSendNS - tRecvNS)
	if delayNS < 0 {
		delayNS = 0
	}
	// offset = ((T2 - T1) + (T3 - T4)) / 2  (wall clocks)
	offsetNS := ((tRecvNS - t1) + (tSendNS - t4)) / 2
	return time.Duration(delayNS), time.Duration(offsetNS)
}

func dateDivergence(tProbe time.Time, rtt time.Duration, date time.Time) (time.Duration, bool) {
	tEst := tProbe.Add(rtt / 2)
	return date.Sub(tEst), true
}
