package observer

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"common/chainoracle/blocks"
	"common/logging"

	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	cmttypes "github.com/cometbft/cometbft/types"
)

var (
	// ErrDAPIHeader is a dapi BLOCK whose header fails the self-check.
	ErrDAPIHeader = errors.New("observer: dapi header failed self-check")
	// ErrHeaderPendingLink is a height held in the dapi buffer until it links
	// down from a verified header, or until the link budget expires.
	ErrHeaderPendingLink = fmt.Errorf("%w: waiting for the link from the trusted header", blocks.ErrHeaderNotFound)

	errNoVerifier = errors.New("observer: dapi blocks need a verifying oracle")
)

// DAPIBlock is one dapi BLOCK payload. Header is the encoded cmtproto.Header.
type DAPIBlock struct {
	Height int64
	Hash   []byte
	TimeMs int64
	Header []byte
}

type pendingHeader struct {
	hdr     *cmttypes.Header
	hash    []byte
	arrived time.Time
}

// SetPublish receives every verified header this oracle stores. fromDAPI is
// true for a header that came from the dapi buffer.
func (o *Oracle) SetPublish(fn func(h *blocks.Header, fromDAPI bool)) {
	if o == nil {
		return
	}
	o.hookMu.Lock()
	o.publish = fn
	o.hookMu.Unlock()
}

// SetPendingFloor bounds the dapi buffer from below. It is applied once the
// trusted header is known. It is called without any oracle lock held.
func (o *Oracle) SetPendingFloor(fn func() int64) {
	if o == nil {
		return
	}
	o.hookMu.Lock()
	o.pendFloor = fn
	o.hookMu.Unlock()
}

// SetOnDAPIFailure is called after a rejected or unlinked dapi header.
func (o *Oracle) SetOnDAPIFailure(fn func(error)) {
	if o == nil {
		return
	}
	o.hookMu.Lock()
	o.onFailure = fn
	o.hookMu.Unlock()
}

// FromDAPI is true when height was stored from a dapi BLOCK.
func (o *Oracle) FromDAPI(height int64) bool {
	if o == nil || o.history == nil {
		return false
	}
	e, ok := o.history.entry(height)
	return ok && e.fromDAPI
}

// DAPIFailures counts dapi headers rejected by the self-check, a broken
// back-link, or a verified header with a different hash.
func (o *Oracle) DAPIFailures() uint64 {
	if o == nil {
		return 0
	}
	return o.failures.Load()
}

// Trusted returns the trusted header, reading it from the RPC on first use.
// It publishes that header the first time its height is seen.
func (o *Oracle) Trusted(ctx context.Context) (*blocks.Header, error) {
	if o == nil || o.verifier == nil {
		return nil, errNoVerifier
	}
	hdr, err := o.verifier.ensureTrusted(ctx)
	if err != nil {
		return nil, err
	}
	o.rememberTrusted(hdr)
	return o.headerFromRow(verifiedFromHeader(hdr)), nil
}

func (o *Oracle) trust(ctx context.Context) (int64, error) {
	h, err := o.Trusted(ctx)
	if err != nil {
		return 0, err
	}
	return h.Height, nil
}

// OfferDAPI self-checks one page of dapi BLOCK events and holds the rest
// until they link down from a verified header. It stores the page and
// returns. The newest height at or above the trusted header goes on the
// verifier queue for one commit check. Heights below it signal the verifier
// goroutine to link down from the trusted header with no RPC.
func (o *Oracle) OfferDAPI(page []DAPIBlock) error {
	if o == nil || o.verifier == nil {
		return errNoVerifier
	}
	floor := o.pendingFloorNow()
	expect := o.expectedChainID()
	var errs []error
	added := false
	o.pendMu.Lock()
	for _, b := range page {
		hdr, err := selfCheck(b, expect)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if floor > 0 && b.Height < floor {
			continue
		}
		arrived := o.clock()
		if prev, ok := o.pending[b.Height]; ok && !prev.arrived.IsZero() {
			arrived = prev.arrived
		}
		o.pending[b.Height] = pendingHeader{hdr: hdr, hash: append([]byte(nil), b.Hash...), arrived: arrived}
		added = true
	}
	o.dropBelowLocked(floor)
	top := int64(0)
	for h := range o.pending {
		top = max(top, h)
	}
	o.pendMu.Unlock()
	for _, err := range errs {
		o.fail(err)
	}
	if added && top > 0 {
		if trusted := o.verifier.trustedHeight(); trusted == 0 || top >= trusted {
			o.queueVerify(top)
		} else {
			o.queueLink()
		}
	}
	return errors.Join(errs...)
}

func selfCheck(b DAPIBlock, chainID string) (*cmttypes.Header, error) {
	if len(b.Header) == 0 {
		return nil, fmt.Errorf("%w: height %d has no header", ErrDAPIHeader, b.Height)
	}
	var pb cmtproto.Header
	if err := pb.Unmarshal(b.Header); err != nil {
		return nil, fmt.Errorf("%w: height %d: %v", ErrDAPIHeader, b.Height, err)
	}
	hdr, err := cmttypes.HeaderFromProto(&pb)
	if err != nil {
		return nil, fmt.Errorf("%w: height %d: %v", ErrDAPIHeader, b.Height, err)
	}
	if hdr.Height != b.Height {
		return nil, fmt.Errorf("%w: header height %d, event height %d", ErrDAPIHeader, hdr.Height, b.Height)
	}
	if !bytes.Equal(hdr.Hash(), b.Hash) {
		return nil, fmt.Errorf("%w: height %d does not hash to the event hash", ErrDAPIHeader, b.Height)
	}
	if hdr.Time.UnixMilli() != b.TimeMs {
		return nil, fmt.Errorf("%w: height %d header time %d, event time %d", ErrDAPIHeader, b.Height, hdr.Time.UnixMilli(), b.TimeMs)
	}
	if chainID != "" && hdr.ChainID != chainID {
		return nil, fmt.Errorf("%w: height %d is on chain %q", ErrDAPIHeader, b.Height, hdr.ChainID)
	}
	return &hdr, nil
}

