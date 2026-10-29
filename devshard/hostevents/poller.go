package hostevents

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"common/chainoracle/blocks"
	"common/chainoracle/blocks/observer"
	"common/nodemanager/gen"
	"devshard/bridge"
	"devshard/chainoracle/blocks/headertime"

	"github.com/prometheus/client_golang/prometheus"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const holeBatch = 20

// HoleReader fills block holes with header-only reads. Header is one Comet
// /header. BlockchainInfo is one batch of at most holeBatch heights.
// GetBlockByHeight and GetBlockHeader are not part of this interface.
type HoleReader interface {
	Header(ctx context.Context, height int64) (*blocks.Header, error)
	BlockchainInfo(ctx context.Context, from, to int64) ([]*blocks.Header, error)
}

// PollerConfig is one GetHostEvents loop shared by devshardd and the gateway.
type PollerConfig struct {
	Config
	// Source is the process header-time map. Nil skips block writes.
	Source *headertime.Source
	// IncludeEscrow adds the two escrow kinds. The gateway leaves this false.
	IncludeEscrow bool
	// BlocksVerified writes dapi BLOCK events into Source as verified. devshardd
	// sets this because dapi is the host's own. The gateway sets it when Comet
	// seed failed and NODE_MANAGER_ADDR is set: that dapi is trusted. When
	// Comet seed succeeded, the gateway leaves this false and sets Verify.
	BlocksVerified bool
	// StartAtHead, when the map has no high-water and no floor, sends
	// after_height at head instead of 0. 0 would replay the oldest retained
	// events. The gateway sets this whenever NODE_MANAGER_ADDR is set.
	StartAtHead bool
	// Escrow receives escrow and, when set, warm-key events.
	Escrow Sink
	// Warm is the warm-key log copy. The gateway applies events here. devshardd's
	// escrow sink applies them too. A gap resets completeness. The next read
	// of a granter loads that granter.
	Warm *bridge.WarmKeyLogCopy
	// Holes fills heights the feed skipped. Nil leaves misses to the map's
	// existing filler.
	Holes HoleReader
	// EpochFloor overrides the epoch window built from BLOCK payloads.
	// Tests set it. Production leaves it nil.
	EpochFloor func() uint64
	// Verify checks dapi BLOCK pages before they reach Source. The gateway
	// sets it. It is not called when BlocksVerified is true.
	Verify BlockVerifier
}

// BlockVerifier takes one response's BLOCK events and publishes the ones it
// verifies. LinkBuffered walks the pending buffer down from the trusted header
// when dapi is no longer offering the connecting pages.
type BlockVerifier interface {
	OfferDAPI(page []observer.DAPIBlock) error
	LinkBuffered()
}

const (
	// DAPIBlockPenalty is how long blocks use the fallback after a dapi
	// header fails verification.
	DAPIBlockPenalty = 5 * time.Minute
	// AfterHeightHead is after_height when the poller must sit at dapi head
	// instead of replaying the oldest retained events. GetHostEvents treats
	// a value above emitted_height as start-at-head.
	AfterHeightHead = ^uint64(0)
	// BlockFeedLiveMetric is the Prometheus name for whether dapi is the
	// primary BLOCK writer.
	BlockFeedLiveMetric = "devshard_hostevents_block_feed_live"
)

// HeaderTimeView is the operator snapshot of the header-time map and dapi feed.
type HeaderTimeView struct {
	HighWater     uint64 `json:"high_water"`
	TipHeight     uint64 `json:"tip_height"`
	TipOrigin     string `json:"tip_origin,omitempty"`
	TipVerified   bool   `json:"tip_verified"`
	BlockFeedLive bool   `json:"block_feed_live"`
}

// Poller is the one GetHostEvents loop.
type Poller struct {
	PollerConfig
	blockLive       atomic.Bool
	warmLive        atomic.Bool
	lastCometHeight atomic.Int64
	suppressUntil   atomic.Int64
	blockWatch      time.Time
	epochs          epochWindow
}

// NewPoller returns a poller. Run owns the loop.
func NewPoller(cfg PollerConfig) *Poller {
	if cfg.Clock == nil {
		cfg.Clock = realClock{}
	}
	p := &Poller{PollerConfig: cfg}
	p.publishBlockLive(cfg.Config.Client != nil, false)
	return p
}

