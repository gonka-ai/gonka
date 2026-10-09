package probe_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"common/probe"

	"github.com/stretchr/testify/require"
)

func TestNew_RejectsShortInterval(t *testing.T) {
	_, err := probe.New(probe.Config{
		Interval: 100 * time.Millisecond,
		Timeout:  60 * time.Millisecond,
	})
	require.Error(t, err)
}

func TestProbeOnce_PingFourTimestamp(t *testing.T) {
	// Server processing = 20ms; one-way delay ~5ms each direction approximated by sleep before headers.
	const serverProc = 20 * time.Millisecond
	var tRecv, tSend int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tRecv = time.Now().UnixNano()
		time.Sleep(serverProc)
		tSend = time.Now().UnixNano()
		w.Header().Set(probe.HeaderServerRecvNs, strconv.FormatInt(tRecv, 10))
		w.Header().Set(probe.HeaderServerSendNs, strconv.FormatInt(tSend, 10))
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(srv.Close)

	p, err := probe.New(probe.Config{
		Interval:  time.Second,
		Timeout:   200 * time.Millisecond,
		Transport: srv.Client().Transport,
	})
	require.NoError(t, err)

	res := p.ProbeOnce(context.Background(), probe.Target{
		Key:         "h1",
		ClockURL:     srv.URL,
		FallbackURL: srv.URL + "/fallback",
	})
	require.True(t, res.Up)
	require.Equal(t, probe.KindClock, res.Kind)
	require.True(t, res.HasDivergence)
	require.Equal(t, probe.KindClock, res.DivergenceSource)
	// RTT (delay) should be roughly wall RTT minus server processing.
	require.Greater(t, res.RTT, time.Duration(0))
	require.Less(t, res.RTT, 200*time.Millisecond)
	// delay should be less than naive wall elapsed including server proc
	require.Less(t, res.RTT, serverProc+50*time.Millisecond)
}

func TestProbeOnce_KnownOffsetSymmetric(t *testing.T) {
	offset := 5 * time.Second
	base := time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC)
	var wall atomic.Value
	wall.Store(base)
	clock := func() time.Time { return wall.Load().(time.Time) }

	rt := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		h := make(http.Header)
		recv := base.Add(offset).UnixNano()
		send := base.Add(offset + 10*time.Millisecond).UnixNano()
		h.Set(probe.HeaderServerRecvNs, strconv.FormatInt(recv, 10))
		h.Set(probe.HeaderServerSendNs, strconv.FormatInt(send, 10))
		wall.Store(base.Add(10 * time.Millisecond))
		return &http.Response{
			StatusCode: http.StatusNoContent,
			Header:     h,
			Body:       http.NoBody,
			Request:    req,
		}, nil
	})

	p, err := probe.New(probe.Config{
		Interval:  time.Second,
		Timeout:   200 * time.Millisecond,
		Transport: rt,
		Clock:     clock,
	})
	require.NoError(t, err)

	res := p.ProbeOnce(context.Background(), probe.Target{
		Key: "h1", ClockURL: "http://x/", FallbackURL: "http://x/",
	})
	require.True(t, res.HasDivergence)
	require.InDelta(t, offset.Seconds(), res.Divergence.Seconds(), 0.05)
}

func TestProbeOnce_AsymmetricDelayBoundsError(t *testing.T) {
	// Classic NTP: asymmetry of A means offset error up to A/2.
	base := time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC)
	asymmetry := 40 * time.Millisecond
	var wall atomic.Value
	wall.Store(base)
	clock := func() time.Time { return wall.Load().(time.Time) }

	rt := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		h := make(http.Header)
		// True offset 0; server saw request at +5ms and replied at +10ms.
		h.Set(probe.HeaderServerRecvNs, strconv.FormatInt(base.Add(5*time.Millisecond).UnixNano(), 10))
		h.Set(probe.HeaderServerSendNs, strconv.FormatInt(base.Add(10*time.Millisecond).UnixNano(), 10))
		// Asymmetric return path: t_done = t_probe + 50ms (5+5 forward/back + 40ms asym).
		wall.Store(base.Add(50 * time.Millisecond))
		return &http.Response{
			StatusCode: http.StatusNoContent,
			Header:     h,
			Body:       http.NoBody,
			Request:    req,
		}, nil
	})

	p, err := probe.New(probe.Config{
		Interval: time.Second, Timeout: 200 * time.Millisecond,
		Transport: rt, Clock: clock,
	})
	require.NoError(t, err)
	res := p.ProbeOnce(context.Background(), probe.Target{Key: "h1", ClockURL: "http://x/", FallbackURL: "http://x/"})
	require.True(t, res.HasDivergence)
	d := res.Divergence
	if d < 0 {
		d = -d
	}
	require.LessOrEqual(t, d, asymmetry/2+time.Millisecond)
}

