package observability

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"sync"
	"time"

	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/common/expfmt"
	"github.com/prometheus/common/model"
)

// Per-node snapshot ceilings (contract D7): a broken or malicious node must
// not balloon the merged output.
const (
	maxBodyBytes     = 2 << 20 // 2 MiB
	maxSeriesPerNode = 5000

	// errRetryDelay bounds how often a failing rebuild is retried under
	// request load (errors are not cached like snapshots are).
	errRetryDelay = 2 * time.Second
)

// errNotExposed marks a 404 (GONKA_METRICS=off or a pre-metrics image);
// such nodes are absent from the output entirely per the schema contract.
var errNotExposed = errors.New("mlnode does not expose metrics")

// MLNodeTarget identifies one mlnode and the URL of its metrics exporter.
type MLNodeTarget struct {
	ID  string
	URL string
}

// MLNodeMetricsConfig tunes the aggregating handler.
type MLNodeMetricsConfig struct {
	// CacheTTL is how long a merged snapshot is served before a rebuild.
	CacheTTL time.Duration
	// ScrapeTimeout bounds each per-mlnode fetch.
	ScrapeTimeout time.Duration
}

func (c MLNodeMetricsConfig) withDefaults() MLNodeMetricsConfig {
	if c.CacheTTL <= 0 {
		c.CacheTTL = 10 * time.Second
	}
	if c.ScrapeTimeout <= 0 {
		c.ScrapeTimeout = 5 * time.Second
	}
	return c
}

// MLNodeMetricsHandler returns an http.Handler that federates the metrics of
// every mlnode returned by list() into one exposition: each node's exporter
// is scraped concurrently, every series gets a node_id label, families are
// merged by name (naive text concatenation breaks Prometheus on duplicate
// HELP/TYPE and colliding series) and mlnode_up{node_id} reports scrape
// health. One fan-out per CacheTTL fills the cache for every negotiated
// response format, and rebuilds are single-flighted, so public scrapes cost
// the mlnodes at most one fan-out per TTL. The response format honors the
// Accept header (classic text by default, OpenMetrics with its # EOF
// truncation marker on request).
func MLNodeMetricsHandler(list func() ([]MLNodeTarget, error), cfg MLNodeMetricsConfig) http.Handler {
	a := &mlnodeAggregator{
		list:   list,
		cfg:    cfg.withDefaults(),
		client: &http.Client{Timeout: cfg.withDefaults().ScrapeTimeout},
		cached: map[expfmt.Format][]byte{},
	}
	return http.HandlerFunc(a.serve)
}

// responseFormats are the encodings kept in the cache; Negotiate resolves any
// Accept header to one of them.
var responseFormats = []expfmt.Format{
	expfmt.NewFormat(expfmt.TypeTextPlain),
	expfmt.NewFormat(expfmt.TypeOpenMetrics),
}

type mlnodeAggregator struct {
	list   func() ([]MLNodeTarget, error)
	cfg    MLNodeMetricsConfig
	client *http.Client

	buildMu  sync.Mutex // single-flights rebuilds so scrapes coalesce
	cacheMu  sync.Mutex
	cached   map[expfmt.Format][]byte
	cachedAt time.Time
	lastErr  error
	errAt    time.Time
}

