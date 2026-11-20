package observer

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"common/chainoracle/blocks"
	"common/chainoracle/blocks/tipcache"
	"common/logging"

	rpchttp "github.com/cometbft/cometbft/rpc/client/http"
	ctypes "github.com/cometbft/cometbft/rpc/core/types"
	cmttypes "github.com/cometbft/cometbft/types"
)

const (
	MaxAtWait           = 30 * time.Second
	atWaitPoll          = 500 * time.Millisecond
	maxFillPerCall      = 20_000
	maxTipGapFill       = 1000
	minLinkBudget       = 5 * time.Second
	DefaultRPCTimeout   = 10 * time.Second
	historyLoadTimeout  = 30 * time.Second
	storeWriteQueue     = 256
	storePruneEvery     = 1024
	historyStoreTimeout = 10 * time.Second
)

// TendermintConfig pins the hash-only oracle to a CometBFT RPC endpoint.
// PollPeriod is ignored: tip motion is Observe (the existing dapi NewBlock
// listener), not a 1s Block poll.
type TendermintConfig struct {
	ChainID    string
	RPCURL     string
	PollPeriod string // ignored; kept so callers do not have to drop the field

	VerifyCommits  bool
	TrustingPeriod time.Duration
	Store          HeaderStore
	HistoryHeights int64
	ResetTrust     bool
	// RPCTimeout bounds one HTTP call. Zero uses DefaultRPCTimeout.
	RPCTimeout time.Duration
}

type headerRPC interface {
	Header(ctx context.Context, height *int64) (*ctypes.ResultHeader, error)
}

// Oracle is a hash-only BlockOracle. Tip is pushed via Observe (Comet
// NewBlock). At() is the last tipcache.HistoryWindow heights, then Comet
// Header RPC (LoadBlockMeta — not Block / LoadBlock).
type Oracle struct {
	rpc      headerRPC
	cache    *tipcache.Cache
	history  *History
	verifier *lightVerifier
	store    HeaderStore

	writes  chan []StoredHeader
	verifyQ chan int64
	linkQ   chan struct{}
	fillQ   chan struct{}
	fillMu  sync.Mutex

	chainID    string
	now        func() time.Time
	linkBudget time.Duration
	pendMu     sync.Mutex
	pending    map[int64]pendingHeader
	aheadLog   sync.Map

	hookMu    sync.RWMutex
	publish   func(h *blocks.Header, fromDAPI bool)
	pendFloor func() int64
	onFailure func(error)
	failures  atomic.Uint64
	// publishedTrusted is the last trusted height remember published.
	publishedTrusted atomic.Int64
}

type oracleOptions struct {
	chainID        string
	verify         bool
	trustingPeriod time.Duration
	store          HeaderStore
	historyHeights int64
	resetTrust     bool
}

// NewTendermint builds an Oracle against cfg.RPCURL. It does not start a
// poll loop. Cancel is not required; stop feeding Observe when shutting down.
func NewTendermint(cfg TendermintConfig) (*Oracle, error) {
	if cfg.RPCURL == "" {
		return nil, errors.New("observer: empty RPC URL")
	}
	rpc, err := rpchttp.NewWithTimeout(cfg.RPCURL, "/websocket", rpcTimeoutSeconds(cfg.RPCTimeout))
	if err != nil {
		return nil, fmt.Errorf("observer: tendermint rpc: %w", err)
	}
	return newOracleWith(rpc, oracleOptions{
		chainID:        cfg.ChainID,
		verify:         cfg.VerifyCommits,
		trustingPeriod: cfg.TrustingPeriod,
		store:          cfg.Store,
		historyHeights: cfg.HistoryHeights,
		resetTrust:     cfg.ResetTrust,
	}), nil
}

func rpcTimeoutSeconds(d time.Duration) uint {
	if d <= 0 {
		d = DefaultRPCTimeout
	}
	sec := uint(d / time.Second)
	if time.Duration(sec)*time.Second < d {
		sec++
	}
	if sec == 0 {
		return 1
	}
	return sec
}

