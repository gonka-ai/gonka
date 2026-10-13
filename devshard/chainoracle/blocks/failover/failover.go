// Package failover is the host/gateway BlockOracle. Latest prefers a live
// Comet NewBlock cache, then NodeManager GetBlockHeader, then chain
// GetLatestBlock. Comet is skipped when the WS is down or the last Observe
// is older than CometMaxAge. GetBlockHeader(0) backs off 1m → 5m → 15m on
// any non-success. Unimplemented (old dapi) re-probes every
// NMUnimplementedReprobe (15m). Dummy / not found / transient use 1m → 5m → 15m.
// GetLatestBlock is at most once per ChainRefresh; within that window the
// last chain header is reused. At prefers the cached window, then
// GetBlockHeader (same Unimplemented / backoff skip as Latest), then chain
// At (GetBlockByHeight). Heights above the known tip or more than
// AtLookback below it return a dummy without RPC. Misses are negative-
// cached for AtNegativeTTL. Concurrent At of the same height share one
// RPC. Dummy headers are not cached. HTTP GET /block is not used.
package failover

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"sync"
	"time"

	"common/chainoracle/blocks"
	"common/logging"

	"golang.org/x/sync/singleflight"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	// CometMaxAge is the longest a Comet Observe may be served as Latest.
	// Failover compares tip.LastObservedAt() to this budget. tipcache.New
	// may take the same value as a default for Cache.Stale(); failover
	// does not consult the cache's staleAfter.
	CometMaxAge = 20 * time.Second
	// ChainRefresh is the minimum interval between GetLatestBlock RPCs.
	ChainRefresh = 10 * time.Second
	// AtLookback is how far below the known tip At() will RPC. Same size
	// as tipcache.HistoryWindow: a height outside the cached window is
	// not fetched. L6 treats the dummy as still pending.
	AtLookback = 100
	// AtNegativeTTL is how long a height miss (or dummy) suppresses
	// GetBlockHeader / GetBlockByHeight for that height.
	AtNegativeTTL = 5 * time.Second
	// NMUnimplementedReprobe is how long GetBlockHeader is skipped after
	// Unimplemented. Same as the last 1m→5m→15m step; old dapi is retried
	// so an upgrade is picked up without a process restart.
	NMUnimplementedReprobe = 15 * time.Minute
	atMissCap              = 128
)

var nmBackoffSchedule = []time.Duration{
	time.Minute,
	5 * time.Minute,
	NMUnimplementedReprobe,
}

// Oracle: tip is the Comet NewBlock cache (Latest primary and At window).
// nm is GetBlockHeader. chain is GetLatestBlock for Latest() and
// GetBlockByHeight for At outside the window. HTTP is never used.
type Oracle struct {
	tip   blocks.BlockOracle
	nm    blocks.BlockOracle
	chain blocks.BlockOracle

	mu             sync.Mutex
	nowFn          func() time.Time
	cometConnected bool
	lastOK         bool
	fetched        bool

	nmUnimpl   bool
	nmFailures int
	nmNextTry  time.Time

	chainLast    *blocks.Header
	chainLastAt  time.Time
	lastServed   *blocks.Header
	lastServedAt time.Time

	atMiss   map[int64]time.Time
	atFlight singleflight.Group
}

// New wraps tip (Comet cache), optional nm (GetBlockHeader), and optional
// chain (GetLatestBlock / GetBlockByHeight). Comet is not served until
// SetCometConnected(true).
func New(tip blocks.BlockOracle, nm blocks.BlockOracle, chain blocks.BlockOracle) *Oracle {
	return &Oracle{tip: tip, nm: nm, chain: chain, nowFn: time.Now}
}

// SetCometConnected reports whether the Comet NewBlock subscription is up.
// A disconnected feed is skipped even if the cache still holds a header.
func (o *Oracle) SetCometConnected(ok bool) {
	if o == nil {
		return
	}
	o.mu.Lock()
	o.cometConnected = ok
	o.mu.Unlock()
}

