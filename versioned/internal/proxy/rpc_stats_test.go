package proxy

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestMergeRPCStats_SumsAndConcatShards(t *testing.T) {
	a := rpcStatsSnapshot{
		HostAddress:     "gonka1host",
		ProtocolVersion: "v5",
		BinaryVersion:   "b1",
		MinuteUnix:      1710000000,
		Host: rpcStatsHost{
			Requests: 10, Banned: 2,
			Endpoints: []rpcStatsEndpoint{{Endpoint: "GetDiffs", Zone: "shared", Requests: 10, Banned: 2}},
			Zones:     []rpcStatsZone{{Zone: "shared", Requests: 10, Banned: 2}},
			Attach:    rpcStatsAttach{Attempts: 4, Banned: 1, BannedFloor: 1},
		},
		Shards: []rpcStatsShard{{EscrowID: "42", ProtocolVersion: "v5", Requests: 10, Banned: 2}},
	}
	b := rpcStatsSnapshot{
		HostAddress:     "gonka1host",
		ProtocolVersion: "v6",
		BinaryVersion:   "b2",
		MinuteUnix:      1710000000,
		Host: rpcStatsHost{
			Requests: 5, Banned: 1,
			Endpoints: []rpcStatsEndpoint{{Endpoint: "GetDiffs", Zone: "shared", Requests: 5, Banned: 1}},
			Zones:     []rpcStatsZone{{Zone: "shared", Requests: 5, Banned: 1}},
			Attach:    rpcStatsAttach{Attempts: 2, Banned: 0, BannedFloor: 0},
		},
		Shards: []rpcStatsShard{{EscrowID: "99", ProtocolVersion: "v6", Requests: 5, Banned: 1}},
	}
	got := mergeRPCStats([]rpcStatsSnapshot{a, b})
	if got.Host.Requests != 15 || got.Host.Banned != 3 {
		t.Fatalf("host totals = %+v", got.Host)
	}
	if got.Host.Attach.Attempts != 6 || got.Host.Attach.BannedFloor != 1 {
		t.Fatalf("attach = %+v", got.Host.Attach)
	}
	if got.Host.Endpoints[0].Requests != 15 {
		t.Fatalf("endpoints = %+v", got.Host.Endpoints)
	}
	if len(got.Shards) != 2 || got.Shards[0].EscrowID != "42" || got.Shards[1].EscrowID != "99" {
		t.Fatalf("shards = %+v", got.Shards)
	}
	if got.Partial {
		t.Fatal("same minute should not be partial")
	}
}

func TestMergeRPCStats_ChildDownPartial(t *testing.T) {
	// Exercised via HTTP: one backend 500, one OK.
	okBody, _ := json.Marshal(rpcStatsSnapshot{
		HostAddress: "gonka1host",
		MinuteUnix:  1710000000,
		Host:        rpcStatsHost{Requests: 3, Zones: []rpcStatsZone{}},
		Shards:      []rpcStatsShard{{EscrowID: "1", Requests: 3}},
	})
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/stats/rpc" {
			t.Errorf("path = %s", r.URL.Path)
		}
		if r.Header.Get("Accept-Encoding") != "gzip" {
			t.Errorf("Accept-Encoding = %q", r.Header.Get("Accept-Encoding"))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(okBody)
	}))
	defer ok.Close()
	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "down", http.StatusInternalServerError)
	}))
	defer down.Close()

	routes := newRoutes(map[string]string{
		"v1": strings.TrimPrefix(down.URL, "http://"),
		"v2": strings.TrimPrefix(ok.URL, "http://"),
	})
	srv := httptest.NewServer(Handler(routes))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/stats/rpc")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d body=%s", resp.StatusCode, body)
	}
	var snap rpcStatsSnapshot
	if err := json.Unmarshal(body, &snap); err != nil {
		t.Fatal(err)
	}
	if !snap.Partial {
		t.Fatalf("want partial, got %+v", snap)
	}
	if snap.Host.Requests != 3 {
		t.Fatalf("requests = %d", snap.Host.Requests)
	}
}