func newOracle(rpc headerRPC) *Oracle {
	return newOracleWith(rpc, oracleOptions{})
}

func newOracleWith(rpc headerRPC, opts oracleOptions) *Oracle {
	o := &Oracle{
		rpc:     rpc,
		cache:   tipcache.New(0),
		history: NewHistory(opts.historyHeights),
		store:   opts.store,
		chainID: opts.chainID,
		pending: make(map[int64]pendingHeader),
	}
	if opts.verify {
		if lrpc, ok := rpc.(lightRPC); ok {
			o.verifier = newLightVerifier(lrpc, opts.trustingPeriod)
		}
	}
	if o.store != nil {
		o.writes = make(chan []StoredHeader, storeWriteQueue)
		go o.runStoreWriter()
		if o.verifier == nil {
			o.loadFromStore(nil)
		} else {
			o.verifier.loadTrust = o.store.LoadTrust
			o.verifier.storeTrust = o.store.SaveTrust
			o.verifier.resetTrust = opts.resetTrust
			o.verifier.onEpoch = func(epoch int64) { o.loadFromStore(&epoch) }
			o.verifier.saveTrust = func(tp TrustPoint) {
				ctx, cancel := context.WithTimeout(context.Background(), historyStoreTimeout)
				defer cancel()
				if err := o.store.SaveTrust(ctx, tp); err != nil {
					logging.Warn("chainoracle: save trust point", "chainoracle", "error", err, "height", tp.Height)
				}
			}
		}
	}
	if o.verifier != nil {
		if o.store != nil {
			ctx, cancel := context.WithTimeout(context.Background(), historyLoadTimeout)
			if err := o.verifier.prepareTrust(ctx); err != nil {
				logging.Warn("chainoracle: trust point not read yet; verification waits", "chainoracle", "error", err)
			}
			cancel()
		}
		o.verifyQ = make(chan int64, 1)
		o.linkQ = make(chan struct{}, 1)
		o.fillQ = make(chan struct{}, 1)
		go o.runVerifier()
	}
	return o
}

func (o *Oracle) Verifying() bool { return o != nil && o.verifier != nil }

func (o *Oracle) loadFromStore(epoch *int64) {
	ctx, cancel := context.WithTimeout(context.Background(), historyLoadTimeout)
	defer cancel()
	top, err := o.store.MaxHeight(ctx)
	if err != nil {
		logging.Warn("chainoracle: load header history", "chainoracle", "error", err)
		return
	}
	from := top - o.history.window + 1
	if from < 1 {
		from = 1
	}
	rows, err := o.store.LoadHeaders(ctx, from)
	if err != nil {
		logging.Warn("chainoracle: load header history", "chainoracle", "error", err)
		return
	}
	loaded := 0
	for _, r := range rows {
		if epoch != nil && (!r.Verified || r.Epoch != *epoch) {
			continue
		}
		if o.history.PutStored(r) {
			loaded++
		}
	}
	logging.Info("chainoracle: header history loaded", "chainoracle", "headers", loaded, "stored", len(rows), "top", top)
}

func (o *Oracle) runStoreWriter() {
	var sinceprune int
	for batch := range o.writes {
		ctx, cancel := context.WithTimeout(context.Background(), historyStoreTimeout)
		if err := o.store.SaveHeaders(ctx, batch); err != nil {
			logging.Warn("chainoracle: save headers", "chainoracle", "error", err, "count", len(batch))
		}
		sinceprune += len(batch)
		if sinceprune >= storePruneEvery {
			sinceprune = 0
			if floor := o.history.Top() - o.history.window; floor > 0 {
				if err := o.store.PruneHeaders(ctx, floor); err != nil {
					logging.Warn("chainoracle: prune headers", "chainoracle", "error", err)
				}
			}
		}
		cancel()
	}
}