// SetNow overrides the clock (tests). nil restores time.Now.
func (o *Oracle) SetNow(now func() time.Time) {
	if o == nil {
		return
	}
	o.mu.Lock()
	if now == nil {
		o.nowFn = time.Now
	} else {
		o.nowFn = now
	}
	o.mu.Unlock()
}

func (o *Oracle) now() time.Time {
	o.mu.Lock()
	fn := o.nowFn
	o.mu.Unlock()
	if fn == nil {
		return time.Now()
	}
	return fn()
}

func usable(h *blocks.Header) bool {
	return h != nil && !blocks.IsDummyHeader(h)
}

func cloneHeader(h *blocks.Header) *blocks.Header {
	if h == nil {
		return nil
	}
	cp := *h
	cp.BlockHash = append([]byte(nil), h.BlockHash...)
	return &cp
}

func (o *Oracle) cometFresh() bool {
	if o == nil || o.tip == nil {
		return false
	}
	if lo, ok := o.tip.(interface{ LastObservedAt() time.Time }); ok {
		last := lo.LastObservedAt()
		if last.IsZero() {
			return false
		}
		return o.now().Sub(last) <= CometMaxAge
	}
	if so, ok := o.tip.(interface{ Stale() bool }); ok {
		return !so.Stale()
	}
	return true
}

func (o *Oracle) Latest(ctx context.Context) (*blocks.Header, error) {
	if o == nil {
		return nil, errors.New("blockoracle/failover: no tip")
	}
	now := o.now()

	o.mu.Lock()
	cometOn := o.cometConnected
	o.mu.Unlock()
	if cometOn && o.tip != nil && o.cometFresh() {
		h, err := o.tip.Latest(ctx)
		logLatestTry("comet_cache", h, err)
		if err == nil && usable(h) {
			o.noteLatest(h, true)
			return h, nil
		}
	} else if !cometOn {
		logLatestTry("comet_cache", nil, errors.New("disconnected"))
	} else if o.tip == nil {
		logLatestTry("comet_cache", nil, errors.New("not wired"))
	} else {
		logLatestTry("comet_cache", nil, errors.New("stale"))
	}

	var nmErr error
	if h, err, skipped := o.tryNM(ctx, now); skipped {
		logLatestTry("get_block_header", nil, err)
	} else if err == nil && usable(h) {
		o.noteLatest(h, true)
		return h, nil
	} else {
		nmErr = err
		logLatestTry("get_block_header", h, err)
	}

	if o.chain != nil {
		if h, err := o.tryChain(ctx, now); err == nil && usable(h) {
			o.noteLatest(h, true)
			return h, nil
		} else if err != nil {
			o.noteLatest(nil, false)
			return nil, err
		}
	} else {
		logLatestTry("get_latest_block", nil, errors.New("not wired"))
	}

	o.noteLatest(nil, false)
	if nmErr != nil {
		return nil, nmErr
	}
	return nil, errors.New("blockoracle/failover: no tip")
}

func (o *Oracle) tryNM(ctx context.Context, now time.Time) (h *blocks.Header, err error, skipped bool) {
	if blocked, skipErr := o.nmBlocked(now); blocked {
		return nil, skipErr, true
	}

	h, err = o.nm.Latest(ctx)
	o.noteNMOutcome(now, h, err)
	return h, err, false
}