func TestProbeOnce_DateOnly(t *testing.T) {
	date := time.Date(2026, 8, 11, 12, 0, 5, 0, time.UTC)
	base := time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC)
	var wall atomic.Value
	wall.Store(base)
	clock := func() time.Time { return wall.Load().(time.Time) }
	rt := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		h := make(http.Header)
		h.Set("Date", date.Format(http.TimeFormat))
		wall.Store(base.Add(20 * time.Millisecond))
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     h,
			Body:       io.NopCloser(bytes.NewReader([]byte("ok"))),
			Request:    req,
		}, nil
	})

	p, err := probe.New(probe.Config{
		Interval: time.Second, Timeout: 200 * time.Millisecond,
		Transport: rt, Clock: clock,
	})
	require.NoError(t, err)
	res := p.ProbeOnce(context.Background(), probe.Target{
		Key: "h1", FallbackURL: "http://x/",
	})
	require.True(t, res.Up)
	require.Equal(t, probe.KindDate, res.Kind)
	require.True(t, res.HasDivergence)
	require.Equal(t, probe.KindDate, res.DivergenceSource)
	// midpoint estimate ≈ base+10ms; date is +5s → ~5s ±1s truncation
	require.InDelta(t, 5.0, res.Divergence.Seconds(), 1.1)
}

func TestProbeOnce_NoTimestamp(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Strip Date by writing without going through default? httptest sets Date.
		// Use a RoundTripper instead.
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	rt := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header), // no Date
			Body:       io.NopCloser(http.NoBody),
			Request:    req,
		}, nil
	})
	p, err := probe.New(probe.Config{
		Interval: time.Second, Timeout: 200 * time.Millisecond, Transport: rt,
	})
	require.NoError(t, err)
	res := p.ProbeOnce(context.Background(), probe.Target{Key: "h1", FallbackURL: "http://example/"})
	require.True(t, res.Up)
	require.False(t, res.HasDivergence, "must not emit zero divergence")
	require.Equal(t, time.Duration(0), res.Divergence)
}

func TestProbeOnce_WallClockStepsBackwards(t *testing.T) {
	base := time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC)
	var call atomic.Int32
	clock := func() time.Time {
		n := call.Add(1)
		if n == 1 {
			return base
		}
		return base.Add(-time.Hour) // wall steps backwards before t_done
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(15 * time.Millisecond)
		now := time.Now().UnixNano()
		w.Header().Set(probe.HeaderServerRecvNs, strconv.FormatInt(now-5e6, 10))
		w.Header().Set(probe.HeaderServerSendNs, strconv.FormatInt(now, 10))
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(srv.Close)

	p, err := probe.New(probe.Config{
		Interval: time.Second, Timeout: 200 * time.Millisecond,
		Transport: srv.Client().Transport, Clock: clock,
	})
	require.NoError(t, err)
	res := p.ProbeOnce(context.Background(), probe.Target{Key: "h1", ClockURL: srv.URL, FallbackURL: srv.URL})
	require.True(t, res.Up)
	require.Greater(t, res.RTT, 10*time.Millisecond, "RTT must come from monotonic clock")
}

func TestProbeOnce_MillisInNanosFieldRejected(t *testing.T) {
	ms := time.Now().UnixMilli()
	rt := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		h := make(http.Header)
		h.Set(probe.HeaderServerRecvNs, strconv.FormatInt(ms, 10))
		h.Set(probe.HeaderServerSendNs, strconv.FormatInt(ms+1, 10))
		return &http.Response{
			StatusCode: http.StatusNoContent,
			Header:     h,
			Body:       io.NopCloser(http.NoBody),
			Request:    req,
		}, nil
	})
	p, err := probe.New(probe.Config{
		Interval: time.Second, Timeout: 200 * time.Millisecond, Transport: rt,
	})
	require.NoError(t, err)
	res := p.ProbeOnce(context.Background(), probe.Target{Key: "h1", ClockURL: "http://x/", FallbackURL: "http://x/"})
	require.True(t, res.Up)
	require.False(t, res.HasDivergence)
	require.Error(t, res.Err)
	require.Contains(t, res.Err.Error(), "milliseconds")
}