func (o *Oracle) remember(rows ...StoredHeader) {
	var kept []StoredHeader
	link := false
	for _, r := range rows {
		if r.Verified && o.verifier != nil {
			r.Epoch = o.verifier.epoch.Load()
		}
		if o.history.PutStored(r) {
			kept = append(kept, r)
			o.aheadLog.Delete(r.Height)
			if o.pendingHas(r.Height - 1) {
				link = true
			}
		}
	}
	o.publishRows(kept)
	if link {
		o.queueLink()
	}
	if o.writes == nil || len(kept) == 0 {
		return
	}
	select {
	case o.writes <- kept:
	default:
		logging.Warn("chainoracle: header store queue full", "chainoracle", "dropped", len(kept))
	}
}

// rememberTrusted publishes the trusted header only when its height advances.
func (o *Oracle) rememberTrusted(hdr *cmttypes.Header) {
	if hdr == nil || hdr.Height <= 0 {
		return
	}
	for {
		prev := o.publishedTrusted.Load()
		if hdr.Height <= prev {
			return
		}
		if o.publishedTrusted.CompareAndSwap(prev, hdr.Height) {
			o.remember(verifiedFromHeader(hdr))
			return
		}
	}
}

func (o *Oracle) runVerifier() {
	var lastTop int64
	for {
		select {
		case height := <-o.verifyQ:
			ctx, cancel := context.WithTimeout(context.Background(), historyStoreTimeout)
			ok, tip := o.verifyTip(ctx, height)
			if !ok {
				cancel()
				continue
			}
			o.linkPending()
			if tip {
				if lastTop > 0 && height-lastTop > 1 && height-lastTop <= maxTipGapFill {
					_, _ = o.fillDown(ctx, lastTop+1)
				}
				lastTop = height
			}
			cancel()
		case <-o.linkQ:
			o.linkPending()
		case <-o.fillQ:
			o.fillPendingFromTrusted()
		}
	}
}

// verifyTip checks one queued height. A dapi height below the trusted header
// is not sent to verifiedAt: the trusted header is already its anchor, and
// linkPending walks down to it.
func (o *Oracle) verifyTip(ctx context.Context, height int64) (ok, tip bool) {
	if _, have := o.history.entry(height); have {
		return true, true
	}
	if o.pendingHas(height) {
		trusted, err := o.trust(ctx)
		if err != nil {
			logging.Warn("chainoracle: trusted header for dapi blocks", "chainoracle", "height", height, "error", err)
			return false, false
		}
		if height < trusted {
			return true, false
		}
	}
	if _, err := o.verifiedAt(ctx, height); err != nil {
		if o.logDAPIAhead(height, err) {
			return false, false
		}
		logging.Warn("chainoracle: verify tip", "chainoracle", "height", height, "error", err)
		return false, false
	}
	return true, true
}

func (o *Oracle) queueVerify(height int64) {
	select {
	case o.verifyQ <- height:
	default:
		select {
		case <-o.verifyQ:
		default:
		}
		select {
		case o.verifyQ <- height:
		default:
		}
	}
}

func (o *Oracle) queueLink() {
	if o.linkQ == nil {
		return
	}
	select {
	case o.linkQ <- struct{}{}:
	default:
	}
}

func (o *Oracle) queueFillPending() {
	if o.fillQ == nil {
		return
	}
	select {
	case o.fillQ <- struct{}{}:
	default:
	}
}

// LinkBuffered walks down from the trusted header through the pending
// buffer. The poller calls it on SuppressBlocks and needs_reset.
func (o *Oracle) LinkBuffered() {
	if o == nil || o.verifier == nil {
		return
	}
	o.queueFillPending()
}

func (o *Oracle) fillPendingFromTrusted() {
	from := o.pendingLowest()
	if from <= 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), historyStoreTimeout)
	defer cancel()
	if _, err := o.fillDown(ctx, from); err != nil {
		logging.Warn("chainoracle: fill pending dapi heights", "chainoracle", "from", from, "error", err)
	}
}