func (o *Oracle) nmBlocked(now time.Time) (bool, error) {
	if o.nm == nil {
		return true, errors.New("not wired")
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if !o.nmNextTry.IsZero() && now.Before(o.nmNextTry) {
		if o.nmUnimpl {
			return true, blocks.ErrHeaderRPCUnimplemented
		}
		return true, errors.New("nm backoff")
	}
	return false, nil
}

func (o *Oracle) noteNMOutcome(now time.Time, h *blocks.Header, err error) {
	if err == nil && usable(h) {
		recovered := false
		o.mu.Lock()
		recovered = o.nmUnimpl
		o.nmUnimpl = false
		o.nmFailures = 0
		o.nmNextTry = time.Time{}
		o.mu.Unlock()
		if recovered {
			logging.Info("heightsync: GetBlockHeader recovered", "heightsync")
		}
		return
	}
	if err != nil && permanentNMSkip(err) {
		entered := false
		o.mu.Lock()
		entered = !o.nmUnimpl
		o.nmUnimpl = true
		o.nmNextTry = now.Add(NMUnimplementedReprobe)
		o.mu.Unlock()
		if entered {
			logging.Warn("heightsync: GetBlockHeader unimplemented; retrying every 15m", "heightsync",
				"retry", NMUnimplementedReprobe.String())
		}
		return
	}
	o.mu.Lock()
	o.nmFailures++
	o.nmNextTry = now.Add(nmBackoffFor(o.nmFailures))
	o.mu.Unlock()
}

func nmBackoffFor(failures int) time.Duration {
	if failures <= 0 {
		return nmBackoffSchedule[0]
	}
	idx := failures - 1
	if idx >= len(nmBackoffSchedule) {
		idx = len(nmBackoffSchedule) - 1
	}
	return nmBackoffSchedule[idx]
}

func permanentNMSkip(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, blocks.ErrHeaderRPCUnimplemented) {
		return true
	}
	if st, ok := status.FromError(err); ok && st.Code() == codes.Unimplemented {
		return true
	}
	return false
}

func transientNM(err error) bool {
	if err == nil {
		return false
	}
	if st, ok := status.FromError(err); ok {
		switch st.Code() {
		case codes.Unavailable, codes.DeadlineExceeded, codes.ResourceExhausted:
			return true
		}
	}
	text := strings.ToLower(err.Error())
	return strings.Contains(text, "connection reset") ||
		strings.Contains(text, "connection refused") ||
		strings.Contains(text, "transport is closing") ||
		strings.Contains(text, "i/o timeout") ||
		strings.Contains(text, "io exception")
}

func (o *Oracle) tryChain(ctx context.Context, now time.Time) (*blocks.Header, error) {
	o.mu.Lock()
	if o.chainLast != nil && !o.chainLastAt.IsZero() && now.Sub(o.chainLastAt) < ChainRefresh {
		h := cloneHeader(o.chainLast)
		o.mu.Unlock()
		logLatestTry("get_latest_block_cached", h, nil)
		return h, nil
	}
	o.mu.Unlock()

	h, err := o.chain.Latest(ctx)
	logLatestTry("get_latest_block", h, err)
	if err != nil || !usable(h) {
		if err != nil {
			return nil, err
		}
		return nil, errors.New("blockoracle/failover: no tip")
	}
	o.mu.Lock()
	o.chainLast = cloneHeader(h)
	o.chainLastAt = now
	o.mu.Unlock()
	return h, nil
}

func logLatestTry(source string, h *blocks.Header, err error) {
	if err == nil && usable(h) {
		logging.Debug("heightsync: latest try", "heightsync",
			"source", source,
			"ok", true,
			"height", h.Height,
			"hash_len", len(h.BlockHash),
			"chain_id", h.ChainID)
		return
	}
	kvs := []any{"source", source, "ok", false}
	switch {
	case err != nil:
		kvs = append(kvs, "error", err.Error())
	case h == nil:
		kvs = append(kvs, "error", "nil header")
	default:
		kvs = append(kvs, "error", "unusable header", "height", h.Height, "hash_len", len(h.BlockHash))
	}
	logging.Debug("heightsync: latest try", "heightsync", kvs...)
}

func (o *Oracle) At(ctx context.Context, height int64) (*blocks.Header, error) {
	if o == nil || height <= 0 {
		return blocks.DummyHeader(height), nil
	}
	if h := o.fromWindow(ctx, height); h != nil {
		return h, nil
	}
	now := o.now()
	if o.atMissCached(height, now) {
		logAtTry("negative_cache", height, nil, errors.New("cached miss"))
		return blocks.DummyHeader(height), nil
	}
	if tip := o.knownTipHeight(ctx); !withinAtLookback(tip, height) {
		logAtTry("lookback", height, nil, errors.New("outside lookback"))
		return blocks.DummyHeader(height), nil
	}

	v, err, _ := o.atFlight.Do(strconv.FormatInt(height, 10), func() (any, error) {
		if h := o.fromWindow(ctx, height); h != nil {
			return h, nil
		}
		now := o.now()
		if o.atMissCached(height, now) {
			return blocks.DummyHeader(height), nil
		}
		if tip := o.knownTipHeight(ctx); !withinAtLookback(tip, height) {
			return blocks.DummyHeader(height), nil
		}
		return o.atFetch(ctx, height, now)
	})
	if err != nil {
		return nil, err
	}
	h, _ := v.(*blocks.Header)
	if !usable(h) {
		return blocks.DummyHeader(height), nil
	}
	return cloneHeader(h), nil
}