func TestCapability_DemoteOn404NotOn500(t *testing.T) {
	var pingHits atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/clock", func(w http.ResponseWriter, r *http.Request) {
		pingHits.Add(1)
		w.WriteHeader(http.StatusNotFound)
	})
	mux.HandleFunc("/ok", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Date", time.Now().UTC().Format(http.TimeFormat))
		w.WriteHeader(http.StatusOK)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	p, err := probe.New(probe.Config{
		Interval: time.Second, Timeout: 200 * time.Millisecond,
		Transport: srv.Client().Transport, CapabilityTTL: time.Hour,
	})
	require.NoError(t, err)

	res := p.ProbeOnce(context.Background(), probe.Target{
		Key: "h1", ClockURL: srv.URL + "/clock", FallbackURL: srv.URL + "/ok",
	})
	require.True(t, res.Up)
	require.Equal(t, probe.KindDate, res.Kind)
	require.Equal(t, int32(1), pingHits.Load())

	// Second probe must not hit clock (demoted, TTL not expired).
	res = p.ProbeOnce(context.Background(), probe.Target{
		Key: "h1", ClockURL: srv.URL + "/clock", FallbackURL: srv.URL + "/ok",
	})
	require.True(t, res.Up)
	require.Equal(t, int32(1), pingHits.Load())
}

func TestCapability_DemoteOn405(t *testing.T) {
	var pingHits atomic.Int32
	rt := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Path == "/clock" || req.URL.String() == "http://x/clock" {
			pingHits.Add(1)
			return &http.Response{
				StatusCode: http.StatusMethodNotAllowed,
				Header:     make(http.Header),
				Body:       http.NoBody,
				Request:    req,
			}, nil
		}
		h := make(http.Header)
		h.Set("Date", time.Now().UTC().Format(http.TimeFormat))
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     h,
			Body:       http.NoBody,
			Request:    req,
		}, nil
	})
	p, err := probe.New(probe.Config{
		Interval: time.Second, Timeout: 200 * time.Millisecond, Transport: rt, CapabilityTTL: time.Hour,
	})
	require.NoError(t, err)
	res := p.ProbeOnce(context.Background(), probe.Target{Key: "h1", ClockURL: "http://x/clock", FallbackURL: "http://x/ok"})
	require.True(t, res.Up)
	require.Equal(t, probe.KindDate, res.Kind)
	_ = p.ProbeOnce(context.Background(), probe.Target{Key: "h1", ClockURL: "http://x/clock", FallbackURL: "http://x/ok"})
	require.Equal(t, int32(1), pingHits.Load())
}

func TestCapability_500DoesNotDemote(t *testing.T) {
	var pingHits atomic.Int32
	rt := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		pingHits.Add(1)
		return &http.Response{
			StatusCode: http.StatusInternalServerError,
			Header:     make(http.Header),
			Body:       io.NopCloser(http.NoBody),
			Request:    req,
		}, nil
	})
	p, err := probe.New(probe.Config{
		Interval: time.Second, Timeout: 200 * time.Millisecond, Transport: rt, CapabilityTTL: time.Hour,
	})
	require.NoError(t, err)
	res := p.ProbeOnce(context.Background(), probe.Target{Key: "h1", ClockURL: "http://x/clock", FallbackURL: "http://x/ok"})
	require.False(t, res.Up)
	require.Equal(t, probe.KindClock, res.Kind)

	_ = p.ProbeOnce(context.Background(), probe.Target{Key: "h1", ClockURL: "http://x/clock", FallbackURL: "http://x/ok"})
	require.Equal(t, int32(2), pingHits.Load(), "must keep trying ping after 500")
}

func TestCapability_TimeoutDoesNotDemote(t *testing.T) {
	rt := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		<-req.Context().Done()
		return nil, req.Context().Err()
	})
	p, err := probe.New(probe.Config{
		Interval: 200 * time.Millisecond, Timeout: 20 * time.Millisecond, Transport: rt,
	})
	require.NoError(t, err)
	res := p.ProbeOnce(context.Background(), probe.Target{Key: "h1", ClockURL: "http://x/clock", FallbackURL: "http://x/ok"})
	require.False(t, res.Up)
	require.Equal(t, probe.KindClock, res.Kind)
}

