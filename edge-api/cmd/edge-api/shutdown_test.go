package main

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"edge-api/internal/server"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The guarantee this whole change exists for, end to end: a query that is
// already running survives the shutdown, and while it runs the instance already
// reports unready so the balancer stops sending it work. The old code called
// Shutdown immediately with a fixed 10s and left /readyz answering 200, so
// either half of the sequence regressing must fail here.
func TestDrainAndShutdown_ServesInFlightRequestWhileReportingUnready(t *testing.T) {
	release := make(chan struct{})
	srv, baseURL := startTestServer(t, func(c echo.Context) error {
		<-release
		return c.String(http.StatusOK, "finished")
	})

	requestDone := make(chan int, 1)
	go func() {
		resp, err := http.Get(baseURL + "/slow")
		if err != nil {
			requestDone <- 0
			return
		}
		defer resp.Body.Close()
		requestDone <- resp.StatusCode
	}()
	waitForInFlight(t, baseURL)

	// Readiness before shutdown is about chain reachability, which this test
	// deliberately does not provide; what matters here is that it does not yet
	// claim to be draining.
	require.NotContains(t, readyzBody(t, baseURL), "draining",
		"instance should not report draining before shutdown starts")

	shutdownDone := make(chan error, 1)
	go func() {
		shutdownDone <- drainAndShutdown(srv, config{
			DrainAnnounce:  150 * time.Millisecond,
			ShutdownBudget: 30 * time.Second,
		}, make(chan os.Signal))
	}()

	// Inside the announce window the process is unready but still serving: that
	// ordering is what lets the balancer step aside without dropping anything.
	require.Eventually(t, func() bool {
		status, body := readyz(t, baseURL)
		return status == http.StatusServiceUnavailable && strings.Contains(body, "draining")
	}, 2*time.Second, 10*time.Millisecond, "/readyz should report draining during the announce window")

	select {
	case <-shutdownDone:
		t.Fatal("shutdown finished while a request was still running")
	case code := <-requestDone:
		t.Fatalf("request ended on its own with %d before it was released", code)
	case <-time.After(300 * time.Millisecond):
	}

	close(release)
	assert.Equal(t, http.StatusOK, <-requestDone, "in-flight request must complete")
	require.NoError(t, <-shutdownDone)
}

// A shutdown that waits for a request nobody is going to finish must still end,
// and must say why.
func TestDrainAndShutdown_BudgetExpiryClosesRemainingConnections(t *testing.T) {
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	srv, baseURL := startTestServer(t, func(c echo.Context) error {
		<-release
		return c.String(http.StatusOK, "finished")
	})

	go func() { _, _ = http.Get(baseURL + "/slow") }()
	waitForInFlight(t, baseURL)

	err := drainAndShutdown(srv, config{
		DrainAnnounce:  0,
		ShutdownBudget: 200 * time.Millisecond,
	}, make(chan os.Signal))

	require.Error(t, err)
	assert.Contains(t, err.Error(), "shutdown budget")
}

// signal.Notify takes SIGTERM away from the runtime, so a second signal has to
// be honoured here — otherwise an operator watching a stuck two-minute drain has
// nothing left but SIGKILL.
func TestDrainAndShutdown_SecondSignalDuringShutdownForcesClose(t *testing.T) {
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	srv, baseURL := startTestServer(t, func(c echo.Context) error {
		<-release
		return c.String(http.StatusOK, "finished")
	})

	go func() { _, _ = http.Get(baseURL + "/slow") }()
	waitForInFlight(t, baseURL)

	force := make(chan os.Signal, 1)
	done := make(chan error, 1)
	go func() {
		done <- drainAndShutdown(srv, config{
			DrainAnnounce:  0,
			ShutdownBudget: 10 * time.Minute,
		}, force)
	}()

	time.Sleep(50 * time.Millisecond)
	force <- syscall.SIGTERM

	select {
	case err := <-done:
		require.Error(t, err)
		assert.Contains(t, err.Error(), "operator sent")
	case <-time.After(10 * time.Second):
		t.Fatal("a second signal did not force the shutdown")
	}
}

// Readiness must drop before anything stops being served, or the balancer finds
// out by having a request refused.
func TestDrainAndShutdown_ReportsUnreadyBeforeItStopsServing(t *testing.T) {
	srv := &stubServer{}

	require.NoError(t, drainAndShutdown(srv, config{
		DrainAnnounce:  time.Millisecond,
		ShutdownBudget: time.Minute,
	}, make(chan os.Signal)))

	assert.Equal(t, []string{"BeginDrain", "Shutdown"}, srv.order())
}

func TestGracefulShutdown_ReportsBothTheReasonAndACloseFailure(t *testing.T) {
	closeErr := errors.New("listener already gone")
	srv := &stubServer{shutdownErr: errors.New("context deadline exceeded"), closeErr: closeErr}

	err := gracefulShutdown(srv, time.Millisecond, make(chan os.Signal))

	require.Error(t, err)
	assert.Contains(t, err.Error(), "shutdown budget")
	assert.ErrorIs(t, err, closeErr)
}

// startTestServer runs a real server with one blocking route on a real port, so
// the test exercises the same http.Server shutdown path production uses.
func startTestServer(t *testing.T, slow echo.HandlerFunc) (*server.Server, string) {
	t.Helper()
	srv := server.New(nil)
	srv.GET("/slow", slow)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	srv.Listener = ln
	go func() { _ = srv.Start("") }()

	t.Cleanup(func() { _ = srv.Close() })
	return srv, "http://" + ln.Addr().String()
}

// waitForInFlight blocks until the server is up and the slow request has been
// accepted, so shutdown cannot start before there is anything to wait for.
func waitForInFlight(t *testing.T, baseURL string) {
	t.Helper()
	require.Eventually(t, func() bool {
		resp, err := http.Get(baseURL + "/healthz")
		if err != nil {
			return false
		}
		_ = resp.Body.Close()
		return resp.StatusCode == http.StatusOK
	}, 5*time.Second, 10*time.Millisecond, "server never came up")
	time.Sleep(50 * time.Millisecond)
}

func readyz(t *testing.T, baseURL string) (int, string) {
	t.Helper()
	resp, err := http.Get(baseURL + "/readyz")
	require.NoError(t, err)
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return resp.StatusCode, string(body)
}

func readyzBody(t *testing.T, baseURL string) string {
	t.Helper()
	_, body := readyz(t, baseURL)
	return body
}

type stubServer struct {
	mu          sync.Mutex
	calls       []string
	shutdownErr error
	closeErr    error
}

func (s *stubServer) record(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, name)
}

func (s *stubServer) order() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.calls...)
}

func (s *stubServer) BeginDrain() { s.record("BeginDrain") }

func (s *stubServer) Shutdown(context.Context) error {
	s.record("Shutdown")
	return s.shutdownErr
}

func (s *stubServer) ForceClose() error {
	s.record("ForceClose")
	return s.closeErr
}