func (o *Oracle) clock() time.Time {
	if o != nil && o.now != nil {
		return o.now()
	}
	return time.Now()
}

func (o *Oracle) pendingLinkBudget() time.Duration {
	if o.linkBudget > 0 {
		return o.linkBudget
	}
	b := 2 * o.headerInterval()
	if b < minLinkBudget {
		return minLinkBudget
	}
	return b
}

func (o *Oracle) headerInterval() time.Duration {
	if o == nil || o.history == nil {
		return 0
	}
	top := o.history.Top()
	if top < 2 {
		return 0
	}
	newer, ok := o.history.Get(top)
	if !ok {
		return 0
	}
	older, ok := o.history.Get(top - 1)
	if !ok {
		return 0
	}
	d := newer.Time.Sub(older.Time)
	if d <= 0 {
		return 0
	}
	return d
}

func (o *Oracle) logDAPIAhead(height int64, err error) bool {
	if o.verifier == nil || height <= o.verifier.trustedHeight() {
		return false
	}
	if !errors.Is(err, blocks.ErrHeaderNotFound) && !isCometHeightErr(err) {
		return false
	}
	if _, loaded := o.aheadLog.LoadOrStore(height, struct{}{}); loaded {
		return true
	}
	logging.Debug("dapi ahead of the RPC", "chainoracle", "height", height)
	return true
}

// Observe records a committed header from the dapi NewBlock listener.
func (o *Oracle) Observe(h *blocks.Header) {
	if o == nil {
		return
	}
	o.cache.Observe(h)
	if h == nil || h.Height <= 0 || blocks.IsDummyHeader(h) {
		return
	}
	if o.verifier == nil {
		o.remember(StoredHeader{Height: h.Height, Hash: h.BlockHash, TimeMs: h.Time.UnixMilli(), ChainID: h.ChainID})
		return
	}
	o.queueVerify(h.Height)
}

// ObserveHex decodes a hex block hash (Comet WS block_id.hash) and Observes.
func (o *Oracle) ObserveHex(height int64, hashHex string, t time.Time, chainID string) error {
	if o == nil {
		return nil
	}
	hash, err := decodeBlockHash(hashHex)
	if err != nil {
		return err
	}
	o.Observe(blocks.HashOnlyHeader(height, t, chainID, hash))
	return nil
}

func (o *Oracle) Latest(ctx context.Context) (*blocks.Header, error) {
	if o == nil {
		return nil, blocks.ErrHeaderNotFound
	}
	h, err := o.cache.Latest(ctx)
	if err == nil && h != nil {
		return h, nil
	}
	return o.fetchHeader(ctx, nil, true)
}

func (o *Oracle) At(ctx context.Context, height int64) (*blocks.Header, error) {
	if o == nil {
		return nil, blocks.ErrHeaderNotFound
	}
	if o.verifier != nil {
		if _, ok := o.history.Get(height); !ok && o.pendingWaiting(height) {
			return nil, ErrHeaderPendingLink
		}
		return o.verifiedAt(ctx, height)
	}
	h, err := o.cache.At(ctx, height)
	if err == nil && h != nil {
		return h, nil
	}
	if h, ok := o.history.Get(height); ok {
		return h, nil
	}
	if h, ok := o.fetchBatch(ctx, height); ok {
		return h, nil
	}
	return o.fetchHeader(ctx, &height, false)
}

func (o *Oracle) AtWait(ctx context.Context, height int64, wait time.Duration) (*blocks.Header, error) {
	if wait > MaxAtWait {
		wait = MaxAtWait
	}
	deadline := time.Now().Add(wait)
	for {
		changed := o.history.Changed()
		h, err := o.At(ctx, height)
		if err == nil || !errors.Is(err, blocks.ErrHeaderNotFound) || !time.Now().Before(deadline) {
			return h, err
		}
		timer := time.NewTimer(min(atWaitPoll, time.Until(deadline)))
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, err
		case <-changed:
			timer.Stop()
		case <-timer.C:
		}
	}
}

