package bridge

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"common/chain"

	inferencetypes "github.com/productscience/inference/x/inference/types"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// WarmKeyEntry is one entry of a participant's warm-key log on chain: a grant
// of the warm-key marker from Granter to Grantee, with the id the module gave
// it. Entries are append-only on chain, so an entry with a given (Granter, ID)
// never changes except to end. See docs/warm-keys-v7.md.
type WarmKeyEntry struct {
	Granter         string
	ID              uint64
	Grantee         string
	GrantedAtHeight int64
	RevokedAtHeight int64
	ExpiresAtUnix   int64
	// End names how the entry ended: "" while live, otherwise the chain's
	// WarmKeyLogEnd name (REVOKED, REPLACED, EXPIRED).
	End string
}

// Ended reports whether the chain has ended the entry.
func (e WarmKeyEntry) Ended() bool { return e.RevokedAtHeight != 0 || e.End != "" }

// LiveAt reports whether the entry is live at now: not ended on chain and not
// past its expiration. The chain ends an expired entry in the block whose
// time passes the expiration; a copy that has not seen that block yet reads
// the expiration itself so it never counts a lapsed grant as live.
func (e WarmKeyEntry) LiveAt(now time.Time) bool {
	if e.Ended() {
		return false
	}
	return e.ExpiresAtUnix == 0 || e.ExpiresAtUnix > now.Unix()
}

// WarmKeyGrantSource is the grant source: the view of the chain's warm-key
// log that the gateway proposes binds and revokes from and that hosts confirm
// them against and use for local admission.
type WarmKeyGrantSource interface {
	// LiveWarmKeys returns the live entries of granter.
	LiveWarmKeys(granter string) ([]WarmKeyEntry, error)
	// WarmKeyEntry returns entry id of granter, found=false when the log has
	// no such entry.
	WarmKeyEntry(granter string, id uint64) (entry WarmKeyEntry, found bool, err error)
}

// ErrWarmKeyEntryMismatch is a bind whose named log entry exists but is not a
// grant of the named key: a gateway fault, never a transient condition.
var ErrWarmKeyEntryMismatch = errors.New("warm-key log entry does not name this grantee")

// ErrWarmKeyEntryNotFound is a bind naming an entry the log does not have. A
// host that is behind the chain sees it transiently; the confirmer retries
// before it reports.
var ErrWarmKeyEntryNotFound = errors.New("warm-key log entry not found")

// ConfirmWarmKeyBind is the check every host runs on a MsgWarmKeyBind: entry
// id of cold exists in the log and names warm as its grantee. Whether the
// entry is live is not part of it: the rule is nonce-only and the answer must
// be the same whenever it is asked.
func ConfirmWarmKeyBind(src WarmKeyGrantSource, cold, warm string, id uint64) error {
	entry, found, err := src.WarmKeyEntry(cold, id)
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("%w: %s/%d", ErrWarmKeyEntryNotFound, cold, id)
	}
	if entry.Grantee != warm {
		return fmt.Errorf("%w: %s/%d names %s, bind names %s", ErrWarmKeyEntryMismatch, cold, id, entry.Grantee, warm)
	}
	return nil
}

// IsWarmKeyLive reports whether warm holds a live grant from cold according
// to src. A miss on a source that can reload (the log copy) schedules a
// reload, so a grant the events did not deliver is found on the next ask.
func IsWarmKeyLive(src WarmKeyGrantSource, cold, warm string, now time.Time) (bool, error) {
	live, err := src.LiveWarmKeys(cold)
	if err != nil {
		return false, err
	}
	for _, e := range live {
		if e.Grantee == warm && e.LiveAt(now) {
			return true, nil
		}
	}
	if n, ok := src.(interface{ NoteMiss(granter string) }); ok {
		n.NoteMiss(cold)
	}
	return false, nil
}

// WarmKeyLogFetcher reads the warm-key log from the chain.
type WarmKeyLogFetcher interface {
	FetchWarmKeyLog(ctx context.Context, granter string) ([]WarmKeyEntry, error)
	FetchWarmKeyEntry(ctx context.Context, granter string, id uint64) (WarmKeyEntry, bool, error)
}

// ChainWarmKeyFetcher is a WarmKeyLogFetcher over the inference query client.
type ChainWarmKeyFetcher struct {
	client chain.InferenceClient
}

func NewChainWarmKeyFetcher(client chain.InferenceClient) *ChainWarmKeyFetcher {
	return &ChainWarmKeyFetcher{client: client}
}