func TestCapability_TTLAllowsOneRediscovery(t *testing.T) {
	var pingHits atomic.Int32
	var now atomic.Int64
	base := time.Now()
	now.Store(base.UnixNano())
	clock := func() time.Time { return time.Unix(0, now.Load()) }

	mux := http.NewServeMux()
	mux.HandleFunc("/clock", func(w http.ResponseWriter, r *http.Request) {
		pingHits.Add(1)
		w.WriteHeader(http.StatusNotFound)
	})
	mux.HandleFunc("/ok", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	ttl := time.Minute
	p, err := probe.New(probe.Config{
		Interval: time.Second, Timeout: 200 * time.Millisecond,
		Transport: srv.Client().Transport, CapabilityTTL: ttl, Clock: clock,
	})
	require.NoError(t, err)
	tgt := probe.Target{Key: "h1", ClockURL: srv.URL + "/clock", FallbackURL: srv.URL + "/ok"}

	_ = p.ProbeOnce(context.Background(), tgt) // demote
	require.Equal(t, int32(1), pingHits.Load())
	for i := 0; i < 5; i++ {
		_ = p.ProbeOnce(context.Background(), tgt)
	}
	require.Equal(t, int32(1), pingHits.Load(), "no ping during TTL")

	now.Store(base.Add(ttl + time.Second).UnixNano())
	_ = p.ProbeOnce(context.Background(), tgt)
	require.Equal(t, int32(2), pingHits.Load(), "exactly one rediscovery after TTL")
}

func TestInvalidate_ReenablesPing(t *testing.T) {
	var pingHits atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/clock", func(w http.ResponseWriter, r *http.Request) {
		pingHits.Add(1)
		if pingHits.Load() == 1 {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		n := time.Now().UnixNano()
		w.Header().Set(probe.HeaderServerRecvNs, strconv.FormatInt(n, 10))
		w.Header().Set(probe.HeaderServerSendNs, strconv.FormatInt(n, 10))
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("/ok", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	p, err := probe.New(probe.Config{
		Interval: time.Second, Timeout: 200 * time.Millisecond,
		Transport: srv.Client().Transport, CapabilityTTL: time.Hour,
	})
	require.NoError(t, err)
	tgt := probe.Target{Key: "h1", ClockURL: srv.URL + "/clock", FallbackURL: srv.URL + "/ok"}
	_ = p.ProbeOnce(context.Background(), tgt)
	require.Equal(t, int32(1), pingHits.Load())
	p.Invalidate("h1")
	res := p.ProbeOnce(context.Background(), tgt)
	require.Equal(t, int32(2), pingHits.Load())
	require.Equal(t, probe.KindClock, res.Kind)
	require.True(t, res.Up)
}

func TestProbeOnce_ColdThenWarm(t *testing.T) {
	srv := httptest.NewServer(probe.Handler(nil))
	t.Cleanup(srv.Close)

	tr := &http.Transport{DisableKeepAlives: false, MaxIdleConnsPerHost: 4}
	p, err := probe.New(probe.Config{
		Interval: time.Second, Timeout: 200 * time.Millisecond, Transport: tr,
	})
	require.NoError(t, err)
	tgt := probe.Target{Key: "h1", ClockURL: srv.URL, FallbackURL: srv.URL}
	r1 := p.ProbeOnce(context.Background(), tgt)
	r2 := p.ProbeOnce(context.Background(), tgt)
	require.True(t, r1.Up)
	require.True(t, r2.Up)
	require.False(t, r1.ConnReused)
	require.True(t, r2.ConnReused)
}

func TestJSONPingFallback(t *testing.T) {
	n := time.Now().UnixNano()
	body, _ := json.Marshal(map[string]int64{"recv_unix_ns": n, "send_unix_ns": n + 1000})
	rt := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(bytesReader(body)),
			Request:    req,
		}, nil
	})
	p, err := probe.New(probe.Config{
		Interval: time.Second, Timeout: 200 * time.Millisecond, Transport: rt,
	})
	require.NoError(t, err)
	res := p.ProbeOnce(context.Background(), probe.Target{Key: "h1", ClockURL: "http://x/", FallbackURL: "http://x/"})
	require.True(t, res.Up)
	require.True(t, res.HasDivergence)
	require.Equal(t, probe.KindClock, res.Kind)
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func bytesReader(b []byte) io.ReadCloser {
	return io.NopCloser(bytes.NewReader(b))
}
