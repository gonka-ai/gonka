package heightsync

import (
	"maps"
	"sync"
)

const (
	AnchorKindCadence   = "cadence"
	AnchorKindHeartbeat = "heartbeat"
	AnchorKindForced    = "forced"
	AnchorKindResponse  = "response"

	defaultSealedRetain = 32
	// openBucketsPerRetain bounds provisional buckets as a multiple of the
	// debug ring. Until the first gateway tip there is no reference frame to
	// refuse an absurd claim against, and every distinct height a host names
	// would otherwise allocate a bucket that nothing ever seals.
	openBucketsPerRetain = 4
	minOpenBuckets       = 64
)

var anchorHistogramBounds = []float64{0, 1, 2, 3, 4, 6, 8, 12, 16}

// AnchorTally buckets host-signed height claims by the height they stamp.
// A bucket is provisional until the observed tip has moved past it by D_ack.
//
// Only ObserveTip (gateway tip) may advance the seal cursor (`next`). Host
// Record/RecordTurnover heights never pin `next`. Claims behind `next` count
// as late; claims more than D_ack ahead of tip count as future and do not
// allocate open buckets sealLocked will never visit.
type AnchorTally struct {
	mu     sync.Mutex
	dAck   uint64
	tip    uint64
	retain int

	open   map[uint64]*anchorBucket
	sealed []SealedHeightDetail
	next   uint64 // next height that will be considered for sealing (1-based)

	without uint64
	late    uint64 // Record/RecordTurnover below next (past the seal watermark)
	// future counts claims the tally could not place: above tip+D_ack, or —
	// before any tip exists — beyond the provisional bucket budget.
	future  uint64
	anchors histAcc
	turns   histAcc
}

type anchorBucket struct {
	kinds     map[string]int
	turnovers int
}

type histAcc struct {
	count   uint64
	sum     float64
	buckets []uint64 // parallel to anchorHistogramBounds, cumulative
}

// NewAnchorTally constructs an empty tally. dAck is the seal lag; retain
// bounds the debug ring (default 32).
func NewAnchorTally(dAck uint64, retain int) *AnchorTally {
	if retain <= 0 {
		retain = defaultSealedRetain
	}
	return &AnchorTally{
		dAck:   dAck,
		retain: retain,
		open:   make(map[uint64]*anchorBucket),
		// One slot of headroom: sealOneLocked appends before trimming, so the
		// ring never reallocates in steady state.
		sealed:  make([]SealedHeightDetail, 0, retain+1),
		anchors: histAcc{buckets: make([]uint64, len(anchorHistogramBounds))},
		turns:   histAcc{buckets: make([]uint64, len(anchorHistogramBounds))},
	}
}

// maxOpenLocked bounds provisional buckets. With a tip, sealing keeps the live
// window small on its own; this only has to stop unbounded growth while the
// tally has no tip to refuse claims against.
func (t *AnchorTally) maxOpenLocked() int {
	n := max(t.retain*openBucketsPerRetain, minOpenBuckets)
	return n
}

func (t *AnchorTally) dAckLocked() uint64 {
	if t.dAck == 0 {
		return 1
	}
	return t.dAck
}

// startLocked initializes next from the gateway tip only. Host claims never
// call this. With no open buckets, the cursor starts at tip−D_ack so
// near-tip heights stay provisional. If Record ran before the first tip, the
// oldest open claim becomes the cursor so that bucket still waits for
// tip ≥ H+D_ack instead of sealing an empty tip−D_ack ahead of it.
func (t *AnchorTally) startLocked(tip uint64) {
	if t.next != 0 || tip == 0 {
		return
	}
	dAck := t.dAckLocked()
	start := uint64(1)
	if tip > dAck {
		start = tip - dAck
	}
	if len(t.open) > 0 {
		first := true
		var minOpen uint64
		for h := range t.open {
			if first || h < minOpen {
				minOpen = h
				first = false
			}
		}
		if minOpen > 0 {
			start = minOpen
		}
	}
	t.next = start
}