func (f *ChainWarmKeyFetcher) FetchWarmKeyLog(ctx context.Context, granter string) ([]WarmKeyEntry, error) {
	if f == nil || f.client == nil {
		return nil, fmt.Errorf("warm-key log: chain client is nil")
	}
	resp, err := f.client.WarmKeyLog(ctx, &inferencetypes.QueryWarmKeyLogRequest{Granter: granter})
	if err != nil {
		return nil, ClassifyQueryError(fmt.Errorf("WarmKeyLog %s: %w", granter, err))
	}
	out := make([]WarmKeyEntry, 0, len(resp.GetEntries()))
	for _, e := range resp.GetEntries() {
		if e != nil {
			out = append(out, warmKeyEntryFromQuery(e))
		}
	}
	return out, nil
}

func (f *ChainWarmKeyFetcher) FetchWarmKeyEntry(ctx context.Context, granter string, id uint64) (WarmKeyEntry, bool, error) {
	if f == nil || f.client == nil {
		return WarmKeyEntry{}, false, fmt.Errorf("warm-key log: chain client is nil")
	}
	resp, err := f.client.WarmKeyEntry(ctx, &inferencetypes.QueryWarmKeyEntryRequest{Granter: granter, Id: id})
	if status.Code(err) == codes.NotFound {
		return WarmKeyEntry{}, false, nil
	}
	if err != nil {
		return WarmKeyEntry{}, false, ClassifyQueryError(fmt.Errorf("WarmKeyEntry %s/%d: %w", granter, id, err))
	}
	if resp.GetEntry() == nil {
		return WarmKeyEntry{}, false, nil
	}
	return warmKeyEntryFromQuery(resp.GetEntry()), true, nil
}

func warmKeyEntryFromQuery(e *inferencetypes.WarmKeyLogEntry) WarmKeyEntry {
	out := WarmKeyEntry{
		Granter:         e.GetGranter(),
		ID:              e.GetId(),
		Grantee:         e.GetGrantee(),
		GrantedAtHeight: e.GetGrantedAtHeight(),
		RevokedAtHeight: e.GetRevokedAtHeight(),
		ExpiresAtUnix:   e.GetExpiresAtUnix(),
	}
	if e.GetEnd() != inferencetypes.WarmKeyLogEnd_WARM_KEY_LOG_END_NONE {
		out.End = e.GetEnd().String()
	}
	return out
}

const (
	// DefaultWarmKeyRefresh is how old a granter's copy may be before an
	// access reloads it from the chain in the background. The copy is
	// append-only, so staleness only delays a new grant or an end; it never
	// answers wrongly about an entry it has.
	DefaultWarmKeyRefresh = 30 * time.Second
	// warmKeyMissRefresh bounds how often a miss (a key the copy does not
	// hold a live grant for) may trigger a reload of the granter's log, so a
	// stream of unknown keys cannot turn into a stream of chain queries.
	warmKeyMissRefresh = 5 * time.Second
	// warmKeyFetchTimeout bounds one chain read. Hosts confirm binds from
	// inside diff application, so a stalled node must not hold the escrow.
	warmKeyFetchTimeout = 10 * time.Second
	// warmKeyEntryWait is how long a host waits for an entry its chain node
	// has not served yet before refusing the bind: the gateway proposed it
	// from a node that has the entry, so a node a few blocks behind catches
	// up within this.
	warmKeyEntryWait = 30 * time.Second
	warmKeyEntryPoll = 2 * time.Second
	// warmKeyRetryMin and warmKeyRetryMax bound the background retry of a
	// failed log read. The delay doubles on each failure and resets when a
	// read succeeds.
	warmKeyRetryMin = warmKeyMissRefresh
	warmKeyRetryMax = time.Minute
)

// WarmKeyLogCopy is an in-memory copy of the chain's warm-key log, one
// append-only list per granter, filled from the chain on first use and kept
// current by host events (Apply) and periodic reloads. It implements
// WarmKeyGrantSource for the gateway and the hosts.
type WarmKeyLogCopy struct {
	fetch        WarmKeyLogFetcher
	refreshAfter time.Duration
	now          func() time.Time
	// after runs f once d has passed. It schedules the retry of a failed read.
	after func(d time.Duration, f func())

	mu       sync.Mutex
	granters map[string]*granterLog
	loading  map[string]chan struct{}
	// resetGen advances on every Reset. A load marks its granter complete
	// only when this is still the generation the load started in, so a
	// snapshot fetched before the reset cannot restore completeness.
	resetGen uint64
}