func (o *Oracle) atFetch(ctx context.Context, height int64, now time.Time) (*blocks.Header, error) {
	if blocked, skipErr := o.nmBlocked(now); blocked {
		logAtTry("get_block_header", height, nil, skipErr)
	} else {
		h, err := o.nm.At(ctx, height)
		if err == nil && usable(h) && h.Height == height {
			o.noteNMOutcome(now, h, nil)
			o.remember(h)
			logAtTry("get_block_header", height, h, nil)
			return h, nil
		}
		// Per-height miss (not found / dummy / wrong height) is negative-
		// cached below. Do not put the whole GetBlockHeader backend into
		// Latest backoff for one historical At(). Transport death and
		// Unimplemented still go through noteNMOutcome.
		if err != nil && (permanentNMSkip(err) || transientNM(err)) {
			o.noteNMOutcome(now, nil, err)
		}
		logAtTry("get_block_header", height, h, err)
	}

	if o.chain != nil {
		h, err := o.chain.At(ctx, height)
		logAtTry("get_block_by_height", height, h, err)
		if err == nil && usable(h) && h.Height == height {
			o.remember(h)
			return h, nil
		}
		if err != nil && o.nm == nil {
			o.rememberAtMiss(height, now)
			return nil, err
		}
	}

	o.rememberAtMiss(height, now)
	return blocks.DummyHeader(height), nil
}

func (o *Oracle) knownTipHeight(ctx context.Context) int64 {
	if o.tip != nil {
		if h, err := o.tip.Latest(ctx); err == nil && usable(h) && h.Height > 0 {
			return h.Height
		}
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.lastServed != nil && o.lastServed.Height > 0 {
		return o.lastServed.Height
	}
	if o.chainLast != nil && o.chainLast.Height > 0 {
		return o.chainLast.Height
	}
	return 0
}

func withinAtLookback(tip, height int64) bool {
	if height <= 0 {
		return false
	}
	if tip <= 0 {
		return true
	}
	if height > tip {
		return false
	}
	floor := tip - (AtLookback - 1)
	if floor < 1 {
		floor = 1
	}
	return height >= floor
}

func (o *Oracle) atMissCached(height int64, now time.Time) bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	t, ok := o.atMiss[height]
	if !ok {
		return false
	}
	if now.Sub(t) >= AtNegativeTTL {
		delete(o.atMiss, height)
		return false
	}
	return true
}

func (o *Oracle) rememberAtMiss(height int64, now time.Time) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.atMiss == nil {
		o.atMiss = make(map[int64]time.Time)
	}
	for h, t := range o.atMiss {
		if now.Sub(t) >= AtNegativeTTL {
			delete(o.atMiss, h)
		}
	}
	if len(o.atMiss) >= atMissCap {
		var oldest int64
		var oldestT time.Time
		first := true
		for h, t := range o.atMiss {
			if first || t.Before(oldestT) {
				oldest, oldestT, first = h, t, false
			}
		}
		delete(o.atMiss, oldest)
	}
	o.atMiss[height] = now
}

func logAtTry(source string, height int64, h *blocks.Header, err error) {
	if err == nil && usable(h) {
		logging.Debug("heightsync: at try", "heightsync",
			"source", source,
			"ok", true,
			"height", h.Height,
			"hash_len", len(h.BlockHash),
			"chain_id", h.ChainID)
		return
	}
	kvs := []any{"source", source, "ok", false, "height", height}
	switch {
	case err != nil:
		kvs = append(kvs, "error", err.Error())
	case h == nil:
		kvs = append(kvs, "error", "nil header")
	default:
		kvs = append(kvs, "error", "unusable header", "got_height", h.Height, "hash_len", len(h.BlockHash))
	}
	logging.Debug("heightsync: at try", "heightsync", kvs...)
}

