// Package client consumes the blockoracle HTTP + SSE API over the network
// or in-process.
//
// It subscribes on startup, caches the latest header, re-verifies every
// ingested header against a pinned validator set, and serves the
// BlockOracle interface to downstream callers (devshardd, internal dapi
// callers).
package client

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"devshard/chainoracle/blocks"
	"devshard/chainoracle/blocks/verifier"
	"devshard/logging"
)

// HTTPConfig pins the HTTP consumer to a specific producer and validator set.
//
// Verifier is optional: when nil the client trusts the producer and
// caches every header as-is (including Commit.Signatures) without
// cryptographic verification. This mode is used by devshardd hosts,
// which have an authenticated relationship with the height-sync oracle
// and need the full signature set to forward as proofs downstream.
// Non-host consumers (devshardctl, cross-host auditors) MUST set a
// Verifier so tampering is caught at ingest.
type HTTPConfig struct {
	BaseURL          string
	Verifier         *verifier.Verifier
	HTTPClient       *http.Client
	SubscribeFrom    int64         // 0 = from latest
	ResubscribeAfter time.Duration // sleep between SSE reconnects
	StaleAfter       time.Duration // Latest() marks cached header stale after this quiet period
}

// Client implements blocks.BlockOracle by consuming the HTTP+SSE
// wire protocol exposed by blockoracle/server.
type Client struct {
	cfg HTTPConfig
	hc  *http.Client

	mu           sync.RWMutex
	latest       *blocks.Header
	cache        map[int64]*blocks.Header
	lastVerified int64
	lastRecvUnix int64 // atomic; UnixNano of most recent verified header

	subMu sync.Mutex
	subs  map[int]*subscription

	runCancel context.CancelFunc
	runDone   chan struct{}

	rejected atomic.Int64 // headers dropped due to verification failure
}

type subscription struct {
	ch   chan *blocks.Header
	from int64
}

// NewHTTP constructs and starts an HTTP consumer. It opens a background
// SSE subscription and caches headers as they arrive. Close the returned
// Client (via context cancellation of ctx) to tear down the goroutine.
func NewHTTP(ctx context.Context, cfg HTTPConfig) (*Client, error) {
	if cfg.BaseURL == "" {
		return nil, errors.New("blockoracle/client: empty base url")
	}
	if _, err := url.Parse(cfg.BaseURL); err != nil {
		return nil, fmt.Errorf("blockoracle/client: invalid base url: %w", err)
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{Timeout: 0} // SSE: no client-side timeout
	}
	if cfg.ResubscribeAfter <= 0 {
		cfg.ResubscribeAfter = time.Second
	}
	if cfg.StaleAfter <= 0 {
		cfg.StaleAfter = 10 * time.Second
	}
	c := &Client{
		cfg:   cfg,
		hc:    cfg.HTTPClient,
		cache: make(map[int64]*blocks.Header),
		subs:  make(map[int]*subscription),
	}

	runCtx, cancel := context.WithCancel(ctx)
	c.runCancel = cancel
	c.runDone = make(chan struct{})
	go c.runSubscribeLoop(runCtx)
	return c, nil
}

// Close stops the background subscription goroutine. Further calls on
// the client return the last cached state.
func (c *Client) Close() {
	if c.runCancel != nil {
		c.runCancel()
		<-c.runDone
	}
}

// Latest returns the most recently verified header. If no header has
// been received yet, it falls back to a single synchronous GET against
// /block/latest so eager callers don't race the subscribe goroutine.
func (c *Client) Latest(ctx context.Context) (*blocks.Header, error) {
	c.mu.RLock()
	h := c.latest
	c.mu.RUnlock()
	if h != nil {
		return cloneHeader(h), nil
	}
	fetched, err := c.fetchAndVerifyLatest(ctx)
	if err != nil {
		return nil, err
	}
	return cloneHeader(fetched), nil
}

// Stale reports whether Latest() would return a header older than
// StaleAfter. Useful for consumers that need to know whether to act on a
// cached value (see testenv.md §7.2 scenario I4).
func (c *Client) Stale() bool {
	stale, _, _, _ := c.StaleDetails()
	return stale
}

// StaleDetails reports staleness and timing for height-sync decide debug logs.
func (c *Client) StaleDetails() (stale bool, lastRecvAgeMs int64, latestHeight int64, neverReceived bool) {
	last := atomic.LoadInt64(&c.lastRecvUnix)
	if last == 0 {
		return true, 0, 0, true
	}
	age := time.Since(time.Unix(0, last))
	stale = age > c.cfg.StaleAfter
	c.mu.RLock()
	if c.latest != nil {
		latestHeight = c.latest.Height
	}
	c.mu.RUnlock()
	return stale, age.Milliseconds(), latestHeight, false
}

// RejectedCount returns the number of headers dropped by the client due
// to verification failure. Tests and operators use it to detect
// tampering.
func (c *Client) RejectedCount() int64 {
	return c.rejected.Load()
}

