package headertime

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"common/chainoracle/blocks"
	"common/logging"
	"devshard/types"
)

const (
	DefaultCapacity      = 200_000
	DefaultFetchTimeout  = 5 * time.Second
	DefaultBatchTimeout  = 4 * DefaultFetchTimeout
	DefaultMissWait      = 250 * time.Millisecond
	DefaultBlockInterval = time.Second

	// blockIntervalSample is how many header-time gaps BlockInterval medians,
	// and how many entries must exist before that median replaces the default.
	blockIntervalSample = 20

	// catchUpBatch is how many missing heights one catch-up read collects
	// before they are stored together.
	catchUpBatch = 20

	// catchMissLimit is how many times one catch-up job asks for a height
	// that came back missing. The next attempt is a new job, behind ranges
	// that can still be stored.
	catchMissLimit = 3
)

// Origin is who wrote a header into the map.
type Origin string

const (
	OriginDAPI  Origin = "dapi"
	OriginComet Origin = "comet"
	OriginChain Origin = "chain"
)

// Record is one stored header time.
type Record struct {
	Hash     []byte
	TimeMs   int64
	Origin   Origin
	Verified bool
	Epoch    uint64
}

type entry struct {
	hash     []byte
	ms       int64
	origin   Origin
	verified bool
	epoch    uint64
}

// Source is the header-time map: height → hash, time in ms, writer, verified.
// A miss asks the Filler and waits DefaultMissWait. Entries below the floor
// function are dropped. A lookup below the floor asks the filler and returns
// the time without storing. DefaultCapacity is the hard cap when the floor is 0.
type Source struct {
	filler        Filler
	fetchTimeout  time.Duration
	missWait      time.Duration
	capacity      int
	blockInterval time.Duration
	floor         func() uint64
	gateway       bool

	conflicts atomic.Uint64
	// cachedFloor is the retention floor last set on a write (Observe,
	// catch-up apply, SetFloorHeight). Readers use it without sampling.
	cachedFloor atomic.Uint64

	floorMu sync.Mutex

	mu       sync.Mutex
	entries  map[uint64]entry
	order    []uint64
	inflight map[uint64]*fetchFlight

	// runBase is the lowest height of the contiguous tip run. tip is its top.
	// catchFloor is the lowest anchor whose gap has been applied. A queued
	// job fills from its anchor up to this floor. The floor stays put until
	// that apply connects. One catch-up job runs.
	runBase    uint64
	tip        uint64
	catchFloor uint64
	catchOn    bool
	catchQ     []catchSpan
	// minHeight is the lowest stored height. pruneFloorLocked returns at
	// once when the floor is at or below it.
	minHeight uint64
	// intervalGaps is the last 20 consecutive header-time gaps at the top
	// of the run, newest last. BlockInterval reads it without a sort.
	intervalGaps []int64

	cache     Cache
	restoring bool
}

// catchSpan is one queued catch-up. A backward job stores its anchor and
// reads the catch floor when it starts. A forward job stores a fixed span.
// tries is how many times this job has already failed.
type catchSpan struct {
	anchor  uint64
	from    uint64
	to      uint64
	forward bool
	tries   int
}

// fetchFlight is one shared miss. hit is the filler's header, including when
// the hard cap drops that height before the caller reads the map.
type fetchFlight struct {
	done chan struct{}
	ms   int64
	hash []byte
	hit  bool
}

type Option func(*Source)

func WithFetchTimeout(d time.Duration) Option {
	return func(s *Source) { s.fetchTimeout = d }
}

func WithMissWait(d time.Duration) Option {
	return func(s *Source) { s.missWait = d }
}

func WithCapacity(n int) Option {
	return func(s *Source) { s.capacity = n }
}

// WithBlockInterval sets the block interval used until 20 entries exist,
// and when the median gap is 0.
func WithBlockInterval(d time.Duration) Option {
	return func(s *Source) {
		if d > 0 {
			s.blockInterval = d
		}
	}
}

// WithFloor drops entries below the returned height. Zero keeps the hard cap
// as the only bound. The function runs on writes, not on lookups, and may
// call HighWater and BlockInterval.
func WithFloor(fn func() uint64) Option {
	return func(s *Source) { s.floor = fn }
}

// WithGateway keeps the first header when two writes disagree. Devshardd
// replaces a dapi header with a comet or chain header.
func WithGateway() Option {
	return func(s *Source) { s.gateway = true }
}

func WithFiller(f Filler) Option {
	return func(s *Source) { s.filler = f }
}

