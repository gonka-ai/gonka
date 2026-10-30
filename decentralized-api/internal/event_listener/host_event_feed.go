package event_listener

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"common/logging"
	"decentralized-api/apiconfig"

	abcitypes "github.com/cometbft/cometbft/abci/types"
	coretypes "github.com/cometbft/cometbft/rpc/core/types"
	cmttypes "github.com/cometbft/cometbft/types"
	gogoproto "github.com/cosmos/gogoproto/proto"
	"github.com/productscience/inference/x/inference/types"
)

const (
	blockResultsCacheSize     = 8
	escrowLookupBudget        = 2 * time.Second
	escrowLookupParallel      = 8
	hostEventRetryMin         = 200 * time.Millisecond
	hostEventRetryMax         = 5 * time.Second
	hostEventBootTimeout      = 30 * time.Second
	hostEventChainFailRetries = 3
	hostEventEarliestRefresh  = 30 * time.Second
	hostEventHeaderBatch      = 20
	blockModeBegin            = "BeginBlock"
	blockModeEnd              = "EndBlock"
)

// blockResultsCache shares recent BlockResults between the handler walk and
// the host-event feed. A height in flight is fetched once.
type blockResultsCache struct {
	client   TmHTTPClient
	capacity int

	mu       sync.Mutex
	order    []int64
	items    map[int64]*coretypes.ResultBlockResults
	inflight map[int64]*blockResultsCall
}

type blockResultsCall struct {
	done chan struct{}
	res  *coretypes.ResultBlockResults
	err  error
}

func newBlockResultsCache(client TmHTTPClient, capacity int) *blockResultsCache {
	if capacity <= 0 {
		capacity = blockResultsCacheSize
	}
	return &blockResultsCache{
		client:   client,
		capacity: capacity,
		items:    make(map[int64]*coretypes.ResultBlockResults),
		inflight: make(map[int64]*blockResultsCall),
	}
}