// linkPending walks the buffer down from verified headers. A height whose
// header above is verified, and whose hash that header links to, is verified.
// A height still missing its header above stays pending.
func (o *Oracle) linkPending() {
	if o == nil || o.verifier == nil {
		return
	}
	floor := o.pendingFloorNow()
	o.fillMu.Lock()
	defer o.fillMu.Unlock()
	o.pendMu.Lock()
	o.dropBelowLocked(floor)
	heights := make([]int64, 0, len(o.pending))
	for h := range o.pending {
		heights = append(heights, h)
	}
	o.pendMu.Unlock()
	sort.Slice(heights, func(i, j int) bool { return heights[i] > heights[j] })
	chainID := o.history.ChainID()
	for _, height := range heights {
		p, ok := o.pendingAt(height)
		if !ok {
			continue
		}
		if have, ok := o.history.entry(height); ok {
			if bytes.Equal(have.hash, p.hash) {
				row := verifiedFromHeader(p.hdr)
				row.fromDAPI = true
				o.remember(row)
				o.dropPending(height)
				continue
			}
			o.breakAt(height, fmt.Errorf("%w: dapi header %d differs from the verified header", ErrUnverifiedHeader, height))
			return
		}
		above, ok := o.history.entry(height + 1)
		if !ok || len(above.prevHash) == 0 {
			continue
		}
		if err := verifyBackwardLink(p.hdr, above, height+1, chainID); err != nil {
			o.breakAt(height, err)
			return
		}
		row := verifiedFromHeader(p.hdr)
		row.fromDAPI = true
		o.remember(row)
		o.dropPending(height)
	}
}

// breakAt drops height and every pending header below it, reports the
// failure, and reads the dropped heights from the RPC in the background.
func (o *Oracle) breakAt(height int64, err error) {
	lowest := int64(0)
	o.pendMu.Lock()
	for h := range o.pending {
		if h <= height {
			delete(o.pending, h)
			if lowest == 0 || h < lowest {
				lowest = h
			}
		}
	}
	o.pendMu.Unlock()
	o.fail(err)
	if lowest > 0 {
		go o.fillHole(lowest)
	}
}

func (o *Oracle) fillHole(from int64) {
	ctx, cancel := context.WithTimeout(context.Background(), historyStoreTimeout)
	defer cancel()
	if _, err := o.fillDown(ctx, from); err != nil {
		logging.Warn("chainoracle: fill dropped dapi heights", "chainoracle", "from", from, "error", err)
	}
}

func (o *Oracle) fail(err error) {
	o.failures.Add(1)
	logging.Error("chainoracle: dapi header rejected", "chainoracle", "error", err)
	o.hookMu.RLock()
	fn := o.onFailure
	o.hookMu.RUnlock()
	if fn != nil {
		fn(err)
	}
}

func (o *Oracle) publishRows(rows []StoredHeader) {
	if o.verifier == nil || len(rows) == 0 {
		return
	}
	o.hookMu.RLock()
	fn := o.publish
	o.hookMu.RUnlock()
	if fn == nil {
		return
	}
	for _, r := range rows {
		if r.Verified {
			fn(o.headerFromRow(r), r.fromDAPI)
		}
	}
}

func (o *Oracle) pendingFloorNow() int64 {
	if o.verifier == nil || o.verifier.trustedHeight() == 0 {
		return 0
	}
	o.hookMu.RLock()
	fn := o.pendFloor
	o.hookMu.RUnlock()
	if fn == nil {
		return 0
	}
	return fn()
}

func (o *Oracle) expectedChainID() string {
	if o.chainID != "" {
		return o.chainID
	}
	return o.history.ChainID()
}

func (o *Oracle) dropBelowLocked(floor int64) {
	if floor <= 0 {
		return
	}
	for h := range o.pending {
		if h < floor {
			delete(o.pending, h)
		}
	}
}

func (o *Oracle) pendingAt(height int64) (pendingHeader, bool) {
	o.pendMu.Lock()
	defer o.pendMu.Unlock()
	p, ok := o.pending[height]
	return p, ok
}

func (o *Oracle) pendingHas(height int64) bool {
	_, ok := o.pendingAt(height)
	return ok
}

func (o *Oracle) pendingWaiting(height int64) bool {
	p, ok := o.pendingAt(height)
	if !ok {
		return false
	}
	return o.clock().Sub(p.arrived) < o.pendingLinkBudget()
}

func (o *Oracle) pendingLowest() int64 {
	o.pendMu.Lock()
	defer o.pendMu.Unlock()
	lowest := int64(0)
	for h := range o.pending {
		if lowest == 0 || h < lowest {
			lowest = h
		}
	}
	return lowest
}

func (o *Oracle) dropPending(height int64) {
	o.pendMu.Lock()
	delete(o.pending, height)
	o.pendMu.Unlock()
}

// missingFrom raises lo above the highest buffered height in [lo, hi), so an
// RPC read covers only heights the buffer does not have.
func (o *Oracle) missingFrom(lo, hi int64) int64 {
	o.pendMu.Lock()
	defer o.pendMu.Unlock()
	for h := hi - 1; h >= lo; h-- {
		if _, ok := o.pending[h]; ok {
			return h + 1
		}
	}
	return lo
}

func (o *Oracle) pendingLen() int {
	o.pendMu.Lock()
	defer o.pendMu.Unlock()
	return len(o.pending)
}