func New(opts ...Option) *Source {
	s := &Source{
		fetchTimeout:  DefaultFetchTimeout,
		missWait:      DefaultMissWait,
		capacity:      DefaultCapacity,
		blockInterval: DefaultBlockInterval,
		entries:       make(map[uint64]entry),
		inflight:      make(map[uint64]*fetchFlight),
		intervalGaps:  make([]int64, 0, blockIntervalSample),
	}
	for _, o := range opts {
		o(s)
	}
	return s
}

// SetFiller binds the miss path. The poller calls this when it takes over
// from OracleFiller.
func (s *Source) SetFiller(f Filler) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.filler = f
	s.mu.Unlock()
}

// Observe stores a header. A dummy header, a zero time, or an empty hash is
// ignored. The same hash upgrades origin and verified, except on the gateway a
// verified dapi origin ranks above chain so a filler read does not relabel it
// and a later dapi page can mark a comet-verified tip as dapi. A different hash
// follows the devshardd or gateway conflict rule.
func (s *Source) Observe(h *blocks.Header, origin Origin, verified bool) {
	s.ObserveEpoch(h, origin, verified, 0)
}

// ObserveEpoch is Observe with the chain epoch_index from a BLOCK payload.
func (s *Source) ObserveEpoch(h *blocks.Header, origin Origin, verified bool, epoch uint64) {
	if s == nil || h == nil || h.Height <= 0 {
		return
	}
	floor := s.writeFloor()
	s.mu.Lock()
	note, row, pruned := s.putLocked(uint64(h.Height), h.BlockHash, h.Time, origin, verified, floor, epoch)
	cache := s.cache
	restoring := s.restoring
	s.mu.Unlock()
	s.emit(note)
	if restoring {
		return
	}
	var rows []Row
	if row != nil {
		rows = []Row{*row}
	}
	s.persistUnlocked(cache, rows, floor, pruned)
}

// StoredAt returns the header at height after dropping entries below the floor.
// It does not ask the filler.
func (s *Source) StoredAt(height uint64) (Record, bool) {
	if s == nil || height == 0 {
		return Record{}, false
	}
	floor := s.cachedFloor.Load()
	if floor > 0 && height < floor {
		return Record{}, false
	}
	s.mu.Lock()
	e, ok := s.entries[height]
	s.mu.Unlock()
	if !ok {
		return Record{}, false
	}
	return Record{
		Hash:     append([]byte(nil), e.hash...),
		TimeMs:   e.ms,
		Origin:   e.origin,
		Verified: e.verified,
		Epoch:    e.epoch,
	}, true
}

// Conflicts is how many different-hash writes this map has seen.
func (s *Source) Conflicts() uint64 {
	if s == nil {
		return 0
	}
	return s.conflicts.Load()
}