func (c *blockResultsCache) Get(ctx context.Context, height int64) (*coretypes.ResultBlockResults, error) {
	if c == nil || c.client == nil {
		return nil, fmt.Errorf("block results: no client")
	}
	c.mu.Lock()
	if res, ok := c.items[height]; ok {
		c.mu.Unlock()
		return res, nil
	}
	if call, ok := c.inflight[height]; ok {
		c.mu.Unlock()
		select {
		case <-call.done:
			return call.res, call.err
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	call := &blockResultsCall{done: make(chan struct{})}
	c.inflight[height] = call
	c.mu.Unlock()

	res, err := c.client.BlockResults(ctx, &height)
	if err == nil && res == nil {
		err = fmt.Errorf("block results: empty response")
	}

	c.mu.Lock()
	if err == nil {
		c.storeLocked(height, res)
	}
	call.res, call.err = res, err
	delete(c.inflight, height)
	close(call.done)
	c.mu.Unlock()
	return res, err
}

func (c *blockResultsCache) storeLocked(height int64, res *coretypes.ResultBlockResults) {
	if _, ok := c.items[height]; ok {
		return
	}
	c.items[height] = res
	c.order = append(c.order, height)
	for len(c.order) > c.capacity {
		drop := c.order[0]
		c.order = c.order[1:]
		delete(c.items, drop)
	}
}

// epochSource labels a block height with its epoch. LatestEpoch is the
// current epoch. EpochStart loads an older epoch's poc start height.
type epochSource interface {
	LatestEpoch() (index uint64, pocStart int64, ok bool)
	EpochStart(ctx context.Context, index uint64) (pocStart int64, err error)
}

func (el *EventListener) latestEpoch() (uint64, int64, bool) {
	if el == nil || el.phaseTracker == nil {
		return 0, 0, false
	}
	st := el.phaseTracker.GetCurrentEpochState()
	if st == nil {
		return 0, 0, false
	}
	return st.LatestEpoch.EpochIndex, st.LatestEpoch.PocStartBlockHeight, true
}

func (el *EventListener) epochStart(ctx context.Context, index uint64) (int64, error) {
	qc := el.transactionRecorder.NewInferenceQueryClient()
	if qc == nil {
		return 0, fmt.Errorf("epoch %d: no query client", index)
	}
	resp, err := qc.EpochGroupData(ctx, &types.QueryGetEpochGroupDataRequest{
		EpochIndex: index,
		ModelId:    "",
	})
	if err != nil {
		return 0, err
	}
	if resp == nil {
		return 0, fmt.Errorf("epoch %d: empty response", index)
	}
	return int64(resp.EpochGroupData.PocStartBlockHeight), nil
}

type epochBook struct {
	src    epochSource
	mu     sync.Mutex
	starts map[uint64]int64
}

func (b *epochBook) start(ctx context.Context, index uint64) (int64, error) {
	if st, found := b.get(index); found {
		return st, nil
	}
	st, err := b.src.EpochStart(ctx, index)
	if err != nil {
		return 0, fmt.Errorf("epoch %d: %w", index, err)
	}
	b.put(index, st)
	return st, nil
}

func (b *epochBook) label(ctx context.Context, height int64) (uint64, error) {
	if b == nil || b.src == nil {
		return 0, fmt.Errorf("epoch label: no source")
	}
	idx, start, ok := b.src.LatestEpoch()
	if !ok {
		return 0, fmt.Errorf("epoch label: latest epoch unknown")
	}
	b.put(idx, start)
	for {
		st, found := b.get(idx)
		if !found {
			var err error
			st, err = b.src.EpochStart(ctx, idx)
			if err != nil {
				return 0, fmt.Errorf("epoch %d: %w", idx, err)
			}
			b.put(idx, st)
		}
		if height >= st {
			return idx, nil
		}
		if idx == 0 {
			return 0, fmt.Errorf("epoch label: height %d is before epoch 0", height)
		}
		idx--
	}
}

func (b *epochBook) put(index uint64, start int64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.starts == nil {
		b.starts = make(map[uint64]int64)
	}
	b.starts[index] = start
}

func (b *epochBook) get(index uint64) (int64, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	st, ok := b.starts[index]
	return st, ok
}

// HostEventFeed is the only writer of the host-event ring. It walks heights
// in order on its own cursor, emittedHeight, and appends each height with
// BLOCK(H) last.
type HostEventFeed struct {
	bo             *BlockObserver
	ring           *apiconfig.HostEventRing
	results        *blockResultsCache
	epochs         *epochBook
	escrow         escrowQuerier
	localAddr      func() string
	notify         chan struct{}
	sem            chan struct{}
	emitted        atomic.Int64
	store          HostEventStore
	writer         string
	markerPath     string
	writeHead      func(headMarker) error
	stalled        atomic.Bool
	bootPending    atomic.Bool
	failHeight     int64
	failCount      int
	pruned         bool
	prunedEpoch    uint64
	built          *builtHeight
	hdrs           map[int64]*cmttypes.Header
	earliestMaxAge time.Duration
	retryMin       time.Duration
	retryMax       time.Duration
}

// builtHeight is one height whose chain reads already succeeded. A failed
// commit retries these rows and does not fetch the height again.
type builtHeight struct {
	height int64
	epoch  uint64
	events []apiconfig.HostEvent
}

func newHostEventFeed(el *EventListener) *HostEventFeed {
	f := &HostEventFeed{
		bo:      el.blockObserver,
		ring:    el.hostEvents,
		results: el.blockObserver.results,
		epochs:  &epochBook{src: listenerEpochSource{el: el}},
		escrow:  el.escrowQuery,
		localAddr: func() string {
			if el == nil {
				return ""
			}
			return el.localParticipantAddress()
		},
		notify: make(chan struct{}, 1),
		sem:    make(chan struct{}, escrowLookupParallel),
	}
	f.store = el.hostStore
	f.markerPath = markerPath(el.hostDataDir)
	f.writeHead = func(m headMarker) error {
		return writeHeadMarkerFile(f.markerPath, m)
	}
	if f.store != nil {
		ctx, cancel := context.WithTimeout(context.Background(), hostEventBootTimeout)
		bootHostEventFeed(ctx, f)
		cancel()
	}
	return f
}

// listenerEpochSource adapts EventListener to epochSource.
type listenerEpochSource struct {
	el *EventListener
}

func (s listenerEpochSource) LatestEpoch() (uint64, int64, bool) {
	return s.el.latestEpoch()
}

func (s listenerEpochSource) EpochStart(ctx context.Context, index uint64) (int64, error) {
	return s.el.epochStart(ctx, index)
}

// Run walks heights when the observer reports a new status. A failed height is
// retried with backoff so a stalled store recovers without waiting for a new block.
func (f *HostEventFeed) Run(ctx context.Context) {
	minWait := f.retryMin
	if minWait <= 0 {
		minWait = hostEventRetryMin
	}
	maxWait := f.retryMax
	if maxWait <= 0 {
		maxWait = hostEventRetryMax
	}
	wait := minWait
	var timer *time.Timer
	var timerC <-chan time.Time
	defer func() {
		if timer != nil {
			timer.Stop()
		}
	}()
	arm := func() {
		if timer == nil {
			timer = time.NewTimer(wait)
		} else {
			timer.Reset(wait)
		}
		timerC = timer.C
		if wait < maxWait {
			wait *= 2
			if wait > maxWait {
				wait = maxWait
			}
		}
	}
	disarm := func() {
		if timer == nil {
			return
		}
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		timerC = nil
		wait = minWait
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-f.notify:
			f.drainNotify()
			disarm()
			if f.walk(ctx) {
				arm()
			}
		case <-timerC:
			timerC = nil
			if f.walk(ctx) {
				arm()
			} else {
				wait = minWait
			}
		}
	}
}

// Stalled reports that the feed is waiting on the store, its writer lock, or
// chain reads for the same height.
func (f *HostEventFeed) Stalled() bool {
	if f == nil {
		return false
	}
	return f.stalled.Load()
}

// EmittedHeight is the last height this feed committed, or 0 before the first one.
func (f *HostEventFeed) EmittedHeight() int64 {
	if f == nil {
		return 0
	}
	return f.emitted.Load()
}

// TipHeight is the observer's current chain height: NewBlock, or a higher
// LatestBlockHeight from the sync checker.
func (f *HostEventFeed) TipHeight() int64 {
	if f == nil || f.bo == nil {
		return 0
	}
	return f.bo.currentBlockHeight.Load()
}

// RingRestored reports that boot has finished with this process's ring.
// False while boot is pending: the ring still holds the process boot nonce,
// and readers take head and generation from the store.
func (f *HostEventFeed) RingRestored() bool {
	if f == nil {
		return false
	}
	return !f.bootPending.Load()
}

// noteChainFail counts consecutive failures of one height. A few failures of
// Status, BlockResults, Header, or the epoch lookup mark the feed stalled.
// The next successful height clears it.
func (f *HostEventFeed) noteChainFail(height int64) {
	if f.failHeight != height {
		f.failHeight = height
		f.failCount = 0
	}
	f.failCount++
	if f.failCount == hostEventChainFailRetries {
		logging.Warn("host_event_feed: chain reads stalled", types.EventProcessing, "height", height, "failures", f.failCount)
	}
	if f.failCount >= hostEventChainFailRetries {
		f.stalled.Store(true)
	}
}

func (f *HostEventFeed) clearChainFail() {
	chainStalled := f.failCount >= hostEventChainFailRetries
	f.failHeight = 0
	f.failCount = 0
	if chainStalled {
		f.stalled.Store(false)
	}
}

func (f *HostEventFeed) drainNotify() {
	for {
		select {
		case <-f.notify:
		default:
			return
		}
	}
}

// walk emits every height after emittedHeight up to the observer tip.
// A fresh feed starts at the tip. A pruned next height, a next height older
// than the previous epoch, or a store that does not match the ring, starts a
// new generation at the tip. An unfinished boot runs again before the first
// append. The 500-block handler jump is not used.
func (f *HostEventFeed) walk(ctx context.Context) bool {
	if f == nil || f.bo == nil || f.ring == nil || !f.bo.caughtUp.Load() {
		return false
	}
	tip := f.bo.currentBlockHeight.Load()
	if tip <= 0 {
		return false
	}
	// Hold the writer lock before Status, EpochGroupData, or boot restore.
	if !f.holdWriter(ctx) {
		return true
	}
	if f.bootPending.Load() {
		bootHostEventFeed(ctx, f)
		if f.bootPending.Load() {
			return true
		}
	}
	if f.emitted.Load() == 0 {
		f.emitted.Store(tip - 1)
	}
	earliest, err := f.earliest(ctx)
	if err != nil {
		logging.Warn("host_event_feed: chain status", types.EventProcessing, "error", err)
		f.noteChainFail(f.emitted.Load() + 1)
		return true
	}
	if earliest > f.emitted.Load()+1 {
		if !f.jumpToTip(ctx, tip) {
			return true
		}
		logging.Info("host_event_feed: pruned heights; new generation at tip", types.EventProcessing,
			"earliest", earliest, "tip", tip, "generation", f.ring.Generation())
	}
	next := f.emitted.Load() + 1
	if next > 0 && next <= tip {
		tooOld, err := f.catchUpTooOld(ctx, next)
		if err != nil {
			logging.Warn("host_event_feed: epoch", types.EventProcessing, "height", next, "error", err)
			f.noteChainFail(next)
			return true
		}
		if tooOld {
			if !f.jumpToTip(ctx, tip) {
				return true
			}
			logging.Info("host_event_feed: catch-up older than the previous epoch; new generation at tip", types.EventProcessing,
				"height", next, "tip", tip, "generation", f.ring.Generation())
		}
	}
	jumped := false
	for {
		if ctx.Err() != nil || !f.bo.caughtUp.Load() {
			return false
		}
		h := f.emitted.Load() + 1
		if h <= 0 || h > f.bo.currentBlockHeight.Load() {
			f.clearChainFail()
			return false
		}
		gen := f.ring.Generation()
		if !f.emitHeight(ctx, h) {
			// Divergence moves the cursor to the tip and bumps the generation.
			// Emit that tip once in this walk. Any other failure retries later.
			if !jumped && f.ring.Generation() != gen {
				jumped = true
				continue
			}
			return true
		}
		jumped = false
		f.emitted.Store(h)
	}
}

func (f *HostEventFeed) jumpToTip(ctx context.Context, tip int64) bool {
	if !f.startNewGeneration(ctx) {
		return false
	}
	f.built = nil
	f.emitted.Store(tip - 1)
	return true
}

// catchUpTooOld reports whether height is older than the previous epoch.
// Catch-up walks only the current epoch and the one before it. A lookup
// error is returned so the walk retries that height instead of jumping.
func (f *HostEventFeed) catchUpTooOld(ctx context.Context, height int64) (bool, error) {
	if f.epochs == nil || f.epochs.src == nil {
		return false, fmt.Errorf("epoch label: no source")
	}
	current, currentStart, ok := f.epochs.src.LatestEpoch()
	if !ok {
		return false, fmt.Errorf("epoch label: latest epoch unknown")
	}
	f.epochs.put(current, currentStart)
	if current == 0 || height >= currentStart {
		return false, nil
	}
	prevStart, err := f.epochs.start(ctx, current-1)
	if err != nil {
		return false, err
	}
	return height < prevStart, nil
}

func (f *HostEventFeed) earliest(ctx context.Context) (int64, error) {
	maxAge := f.earliestMaxAge
	if maxAge <= 0 {
		maxAge = hostEventEarliestRefresh
	}
	if h, ok := f.bo.freshEarliest(maxAge); ok {
		return h, nil
	}
	if f.bo.tmClient == nil {
		return 0, fmt.Errorf("no tendermint client")
	}
	st, err := f.bo.tmClient.Status(ctx)
	if err != nil || st == nil {
		if err == nil {
			err = fmt.Errorf("empty status")
		}
		return 0, err
	}
	h := st.SyncInfo.EarliestBlockHeight
	if h <= 0 {
		h = 1
	}
	f.bo.noteEarliest(h)
	return h, nil
}

func (f *HostEventFeed) emitHeight(ctx context.Context, height int64) bool {
	if !f.holdWriter(ctx) {
		return false
	}
	if f.built != nil && f.built.height != height {
		f.built = nil
	}
	if f.built != nil {
		if !f.commitHeight(ctx, f.built.height, f.built.epoch, f.built.events) {
			return false
		}
		f.built = nil
		f.clearChainFail()
		return true
	}
	res, err := f.blockResults(ctx, height)
	if err != nil || res == nil {
		logging.Warn("host_event_feed: BlockResults", types.EventProcessing, "height", height, "error", err)
		f.noteChainFail(height)
		return false
	}
	if f.bo.tmClient == nil {
		f.noteChainFail(height)
		return false
	}
	hdr, err := f.headerFor(ctx, height, f.bo.currentBlockHeight.Load())
	if err != nil || hdr == nil {
		logging.Warn("host_event_feed: Header", types.EventProcessing, "height", height, "error", err)
		f.noteChainFail(height)
		return false
	}
	epoch, err := f.epochs.label(ctx, height)
	if err != nil {
		logging.Warn("host_event_feed: epoch", types.EventProcessing, "height", height, "error", err)
		f.noteChainFail(height)
		return false
	}
	events := f.eventsForHeight(ctx, res)
	blockEv, err := blockHostEvent(hdr, epoch)
	if err != nil {
		logging.Warn("host_event_feed: block payload", types.EventProcessing, "height", height, "error", err)
		return false
	}
	events = append(events, blockEv)
	f.built = &builtHeight{height: height, epoch: epoch, events: events}
	if !f.commitHeight(ctx, height, epoch, events) {
		return false
	}
	f.built = nil
	f.clearChainFail()
	return true
}

func (f *HostEventFeed) holdWriter(ctx context.Context) bool {
	if f.store == nil {
		return true
	}
	if err := f.store.HoldWriter(ctx); err != nil {
		logging.Warn("host_event_feed: writer lock", types.EventProcessing, "error", err)
		f.noteWriterLost(err)
		return false
	}
	return true
}

// noteWriterLost stalls the feed. Losing the writer lock marks the ring
// unrestored so GetHostEvents takes head and generation from the store.
func (f *HostEventFeed) noteWriterLost(err error) {
	f.stalled.Store(true)
	if errors.Is(err, ErrWriterLocked) {
		f.bootPending.Store(true)
	}
}

func (f *HostEventFeed) headerFor(ctx context.Context, height, tip int64) (*cmttypes.Header, error) {
	if h, ok := f.hdrs[height]; ok {
		delete(f.hdrs, height)
		return h, nil
	}
	if tip > height {
		hi := height + hostEventHeaderBatch - 1
		if hi > tip {
			hi = tip
		}
		if hi > height {
			res, err := f.bo.tmClient.BlockchainInfo(ctx, height, hi)
			if err == nil {
				f.rememberHeaders(res)
				if h, ok := f.hdrs[height]; ok {
					delete(f.hdrs, height)
					return h, nil
				}
			}
		}
	}
	hdrRes, err := f.bo.tmClient.Header(ctx, &height)
	if err != nil || hdrRes == nil || hdrRes.Header == nil {
		if err == nil {
			err = fmt.Errorf("empty header")
		}
		return nil, err
	}
	return hdrRes.Header, nil
}

func (f *HostEventFeed) rememberHeaders(res *coretypes.ResultBlockchainInfo) {
	if res == nil {
		return
	}
	if f.hdrs == nil {
		f.hdrs = make(map[int64]*cmttypes.Header, len(res.BlockMetas))
	}
	for _, meta := range res.BlockMetas {
		if meta == nil || meta.Header.Height == 0 {
			continue
		}
		hdr := meta.Header
		f.hdrs[hdr.Height] = &hdr
	}
}

func (f *HostEventFeed) blockResults(ctx context.Context, height int64) (*coretypes.ResultBlockResults, error) {
	if f.results != nil {
		return f.results.Get(ctx, height)
	}
	if f.bo.tmClient == nil {
		return nil, fmt.Errorf("no tendermint client")
	}
	return f.bo.tmClient.BlockResults(ctx, &height)
}

type pendingEvent struct {
	ev       apiconfig.HostEvent
	escrowID string
}

func (f *HostEventFeed) eventsForHeight(ctx context.Context, res *coretypes.ResultBlockResults) []apiconfig.HostEvent {
	var pending []pendingEvent
	for _, ev := range res.FinalizeBlockEvents {
		if eventMode(ev) != blockModeBegin || warmKeyBlockEvent(ev.Type) {
			continue
		}
		pending = append(pending, f.parseABCI(ev)...)
	}
	for _, tx := range res.TxsResults {
		if tx == nil {
			continue
		}
		for _, ev := range tx.Events {
			pending = append(pending, f.parseABCI(ev)...)
		}
	}
	for _, ev := range res.FinalizeBlockEvents {
		if eventMode(ev) != blockModeEnd || warmKeyBlockEvent(ev.Type) {
			continue
		}
		pending = append(pending, f.parseABCI(ev)...)
	}
	f.filterEscrow(ctx, pending)
	out := make([]apiconfig.HostEvent, 0, len(pending))
	for _, p := range pending {
		if p.ev.Kind == 0 {
			continue
		}
		out = append(out, p.ev)
	}
	return out
}

// warmKeyBlockEvent reports a warm-key event carried on the block itself.
// Grant and revoke are transaction events. An EndBlock warm_key_revoked was
// the chain expiry signal; the chain does not emit it, and the feed does not
// forward one if it is present.
func warmKeyBlockEvent(eventType string) bool {
	return eventType == warmKeyGrantedEvent || eventType == warmKeyRevokedEvent
}

func (f *HostEventFeed) parseABCI(ev abcitypes.Event) []pendingEvent {
	attrs := attrsOf(ev)
	var parsed []apiconfig.HostEvent
	var err error
	switch ev.Type {
	case escrowCreatedEvent:
		var one apiconfig.HostEvent
		var ok bool
		one, ok, err = parseEscrowCreated(attrs)
		if ok {
			parsed = []apiconfig.HostEvent{one}
		}
	case escrowSettledEvent:
		var one apiconfig.HostEvent
		var ok bool
		one, ok, err = parseEscrowSettled(attrs)
		if ok {
			parsed = []apiconfig.HostEvent{one}
		}
	case maintenanceScheduledEvent:
		var one apiconfig.HostEvent
		var ok bool
		one, ok, err = parseMaintenanceScheduled(attrs)
		if ok && !skipForeignMaintenance(f.addr(), one.Maintenance.Participant) {
			parsed = []apiconfig.HostEvent{one}
		}
	case maintenanceCanceledEvent:
		var one apiconfig.HostEvent
		var ok bool
		one, ok, err = parseMaintenanceCanceled(attrs)
		if ok && !skipForeignMaintenance(f.addr(), one.Maintenance.Participant) {
			parsed = []apiconfig.HostEvent{one}
		}
	case warmKeyGrantedEvent:
		parsed, err = parseWarmKeyEvents(attrs, warmKeyGrantedEvent, apiconfig.HostEventKindWarmKeyGranted)
	case warmKeyRevokedEvent:
		parsed, err = parseWarmKeyEvents(attrs, warmKeyRevokedEvent, apiconfig.HostEventKindWarmKeyRevoked)
	default:
		return nil
	}
	if err != nil {
		logging.Warn("host_event_feed: skip malformed event", types.EventProcessing, "type", ev.Type, "error", err)
		return nil
	}
	out := make([]pendingEvent, 0, len(parsed))
	for _, evn := range parsed {
		p := pendingEvent{ev: evn}
		if evn.Escrow != nil && (evn.Kind == apiconfig.HostEventKindEscrowCreated || evn.Kind == apiconfig.HostEventKindEscrowSettled) {
			p.escrowID = strconv.FormatUint(evn.Escrow.EscrowID, 10)
		}
		out = append(out, p)
	}
	return out
}

func (f *HostEventFeed) addr() string {
	if f.localAddr == nil {
		return ""
	}
	return f.localAddr()
}

func (f *HostEventFeed) filterEscrow(ctx context.Context, pending []pendingEvent) {
	ids := make([]string, 0)
	seen := map[string]struct{}{}
	for _, p := range pending {
		if p.escrowID == "" {
			continue
		}
		if _, ok := seen[p.escrowID]; ok {
			continue
		}
		seen[p.escrowID] = struct{}{}
		ids = append(ids, p.escrowID)
	}
	if len(ids) == 0 {
		return
	}
	lookupCtx, cancel := context.WithTimeout(ctx, escrowLookupBudget)
	defer cancel()
	holds := make([]bool, len(ids))
	resolved := make([]bool, len(ids))
	var wg sync.WaitGroup
	for i := 0; i < len(ids); {
		select {
		case <-lookupCtx.Done():
			i = len(ids)
		case f.sem <- struct{}{}:
			if lookupCtx.Err() != nil {
				<-f.sem
				i = len(ids)
				continue
			}
			idx, id := i, ids[i]
			i++
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer func() { <-f.sem }()
				holds[idx], resolved[idx] = escrowMembership(lookupCtx, f.escrow, f.addr(), id)
			}()
		}
	}
	wg.Wait()
	byID := make(map[string]struct{ holds, resolved bool }, len(ids))
	for i, id := range ids {
		byID[id] = struct{ holds, resolved bool }{holds[i], resolved[i]}
	}
	for i := range pending {
		id := pending[i].escrowID
		if id == "" {
			continue
		}
		mem, ok := byID[id]
		if !ok {
			continue
		}
		if mem.resolved && !mem.holds {
			pending[i].ev = apiconfig.HostEvent{}
			continue
		}
		if !mem.resolved {
			logging.Warn("host_events: escrow membership unresolved; appending anyway", types.EventProcessing,
				"escrow_id", id, "kind", pending[i].ev.Kind)
		}
	}
}

func eventMode(ev abcitypes.Event) string {
	for _, attr := range ev.Attributes {
		if attr.Key == "mode" {
			return attr.Value
		}
	}
	return ""
}

func attrsOf(ev abcitypes.Event) map[string][]string {
	out := make(map[string][]string, len(ev.Attributes))
	for _, attr := range ev.Attributes {
		if attr.Key == "mode" {
			continue
		}
		key := ev.Type + "." + attr.Key
		out[key] = append(out[key], attr.Value)
	}
	return out
}

func blockHostEvent(hdr *cmttypes.Header, epoch uint64) (apiconfig.HostEvent, error) {
	raw, err := gogoproto.Marshal(hdr.ToProto())
	if err != nil {
		return apiconfig.HostEvent{}, err
	}
	sum := hdr.Hash()
	return apiconfig.HostEvent{
		Kind: apiconfig.HostEventKindBlock,
		Block: &apiconfig.BlockPayload{
			Height:           hdr.Height,
			Hash:             append([]byte(nil), sum...),
			HeaderTimeUnixMs: hdr.Time.UnixMilli(),
			EpochIndex:       epoch,
			Header:           raw,
		},
	}, nil
}
