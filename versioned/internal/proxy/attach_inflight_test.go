package proxy

import (
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

func attachInFlightProxy(t *testing.T, child http.HandlerFunc) *httptest.Server {
	t.Helper()
	backend := newH2CChild(child)
	t.Cleanup(backend.Close)
	srv := httptest.NewServer(Handler(newRoutes(map[string]string{"v1": strings.TrimPrefix(backend.URL, "http://")})))
	t.Cleanup(srv.Close)
	return srv
}

// holdChild returns a channel the child blocks on and its release. Cleanup
// releases it before the servers close, so a failed assertion cannot leave
// handlers blocked and hang the test.
func holdChild(t *testing.T) (<-chan struct{}, func()) {
	t.Helper()
	hold := make(chan struct{})
	var once sync.Once
	release := func() { once.Do(func() { close(hold) }) }
	t.Cleanup(release)
	return hold, release
}

func postAttach(t *testing.T, srv *httptest.Server, id, ip string) int {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, srv.URL+"/v1/sessions/"+id+"/rpc/devshard.transport.v1.PeerAuthService/Attach", nil)
	if err != nil {
		t.Error(err)
		return 0
	}
	req.Header.Set(originIPHeader, ip)
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Error(err)
		return 0
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	return resp.StatusCode
}

func TestProxy_ParallelAttachMissesStopAtInFlightCap(t *testing.T) {
	const burst = 50
	var forwarded atomic.Int32
	var hold <-chan struct{}
	srv := attachInFlightProxy(t, func(w http.ResponseWriter, r *http.Request) {
		forwarded.Add(1)
		<-hold
		w.Header().Set(headerDevshardError, errorEscrowNotFound)
		http.Error(w, "escrow is not open on this host", http.StatusPreconditionFailed)
	})
	hold, release := holdChild(t)

	var wg sync.WaitGroup
	var limited atomic.Int32
	for i := 0; i < burst; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if postAttach(t, srv, fmt.Sprintf("%d", 7000+i), "203.0.113.50") == http.StatusTooManyRequests {
				limited.Add(1)
			}
		}(i)
	}
	deadline := time.Now().Add(5 * time.Second)
	for forwarded.Load() < maxAttachInFlightPerIP && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(50 * time.Millisecond)
	if got := forwarded.Load(); got != maxAttachInFlightPerIP {
		release()
		wg.Wait()
		t.Fatalf("in flight before any answer = %d, want %d", got, maxAttachInFlightPerIP)
	}
	release()
	wg.Wait()

	// The first miss frees one slot before the second miss lands.
	if got := forwarded.Load(); got > maxAttachInFlightPerIP+1 {
		t.Fatalf("forwarded = %d, want at most %d", got, maxAttachInFlightPerIP+1)
	}
	if got := limited.Load(); got < burst-maxAttachInFlightPerIP-1 {
		t.Fatalf("limited = %d, want at least %d", got, burst-maxAttachInFlightPerIP-1)
	}
}

func TestProxy_ParallelAttachBindsQueueWithoutLimit(t *testing.T) {
	const burst = 50
	var inFlight, peak atomic.Int32
	srv := attachInFlightProxy(t, func(w http.ResponseWriter, r *http.Request) {
		n := inFlight.Add(1)
		for {
			p := peak.Load()
			if n <= p || peak.CompareAndSwap(p, n) {
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
		inFlight.Add(-1)
		w.WriteHeader(http.StatusOK)
	})

	var wg sync.WaitGroup
	var ok atomic.Int32
	for i := 0; i < burst; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if postAttach(t, srv, fmt.Sprintf("%d", 7100+i), "203.0.113.51") == http.StatusOK {
				ok.Add(1)
			}
		}(i)
	}
	wg.Wait()
	if got := ok.Load(); got != burst {
		t.Fatalf("ok = %d, want %d: real binds over the cap queue, they are not refused", got, burst)
	}
	if got := peak.Load(); got > maxAttachInFlightPerIP {
		t.Fatalf("peak in flight = %d, want at most %d", got, maxAttachInFlightPerIP)
	}
}

func TestProxy_AttachInFlightIsPerOriginIP(t *testing.T) {
	var hold <-chan struct{}
	var forwarded atomic.Int32
	srv := attachInFlightProxy(t, func(w http.ResponseWriter, r *http.Request) {
		forwarded.Add(1)
		if r.Header.Get(originIPHeader) == "203.0.113.52" {
			<-hold
		}
		w.WriteHeader(http.StatusOK)
	})
	hold, release := holdChild(t)
	var wg sync.WaitGroup
	for i := 0; i < maxAttachInFlightPerIP; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			postAttach(t, srv, fmt.Sprintf("%d", 7200+i), "203.0.113.52")
		}(i)
	}
	deadline := time.Now().Add(5 * time.Second)
	for forwarded.Load() < maxAttachInFlightPerIP && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	got := postAttach(t, srv, "7299", "203.0.113.53")
	release()
	wg.Wait()
	if got != http.StatusOK {
		t.Fatalf("other origin status = %d, want 200", got)
	}
}

func TestOriginLookupLimiter_AttachReleaseIsIdempotent(t *testing.T) {
	l := newOriginLookupLimiter()
	req := httptest.NewRequest(http.MethodPost, "/sessions/1/rpc/devshard.transport.v1.PeerAuthService/Attach", nil)
	req.Header.Set(originIPHeader, "203.0.113.54")
	rest := "/sessions/1/rpc/devshard.transport.v1.PeerAuthService/Attach"
	first, ok := l.admit(req, rest)
	if !ok {
		t.Fatal("first admit refused")
	}
	second, ok := l.admit(req, rest)
	if !ok {
		t.Fatal("second admit refused")
	}
	first()
	first()
	l.mu.Lock()
	n := 0
	if gate := l.attaching["203.0.113.54"]; gate != nil {
		n = gate.n
	}
	l.mu.Unlock()
	if n != 1 {
		t.Fatalf("in flight = %d after one release called twice, want 1", n)
	}
	second()
	l.mu.Lock()
	_, left := l.attaching["203.0.113.54"]
	l.mu.Unlock()
	if left {
		t.Fatal("gate must be dropped when nothing is in flight")
	}
}