// HighWater is the highest height with every height from the contiguous run
// present. A stored height below that run does not move it. The walk starts
// at the stored run, not at a missing floor height. An empty map returns 0.
func (s *Source) HighWater() uint64 {
	if s == nil {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.highWaterLocked()
}

// BlockInterval is the median of the last 20 gaps between consecutive heights,
// in header time. Fewer than 20 entries, no consecutive pair, or a median of
// 0 returns the configured default. It does not call the floor function.
func (s *Source) BlockInterval() time.Duration {
	if s == nil {
		return DefaultBlockInterval
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.blockIntervalLocked()
}

// blockIntervalLocked reads the gaps already kept at the top of the run.
// The caller holds s.mu.
func (s *Source) blockIntervalLocked() time.Duration {
	if len(s.entries) < blockIntervalSample || s.blockInterval <= 0 {
		if s.blockInterval <= 0 {
			return DefaultBlockInterval
		}
		return s.blockInterval
	}
	if len(s.intervalGaps) == 0 {
		return s.blockInterval
	}
	med := medianGap(s.intervalGaps)
	if med <= 0 {
		return s.blockInterval
	}
	return time.Duration(med) * time.Millisecond
}

func (s *Source) HeaderTimeMs(height uint64, hash []byte) (int64, error) {
	if s == nil {
		return 0, fmt.Errorf("%w: no header time source", types.ErrHeaderPending)
	}
	if height == 0 || len(hash) == 0 {
		return 0, fmt.Errorf("%w: empty floor point", types.ErrHeaderPending)
	}
	floor := s.cachedFloor.Load()
	s.mu.Lock()
	e, ok := s.entries[height]
	if ok && (floor == 0 || height >= floor) {
		s.mu.Unlock()
		return checkEntry(height, hash, e)
	}
	filler := s.filler
	if filler == nil {
		s.mu.Unlock()
		return 0, fmt.Errorf("%w: height %d", types.ErrHeaderPending, height)
	}
	flight, running := s.inflight[height]
	if !running {
		flight = &fetchFlight{done: make(chan struct{})}
		s.inflight[height] = flight
		go s.fetchInto(height, flight, filler)
	}
	s.mu.Unlock()

	timer := time.NewTimer(s.missWait)
	defer timer.Stop()
	select {
	case <-flight.done:
	case <-timer.C:
		return 0, fmt.Errorf("%w: height %d: lookup still running", types.ErrHeaderPending, height)
	}
	return s.fetched(height, hash, flight)
}

func (s *Source) AwaitHeaderTimeMs(ctx context.Context, height uint64, hash []byte) (int64, error) {
	for {
		ms, err := s.HeaderTimeMs(height, hash)
		if !errors.Is(err, types.ErrHeaderPending) {
			return ms, err
		}
		s.mu.Lock()
		flight, running := s.inflight[height]
		s.mu.Unlock()
		if !running {
			return ms, err
		}
		select {
		case <-ctx.Done():
			return 0, err
		case <-flight.done:
		}
	}
}

func (s *Source) fetchInto(height uint64, flight *fetchFlight, filler Filler) {
	defer func() {
		s.mu.Lock()
		delete(s.inflight, height)
		s.mu.Unlock()
		close(flight.done)
	}()
	if filler == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), s.fetchTimeout)
	defer cancel()
	got, err := filler.Fill(ctx, int64(height))
	if err != nil || !headerStoreable(got.Header, height) {
		return
	}
	floor := s.writeFloor()
	s.mu.Lock()
	note, row, pruned := s.putLocked(height, got.Header.BlockHash, got.Header.Time, got.Origin, got.Verified, floor, 0)
	cache := s.cache
	restoring := s.restoring
	s.mu.Unlock()
	s.emit(note)
	if !restoring {
		var rows []Row
		if row != nil {
			rows = []Row{*row}
		}
		s.persistUnlocked(cache, rows, floor, pruned)
	}
	flight.ms = got.Header.Time.UnixMilli()
	flight.hash = append([]byte(nil), got.Header.BlockHash...)
	flight.hit = true
}

// fetched returns the stored header, or the filler result when the hard cap
// dropped that height before this read.
func (s *Source) fetched(height uint64, hash []byte, flight *fetchFlight) (int64, error) {
	s.mu.Lock()
	e, ok := s.entries[height]
	s.mu.Unlock()
	if ok {
		return checkEntry(height, hash, e)
	}
	if flight != nil && flight.hit {
		return checkEntry(height, hash, entry{hash: flight.hash, ms: flight.ms})
	}
	return 0, fmt.Errorf("%w: height %d", types.ErrHeaderPending, height)
}

func checkEntry(height uint64, hash []byte, e entry) (int64, error) {
	if !bytes.Equal(e.hash, hash) {
		return 0, fmt.Errorf("%w: height %d", types.ErrHeaderMismatch, height)
	}
	return e.ms, nil
}

func headerStoreable(h *blocks.Header, height uint64) bool {
	return h != nil && h.Height > 0 && h.Height == int64(height) && !blocks.IsDummyHeader(h) && !h.Time.IsZero() && len(h.BlockHash) > 0
}

func (s *Source) putLocked(height uint64, hash []byte, at time.Time, origin Origin, verified bool, floor uint64, epoch uint64) (conflict, *Row, bool) {
	if !headerStoreable(&blocks.Header{Height: int64(height), Time: at, BlockHash: hash}, height) {
		return conflict{}, nil, false
	}
	pruned := s.pruneFloorLocked(floor)
	if floor > 0 && height < floor {
		return conflict{}, nil, pruned
	}
	ms := at.UnixMilli()
	old, exists := s.entries[height]
	if !exists {
		s.entries[height] = entry{
			hash:     append([]byte(nil), hash...),
			ms:       ms,
			origin:   origin,
			verified: verified,
			epoch:    epoch,
		}
		s.order = append(s.order, height)
		s.noteMinLocked(height)
		s.evictCapacityLocked()
		if e, ok := s.entries[height]; ok {
			s.noteHeightLocked(height)
			s.refreshIntervalLocked()
			row := rowFromEntry(height, e)
			return conflict{}, &row, pruned
		}
		return conflict{}, nil, pruned
	}
	if bytes.Equal(old.hash, hash) {
		if s.sameHashRank(origin, verified) > s.sameHashRank(old.origin, old.verified) {
			old.origin = origin
		}
		if verified {
			old.verified = true
		}
		if epoch > 0 {
			old.epoch = epoch
		}
		s.entries[height] = old
		row := rowFromEntry(height, old)
		return conflict{}, &row, pruned
	}
	note := conflict{
		ok:       true,
		gateway:  s.gateway,
		height:   height,
		kept:     old.origin,
		incoming: origin,
		keptHash: append([]byte(nil), old.hash...),
		newHash:  append([]byte(nil), hash...),
	}
	if !s.gateway && originRank(old.origin) == originRank(OriginDAPI) && originRank(origin) > originRank(OriginDAPI) {
		note.replaced = true
		next := entry{
			hash:     append([]byte(nil), hash...),
			ms:       ms,
			origin:   origin,
			verified: verified,
			epoch:    epoch,
		}
		s.entries[height] = next
		row := rowFromEntry(height, next)
		return note, &row, pruned
	}
	row := rowFromEntry(height, old)
	return note, &row, pruned
}