// refuseLateLocked drops heights already behind the seal cursor. Those are
// protocol-late acks: still real for the turn, but not this block's anchors.
func (t *AnchorTally) refuseLateLocked(h uint64) bool {
	if t.next > 0 && h < t.next {
		t.late++
		return true
	}
	return false
}

// refuseFutureLocked drops heights the tally cannot place. Above tip+D_ack
// those would allocate open buckets sealing never visits. With no tip yet
// there is nothing to compare against, so only the bucket budget applies: a
// host is free to name a new height every ack, and sealing — which only a tip
// can start — would never reclaim any of them.
func (t *AnchorTally) refuseFutureLocked(h uint64) bool {
	if t.tip == 0 {
		if _, open := t.open[h]; !open && len(t.open) >= t.maxOpenLocked() {
			t.future++
			return true
		}
		return false
	}
	maxH := t.tip + t.dAckLocked()
	if h > maxH {
		t.future++
		return true
	}
	return false
}

// pruneFutureOpenLocked drops provisional buckets above tip+D_ack (e.g.
// Record-before-tip with an absurd height). Counted as future, not late.
func (t *AnchorTally) pruneFutureOpenLocked() {
	if t.tip == 0 || len(t.open) == 0 {
		return
	}
	maxH := t.tip + t.dAckLocked()
	for h := range t.open {
		if h > maxH {
			delete(t.open, h)
			t.future++
		}
	}
}

func (t *AnchorTally) bucketLocked(h uint64) *anchorBucket {
	if t.open == nil {
		t.open = make(map[uint64]*anchorBucket)
	}
	b := t.open[h]
	if b == nil {
		b = &anchorBucket{kinds: make(map[string]int)}
		t.open[h] = b
	}
	return b
}