// SuppressBlocks treats dapi as having no live block feed for d. Warm-key and
// escrow events are still applied.
func (p *Poller) SuppressBlocks(d time.Duration) {
	if p == nil {
		return
	}
	p.suppressUntil.Store(p.Clock.Now().Add(d).UnixNano())
	p.setBlockLive(false)
	p.linkBuffered()
}

func (p *Poller) linkBuffered() {
	if p.Verify == nil {
		return
	}
	p.Verify.LinkBuffered()
}

// DAPIBlockRejected counts a dapi header that failed verification and
// suppresses blocks for DAPIBlockPenalty.
func (p *Poller) DAPIBlockRejected(error) {
	dapiBlocksRejected.Inc()
	p.SuppressBlocks(DAPIBlockPenalty)
}

var (
	dapiBlocksRejected = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "devshard_hostevents_dapi_blocks_rejected_total",
		Help: "dapi block headers the gateway rejected: a failed self-check, a broken back-link, or a hash that differs from the verified header.",
	})
	blockFeedLive = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: BlockFeedLiveMetric,
		Help: "1 when dapi is the primary BLOCK writer into the header-time map, 0 otherwise.",
	})
)

func init() {
	_ = prometheus.Register(dapiBlocksRejected)
	_ = prometheus.Register(blockFeedLive)
}

func (p *Poller) blocksSuppressed() bool {
	until := p.suppressUntil.Load()
	return until != 0 && p.Clock.Now().UnixNano() < until
}

func (p *Poller) offerBlocks(events []*gen.HostEvent) {
	if p.BlocksVerified || p.Verify == nil || p.blocksSuppressed() {
		return
	}
	var page []observer.DAPIBlock
	for _, ev := range events {
		b := ev.GetBlock()
		if ev.GetKind() != gen.HostEventKind_HOST_EVENT_KIND_BLOCK || b == nil || b.GetHeight() <= 0 {
			continue
		}
		page = append(page, observer.DAPIBlock{
			Height: b.GetHeight(),
			Hash:   b.GetHash(),
			TimeMs: b.GetHeaderTimeUnixMs(),
			Header: b.GetHeader(),
		})
	}
	if len(page) == 0 {
		return
	}
	if err := p.Verify.OfferDAPI(page); err != nil {
		p.Log.Warn("hostevents: dapi blocks rejected", "err", err)
	}
}

// SubscribeSet is the kinds this process asks for. Maintenance is never included.
func SubscribeSet(includeEscrow bool) []gen.HostEventKind {
	kinds := []gen.HostEventKind{
		gen.HostEventKind_HOST_EVENT_KIND_BLOCK,
		gen.HostEventKind_HOST_EVENT_KIND_WARM_KEY_GRANTED,
		gen.HostEventKind_HOST_EVENT_KIND_WARM_KEY_REVOKED,
	}
	if includeEscrow {
		kinds = append(kinds,
			gen.HostEventKind_HOST_EVENT_KIND_ESCROW_CREATED,
			gen.HostEventKind_HOST_EVENT_KIND_ESCROW_SETTLED,
		)
	}
	return kinds
}

// BlockFeedLive reports whether dapi is the primary block writer.
func (p *Poller) BlockFeedLive() bool {
	if p == nil || (!p.BlocksVerified && p.Verify == nil) {
		return false
	}
	return p.blockLive.Load()
}

func (p *Poller) setBlockLive(live bool) {
	p.publishBlockLive(live, true)
}

func (p *Poller) publishBlockLive(live bool, logFlip bool) {
	if p == nil {
		return
	}
	prev := p.BlockFeedLive()
	p.blockLive.Store(live)
	now := p.BlockFeedLive()
	if now {
		blockFeedLive.Set(1)
	} else {
		blockFeedLive.Set(0)
	}
	if !logFlip || prev == now || p.Log == nil {
		return
	}
	if now {
		p.Log.Info("hostevents: block feed live")
		return
	}
	p.Log.Info("hostevents: block feed not live")
}

// HeaderTimeView reports the map tip and whether dapi is the primary writer.
func (p *Poller) HeaderTimeView() HeaderTimeView {
	if p == nil {
		return HeaderTimeView{}
	}
	v := HeaderTimeView{BlockFeedLive: p.BlockFeedLive()}
	if p.Source == nil {
		return v
	}
	hw := p.Source.HighWater()
	v.HighWater = hw
	v.TipHeight = hw
	if rec, ok := p.Source.StoredAt(hw); ok {
		v.TipOrigin = string(rec.Origin)
		v.TipVerified = rec.Verified
	}
	return v
}