// noteHeightLocked extends the tip run, or queues the gap under it.
// The caller holds s.mu. A height below the run is already stored. Its job
// fills from that anchor up to the catch floor. The floor moves when the job
// connects, not when the anchor is stored.
func (s *Source) noteHeightLocked(height uint64) {
	if s.runBase == 0 || s.tip == 0 {
		s.runBase = height
		s.tip = height
		s.catchFloor = height
		return
	}
	if height > s.tip {
		if height > s.tip+1 {
			s.enqueueForwardLocked(s.tip+1, height-1)
		}
		s.tip = height
		return
	}
	if height < s.runBase && (s.catchFloor == 0 || height < s.catchFloor) {
		if s.gapConnectedLocked(height) {
			s.catchFloor = height
		} else {
			s.enqueueBackwardLocked(height)
		}
	}
	s.pullBaseLocked()
}

// pullBaseLocked walks the contiguous run down through heights already stored.
func (s *Source) pullBaseLocked() {
	for s.runBase > 1 {
		if _, ok := s.entries[s.runBase-1]; !ok {
			return
		}
		s.runBase--
	}
}

// gapConnectedLocked reports that every height from anchor up to the catch
// floor is already stored. The caller holds s.mu.
func (s *Source) gapConnectedLocked(anchor uint64) bool {
	if anchor == 0 || s.catchFloor == 0 || anchor >= s.catchFloor {
		return false
	}
	for h := anchor; h < s.catchFloor; h++ {
		if _, ok := s.entries[h]; !ok {
			return false
		}
	}
	return true
}

// advanceCatchFloorLocked moves the catch floor down to anchor when the gap
// now connects. A queued retry above the new floor is dropped when it starts.
func (s *Source) advanceCatchFloorLocked(anchor uint64) bool {
	if !s.gapConnectedLocked(anchor) {
		return false
	}
	s.catchFloor = anchor
	s.pullBaseLocked()
	return true
}

func (s *Source) enqueueBackwardLocked(anchor uint64) {
	if s.filler == nil || anchor == 0 {
		return
	}
	for _, job := range s.catchQ {
		if !job.forward && job.anchor == anchor {
			return
		}
	}
	s.insertJobLocked(catchSpan{anchor: anchor})
}

func (s *Source) enqueueForwardLocked(from, to uint64) {
	if s.filler == nil || from == 0 || to < from {
		return
	}
	s.insertJobLocked(catchSpan{forward: true, from: from, to: to})
}

// insertJobLocked places a first attempt ahead of retries. A retry stays at
// the back, behind a forward hole and behind a lower range.
func (s *Source) insertJobLocked(job catchSpan) {
	if job.tries > 0 {
		s.catchQ = append(s.catchQ, job)
	} else {
		at := len(s.catchQ)
		for i, queued := range s.catchQ {
			if queued.tries > 0 {
				at = i
				break
			}
		}
		s.catchQ = append(s.catchQ, catchSpan{})
		copy(s.catchQ[at+1:], s.catchQ[at:])
		s.catchQ[at] = job
	}
	logging.Info("header time catch-up", "headertime", "anchor", job.anchor, "from", job.from, "to", job.to, "tries", job.tries)
	s.kickCatchLocked()
}

func (s *Source) retryLocked(job catchSpan) {
	job.tries++
	if job.tries >= catchMissLimit {
		return
	}
	s.catchQ = append(s.catchQ, job)
	logging.Info("header time catch-up retry", "headertime", "anchor", job.anchor, "from", job.from, "to", job.to, "tries", job.tries)
}