func TestProxy_RPCStats_GzipRoundTrip(t *testing.T) {
	payload := rpcStatsSnapshot{
		HostAddress: "gonka1host",
		MinuteUnix:  1710000000,
		Host: rpcStatsHost{
			Requests: 1,
			Peers:    make([]rpcStatsPeer, 40),
		},
		Shards: []rpcStatsShard{{EscrowID: "42", Requests: 1}},
	}
	for i := range payload.Host.Peers {
		payload.Host.Peers[i] = rpcStatsPeer{Peer: fmt.Sprintf("gonka1peer-%02d-padding-for-gzip-floor", i), Requests: 1}
	}
	raw, _ := json.Marshal(payload)
	if len(raw) < rpcStatsGzipFloor {
		t.Fatalf("fixture too small: %d", len(raw))
	}
	gz, err := gzipBestSpeed(raw)
	if err != nil {
		t.Fatal(err)
	}
	child := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Encoding", "gzip")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(gz)
	}))
	defer child.Close()

	routes := newRoutes(map[string]string{"v5": strings.TrimPrefix(child.URL, "http://")})
	srv := httptest.NewServer(Handler(routes))
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/devshard/stats/rpc", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.Header.Get("Content-Encoding") != "gzip" {
		t.Fatalf("encoding = %q", resp.Header.Get("Content-Encoding"))
	}
	gr, err := gzip.NewReader(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	out, err := io.ReadAll(gr)
	if err != nil {
		t.Fatal(err)
	}
	var snap rpcStatsSnapshot
	if err := json.Unmarshal(out, &snap); err != nil {
		t.Fatal(err)
	}
	if snap.Host.Requests != 1 || snap.HostAddress != "gonka1host" {
		t.Fatalf("snap = %+v", snap)
	}
}

func TestProxy_RPCStats_TwoChildrenMerged(t *testing.T) {
	mk := func(escrow string, n uint64) *httptest.Server {
		body, _ := json.Marshal(rpcStatsSnapshot{
			HostAddress:     "gonka1host",
			ProtocolVersion: "v",
			MinuteUnix:      1710000000,
			Host:            rpcStatsHost{Requests: n, Banned: 1, Attach: rpcStatsAttach{Attempts: n}},
			Shards:          []rpcStatsShard{{EscrowID: escrow, Requests: n, Banned: 1}},
		})
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write(body)
		}))
	}
	a := mk("42", 4)
	defer a.Close()
	b := mk("99", 6)
	defer b.Close()
	routes := newRoutes(map[string]string{
		"v5":   strings.TrimPrefix(a.URL, "http://"),
		"v5r2": strings.TrimPrefix(b.URL, "http://"),
	})
	srv := httptest.NewServer(Handler(routes))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/stats/rpc")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var snap rpcStatsSnapshot
	if err := json.Unmarshal(raw, &snap); err != nil {
		t.Fatal(err)
	}
	if snap.Host.Requests != 10 || snap.Host.Banned != 2 {
		t.Fatalf("host = %+v body=%s", snap.Host, raw)
	}
	if snap.Host.Attach.Attempts != 10 {
		t.Fatalf("attach = %+v", snap.Host.Attach)
	}
	if len(snap.Shards) != 2 {
		t.Fatalf("shards = %+v", snap.Shards)
	}
}