// TrustedWarmKeyMessage is the gateway startup line. NODE_MANAGER_ADDR names a
// dapi the gateway operator trusts for warm-key events and, when Comet seed
// failed, for block times. Unset means dapi is never contacted.
func TrustedWarmKeyMessage(addr string) string {
	if addr == "" {
		return "NODE_MANAGER_ADDR is unset; dapi is never used"
	}
	return "NODE_MANAGER_ADDR is the trusted source of warm-key events and block times"
}

// Run long-polls until ctx is canceled. A nil client does not dial. Unimplemented
// does not stop the loop.
func (p *Poller) Run(ctx context.Context) {
	if p == nil {
		return
	}
	if err := p.Config.applyDefaults(); err != nil {
		if p.Client == nil {
			p.setBlockLive(false)
			p.setWarmLive(false)
			p.installFloor()
			p.installHoles()
			p.wait(ctx)
			return
		}
		if p.Log != nil {
			p.Log.Error("hostevents: invalid config", "err", err)
		}
		return
	}
	p.installFloor()
	p.installHoles()
	if p.Client == nil {
		p.setBlockLive(false)
		p.setWarmLive(false)
		p.wait(ctx)
		return
	}
	if p.IncludeEscrow && p.Escrow != nil {
		p.Escrow.RehydrateOpenEscrows()
	}

	var (
		cursor           uint64
		generation       uint64
		afterSent        bool
		logResetFollowUp bool
		backoff          time.Duration
		retrySeq         uint64
		retries          int
		gaveUp           bool
	)
	for {
		if backoff > 0 {
			select {
			case <-ctx.Done():
				return
			case <-p.Clock.After(backoff):
			}
		}
		if ctx.Err() != nil {
			return
		}
		after := uint64(0)
		if !afterSent {
			after = p.afterHeight()
		}
		resp, err := p.poll(ctx, cursor, generation, after)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			if status.Code(err) == codes.Unimplemented {
				if !gaveUp {
					gaveUp = true
					p.setBlockLive(false)
					p.setWarmLive(false)
					p.fallback()
					p.Log.Info("hostevents: GetHostEvents Unimplemented; using fallbacks")
				}
				backoff = nextBackoff(backoff, p.ErrorBackoffMin, p.ErrorBackoffMax)
				continue
			}
			if status.Code(err) == codes.Unavailable {
				p.setBlockLive(false)
				p.setWarmLive(false)
				backoff = nextBackoff(backoff, p.ErrorBackoffMin, p.ErrorBackoffMax)
				p.Log.Warn("hostevents: feed unavailable", "err", err, "cursor", cursor, "backoff", backoff)
				continue
			}
			backoff = nextBackoff(backoff, p.ErrorBackoffMin, p.ErrorBackoffMax)
			p.Log.Warn("hostevents: long-poll failed", "err", err, "backoff", backoff)
			continue
		}
		logPoll := !afterSent || after != 0 || logResetFollowUp
		afterSent = true
		if logPoll && p.Log != nil {
			p.Log.Info("hostevents: GetHostEvents",
				"cursor", cursor,
				"after_height", after,
				"generation", generation)
			logResetFollowUp = false
		}
		if p.IncludeEscrow && p.LoadMap != nil {
			p.LoadMap.Replace(resp.GetEscrowLoad(), p.Clock.Now())
		}
		p.noteFeed(resp)
		p.noteWarm(resp)
		if resp.GetGeneration() != 0 {
			generation = resp.GetGeneration()
		}
		if resp.GetNeedsReset() {
			p.Log.Info("hostevents: needs_reset",
				"generation", resp.GetGeneration(),
				"next_cursor", resp.GetNextCursor())
			p.fallback()
			p.linkBuffered()
			cursor = resp.GetNextCursor()
			backoff, retrySeq, retries = 0, 0, 0
			logResetFollowUp = true
			continue
		}
		failed, ok := p.dispatch(resp.GetEvents())
		p.offerBlocks(resp.GetEvents())
		if !ok && failed.GetSeq() > 0 {
			seq := failed.GetSeq()
			if seq != retrySeq {
				retrySeq, retries = seq, 0
			}
			retries++
			if retries <= p.MaxDispatchAttempts {
				cursor = seq - 1
				backoff = nextBackoff(backoff, p.ErrorBackoffMin, p.ErrorBackoffMax)
				p.Log.Warn("hostevents: dispatch failed, redelivering",
					"kind", failed.GetKind().String(),
					"seq", seq,
					"attempt", retries,
					"backoff", backoff)
				continue
			}
			p.Log.Error("hostevents: dispatch permanently failed, skipping event",
				"kind", failed.GetKind().String(),
				"seq", seq,
				"attempts", retries)
			if p.IncludeEscrow && p.Escrow != nil {
				p.Escrow.RehydrateOpenEscrows()
			}
		}
		cursor = resp.GetNextCursor()
		backoff, retrySeq, retries = 0, 0, 0
	}
}