type granterLog struct {
	entries  map[uint64]WarmKeyEntry
	loadedAt time.Time
	// attemptedAt is the start of the last reload, successful or not, so a
	// failing chain does not turn misses into a reload per request.
	attemptedAt time.Time
	// complete is false until the granter's whole log was read from the
	// chain; Apply alone (events for a granter never loaded) leaves holes.
	// Reset clears it again; loadedAt stays, so the entries keep answering.
	complete bool
	// retryArmed is set while a retry of a failed read is scheduled, so a
	// granter has at most one. retryDelay is the delay that retry used.
	retryArmed bool
	retryDelay time.Duration
}

// NewWarmKeyLogCopy returns an empty copy over fetch. refreshAfter <= 0 uses
// DefaultWarmKeyRefresh.
func NewWarmKeyLogCopy(fetch WarmKeyLogFetcher, refreshAfter time.Duration) *WarmKeyLogCopy {
	if refreshAfter <= 0 {
		refreshAfter = DefaultWarmKeyRefresh
	}
	return &WarmKeyLogCopy{
		fetch:        fetch,
		refreshAfter: refreshAfter,
		now:          time.Now,
		after:        func(d time.Duration, f func()) { time.AfterFunc(d, f) },
		granters:     make(map[string]*granterLog),
		loading:      make(map[string]chan struct{}),
	}
}

// LiveWarmKeys returns the live entries of granter in id order. A granter the
// copy has not loaded is read from the chain first; a granter whose copy is
// older than refreshAfter is answered from the copy while a reload runs in
// the background.
func (c *WarmKeyLogCopy) LiveWarmKeys(granter string) ([]WarmKeyEntry, error) {
	log, err := c.loaded(granter)
	if err != nil {
		return nil, err
	}
	now := c.now()
	var out []WarmKeyEntry
	for _, e := range log {
		if e.LiveAt(now) {
			out = append(out, e)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// WarmKeyEntry returns entry id of granter. An id the copy does not have is
// read from the chain directly, waiting a bounded time for a node that is
// behind, and stored on success.
func (c *WarmKeyLogCopy) WarmKeyEntry(granter string, id uint64) (WarmKeyEntry, bool, error) {
	if e, ok := c.cached(granter, id); ok {
		return e, true, nil
	}
	if c.fetch == nil {
		return WarmKeyEntry{}, false, nil
	}
	deadline := c.now().Add(warmKeyEntryWait)
	for {
		ctx, cancel := context.WithTimeout(context.Background(), warmKeyFetchTimeout)
		e, found, err := c.fetch.FetchWarmKeyEntry(ctx, granter, id)
		cancel()
		if err != nil {
			return WarmKeyEntry{}, false, err
		}
		if found {
			c.apply(e)
			return e, true, nil
		}
		if !c.now().Before(deadline) {
			return WarmKeyEntry{}, false, nil
		}
		time.Sleep(warmKeyEntryPoll)
	}
}

// Apply records an entry delivered by an event: a new grant, or the end of
// one the copy has. The copy stays append-only: an entry is only ever added
// or ended, never removed.
func (c *WarmKeyLogCopy) Apply(e WarmKeyEntry) {
	c.apply(e)
}

// NoteMiss records that a key was asked for and not found live under
// granter. The copy may be behind the chain (an old dapi serves no warm-key
// events), so a reload is started in the background, at most once per
// warmKeyMissRefresh per granter.
func (c *WarmKeyLogCopy) NoteMiss(granter string) {
	c.mu.Lock()
	g := c.granters[granter]
	due := g != nil && g.complete && c.now().Sub(g.attemptedAt) > warmKeyMissRefresh
	if due {
		g.attemptedAt = c.now()
	}
	c.mu.Unlock()
	if due {
		go func() { _ = c.load(granter, true) }()
	}
}

// Reset forgets completeness: every granter is reloaded from the chain on
// its next use. Called when the host-events stream resets, because events
// may have been lost in the gap. Entries already stored stay; an ended id
// is not revived by a later load. A granter loaded before keeps answering
// from those entries while its reload runs in the background.
func (c *WarmKeyLogCopy) Reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.resetGen++
	for _, g := range c.granters {
		g.complete = false
		g.attemptedAt = time.Time{}
	}
}

// HasUnendedGrant reports whether warm is named by a grant from cold that the
// log has not ended. Expiration is not part of the answer: hosts applying the
// same diff must agree when their clocks sit on opposite sides of it.
func (c *WarmKeyLogCopy) HasUnendedGrant(cold, warm string) (bool, error) {
	if c == nil {
		return false, fmt.Errorf("warm-key log: copy is nil")
	}
	entries, err := c.loaded(cold)
	if err != nil {
		return false, err
	}
	for _, e := range entries {
		if e.Grantee == warm && !e.Ended() {
			return true, nil
		}
	}
	c.NoteMiss(cold)
	return false, nil
}

// ApplyWarmKeyCheck is the state-machine resolver. It uses ConsensusWarmKey
// when the bridge has it, so diff application ignores grant expiration.
// VerifyWarmKey stays the local-admission check, which still uses the clock.
func ApplyWarmKeyCheck(b MainnetBridge) func(warmAddress, validatorAddress string) (bool, error) {
	type consensusWarmKey interface {
		ConsensusWarmKey(warmAddress, validatorAddress string) (bool, error)
	}
	if c, ok := b.(consensusWarmKey); ok {
		return c.ConsensusWarmKey
	}
	return b.VerifyWarmKey
}

// KnownGranters returns the granters the copy has already seen, in order.
// A granter that has never been applied or loaded is absent.
func (c *WarmKeyLogCopy) KnownGranters() []string {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]string, 0, len(c.granters))
	for g := range c.granters {
		out = append(out, g)
	}
	sort.Strings(out)
	return out
}