// At returns the header at height h, fetching and verifying it on
// demand if not cached.
func (c *Client) At(ctx context.Context, height int64) (*blocks.Header, error) {
	c.mu.RLock()
	if h, ok := c.cache[height]; ok {
		c.mu.RUnlock()
		return cloneHeader(h), nil
	}
	c.mu.RUnlock()
	h, err := c.fetchAndVerifyAt(ctx, height)
	if err != nil {
		return nil, err
	}
	return cloneHeader(h), nil
}

// Prove fetches a proof for (path, height). Proofs are not verified
// locally; callers that need end-to-end authentication should cross-
// check Proof.Value against the header's AppHash (testenv simplification).
func (c *Client) Prove(ctx context.Context, path string, height int64) (*blocks.Proof, error) {
	u, err := c.joinURL(fmt.Sprintf("/block/%d/prove", height))
	if err != nil {
		return nil, err
	}
	q := u.Query()
	q.Set("path", path)
	u.RawQuery = q.Encode()

	payload, err := c.get(ctx, u.String())
	if err != nil {
		return nil, err
	}
	var proof blocks.Proof
	if err := json.Unmarshal(payload, &proof); err != nil {
		return nil, fmt.Errorf("blockoracle/client: decode proof: %w", err)
	}
	return &proof, nil
}

// Subscribe returns a channel of verified headers starting at
// fromHeight. Each subscription is independent; the background SSE
// goroutine fans out to all active subscribers.
func (c *Client) Subscribe(ctx context.Context, fromHeight int64) (<-chan *blocks.Header, error) {
	sub := &subscription{ch: make(chan *blocks.Header, 16), from: fromHeight}

	c.subMu.Lock()
	id := len(c.subs)
	for {
		if _, exists := c.subs[id]; !exists {
			break
		}
		id++
	}
	c.subs[id] = sub
	c.subMu.Unlock()

	// Replay anything we already have that matches fromHeight.
	c.mu.RLock()
	replay := make([]*blocks.Header, 0)
	if c.latest != nil {
		for h := fromHeight; h <= c.latest.Height; h++ {
			if v, ok := c.cache[h]; ok {
				replay = append(replay, cloneHeader(v))
			}
		}
	}
	c.mu.RUnlock()

	go func() {
		for _, h := range replay {
			select {
			case <-ctx.Done():
				c.unsubscribe(id)
				return
			case sub.ch <- h:
			}
		}
		<-ctx.Done()
		c.unsubscribe(id)
	}()

	return sub.ch, nil
}

func (c *Client) unsubscribe(id int) {
	c.subMu.Lock()
	sub, ok := c.subs[id]
	if !ok {
		c.subMu.Unlock()
		return
	}
	delete(c.subs, id)
	c.subMu.Unlock()
	close(sub.ch)
}

// runSubscribeLoop maintains a long-lived SSE connection and reconnects
// on failure. It is the sole writer to c.latest / c.cache.
func (c *Client) runSubscribeLoop(ctx context.Context) {
	defer close(c.runDone)
	from := c.cfg.SubscribeFrom
	for {
		if ctx.Err() != nil {
			return
		}
		err := c.consumeStream(ctx, from)
		if ctx.Err() != nil {
			return
		}
		// On disconnect, resume at the height after the last one we saw.
		c.mu.RLock()
		if c.latest != nil {
			from = c.latest.Height + 1
		}
		c.mu.RUnlock()

		if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, context.Canceled) {
			// Backoff before reconnect.
			select {
			case <-ctx.Done():
				return
			case <-time.After(c.cfg.ResubscribeAfter):
			}
			continue
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(c.cfg.ResubscribeAfter):
		}
	}
}

func (c *Client) consumeStream(ctx context.Context, from int64) error {
	u, err := c.joinURL("/block/stream")
	if err != nil {
		return err
	}
	if from > 0 {
		q := u.Query()
		q.Set("from", strconv.FormatInt(from, 10))
		u.RawQuery = q.Encode()
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "text/event-stream")

	resp, err := c.hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("blockoracle/client: stream status %d", resp.StatusCode)
	}

	reader := bufio.NewReader(resp.Body)
	var data strings.Builder
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return err
		}
		line = strings.TrimRight(line, "\r\n")
		switch {
		case line == "":
			if data.Len() == 0 {
				continue
			}
			payload := data.String()
			data.Reset()
			if err := c.ingestFrame(payload); err != nil {
				// Ingest errors don't kill the stream: the rejected
				// counter records them for tests / operators.
				c.rejected.Add(1)
			}
		case strings.HasPrefix(line, "data:"):
			d := strings.TrimPrefix(line, "data:")
			d = strings.TrimPrefix(d, " ")
			data.WriteString(d)
		default:
			// ignore event:, id:, : comments, retry:
		}
	}
}

func (c *Client) ingestFrame(payload string) error {
	var h blocks.Header
	if err := json.Unmarshal([]byte(payload), &h); err != nil {
		return fmt.Errorf("decode: %w", err)
	}
	logging.Debug("blockoracle: stream frame received",
		"subsystem", "blockoracle-client",
		"height", h.Height,
		"chain_id", h.ChainID,
		"signatures", len(h.Commit.Signatures),
	)
	c.mu.RLock()
	lastVerified := c.lastVerified
	c.mu.RUnlock()
	if err := c.verify(&h, lastVerified); err != nil {
		logging.Warn("blockoracle: stream frame rejected",
			"subsystem", "blockoracle-client",
			"height", h.Height,
			"last_verified", lastVerified,
			"error", err,
		)
		return err
	}
	c.store(&h)
	return nil
}