func (s *Source) kickCatchLocked() {
	if s.catchOn {
		return
	}
	for len(s.catchQ) > 0 {
		job := s.catchQ[0]
		s.catchQ = s.catchQ[1:]
		if !job.forward && (s.catchFloor == 0 || job.anchor == 0 || job.anchor >= s.catchFloor) {
			continue
		}
		s.catchOn = true
		go s.runCatch(job)
		return
	}
}

func (s *Source) runCatch(job catchSpan) {
	defer func() {
		s.mu.Lock()
		s.catchOn = false
		s.kickCatchLocked()
		s.mu.Unlock()
	}()
	if job.forward {
		s.runForward(job)
		return
	}
	s.runBackward(job)
}

// runBackward fills from the anchor up to the catch floor as it stands when
// the job starts. A miss is queued again behind work that can still be stored.
func (s *Source) runBackward(job catchSpan) {
	s.mu.Lock()
	floor := s.catchFloor
	anchor := job.anchor
	if anchor == 0 || floor == 0 || anchor >= floor {
		s.mu.Unlock()
		return
	}
	from := anchor + 1
	to := floor - 1
	s.mu.Unlock()
	if from <= to {
		s.fillSpanDown(from, to)
	}
	s.mu.Lock()
	if !s.advanceCatchFloorLocked(anchor) {
		s.retryLocked(job)
	}
	s.mu.Unlock()
}

func (s *Source) runForward(job catchSpan) {
	s.fillSpanUp(job.from, job.to)
	s.mu.Lock()
	if !s.spanStoredLocked(job.from, job.to) {
		s.retryLocked(job)
	}
	s.mu.Unlock()
}

func (s *Source) spanStoredLocked(from, to uint64) bool {
	if from == 0 || to < from {
		return true
	}
	for h := from; h <= to; h++ {
		if _, ok := s.entries[h]; !ok {
			return false
		}
	}
	return true
}

// fillSpanUp fills a hole above the run, low end first, so each apply extends
// the contiguous heights HighWater already walks. Stored heights are skipped.
func (s *Source) fillSpanUp(from, to uint64) {
	for from <= to && from != 0 {
		s.mu.Lock()
		start, end, ok := s.nextMissingLocked(from, to, catchUpBatch)
		s.mu.Unlock()
		if !ok {
			return
		}
		s.fillBatch(start, end)
		from = end + 1
	}
}

// fillSpanDown fills a hole below the run, high end first, so the first apply
// moves the run base down before the lower heights are stored. Stored heights
// are skipped.
func (s *Source) fillSpanDown(from, to uint64) {
	for to >= from && to != 0 {
		s.mu.Lock()
		start, end, ok := s.prevMissingLocked(from, to, catchUpBatch)
		s.mu.Unlock()
		if !ok {
			return
		}
		s.fillBatch(start, end)
		if start <= from {
			return
		}
		to = start - 1
	}
}

// nextMissingLocked is the next contiguous missing run in from..to, at most
// n heights. The caller holds s.mu.
func (s *Source) nextMissingLocked(from, to uint64, n int) (start, end uint64, ok bool) {
	if n <= 0 || from == 0 || to < from {
		return 0, 0, false
	}
	for h := from; h <= to; h++ {
		if _, stored := s.entries[h]; stored {
			continue
		}
		start = h
		end = h
		for i := 1; i < n && end < to; i++ {
			if _, stored := s.entries[end+1]; stored {
				break
			}
			end++
		}
		return start, end, true
	}
	return 0, 0, false
}

// prevMissingLocked is the last contiguous missing run in from..to, at most
// n heights. The caller holds s.mu.
func (s *Source) prevMissingLocked(from, to uint64, n int) (start, end uint64, ok bool) {
	if n <= 0 || from == 0 || to < from {
		return 0, 0, false
	}
	h := to
	for h >= from {
		if _, stored := s.entries[h]; stored {
			if h == 0 {
				break
			}
			h--
			continue
		}
		end = h
		start = h
		for i := 1; i < n && start > from; i++ {
			if _, stored := s.entries[start-1]; stored {
				break
			}
			start--
		}
		return start, end, true
	}
	return 0, 0, false
}

type cachedHeader struct {
	height   uint64
	hash     []byte
	at       time.Time
	origin   Origin
	verified bool
}