func (p *Poller) wait(ctx context.Context) {
	<-ctx.Done()
}

func (p *Poller) poll(ctx context.Context, cursor, generation, after uint64) (*gen.GetHostEventsResponse, error) {
	callCtx, cancel := context.WithTimeout(ctx, p.clientCallDeadline())
	defer cancel()
	return p.Client.GetHostEvents(callCtx, &gen.GetHostEventsRequest{
		Cursor:         cursor,
		Generation:     generation,
		MaxWaitSeconds: uint32(p.ServerMaxWait / time.Second),
		Subscribe:      SubscribeSet(p.IncludeEscrow),
		AfterHeight:    after,
	})
}

func (p *Poller) afterHeight() uint64 {
	hw := uint64(0)
	floor := uint64(0)
	if p.Source != nil {
		hw = p.Source.HighWater()
		floor = p.Source.Floor()
	}
	if hw > floor {
		return hw
	}
	if floor > 0 {
		return floor
	}
	if p.StartAtHead {
		return AfterHeightHead
	}
	return 0
}

func (p *Poller) writeFloor() uint64 {
	if p.EpochFloor != nil {
		return p.EpochFloor()
	}
	return p.epochs.Floor()
}

func (p *Poller) bootFloor() uint64 {
	if p.EpochFloor != nil {
		return p.EpochFloor()
	}
	if f := p.epochs.Floor(); f > 0 {
		return f
	}
	if p.Source != nil {
		return p.Source.StoredEpochFloor()
	}
	return 0
}

func (p *Poller) installHoles() {
	if p.Holes == nil || p.Source == nil {
		return
	}
	p.Source.SetFiller(headerFiller{holes: p.Holes, origin: headertime.OriginChain})
}

func (p *Poller) installFloor() {
	if p.Source == nil {
		return
	}
	p.Source.SetFloorHeight(p.bootFloor())
}

func (p *Poller) fallback() {
	if p.IncludeEscrow && p.Escrow != nil {
		p.Escrow.RehydrateOpenEscrows()
	}
	if p.Warm != nil {
		p.Warm.Reset()
	}
}

// WarmFeedLive reports whether the last GetHostEvents answer listed both
// warm-key kinds. It does not gate the Comet socket: that socket always
// applies. A quiet stretch with no grant stays live.
func (p *Poller) WarmFeedLive() bool {
	if p == nil {
		return false
	}
	return p.warmLive.Load()
}

func (p *Poller) setWarmLive(live bool) {
	if p == nil {
		return
	}
	prev := p.warmLive.Load()
	if prev == live {
		return
	}
	p.warmLive.Store(live)
	if p.Log != nil {
		if live {
			p.Log.Info("hostevents: warm feed live")
		} else {
			p.Log.Info("hostevents: warm feed not live")
		}
	}
	if prev && !live && p.Warm != nil {
		p.Warm.Reset()
	}
}

func (p *Poller) noteWarm(resp *gen.GetHostEventsResponse) {
	p.setWarmLive(supportsWarm(resp.GetSupportedKinds()))
}

func supportsWarm(kinds []gen.HostEventKind) bool {
	var grant, revoke bool
	for _, k := range kinds {
		switch k {
		case gen.HostEventKind_HOST_EVENT_KIND_WARM_KEY_GRANTED:
			grant = true
		case gen.HostEventKind_HOST_EVENT_KIND_WARM_KEY_REVOKED:
			revoke = true
		}
	}
	return grant && revoke
}

