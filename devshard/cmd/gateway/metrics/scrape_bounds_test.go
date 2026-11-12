package metrics

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

const scrapeEventWait = 5 * time.Second

type blockingCollector struct {
	description *prometheus.Desc
	entered     chan struct{}
	release     chan struct{}
	releaseOnce sync.Once
}

func newBlockingCollector(entries int) *blockingCollector {
	return &blockingCollector{
		description: prometheus.NewDesc("test_blocking_collector", "A collector that waits for the test.", nil, nil),
		entered:     make(chan struct{}, entries),
		release:     make(chan struct{}),
	}
}

func (c *blockingCollector) Describe(descriptions chan<- *prometheus.Desc) {
	descriptions <- c.description
}

func (c *blockingCollector) Collect(collected chan<- prometheus.Metric) {
	c.entered <- struct{}{}
	<-c.release
	collected <- prometheus.MustNewConstMetric(c.description, prometheus.GaugeValue, 1)
}

func (c *blockingCollector) releaseCollects() { c.releaseOnce.Do(func() { close(c.release) }) }

func awaitSignal(t *testing.T, signals <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-signals:
	case <-time.After(scrapeEventWait):
		t.Fatalf("%s never happened within %v", what, scrapeEventWait)
	}
}

func scrapeThrough(handler http.Handler) <-chan *httptest.ResponseRecorder {
	answered := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/metrics", nil))
		answered <- recorder
	}()
	return answered
}

func awaitScrape(t *testing.T, answered <-chan *httptest.ResponseRecorder, what string) *httptest.ResponseRecorder {
	t.Helper()
	select {
	case recorder := <-answered:
		return recorder
	case <-time.After(scrapeEventWait):
		t.Fatalf("%s was not answered within %v", what, scrapeEventWait)
		return nil
	}
}

// Test flow:
//  1. Register a collector that waits inside `Collect` until released, and start `maxConcurrentScrapes` scrapes, each through a fresh call to `Handler`.
//  2. Wait until every one of them is inside `Collect`.
//  3. Scrape once more and assert it is refused 503 at once, without entering `Collect`.
//  4. Release the collector and assert every scrape inside it was answered 200.
func TestAScrapePastTheConcurrencyCapIsRefusedWhileTheOthersCollect(t *testing.T) {
	gatewayMetrics := New()
	collector := newBlockingCollector(maxConcurrentScrapes + 1)
	gatewayMetrics.Register(collector)
	t.Cleanup(collector.releaseCollects)
	scrapes := make([]<-chan *httptest.ResponseRecorder, maxConcurrentScrapes)
	for index := range scrapes {
		scrapes[index] = scrapeThrough(gatewayMetrics.Handler())
	}
	for index := range maxConcurrentScrapes {
		awaitSignal(t, collector.entered, fmt.Sprintf("scrape %d entering Collect", index+1))
	}

	refused := awaitScrape(t, scrapeThrough(gatewayMetrics.Handler()), "the scrape past the cap")
	if refused.Code != http.StatusServiceUnavailable {
		t.Errorf("scrape past the cap = %d, want 503", refused.Code)
	}
	if extra := len(collector.entered); extra != 0 {
		t.Errorf("scrapes inside Collect past the cap = %d, want 0", extra)
	}

	collector.releaseCollects()
	for index, answered := range scrapes {
		if recorder := awaitScrape(t, answered, fmt.Sprintf("scrape %d", index+1)); recorder.Code != http.StatusOK {
			t.Errorf("scrape %d = %d, want 200", index+1, recorder.Code)
		}
	}
}

// Test flow:
//  1. Build a scrape handler with a one-millisecond timeout over a registry whose collector waits inside `Collect` until released.
//  2. Scrape it and assert the scrape is answered 503 while the collector is still waiting.
func TestAScrapePastItsTimeoutIsAnswered503(t *testing.T) {
	registry := prometheus.NewRegistry()
	collector := newBlockingCollector(1)
	registry.MustRegister(collector)
	t.Cleanup(collector.releaseCollects)

	recorder := awaitScrape(t, scrapeThrough(newScrapeHandler(registry, time.Millisecond)), "the scrape past its timeout")

	if recorder.Code != http.StatusServiceUnavailable {
		t.Errorf("scrape past its timeout = %d, want 503", recorder.Code)
	}
}