// ReloadKnown reads WarmKeyLog once per granter already in the copy.
// A failed read leaves the stored entries in place and does not mark the
// granter complete, so the miss is not cached as "not a grant".
func (c *WarmKeyLogCopy) ReloadKnown() {
	if c == nil {
		return
	}
	for _, g := range c.KnownGranters() {
		_ = c.load(g, false)
	}
}

func (c *WarmKeyLogCopy) apply(e WarmKeyEntry) {
	c.mu.Lock()
	defer c.mu.Unlock()
	g := c.granters[e.Granter]
	if g == nil {
		g = &granterLog{entries: make(map[uint64]WarmKeyEntry)}
		c.granters[e.Granter] = g
	}
	if have, ok := g.entries[e.ID]; ok {
		if have.Ended() && !e.Ended() {
			// An end is final; an older "granted" event replayed after it must
			// not revive the entry.
			return
		}
		if e.Ended() {
			e = mergeWarmKeyEnd(have, e)
		}
	}
	g.entries[e.ID] = e
}

// mergeWarmKeyEnd keeps the grant fields a revoke event does not carry.
// The event names the end height and the reason. The grant height and the
// expiration stay as they were recorded.
func mergeWarmKeyEnd(have, end WarmKeyEntry) WarmKeyEntry {
	if end.GrantedAtHeight == 0 {
		end.GrantedAtHeight = have.GrantedAtHeight
	}
	if end.ExpiresAtUnix == 0 {
		end.ExpiresAtUnix = have.ExpiresAtUnix
	}
	return end
}

func (c *WarmKeyLogCopy) cached(granter string, id uint64) (WarmKeyEntry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	g := c.granters[granter]
	if g == nil {
		return WarmKeyEntry{}, false
	}
	e, ok := g.entries[id]
	return e, ok
}