// OfferCometWarm applies warm-key entries from the process Comet socket.
// dapi being the live writer does not drop them. Apply is idempotent, and an
// ended id stays ended.
func (p *Poller) OfferCometWarm(entries []bridge.WarmKeyEntry) {
	if p == nil || len(entries) == 0 {
		return
	}
	for _, e := range entries {
		_ = p.applyWarm(e, slog.LevelDebug)
	}
}

// OnCometConnected marks the copy incomplete after the socket connects or
// reconnects. The next read of a granter loads that granter. It does not
// query the chain itself.
func (p *Poller) OnCometConnected() {
	if p == nil || p.Warm == nil {
		return
	}
	p.Warm.Reset()
}

// NoteCometHeight records a NewBlock height from this process's Comet socket.
// The first height is not a gap. A later height more than one above the last
// resets the warm-key copy and does not read the chain. The next read of a
// granter loads that granter.
func (p *Poller) NoteCometHeight(height int64) {
	if p == nil || height <= 0 {
		return
	}
	for {
		prev := p.lastCometHeight.Load()
		if height <= prev {
			return
		}
		if !p.lastCometHeight.CompareAndSwap(prev, height) {
			continue
		}
		if prev > 0 && height > prev+1 && p.Warm != nil {
			p.Warm.Reset()
		}
		return
	}
}

func (p *Poller) noteFeed(resp *gen.GetHostEventsResponse) {
	if p.blocksSuppressed() {
		p.setBlockLive(false)
		return
	}
	now := p.Clock.Now()
	saw := false
	for _, ev := range resp.GetEvents() {
		if ev.GetKind() == gen.HostEventKind_HOST_EVENT_KIND_BLOCK {
			saw = true
			break
		}
	}
	if !supportsBlock(resp.GetSupportedKinds()) {
		if !saw {
			p.setBlockLive(false)
		}
	}
	if saw {
		p.setBlockLive(true)
		p.blockWatch = now
		return
	}
	if !supportsBlock(resp.GetSupportedKinds()) {
		return
	}
	if p.blockWatch.IsZero() {
		p.blockWatch = now
		p.setBlockLive(true)
		return
	}
	iv := p.blockInterval()
	if iv > 0 && now.Sub(p.blockWatch) >= 3*iv {
		p.setBlockLive(false)
	}
}

func supportsBlock(kinds []gen.HostEventKind) bool {
	for _, k := range kinds {
		if k == gen.HostEventKind_HOST_EVENT_KIND_BLOCK {
			return true
		}
	}
	return false
}

func (p *Poller) blockInterval() time.Duration {
	if p.Source != nil {
		if d := p.Source.BlockInterval(); d > 0 {
			return d
		}
	}
	return time.Second
}

func (p *Poller) dispatch(events []*gen.HostEvent) (*gen.HostEvent, bool) {
	for _, ev := range events {
		if err := p.dispatchOne(ev); err != nil {
			p.Log.Warn("hostevents: dispatch failed",
				"kind", ev.GetKind().String(),
				"seq", ev.GetSeq(),
				"err", err)
			return ev, false
		}
	}
	return nil, true
}

func (p *Poller) dispatchOne(ev *gen.HostEvent) error {
	switch ev.GetKind() {
	case gen.HostEventKind_HOST_EVENT_KIND_BLOCK:
		p.applyBlock(ev)
		return nil
	case gen.HostEventKind_HOST_EVENT_KIND_ESCROW_CREATED:
		if !p.IncludeEscrow || p.Escrow == nil {
			return nil
		}
		id := escrowIDString(ev)
		if id == "" {
			return nil
		}
		return p.Escrow.WarmEscrow(id)
	case gen.HostEventKind_HOST_EVENT_KIND_ESCROW_SETTLED:
		if !p.IncludeEscrow || p.Escrow == nil {
			return nil
		}
		id := escrowIDString(ev)
		if id == "" {
			return nil
		}
		return p.Escrow.OnEscrowSettled(id)
	case gen.HostEventKind_HOST_EVENT_KIND_WARM_KEY_GRANTED, gen.HostEventKind_HOST_EVENT_KIND_WARM_KEY_REVOKED:
		entry, ok := warmKeyEntryOf(ev)
		if !ok {
			return nil
		}
		return p.applyWarm(entry, slog.LevelInfo)
	default:
		return nil
	}
}