func (o *Oracle) fromWindow(ctx context.Context, height int64) *blocks.Header {
	if o.tip == nil {
		return nil
	}
	h, err := o.tip.At(ctx, height)
	if err != nil || !usable(h) || h.Height != height {
		return nil
	}
	return h
}

func (o *Oracle) remember(h *blocks.Header) {
	if !usable(h) {
		return
	}
	if r, ok := o.tip.(interface{ Remember(*blocks.Header) }); ok {
		r.Remember(h)
	}
}

func (o *Oracle) Prove(ctx context.Context, path string, height int64) (*blocks.Proof, error) {
	if o == nil || o.nm == nil {
		return nil, blocks.ErrProveNotImplemented
	}
	p, err := o.nm.Prove(ctx, path, height)
	if err != nil {
		if errors.Is(err, blocks.ErrProveNotImplemented) || errors.Is(err, blocks.ErrHeaderNotFound) {
			return nil, blocks.ErrProveNotImplemented
		}
		return nil, err
	}
	return p, nil
}

func (o *Oracle) Subscribe(ctx context.Context, fromHeight int64) (<-chan *blocks.Header, error) {
	if o == nil || o.tip == nil {
		ch := make(chan *blocks.Header)
		close(ch)
		return ch, nil
	}
	return o.tip.Subscribe(ctx, fromHeight)
}

func (o *Oracle) noteLatest(h *blocks.Header, ok bool) {
	if o == nil {
		return
	}
	now := o.now()
	o.mu.Lock()
	o.fetched = true
	o.lastOK = ok
	if ok && usable(h) {
		o.lastServed = cloneHeader(h)
		o.lastServedAt = now
	}
	o.mu.Unlock()
}

// StaleDetails is cached-only decide debug. It does not call Oracle.Latest,
// GetBlockHeader, or GetLatestBlock.
func (o *Oracle) StaleDetails() (stale bool, lastRecvAgeMs int64, latestHeight int64, neverReceived bool) {
	if o == nil {
		return true, 0, 0, true
	}
	stale = o.Stale()
	now := o.now()

	var lastAt time.Time
	if lo, ok := o.tip.(interface{ LastObservedAt() time.Time }); ok {
		lastAt = lo.LastObservedAt()
	}

	o.mu.Lock()
	served := o.lastServed
	servedAt := o.lastServedAt
	o.mu.Unlock()

	if served != nil {
		latestHeight = served.Height
	} else if o.cometFresh() && o.tip != nil {
		if h, err := o.tip.Latest(context.Background()); err == nil && usable(h) {
			latestHeight = h.Height
		}
	}

	recv := lastAt
	if recv.IsZero() {
		recv = servedAt
	}
	if recv.IsZero() && served == nil && latestHeight == 0 {
		return stale, 0, 0, true
	}
	if !recv.IsZero() {
		age := now.Sub(recv)
		if age < 0 {
			age = 0
		}
		lastRecvAgeMs = age.Milliseconds()
	}
	return stale, lastRecvAgeMs, latestHeight, false
}

// Stale is true when Latest() has been attempted and no live backend can
// serve: Comet is down or older than CometMaxAge, dapi is in backoff or
// failed, and chain has no header inside ChainRefresh.
func (o *Oracle) Stale() bool {
	if o == nil {
		return true
	}
	o.mu.Lock()
	cometOn := o.cometConnected
	o.mu.Unlock()
	if cometOn && o.cometFresh() {
		return false
	}
	now := o.now()
	o.mu.Lock()
	chainFresh := o.chainLast != nil && !o.chainLastAt.IsZero() && now.Sub(o.chainLastAt) < ChainRefresh
	fetched, lastOK := o.fetched, o.lastOK
	o.mu.Unlock()
	if chainFresh {
		return false
	}
	if !fetched {
		return false
	}
	return !lastOK
}

// Legacy is kept for tests that distinguished old dapi. Tip no longer
// depends on dapi HTTP, so this is always false.
func (o *Oracle) Legacy() bool {
	return false
}