func (a *mlnodeAggregator) serve(w http.ResponseWriter, r *http.Request) {
	format := a.negotiate(r)
	// The rebuild fills a cache shared by every consumer, so it must not
	// live and die with the triggering request: a client disconnect during
	// the fan-out would cancel every scrape and cache an all-down snapshot
	// for the whole TTL. Detach from the request's cancellation but keep
	// its values (trace IDs) and bound the work with our own deadline.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), a.cfg.ScrapeTimeout+time.Second)
	defer cancel()
	body, err := a.render(ctx, format)
	if err != nil {
		// detail stays server-side; this is an unauthenticated endpoint
		_ = logObservabilityError("mlnode_metrics_render", "mlnode metrics render failed", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", string(format))
	_, _ = w.Write(body)
}

func (a *mlnodeAggregator) negotiate(r *http.Request) expfmt.Format {
	format := expfmt.NegotiateIncludingOpenMetrics(r.Header)
	for _, known := range responseFormats {
		if format.FormatType() == known.FormatType() {
			return known
		}
	}
	return responseFormats[0]
}

func (a *mlnodeAggregator) render(ctx context.Context, format expfmt.Format) ([]byte, error) {
	if b, err, done := a.cachedIfFresh(format); done {
		return b, err
	}
	// Single-flight: concurrent scrapers wait, then read the fresh cache.
	a.buildMu.Lock()
	defer a.buildMu.Unlock()
	if b, err, done := a.cachedIfFresh(format); done {
		return b, err
	}

	fams, up, err := a.build(ctx)

	a.cacheMu.Lock()
	defer a.cacheMu.Unlock()
	if err != nil {
		a.lastErr, a.errAt = err, time.Now()
		return nil, err
	}
	a.lastErr = nil
	for _, f := range responseFormats {
		encoded, encErr := encodeExposition(fams, up, f)
		if encErr != nil {
			// only the requested format's failure is the caller's problem;
			// the other format simply stays uncached
			if f == format {
				return nil, encErr
			}
			delete(a.cached, f)
			continue
		}
		a.cached[f] = encoded
	}
	a.cachedAt = time.Now()
	return a.cached[format], nil
}

// cachedIfFresh returns the cached body for format, a recent build error, or
// done=false when a rebuild is needed.
func (a *mlnodeAggregator) cachedIfFresh(format expfmt.Format) ([]byte, error, bool) {
	a.cacheMu.Lock()
	defer a.cacheMu.Unlock()
	if b, ok := a.cached[format]; ok && time.Since(a.cachedAt) < a.cfg.CacheTTL {
		return b, nil, true
	}
	if a.lastErr != nil && time.Since(a.errAt) < errRetryDelay {
		return nil, a.lastErr, true
	}
	return nil, nil, false
}

func (a *mlnodeAggregator) build(ctx context.Context) (map[string]*dto.MetricFamily, []*dto.Metric, error) {
	targets, err := a.list()
	if err != nil {
		return nil, nil, fmt.Errorf("enumerate mlnodes: %w", err)
	}

	var (
		mu     sync.Mutex
		wg     sync.WaitGroup
		merged = map[string]*dto.MetricFamily{}
		up     = make([]*dto.Metric, 0, len(targets))
	)
	for _, t := range targets {
		t := t
		wg.Add(1)
		go func() {
			defer wg.Done()
			fams, scrapeErr := a.scrapeOne(ctx, t)
			mu.Lock()
			defer mu.Unlock()
			if errors.Is(scrapeErr, errNotExposed) {
				return // node absent entirely, no up series
			}
			up = append(up, upMetric(t.ID, scrapeErr == nil))
			if scrapeErr != nil {
				return
			}
			for name, fam := range fams {
				if name == "mlnode_up" {
					// reserved: a node-supplied family would collide with
					// ours and produce a duplicate TYPE block fleet-wide
					continue
				}
				if existing, ok := merged[name]; ok {
					if existing.GetType() != fam.GetType() {
						// mixed image versions can disagree on a family's
						// type; concatenating under one header would make
						// the family invalid for strict consumers
						_ = logObservabilityError("mlnode_metrics_type_mismatch",
							"family type mismatch across nodes, dropping later node's family",
							nil, "family", name, "node_id", t.ID)
						continue
					}
					// same family from another node: HELP/TYPE written once
					existing.Metric = append(existing.Metric, fam.Metric...)
				} else {
					merged[name] = fam
				}
			}
		}()
	}
	wg.Wait()
	return merged, up, nil
}

func encodeExposition(merged map[string]*dto.MetricFamily, up []*dto.Metric, format expfmt.Format) ([]byte, error) {
	names := make([]string, 0, len(merged))
	for name := range merged {
		names = append(names, name)
	}
	sort.Strings(names) // deterministic output so scrape diffs are stable

	var buf bytes.Buffer
	enc := expfmt.NewEncoder(&buf, format)
	// A whole fleet on pre-metrics images yields no up entries; encoders
	// reject an empty family, and an empty exposition is valid output.
	if len(up) > 0 {
		sort.Slice(up, func(i, j int) bool { return nodeIDLabel(up[i]) < nodeIDLabel(up[j]) })
		if err := enc.Encode(upFamily(up)); err != nil {
			return nil, err
		}
	}
	for _, name := range names {
		if err := enc.Encode(merged[name]); err != nil {
			return nil, err
		}
	}
	// OpenMetrics requires the # EOF terminator so consumers can detect a
	// truncated response.
	if closer, ok := enc.(expfmt.Closer); ok {
		if err := closer.Close(); err != nil {
			return nil, err
		}
	}
	return buf.Bytes(), nil
}

func (a *mlnodeAggregator) scrapeOne(ctx context.Context, t MLNodeTarget) (map[string]*dto.MetricFamily, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, t.URL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := a.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, errNotExposed
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("mlnode %s: status %d", t.ID, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes+1))
	if err != nil {
		return nil, fmt.Errorf("mlnode %s: read metrics: %w", t.ID, err)
	}
	if len(body) > maxBodyBytes {
		return nil, fmt.Errorf("mlnode %s: metrics exceed %d bytes, snapshot rejected", t.ID, maxBodyBytes)
	}
	parser := expfmt.NewTextParser(model.UTF8Validation)
	fams, err := parser.TextToMetricFamilies(bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("mlnode %s: parse metrics: %w", t.ID, err)
	}
	total := 0
	for _, fam := range fams {
		total += len(fam.Metric)
	}
	if total > maxSeriesPerNode {
		return nil, fmt.Errorf("mlnode %s: %d series exceed the %d per-node cap, snapshot rejected", t.ID, total, maxSeriesPerNode)
	}
	for _, fam := range fams {
		for _, m := range fam.Metric {
			// node_id is reserved: a node-supplied copy would create
			// duplicate label names and invalidate the whole exposition
			kept := m.Label[:0]
			for _, l := range m.Label {
				if l.GetName() != "node_id" {
					kept = append(kept, l)
				}
			}
			m.Label = append(kept, &dto.LabelPair{Name: strp("node_id"), Value: strp(t.ID)})
		}
	}
	return fams, nil
}

func upFamily(metrics []*dto.Metric) *dto.MetricFamily {
	return &dto.MetricFamily{
		Name:   strp("mlnode_up"),
		Help:   strp("Whether the mlnode's metrics endpoint was reachable on the last scrape (1 = up)."),
		Type:   dto.MetricType_GAUGE.Enum(),
		Metric: metrics,
	}
}

func upMetric(id string, ok bool) *dto.Metric {
	v := 0.0
	if ok {
		v = 1.0
	}
	return &dto.Metric{
		Label: []*dto.LabelPair{{Name: strp("node_id"), Value: strp(id)}},
		Gauge: &dto.Gauge{Value: &v},
	}
}

func nodeIDLabel(m *dto.Metric) string {
	for _, l := range m.Label {
		if l.GetName() == "node_id" {
			return l.GetValue()
		}
	}
	return ""
}

func strp(s string) *string { return &s }

// LogMLNodeTargetError reports a node whose exporter URL could not be built —
// such a node would otherwise vanish from the federation with no trace.
func LogMLNodeTargetError(nodeID string, err error) error {
	return logObservabilityError("mlnode_metrics_target", "failed to build mlnode exporter URL", err, "node_id", nodeID)
}