// fillBatch reads from..to into the job cache, then stores that cache in one apply.
// The requested height's own lookup does not use this path.
func (s *Source) fillBatch(from, to uint64) {
	s.mu.Lock()
	filler := s.filler
	s.mu.Unlock()
	if filler == nil {
		return
	}
	got := s.readBatch(from, to, filler)
	if len(got) == 0 {
		return
	}
	floor := s.writeFloor()
	s.mu.Lock()
	rows, pruned := s.applyCacheLocked(got, floor)
	cache := s.cache
	s.mu.Unlock()
	s.persistUnlocked(cache, rows, floor, pruned)
}

func (s *Source) batchTimeout() time.Duration {
	d := s.fetchTimeout
	if d <= 0 {
		d = DefaultFetchTimeout
	}
	return 4 * d
}

func (s *Source) readBatch(from, to uint64, filler Filler) []cachedHeader {
	ctx, cancel := context.WithTimeout(context.Background(), s.batchTimeout())
	defer cancel()
	if batch, ok := filler.(BatchFiller); ok {
		got, err := batch.FillBatch(ctx, int64(from), int64(to))
		if err != nil && len(got) == 0 {
			return nil
		}
		return cacheFromFills(got, from, to)
	}
	var out []cachedHeader
	for h := from; h <= to; h++ {
		if err := ctx.Err(); err != nil {
			break
		}
		s.mu.Lock()
		_, stored := s.entries[h]
		s.mu.Unlock()
		if stored {
			continue
		}
		got, err := filler.Fill(ctx, int64(h))
		if err != nil {
			continue
		}
		out = append(out, cacheFromFills([]Fill{got}, h, h)...)
	}
	return out
}

func cacheFromFills(got []Fill, from, to uint64) []cachedHeader {
	var out []cachedHeader
	for _, g := range got {
		if g.Header == nil || g.Header.Height <= 0 {
			continue
		}
		h := uint64(g.Header.Height)
		if h < from || h > to || !headerStoreable(g.Header, h) {
			continue
		}
		out = append(out, cachedHeader{
			height:   h,
			hash:     append([]byte(nil), g.Header.BlockHash...),
			at:       g.Header.Time,
			origin:   g.Origin,
			verified: g.Verified,
		})
	}
	return out
}

// applyCacheLocked stores every cached header, then lets the run base walk
// down through the heights that now connect. Eviction runs after that walk,
// so a height is not dropped while its neighbors are still only in the cache.
func (s *Source) applyCacheLocked(cache []cachedHeader, floor uint64) ([]Row, bool) {
	pruned := s.pruneFloorLocked(floor)
	var rows []Row
	for _, c := range cache {
		if floor > 0 && c.height < floor {
			continue
		}
		if _, ok := s.entries[c.height]; ok {
			continue
		}
		e := entry{
			hash:     append([]byte(nil), c.hash...),
			ms:       c.at.UnixMilli(),
			origin:   c.origin,
			verified: c.verified,
		}
		s.entries[c.height] = e
		s.order = append(s.order, c.height)
		s.noteMinLocked(c.height)
		if s.tip > 0 && c.height > s.tip {
			s.tip = c.height
		}
		rows = append(rows, rowFromEntry(c.height, e))
	}
	s.pullBaseLocked()
	s.evictCapacityLocked()
	s.refreshIntervalLocked()
	return rows, pruned
}

// evictCapacityLocked drops the lowest heights below the contiguous run, then
// the lowest heights inside it, until the map fits the hard cap.
func (s *Source) evictCapacityLocked() {
	if s.capacity <= 0 || len(s.entries) <= s.capacity {
		return
	}
	heights := make([]uint64, 0, len(s.entries))
	for h := range s.entries {
		heights = append(heights, h)
	}
	sort.Slice(heights, func(i, j int) bool {
		iBelow := s.runBase > 0 && heights[i] < s.runBase
		jBelow := s.runBase > 0 && heights[j] < s.runBase
		if iBelow != jBelow {
			return iBelow
		}
		return heights[i] < heights[j]
	})
	drop := len(s.entries) - s.capacity
	for _, h := range heights[:drop] {
		delete(s.entries, h)
	}
	s.rebuildOrderLocked()
	s.advanceBaseLocked()
	s.recomputeMinLocked()
	s.refreshIntervalLocked()
}

