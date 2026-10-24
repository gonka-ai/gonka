package probe_test

import (
	"context"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"common/probe"

	"github.com/stretchr/testify/require"
)

func TestIncompleteJSONClockDoesNotEmitDivergence(t *testing.T) {
	fixed := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	p, err := probe.New(probe.Config{
		Interval:  time.Second,
		Timeout:   200 * time.Millisecond,
		Clock:     func() time.Time { return fixed },
		Transport: incompleteJSONClockTransport{body: `{"send_unix_ns":1791547200000000000}`},
	})
	require.NoError(t, err)

	r := p.ProbeOnce(context.Background(), probe.Target{
		Key:      "fixture",
		ClockURL: "http://fixture.invalid/clock",
	})
	require.True(t, r.Up)
	require.False(t, r.HasDivergence, "incomplete JSON became a clock sample: %+v", r)
	require.Error(t, r.Err)
	require.Contains(t, r.Err.Error(), "incomplete ping timestamp json")
}

func TestIncompleteJSONClockMissingSendDoesNotEmitDivergence(t *testing.T) {
	fixed := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	p, err := probe.New(probe.Config{
		Interval:  time.Second,
		Timeout:   200 * time.Millisecond,
		Clock:     func() time.Time { return fixed },
		Transport: incompleteJSONClockTransport{body: `{"recv_unix_ns":1791547200000000000}`},
	})
	require.NoError(t, err)

	r := p.ProbeOnce(context.Background(), probe.Target{
		Key:      "fixture",
		ClockURL: "http://fixture.invalid/clock",
	})
	require.True(t, r.Up)
	require.False(t, r.HasDivergence, "incomplete JSON became a clock sample: %+v", r)
	require.Error(t, r.Err)
	require.Contains(t, r.Err.Error(), "incomplete ping timestamp json")
}

func TestIncompleteJSONClockNullRecvDoesNotEmitDivergence(t *testing.T) {
	fixed := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	p, err := probe.New(probe.Config{
		Interval: time.Second,
		Timeout:  200 * time.Millisecond,
		Clock:    func() time.Time { return fixed },
		Transport: incompleteJSONClockTransport{
			body: `{"send_unix_ns":1791547200000000000,"recv_unix_ns":null}`,
		},
	})
	require.NoError(t, err)

	r := p.ProbeOnce(context.Background(), probe.Target{
		Key:      "fixture",
		ClockURL: "http://fixture.invalid/clock",
	})
	require.True(t, r.Up)
	require.False(t, r.HasDivergence, "null recv_unix_ns became a clock sample: %+v", r)
	require.Error(t, r.Err)
	require.Contains(t, r.Err.Error(), "incomplete ping timestamp json")
}

func TestIncompleteJSONClockFallsBackToDate(t *testing.T) {
	fixed := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	p, err := probe.New(probe.Config{
		Interval: time.Second,
		Timeout:  200 * time.Millisecond,
		Clock:    func() time.Time { return fixed },
		Transport: incompleteJSONClockTransport{
			body: `{"send_unix_ns":1791547200000000000}`,
			date: "Fri, 09 Oct 2026 12:00:00 GMT",
		},
	})
	require.NoError(t, err)

	r := p.ProbeOnce(context.Background(), probe.Target{
		Key:      "fixture",
		ClockURL: "http://fixture.invalid/clock",
	})
	require.True(t, r.Up)
	require.True(t, r.HasDivergence)
	require.Equal(t, probe.KindDate, r.DivergenceSource)
	require.NotEqual(t, probe.KindClock, r.DivergenceSource)
}

func TestExplicitZeroJSONPairDoesNotEmitDivergence(t *testing.T) {
	p, err := probe.New(probe.Config{
		Interval: time.Second,
		Timeout:  200 * time.Millisecond,
		Transport: incompleteJSONClockTransport{
			body: `{"recv_unix_ns":0,"send_unix_ns":0}`,
		},
	})
	require.NoError(t, err)

	r := p.ProbeOnce(context.Background(), probe.Target{
		Key:      "fixture",
		ClockURL: "http://fixture.invalid/clock",
	})
	require.True(t, r.Up)
	require.False(t, r.HasDivergence)
	require.NoError(t, r.Err)
}

func TestCompleteJSONClockEmitsClockDivergence(t *testing.T) {
	fixed := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	n := fixed.UnixNano()
	body := `{"recv_unix_ns":` + strconv.FormatInt(n, 10) + `,"send_unix_ns":` + strconv.FormatInt(n+1000, 10) + `}`
	p, err := probe.New(probe.Config{
		Interval:  time.Second,
		Timeout:   200 * time.Millisecond,
		Clock:     func() time.Time { return fixed },
		Transport: incompleteJSONClockTransport{body: body},
	})
	require.NoError(t, err)

	r := p.ProbeOnce(context.Background(), probe.Target{
		Key:      "fixture",
		ClockURL: "http://fixture.invalid/clock",
	})
	require.True(t, r.Up)
	require.True(t, r.HasDivergence)
	require.Equal(t, probe.KindClock, r.DivergenceSource)
	require.NoError(t, r.Err)
}

type incompleteJSONClockTransport struct {
	body string
	date string
}

func (t incompleteJSONClockTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	h := http.Header{"Content-Type": {"application/json"}}
	if t.date != "" {
		h.Set("Date", t.date)
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     h,
		Body:       io.NopCloser(strings.NewReader(t.body)),
		Request:    req,
	}, nil
}