func (p *Poller) applyWarm(entry bridge.WarmKeyEntry, level slog.Level) error {
	if p.Log != nil {
		p.Log.Log(context.Background(), level, "hostevents: warm-key",
			"granter", entry.Granter,
			"grantee", entry.Grantee,
			"grant_id", entry.ID,
			"end", entry.End,
		)
	}
	if p.Escrow != nil {
		return p.Escrow.OnWarmKeyEvent(entry)
	}
	if p.Warm != nil {
		p.Warm.Apply(entry)
	}
	return nil
}

func (p *Poller) applyBlock(ev *gen.HostEvent) {
	payload := ev.GetBlock()
	if payload == nil || payload.GetHeight() <= 0 {
		return
	}
	if p.blocksSuppressed() {
		return
	}
	height := uint64(payload.GetHeight())
	p.noteEpoch(payload.GetEpochIndex(), height)
	if p.Source != nil {
		if f := p.writeFloor(); p.EpochFloor != nil || f > 0 {
			p.Source.SetFloorHeight(f)
		}
	}
	if !p.BlocksVerified || p.Source == nil {
		return
	}
	h := &blocks.Header{
		Height:    payload.GetHeight(),
		Time:      time.UnixMilli(payload.GetHeaderTimeUnixMs()).UTC(),
		BlockHash: append([]byte(nil), payload.GetHash()...),
	}
	p.Source.ObserveEpoch(h, headertime.OriginDAPI, true, payload.GetEpochIndex())
	if p.Log != nil {
		v := p.HeaderTimeView()
		p.Log.Info("header_time: applied",
			"height", height,
			"origin", headertime.OriginDAPI,
			"verified", true,
			"high_water", v.HighWater,
			"tip_height", v.TipHeight,
			"tip_origin", v.TipOrigin,
			"tip_verified", v.TipVerified,
		)
	}
}

func (p *Poller) noteEpoch(epoch, height uint64) {
	p.epochs.Note(epoch, height)
}

// epochWindow tracks the current and previous epoch starts from BLOCK
// payloads. Floor is the first height of epoch current−1.
type epochWindow struct {
	mu            sync.Mutex
	current       uint64
	currentStart  uint64
	previousStart uint64
}

func (w *epochWindow) Note(epoch, height uint64) {
	if w == nil || epoch == 0 || height == 0 {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if epoch > w.current {
		if w.current > 0 {
			w.previousStart = w.currentStart
		}
		w.current = epoch
		w.currentStart = height
		return
	}
	if epoch == w.current {
		if w.currentStart == 0 || height < w.currentStart {
			w.currentStart = height
		}
		return
	}
	if epoch+1 == w.current {
		if w.previousStart == 0 || height < w.previousStart {
			w.previousStart = height
		}
	}
}

func (w *epochWindow) Floor() uint64 {
	if w == nil {
		return 0
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return headertime.EpochFloor(w.current, w.previousStart)
}

type headerFiller struct {
	holes    HoleReader
	origin   headertime.Origin
	verified bool
}

func (f headerFiller) Fill(ctx context.Context, height int64) (headertime.Fill, error) {
	if f.holes == nil || height <= 0 {
		return headertime.Fill{}, blocks.ErrHeaderNotFound
	}
	h, err := f.holes.Header(ctx, height)
	if err != nil {
		return headertime.Fill{}, err
	}
	return headertime.Fill{Header: h, Origin: f.origin, Verified: f.verified}, nil
}

func (f headerFiller) FillBatch(ctx context.Context, from, to int64) ([]headertime.Fill, error) {
	if f.holes == nil || from <= 0 || to < from {
		return nil, nil
	}
	var out []headertime.Fill
	for from <= to {
		end := from + holeBatch - 1
		if end > to {
			end = to
		}
		got, err := f.holes.BlockchainInfo(ctx, from, end)
		if err != nil && len(got) == 0 {
			return out, err
		}
		for _, h := range got {
			if h == nil {
				continue
			}
			out = append(out, headertime.Fill{Header: h, Origin: f.origin, Verified: f.verified})
		}
		if end == to {
			break
		}
		from = end + 1
	}
	return out, nil
}