// verify runs the pinned verifier against h, or skips verification when
// the consumer opted out by passing a nil verifier in HTTPConfig (host
// trust mode).
func (c *Client) verify(h *blocks.Header, lastHeight int64) error {
	if c.cfg.Verifier == nil {
		return nil
	}
	return c.cfg.Verifier.Verify(h, lastHeight)
}

func (c *Client) store(h *blocks.Header) {
	c.mu.Lock()
	c.cache[h.Height] = h
	if c.latest == nil || h.Height > c.latest.Height {
		c.latest = h
	}
	if h.Height > c.lastVerified {
		c.lastVerified = h.Height
	}
	c.mu.Unlock()
	atomic.StoreInt64(&c.lastRecvUnix, time.Now().UnixNano())
	logging.Debug("blockoracle: header cached",
		"subsystem", "blockoracle-client",
		"height", h.Height,
		"block_hash_len", len(h.BlockHash),
		"app_hash_len", len(h.AppHash),
		"validators_hash_len", len(h.ValidatorsHash),
		"signatures", len(h.Commit.Signatures),
	)
	c.fanout(h)
}

func (c *Client) fanout(h *blocks.Header) {
	c.subMu.Lock()
	defer c.subMu.Unlock()
	for id, sub := range c.subs {
		if h.Height < sub.from {
			continue
		}
		cp := cloneHeader(h)
		select {
		case sub.ch <- cp:
		default:
			// Drop slow subscribers.
			delete(c.subs, id)
			close(sub.ch)
		}
	}
}

func (c *Client) fetchAndVerifyLatest(ctx context.Context) (*blocks.Header, error) {
	u, err := c.joinURL("/block/latest")
	if err != nil {
		return nil, err
	}
	payload, err := c.get(ctx, u.String())
	if err != nil {
		return nil, err
	}
	var h blocks.Header
	if err := json.Unmarshal(payload, &h); err != nil {
		return nil, fmt.Errorf("blockoracle/client: decode latest: %w", err)
	}
	if err := c.verify(&h, 0); err != nil {
		c.rejected.Add(1)
		return nil, fmt.Errorf("blockoracle/client: verify latest: %w", err)
	}
	c.store(&h)
	return &h, nil
}

func (c *Client) fetchAndVerifyAt(ctx context.Context, height int64) (*blocks.Header, error) {
	u, err := c.joinURL(fmt.Sprintf("/block/%d", height))
	if err != nil {
		return nil, err
	}
	payload, err := c.get(ctx, u.String())
	if err != nil {
		return nil, err
	}
	var h blocks.Header
	if err := json.Unmarshal(payload, &h); err != nil {
		return nil, fmt.Errorf("blockoracle/client: decode at: %w", err)
	}
	if err := c.verify(&h, 0); err != nil {
		c.rejected.Add(1)
		return nil, fmt.Errorf("blockoracle/client: verify at: %w", err)
	}
	c.mu.Lock()
	c.cache[h.Height] = &h
	c.mu.Unlock()
	return &h, nil
}

func (c *Client) get(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("blockoracle/client: GET %s: status %d", url, resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
}

func (c *Client) joinURL(path string) (*url.URL, error) {
	u, err := url.Parse(c.cfg.BaseURL)
	if err != nil {
		return nil, err
	}
	u.Path = strings.TrimRight(u.Path, "/") + path
	return u, nil
}

// NewInProcess returns a BlockOracle backed by an in-process oracle
// (typically the producer's observer). Real decentralized-api wires its
// own observer via this constructor; no HTTP hop is involved.
func NewInProcess(o blocks.BlockOracle) blocks.BlockOracle {
	return o
}

func cloneHeader(h *blocks.Header) *blocks.Header {
	if h == nil {
		return nil
	}
	cp := *h
	cp.BlockHash = append([]byte(nil), h.BlockHash...)
	cp.AppHash = append([]byte(nil), h.AppHash...)
	cp.ValidatorsHash = append([]byte(nil), h.ValidatorsHash...)
	cp.NextValidatorsHash = append([]byte(nil), h.NextValidatorsHash...)
	cp.Commit = blocks.Commit{
		Height:     h.Commit.Height,
		Round:      h.Commit.Round,
		BlockID:    append([]byte(nil), h.Commit.BlockID...),
		Signatures: make([]blocks.CommitSig, len(h.Commit.Signatures)),
	}
	for i, s := range h.Commit.Signatures {
		cp.Commit.Signatures[i] = blocks.CommitSig{
			ValidatorAddress: append([]byte(nil), s.ValidatorAddress...),
			Timestamp:        s.Timestamp,
			Signature:        append([]byte(nil), s.Signature...),
		}
	}
	return &cp
}

// Compile-time assertion.
var _ blocks.BlockOracle = (*Client)(nil)