func (o *Oracle) verifiedAt(ctx context.Context, height int64) (*blocks.Header, error) {
	if height <= 0 {
		return nil, blocks.ErrHeaderNotFound
	}
	if h, ok := o.history.Get(height); ok {
		return h, nil
	}
	trusted, err := o.verifier.ensureTrusted(ctx)
	if err != nil {
		return nil, err
	}
	o.rememberTrusted(trusted)
	if height >= trusted.Height {
		hdr, err := o.verifier.verifyForward(ctx, height)
		if err == nil {
			row := verifiedFromHeader(hdr)
			o.remember(row)
			return o.headerFromRow(row), nil
		}
		if !errors.Is(err, errBelowTrusted) {
			return nil, err
		}
	}
	return o.fillDown(ctx, height)
}

func (o *Oracle) headerFromRow(r StoredHeader) *blocks.Header {
	chainID := r.ChainID
	if chainID == "" {
		chainID = o.history.ChainID()
	}
	return blocks.HashOnlyHeader(r.Height, time.UnixMilli(r.TimeMs).UTC(), chainID, r.Hash)
}

func (o *Oracle) fillDown(ctx context.Context, height int64) (*blocks.Header, error) {
	o.fillMu.Lock()
	defer o.fillMu.Unlock()
	if h, ok := o.history.Get(height); ok {
		return h, nil
	}
	nextHeight, next, ok := o.history.lowestLinkedAbove(height)
	if !ok {
		return nil, blocks.ErrHeaderNotFound
	}
	chainID := o.history.ChainID()
	filled := 0
	for filled < maxFillPerCall {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		cur := nextHeight - 1
		if p, ok := o.pendingAt(cur); ok {
			if err := verifyBackwardLink(p.hdr, next, nextHeight, chainID); err != nil {
				o.breakAt(cur, err)
			} else {
				row := verifiedFromHeader(p.hdr)
				row.fromDAPI = true
				o.remember(row)
				o.dropPending(cur)
				filled++
				next = historyEntry{hash: row.Hash, prevHash: row.PrevHash, timeMs: row.TimeMs}
				nextHeight = cur
				if cur == height {
					return o.headerFromRow(row), nil
				}
				continue
			}
		}
		rpc, ok := o.rpc.(blockchainInfoRPC)
		if !ok {
			return nil, blocks.ErrHeaderNotFound
		}
		lo := o.missingFrom(max(height, cur-BlockchainInfoBatch+1), cur)
		res, err := rpc.BlockchainInfo(ctx, lo, cur)
		if err != nil {
			if isCometHeightErr(err) {
				return nil, blocks.ErrHeaderNotFound
			}
			return nil, fmt.Errorf("observer: blockchain info rpc: %w", err)
		}
		byHeight := make(map[int64]*cmttypes.Header, BlockchainInfoBatch)
		for _, h := range headersFromBlockchainInfo(res) {
			byHeight[h.Height] = h
		}
		var rows []StoredHeader
		for cur := nextHeight - 1; cur >= lo; cur-- {
			hdr := byHeight[cur]
			if err := verifyBackwardLink(hdr, next, cur+1, chainID); err != nil {
				o.remember(rows...)
				return nil, err
			}
			row := verifiedFromHeader(hdr)
			rows = append(rows, row)
			next = historyEntry{hash: row.Hash, prevHash: row.PrevHash, timeMs: row.TimeMs}
			nextHeight = cur
		}
		o.remember(rows...)
		filled += len(rows)
		if nextHeight == height && len(rows) > 0 {
			return o.headerFromRow(rows[len(rows)-1]), nil
		}
	}
	return nil, blocks.ErrHeaderNotFound
}