// advanceBaseLocked moves the run up when its base height was removed.
// A height still stored below the new base does not become the base.
func (s *Source) advanceBaseLocked() {
	if len(s.entries) == 0 {
		s.runBase = 0
		s.tip = 0
		s.catchFloor = 0
		s.minHeight = 0
		s.intervalGaps = s.intervalGaps[:0]
		return
	}
	if s.runBase == 0 {
		return
	}
	if _, ok := s.entries[s.runBase]; !ok {
		next := uint64(0)
		for h := range s.entries {
			if h > s.runBase && (next == 0 || h < next) {
				next = h
			}
		}
		if next == 0 {
			s.runBase = 0
			s.tip = 0
			s.catchFloor = 0
			return
		}
		s.runBase = next
	}
	if s.tip > 0 {
		if _, ok := s.entries[s.tip]; !ok {
			max := uint64(0)
			for h := range s.entries {
				if h >= s.runBase && h > max {
					max = h
				}
			}
			s.tip = max
		}
	}
}

func (s *Source) pruneFloorLocked(floor uint64) bool {
	if floor == 0 || s.minHeight == 0 || floor <= s.minHeight {
		return false
	}
	changed := false
	newMin := uint64(0)
	for h := range s.entries {
		if h < floor {
			delete(s.entries, h)
			changed = true
			continue
		}
		if newMin == 0 || h < newMin {
			newMin = h
		}
	}
	if !changed {
		return false
	}
	s.minHeight = newMin
	s.rebuildOrderLocked()
	s.advanceBaseLocked()
	s.refreshIntervalLocked()
	return true
}

func (s *Source) noteMinLocked(height uint64) {
	if s.minHeight == 0 || height < s.minHeight {
		s.minHeight = height
	}
}

func (s *Source) recomputeMinLocked() {
	min := uint64(0)
	for h := range s.entries {
		if min == 0 || h < min {
			min = h
		}
	}
	s.minHeight = min
}

func (s *Source) refreshIntervalLocked() {
	s.intervalGaps = s.intervalGaps[:0]
	if s.tip == 0 || s.runBase == 0 || s.tip <= s.runBase {
		return
	}
	h := s.tip
	var newest [blockIntervalSample]int64
	n := 0
	for n < blockIntervalSample && h > s.runBase {
		cur, ok1 := s.entries[h]
		prev, ok2 := s.entries[h-1]
		if !ok1 || !ok2 {
			break
		}
		d := cur.ms - prev.ms
		if d >= 0 {
			newest[n] = d
			n++
		}
		h--
	}
	for i := n - 1; i >= 0; i-- {
		s.intervalGaps = append(s.intervalGaps, newest[i])
	}
}

func (s *Source) rebuildOrderLocked() {
	next := make([]uint64, 0, len(s.entries))
	for _, h := range s.order {
		if _, ok := s.entries[h]; ok {
			next = append(next, h)
		}
	}
	s.order = next
}

// SetFloor replaces the window function and applies it once. Lookups use
// the cached value until the next write.
func (s *Source) SetFloor(fn func() uint64) {
	if s == nil {
		return
	}
	s.floorMu.Lock()
	s.floor = fn
	s.floorMu.Unlock()
	if fn == nil {
		s.SetFloorHeight(0)
		return
	}
	s.SetFloorHeight(fn())
}

// SetFloorHeight caches floor and drops stored heights below it. The poller
// calls this when a BLOCK moves the epoch window.
func (s *Source) SetFloorHeight(floor uint64) {
	if s == nil {
		return
	}
	s.cachedFloor.Store(floor)
	s.mu.Lock()
	pruned := s.pruneFloorLocked(floor)
	cache := s.cache
	s.mu.Unlock()
	if pruned {
		s.persistUnlocked(cache, nil, floor, true)
	}
}

// Floor is the cached retention floor. It does not sample the floor function.
func (s *Source) Floor() uint64 {
	if s == nil {
		return 0
	}
	return s.cachedFloor.Load()
}

func (s *Source) writeFloor() uint64 {
	if s == nil {
		return 0
	}
	s.floorMu.Lock()
	fn := s.floor
	s.floorMu.Unlock()
	if fn == nil {
		return s.cachedFloor.Load()
	}
	v := fn()
	s.cachedFloor.Store(v)
	return v
}

// SetCache restores rows from c into the map, then persists later writes.
// A previous cache is closed. Nil detaches persistence.
func (s *Source) SetCache(c Cache) {
	if s == nil {
		return
	}
	s.mu.Lock()
	prev := s.cache
	s.cache = c
	s.mu.Unlock()
	if prev != nil && prev != c {
		_ = prev.Close()
	}
	if c == nil {
		return
	}
	rows, err := c.Load(context.Background())
	if err != nil {
		logging.Warn("header time cache load", "headertime", "err", err)
		return
	}
	floor := s.writeFloor()
	s.mu.Lock()
	s.restoring = true
	for _, r := range rows {
		if r.Height == 0 {
			continue
		}
		s.putLocked(r.Height, r.Hash, time.UnixMilli(r.TimeMs).UTC(), r.Origin, r.Verified, floor, r.Epoch)
	}
	pruned := s.pruneFloorLocked(floor)
	s.restoring = false
	s.mu.Unlock()
	if pruned {
		s.persistUnlocked(c, nil, floor, true)
	}
}