// Record notes an anchor of kind at claimed height. Height 0 is ignored.
// Claims below the seal cursor (`next`) or more than D_ack above tip are
// counted (late / future) and do not allocate an open bucket.
func (t *AnchorTally) Record(height uint64, kind string) {
	if t == nil || height == 0 {
		return
	}
	if kind == "" {
		kind = AnchorKindResponse
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.refuseLateLocked(height) || t.refuseFutureLocked(height) {
		return
	}
	b := t.bucketLocked(height)
	b.kinds[kind]++
	t.sealLocked()
}

// RecordTurnover notes a full height-sync turnover attributed to claimed height.
func (t *AnchorTally) RecordTurnover(height uint64) {
	if t == nil || height == 0 {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.refuseLateLocked(height) || t.refuseFutureLocked(height) {
		return
	}
	t.bucketLocked(height).turnovers++
	t.sealLocked()
}

// ObserveTip advances the sealing watermark. Buckets at H seal once tip ≥ H+D_ack.
// This is the only path that may initialize `next`.
func (t *AnchorTally) ObserveTip(tip uint64) {
	if t == nil || tip == 0 {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if tip > t.tip {
		t.tip = tip
	}
	t.pruneFutureOpenLocked()
	t.startLocked(tip)
	t.sealLocked()
}

func (t *AnchorTally) sealLocked() {
	if t.next == 0 {
		return
	}
	// A zero window would seal the current tip on every scrape and publish a
	// fake dip, so dAckLocked floors it at 1 and the newest bucket stays open.
	dAck := t.dAckLocked()
	if t.tip <= dAck {
		return
	}
	lastSealable := t.tip - dAck
	if lastSealable < t.next {
		return
	}

	// Cap per-call work at retain. A large tip jump (oracle catch-up) would
	// otherwise walk O(Δheight) under the session lock and re-slice the
	// debug ring each step. Skip the excess as without in O(|open|).
	// Empty heights skipped here are counted only on `without`, not as
	// histogram observe(0) samples — so histogram Count tracks sealed
	// heights that had a claim, not every height the cursor crossed.
	maxPerCall := uint64(t.retain)
	if maxPerCall == 0 {
		maxPerCall = defaultSealedRetain
	}
	span := lastSealable - t.next + 1
	if span > maxPerCall {
		skipEnd := lastSealable - maxPerCall + 1
		t.fastForwardLocked(skipEnd)
	}

	for h := t.next; h <= lastSealable; h++ {
		t.sealOneLocked(h)
		t.next = h + 1
	}
}

// fastForwardLocked advances next to skipEnd without a debug ring entry per
// height. Open buckets in the skipped range fold into the histograms; empty
// heights add to without only (no observe(0) for empty skips).
func (t *AnchorTally) fastForwardLocked(skipEnd uint64) {
	if skipEnd <= t.next {
		return
	}
	var withAnchor uint64
	for h, b := range t.open {
		if h < t.next || h >= skipEnd {
			continue
		}
		total := 0
		turnovers := 0
		if b != nil {
			turnovers = b.turnovers
			for _, n := range b.kinds {
				total += n
			}
		}
		t.anchors.observe(float64(total))
		t.turns.observe(float64(turnovers))
		delete(t.open, h)
		if total > 0 {
			withAnchor++
		}
	}
	t.without += (skipEnd - t.next) - withAnchor
	t.next = skipEnd
}

func (t *AnchorTally) sealOneLocked(h uint64) {
	b := t.open[h]
	delete(t.open, h)
	detail := SealedHeightDetail{Height: h, ByKind: map[string]int{}}
	total := 0
	if b != nil {
		detail.Turnovers = b.turnovers
		for k, n := range b.kinds {
			detail.ByKind[k] = n
			total += n
		}
	}
	if total == 0 {
		detail.Empty = true
		t.without++
	}
	t.sealed = append(t.sealed, detail)
	if len(t.sealed) > t.retain {
		// Shift in place. Re-slicing off the front would keep the dropped
		// entries alive behind the slice header; reallocating would copy the
		// whole ring on every seal.
		n := copy(t.sealed, t.sealed[len(t.sealed)-t.retain:])
		clear(t.sealed[n:])
		t.sealed = t.sealed[:n]
	}
	t.anchors.observe(float64(total))
	t.turns.observe(float64(detail.Turnovers))
}

func (a *histAcc) observe(v float64) {
	if a.buckets == nil {
		a.buckets = make([]uint64, len(anchorHistogramBounds))
	}
	a.count++
	a.sum += v
	for i, bound := range anchorHistogramBounds {
		if v <= bound {
			a.buckets[i]++
		}
	}
}

func (a *histAcc) snapshot() HistogramSnapshot {
	out := HistogramSnapshot{
		Count:   a.count,
		Sum:     a.sum,
		Buckets: make(map[float64]uint64, len(anchorHistogramBounds)),
	}
	var cum uint64
	for i, bound := range anchorHistogramBounds {
		if i < len(a.buckets) {
			cum = a.buckets[i]
		}
		out.Buckets[bound] = cum
	}
	return out
}

// Snapshot is a defensive copy for scrape / debug.
func (t *AnchorTally) Snapshot() (last *SealedAnchorCounts, debug []SealedHeightDetail, without, late, future uint64, anchors, turns HistogramSnapshot) {
	if t == nil {
		return nil, nil, 0, 0, 0, HistogramSnapshot{}, HistogramSnapshot{}
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	debug = append([]SealedHeightDetail(nil), t.sealed...)
	for i := range debug {
		if debug[i].ByKind != nil {
			cp := make(map[string]int, len(debug[i].ByKind))
			maps.Copy(cp, debug[i].ByKind)
			debug[i].ByKind = cp
		}
	}
	if n := len(t.sealed); n > 0 {
		s := t.sealed[n-1]
		last = &SealedAnchorCounts{
			Height:    s.Height,
			ByKind:    map[string]int{},
			Turnovers: s.Turnovers,
		}
		maps.Copy(last.ByKind, s.ByKind)
	}
	return last, debug, t.without, t.late, t.future, t.anchors.snapshot(), t.turns.snapshot()
}

// OpenLen is the number of provisional buckets (tests / debug).
func (t *AnchorTally) OpenLen() int {
	if t == nil {
		return 0
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.open)
}

// Late is the count of Record/RecordTurnover calls refused below next.
func (t *AnchorTally) Late() uint64 {
	if t == nil {
		return 0
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.late
}

// Future is the count of claims refused or pruned above tip+D_ack.
func (t *AnchorTally) Future() uint64 {
	if t == nil {
		return 0
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.future
}