func (o *Oracle) fetchBatch(ctx context.Context, height int64) (*blocks.Header, bool) {
	rpc, ok := o.rpc.(blockchainInfoRPC)
	if !ok || height <= 0 {
		return nil, false
	}
	res, err := rpc.BlockchainInfo(ctx, height, height+BlockchainInfoBatch-1)
	if err != nil {
		return nil, false
	}
	hdrs := headersFromBlockchainInfo(res)
	var rows []StoredHeader
	for _, h := range hdrs {
		rows = append(rows, storedFromHeader(h))
	}
	o.remember(rows...)
	return o.history.Get(height)
}

func (o *Oracle) fetchHeader(ctx context.Context, height *int64, observe bool) (*blocks.Header, error) {
	res, err := o.rpc.Header(ctx, height)
	if err != nil {
		if isCometHeightErr(err) {
			return nil, blocks.ErrHeaderNotFound
		}
		return nil, fmt.Errorf("observer: header rpc: %w", err)
	}
	hdr, err := HeaderFromResultHeader(res)
	if err != nil {
		return nil, err
	}
	if observe {
		o.cache.Observe(hdr)
	} else {
		o.cache.Remember(hdr)
	}
	if o.verifier == nil && res.Header != nil {
		o.remember(storedFromHeader(res.Header))
	}
	return hdr, nil
}

// Header reads one height with Comet /header.
func (o *Oracle) Header(ctx context.Context, height int64) (*blocks.Header, error) {
	if o == nil || height <= 0 {
		return nil, blocks.ErrHeaderNotFound
	}
	return o.fetchHeader(ctx, &height, false)
}

// BlockchainInfo reads [from, to] with one BlockchainInfo call, capped at
// BlockchainInfoBatch. It does not call Block or GetBlockHeader.
func (o *Oracle) BlockchainInfo(ctx context.Context, from, to int64) ([]*blocks.Header, error) {
	if o == nil || from <= 0 || to < from {
		return nil, nil
	}
	if to-from+1 > BlockchainInfoBatch {
		to = from + BlockchainInfoBatch - 1
	}
	rpc, ok := o.rpc.(blockchainInfoRPC)
	if !ok {
		return nil, blocks.ErrHeaderNotFound
	}
	res, err := rpc.BlockchainInfo(ctx, from, to)
	if err != nil {
		return nil, err
	}
	hdrs := headersFromBlockchainInfo(res)
	out := make([]*blocks.Header, 0, len(hdrs))
	var rows []StoredHeader
	for _, h := range hdrs {
		row := storedFromHeader(h)
		rows = append(rows, row)
		out = append(out, o.headerFromRow(row))
	}
	if o.verifier == nil {
		o.remember(rows...)
	}
	return out, nil
}

func (o *Oracle) Prove(context.Context, string, int64) (*blocks.Proof, error) {
	return nil, blocks.ErrProveNotImplemented
}

func (o *Oracle) Subscribe(ctx context.Context, fromHeight int64) (<-chan *blocks.Header, error) {
	if o == nil {
		ch := make(chan *blocks.Header)
		close(ch)
		return ch, nil
	}
	return o.cache.Subscribe(ctx, fromHeight)
}

func decodeBlockHash(raw string) ([]byte, error) {
	s := strings.TrimSpace(raw)
	s = strings.TrimPrefix(s, "0x")
	s = strings.TrimPrefix(s, "0X")
	if s == "" {
		return nil, fmt.Errorf("observer: empty block hash")
	}
	b, err := hex.DecodeString(s)
	if err != nil {
		return nil, fmt.Errorf("observer: block hash: %w", err)
	}
	return b, nil
}

func isCometHeightErr(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "is not available"):
		return true
	case strings.Contains(msg, "must be less than or equal"):
		return true
	case strings.Contains(msg, "height must be greater"):
		return true
	case strings.Contains(msg, "can't be greater than max height"):
		return true
	default:
		return false
	}
}

var (
	_ blocks.BlockOracle = (*Oracle)(nil)
	_ Observer           = (*Oracle)(nil)
)