// CloseCache detaches and closes the on-disk cache.
func (s *Source) CloseCache() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	c := s.cache
	s.cache = nil
	s.mu.Unlock()
	if c == nil {
		return nil
	}
	return c.Close()
}

// StoredEpochFloor is EpochFloor from the epochs already stored. epoch 0
// rows (Comet, catch-up) are ignored. Unknown previous start returns 0.
func (s *Source) StoredEpochFloor() uint64 {
	if s == nil {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	maxEpoch := uint64(0)
	mins := make(map[uint64]uint64)
	for h, e := range s.entries {
		if e.epoch == 0 {
			continue
		}
		if e.epoch > maxEpoch {
			maxEpoch = e.epoch
		}
		if m, ok := mins[e.epoch]; !ok || h < m {
			mins[e.epoch] = h
		}
	}
	return EpochFloor(maxEpoch, mins[maxEpoch-1])
}

func rowFromEntry(height uint64, e entry) Row {
	return Row{
		Height:   height,
		Hash:     append([]byte(nil), e.hash...),
		TimeMs:   e.ms,
		Origin:   e.origin,
		Verified: e.verified,
		Epoch:    e.epoch,
	}
}

func (s *Source) persistUnlocked(cache Cache, rows []Row, floor uint64, pruned bool) {
	if cache == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if len(rows) > 0 {
		if err := cache.Save(ctx, rows); err != nil {
			logging.Warn("header time cache save", "headertime", "err", err)
		}
	}
	if pruned && floor > 0 {
		if err := cache.DeleteBelow(ctx, floor); err != nil {
			logging.Warn("header time cache prune", "headertime", "err", err)
		}
	}
}

func (s *Source) highWaterLocked() uint64 {
	if len(s.entries) == 0 {
		return 0
	}
	start := s.runBase
	if start == 0 {
		return 0
	}
	if _, ok := s.entries[start]; !ok {
		return 0
	}
	h := start
	for {
		if _, ok := s.entries[h+1]; !ok {
			return h
		}
		if h == ^uint64(0)-1 {
			return h + 1
		}
		h++
	}
}

func originRank(o Origin) int {
	switch o {
	case OriginComet, OriginChain:
		return 2
	default:
		return 1
	}
}

// sameHashRank is originRank, except a verified dapi header on the gateway
// ranks above chain so a filler read of the same hash does not relabel it and
// a later dapi page can mark a comet-verified tip as dapi.
func (s *Source) sameHashRank(o Origin, verified bool) int {
	if s != nil && s.gateway && verified && o == OriginDAPI {
		return originRank(OriginChain) + 1
	}
	return originRank(o)
}

type conflict struct {
	ok       bool
	replaced bool
	gateway  bool
	height   uint64
	kept     Origin
	incoming Origin
	keptHash []byte
	newHash  []byte
}

func (s *Source) emit(c conflict) {
	if s == nil || !c.ok {
		return
	}
	s.conflicts.Add(1)
	result := "kept"
	if c.replaced {
		result = "replaced"
	}
	hashConflicts.WithLabelValues(result).Inc()
	args := []any{
		"height", c.height,
		"kept_origin", string(c.kept),
		"new_origin", string(c.incoming),
		"replaced", c.replaced,
		"kept_hash", hex.EncodeToString(c.keptHash),
		"new_hash", hex.EncodeToString(c.newHash),
	}
	if c.gateway {
		logging.Error("header time hash conflict", "headertime", args...)
		return
	}
	logging.Warn("header time hash conflict", "headertime", args...)
}

var hashConflicts = prometheus.NewCounterVec(prometheus.CounterOpts{
	Name: "devshard_headertime_hash_conflicts_total",
	Help: "Header-time writes whose hash disagreed with the header already stored at that height.",
}, []string{"result"})

func init() {
	_ = prometheus.Register(hashConflicts)
}

func medianGap(gaps []int64) int64 {
	sorted := append([]int64(nil), gaps...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	n := len(sorted)
	if n%2 == 1 {
		return sorted[n/2]
	}
	return (sorted[n/2-1] + sorted[n/2]) / 2
}