// loaded returns a snapshot of granter's entries. A granter never read in
// full is loaded first. A granter read before answers from its entries, and
// a background reload starts when it is stale or was Reset.
func (c *WarmKeyLogCopy) loaded(granter string) ([]WarmKeyEntry, error) {
	c.mu.Lock()
	g := c.granters[granter]
	now := c.now()
	held := g != nil && !g.loadedAt.IsZero()
	// A background reload runs at most once per warmKeyMissRefresh whether
	// or not the last attempt succeeded, so a failing chain does not turn
	// every lookup into a query.
	due := held && (!g.complete || now.Sub(g.loadedAt) > c.refreshAfter) && now.Sub(g.attemptedAt) > warmKeyMissRefresh
	if due {
		g.attemptedAt = now
	}
	c.mu.Unlock()

	if !held {
		if err := c.load(granter, false); err != nil {
			return nil, err
		}
	} else if due {
		go func() { _ = c.load(granter, true) }()
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	g = c.granters[granter]
	if g == nil {
		return nil, nil
	}
	out := make([]WarmKeyEntry, 0, len(g.entries))
	for _, e := range g.entries {
		out = append(out, e)
	}
	return out, nil
}

// load reads granter's whole log from the chain and merges it into the copy.
// Concurrent loads of one granter share a single fetch. background loads
// swallow their error: the copy stays usable and the next access retries.
func (c *WarmKeyLogCopy) load(granter string, background bool) error {
	if c.fetch == nil {
		c.mu.Lock()
		if c.granters[granter] == nil {
			c.granters[granter] = &granterLog{entries: make(map[uint64]WarmKeyEntry)}
		}
		c.granters[granter].complete = true
		c.granters[granter].loadedAt = c.now()
		c.mu.Unlock()
		return nil
	}
	c.mu.Lock()
	startedGen := c.resetGen
	if wait, ok := c.loading[granter]; ok {
		c.mu.Unlock()
		<-wait
		if background {
			return nil
		}
		c.mu.Lock()
		g := c.granters[granter]
		complete := g != nil && g.complete
		c.mu.Unlock()
		if !complete {
			return fmt.Errorf("%w: warm-key log of %s", ErrChainUnavailable, granter)
		}
		return nil
	}
	done := make(chan struct{})
	c.loading[granter] = done
	if !background {
		g := c.granters[granter]
		if g == nil {
			g = &granterLog{entries: make(map[uint64]WarmKeyEntry)}
			c.granters[granter] = g
		}
		g.attemptedAt = c.now()
	}
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		delete(c.loading, granter)
		c.mu.Unlock()
		close(done)
	}()

	ctx, cancel := context.WithTimeout(context.Background(), warmKeyFetchTimeout)
	entries, err := c.fetch.FetchWarmKeyLog(ctx, granter)
	cancel()
	c.mu.Lock()
	defer c.mu.Unlock()
	if err != nil {
		c.retryLocked(granter)
		return err
	}
	if c.resetGen != startedGen {
		c.retryLocked(granter)
		return errWarmKeyLogReset
	}
	g := c.granters[granter]
	if g == nil {
		g = &granterLog{entries: make(map[uint64]WarmKeyEntry)}
		c.granters[granter] = g
	}
	for _, e := range entries {
		if have, ok := g.entries[e.ID]; ok && have.Ended() && !e.Ended() {
			continue
		}
		g.entries[e.ID] = e
	}
	g.complete = true
	g.loadedAt = c.now()
	g.retryDelay = 0
	return nil
}

// retryLocked schedules one background reload of granter after a failed
// read, unless one is already scheduled. The delay doubles from
// warmKeyRetryMin to warmKeyRetryMax. The caller holds c.mu.
func (c *WarmKeyLogCopy) retryLocked(granter string) {
	g := c.granters[granter]
	if g == nil || g.retryArmed || c.after == nil {
		return
	}
	g.retryDelay = nextWarmKeyRetry(g.retryDelay)
	g.retryArmed = true
	c.after(g.retryDelay, func() {
		c.mu.Lock()
		g.retryArmed = false
		fresh := g.complete && c.now().Sub(g.loadedAt) <= c.refreshAfter
		if !fresh {
			g.attemptedAt = c.now()
		}
		c.mu.Unlock()
		if !fresh {
			_ = c.load(granter, true)
		}
	})
}

func nextWarmKeyRetry(cur time.Duration) time.Duration {
	if cur <= 0 {
		return warmKeyRetryMin
	}
	if next := 2 * cur; next < warmKeyRetryMax {
		return next
	}
	return warmKeyRetryMax
}

// errWarmKeyLogReset is a load whose snapshot was read before Reset. The
// granter stays incomplete, and a retry reads it again.
var errWarmKeyLogReset = errors.New("warm-key log reset during load")

// sharedWarmKeyCopies gives every bridge built on the same chain client one
// copy, so a gateway with many escrows reloads each granter once per refresh
// window rather than once per escrow.
var sharedWarmKeyCopies sync.Map // chain.InferenceClient (comparable) -> *WarmKeyLogCopy

// WarmKeyCopyFor returns the process-wide warm-key copy for client.
// A nil client returns a copy that accepts events and does not query the chain.
func WarmKeyCopyFor(client *chain.Client) *WarmKeyLogCopy {
	return sharedWarmKeyCopy(client)
}

func sharedWarmKeyCopy(client *chain.Client) *WarmKeyLogCopy {
	if client == nil {
		return NewWarmKeyLogCopy(nil, 0)
	}
	if v, ok := sharedWarmKeyCopies.Load(client); ok {
		return v.(*WarmKeyLogCopy)
	}
	cp := NewWarmKeyLogCopy(NewChainWarmKeyFetcher(client.InferenceQueryClient()), 0)
	v, _ := sharedWarmKeyCopies.LoadOrStore(client, cp)
	return v.(*WarmKeyLogCopy)
}