func TestProxy_RPCStats_MergeCachedSingleFlight(t *testing.T) {
	rpcStatsCache.reset()
	t.Cleanup(rpcStatsCache.reset)

	var hits atomic.Int32
	body, _ := json.Marshal(rpcStatsSnapshot{
		HostAddress: "gonka1host",
		MinuteUnix:  1710000000,
		Host:        rpcStatsHost{Requests: 3},
		Shards:      []rpcStatsShard{{EscrowID: "42", Requests: 3}},
	})
	child := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		_, _ = w.Write(body)
	}))
	defer child.Close()
	routes := newRoutes(map[string]string{"v5": strings.TrimPrefix(child.URL, "http://")})
	srv := httptest.NewServer(Handler(routes))
	defer srv.Close()

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			resp, err := http.Get(srv.URL + "/stats/rpc")
			if err != nil {
				t.Error(err)
				return
			}
			_, _ = io.ReadAll(resp.Body)
			resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				t.Errorf("status=%d", resp.StatusCode)
			}
		}()
	}
	wg.Wait()
	if hits.Load() != 1 {
		t.Fatalf("child hits = %d, want 1", hits.Load())
	}

	resp, err := http.Get(srv.URL + "/stats/rpc")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if hits.Load() != 1 {
		t.Fatalf("cached GET still hit child: %d", hits.Load())
	}
}

func TestProxy_RPCStats_MergeIgnoresCallerCancel(t *testing.T) {
	rpcStatsCache.reset()
	t.Cleanup(rpcStatsCache.reset)

	var hits atomic.Int32
	started := make(chan struct{})
	body, _ := json.Marshal(rpcStatsSnapshot{
		HostAddress: "gonka1host",
		MinuteUnix:  1710000000,
		Host:        rpcStatsHost{Requests: 9},
		Shards:      []rpcStatsShard{{EscrowID: "42", Requests: 3}},
	})
	child := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		select {
		case <-started:
		default:
			close(started)
		}
		time.Sleep(250 * time.Millisecond)
		_, _ = w.Write(body)
	}))
	defer child.Close()
	routes := newRoutes(map[string]string{"v5": strings.TrimPrefix(child.URL, "http://")})
	srv := httptest.NewServer(Handler(routes))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/stats/rpc", nil)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		resp, err := http.DefaultClient.Do(req)
		if err == nil {
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
		}
	}()
	<-started
	cancel()

	resp, err := http.Get(srv.URL + "/stats/rpc")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d body=%s", resp.StatusCode, raw)
	}
	var snap rpcStatsSnapshot
	if err := json.Unmarshal(raw, &snap); err != nil {
		t.Fatal(err)
	}
	if snap.Host.Requests != 9 {
		t.Fatalf("snap = %+v", snap.Host)
	}
	if hits.Load() != 1 {
		t.Fatalf("child hits = %d, want 1 (shared flight)", hits.Load())
	}
	wg.Wait()
}

func TestGzipIdentityWhenCallerOmitsHeader(t *testing.T) {
	body, _ := json.Marshal(rpcStatsSnapshot{MinuteUnix: 1, Host: rpcStatsHost{Requests: 1}})
	child := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(body)
	}))
	defer child.Close()
	routes := newRoutes(map[string]string{"v1": strings.TrimPrefix(child.URL, "http://")})
	srv := httptest.NewServer(Handler(routes))
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/stats/rpc")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.Header.Get("Content-Encoding") != "" {
		t.Fatalf("encoding = %q", resp.Header.Get("Content-Encoding"))
	}
}

func TestAcceptsGzip(t *testing.T) {
	h := http.Header{}
	h.Set("Accept-Encoding", "gzip, deflate")
	if !acceptsGzip(h) {
		t.Fatal("want gzip")
	}
	if acceptsGzip(http.Header{}) {
		t.Fatal("empty")
	}
}

func TestGzipBestSpeedRoundTrip(t *testing.T) {
	src := bytes.Repeat([]byte("abc"), 400)
	gz, err := gzipBestSpeed(src)
	if err != nil {
		t.Fatal(err)
	}
	r, err := gzip.NewReader(bytes.NewReader(gz))
	if err != nil {
		t.Fatal(err)
	}
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(src, out) {
		t.Fatal("round trip")
	}
}
