// Package registry owns the live escrow set: each escrow's session handle, its membership in the
// participant capacity model, and the in-flight request count routing scores it by.
package registry

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"devshard/cmd/gateway/scheduler"
	"devshard/transport"
	"devshard/user"
)

// See README.md, "Publishing, retiring and draining".
var (
	ErrClosed = errors.New("registry closed")

	ErrDraining = errors.New("escrow is still draining")
)

type Deps struct {
	ServingSessions  SessionFactory
	ReadOnlySessions SessionFactory
	Membership       membership
	Exhaustion       exhaustion
	Publications     publications
	Narrator         escrowNarrator
	Retiring         escrowObserver
	Closing          escrowObserver
	ServedBindings   servedBindingObserver
	PeerRPC          peerRPCAdoption
	Now              func() time.Time
}

type peerRPCAdoption interface {
	BindEscrowHosts(escrowID string, peers []string)
	ReleaseEscrow(escrowID string)
}

type servedBindingObserver func(escrowID, model, participant string, nonce uint64, verdict user.ServedBinding)

type escrowObserver func(escrowID string, session EscrowSession)

// Registry owns the live escrow set: live is written only under mu and read without it. See routing.md, "The escrow registry".
type Registry struct {
	servingSessions  SessionFactory
	readOnlySessions SessionFactory
	membership       membership
	sessions         atomic.Uint64
	exhaustion       exhaustion
	publications     publications
	narrator         escrowNarrator
	retiring         escrowObserver
	closingObserver  escrowObserver
	servedBindings   servedBindingObserver
	peerRPC          peerRPCAdoption
	now              func() time.Time

	live          atomic.Pointer[liveSet]
	drainingView  atomic.Pointer[[]*escrowEntry]
	chainMaxNonce atomic.Uint32

	openings sync.Map

	drainCloseFailures atomic.Int64

	closing sync.WaitGroup

	// sweepCursor rotates where the timeout sweep starts, so one escrow's backlog cannot hold the budget.
	sweepCursor atomic.Uint64

	mu       sync.Mutex
	draining map[*escrowEntry]struct{}
	listings map[*Listing]struct{}
	closed   bool
}

func New(deps Deps) *Registry {
	if deps.Now == nil {
		deps.Now = time.Now
	}
	registry := &Registry{
		servingSessions:  deps.ServingSessions,
		readOnlySessions: deps.ReadOnlySessions,
		membership:       deps.Membership,
		exhaustion:       deps.Exhaustion,
		publications:     deps.Publications,
		narrator:         deps.Narrator,
		retiring:         deps.Retiring,
		closingObserver:  deps.Closing,
		servedBindings:   deps.ServedBindings,
		peerRPC:          deps.PeerRPC,
		now:              deps.Now,
		draining:         map[*escrowEntry]struct{}{},
		listings:         map[*Listing]struct{}{},
	}
	registry.live.Store(emptyLiveSet())
	return registry
}

// Add publishes one escrow for routing and releases an unpublished session without flushing. See routing.md, "The escrow registry".
func (r *Registry) Add(ctx context.Context, escrowID, model string) error {
	return r.add(ctx, escrowID, model, entryFlags{}, nil)
}

// AddReserve publishes an escrow kept back until no regular escrow can take a request. See routing.md, "A reserve escrow".
func (r *Registry) AddReserve(ctx context.Context, escrowID, model string) error {
	return r.add(ctx, escrowID, model, entryFlags{isReserve: true}, nil)
}

// Listing adds escrows from rows read after it was taken, and skips any retired since. See README.md, "Publishing, retiring and draining".
type Listing struct {
	registry *Registry
	// retired is written by unpublish under the registry lock, the lock add publishes under.
	retired map[string]struct{}
}

// Listing must be taken before the rows are read: a park writes its row before it retires, so a row read active is retired after this.
func (r *Registry) Listing() *Listing {
	listing := &Listing{registry: r, retired: map[string]struct{}{}}
	r.mu.Lock()
	r.listings[listing] = struct{}{}
	r.mu.Unlock()
	return listing
}

// Add is Registry.Add, publishing nothing for an escrow retired since the listing was taken.
func (l *Listing) Add(ctx context.Context, escrowID, model string) error {
	return l.registry.add(ctx, escrowID, model, entryFlags{}, l)
}

// AddReserve is Registry.AddReserve, publishing nothing for an escrow retired since the listing was taken.
func (l *Listing) AddReserve(ctx context.Context, escrowID, model string) error {
	return l.registry.add(ctx, escrowID, model, entryFlags{isReserve: true}, l)
}

func (l *Listing) Close() {
	l.registry.mu.Lock()
	delete(l.registry.listings, l)
	l.registry.mu.Unlock()
}

func (l *Listing) retiredLocked(escrowID string) bool {
	if l == nil {
		return false
	}
	_, retired := l.retired[escrowID]
	return retired
}

func (l *Listing) retiredSince(escrowID string) bool {
	if l == nil {
		return false
	}
	l.registry.mu.Lock()
	defer l.registry.mu.Unlock()
	return l.retiredLocked(escrowID)
}

// entryFlags are what a restart reads off the row before the escrow first routes.
type entryFlags struct {
	isReserve bool
}

func (r *Registry) add(ctx context.Context, escrowID, model string, flags entryFlags, listing *Listing) error {
	switch {
	case escrowID == "":
		return fmt.Errorf("escrow id is required")
	case model == "":
		return fmt.Errorf("escrow %s: model is required", escrowID)
	case r.servingSessions == nil:
		return fmt.Errorf("escrow %s: no serving session factory", escrowID)
	}
	if err := r.refuseIfDraining(escrowID); err != nil {
		return err
	}

	opening := r.openingLock(escrowID)
	opening.Lock()
	defer opening.Unlock()
	if _, alreadyLive := r.live.Load().byID[escrowID]; alreadyLive {
		return nil
	}
	if listing.retiredSince(escrowID) {
		return nil
	}

	session, err := r.openServingSession(ctx, escrowID)
	if err != nil {
		return fmt.Errorf("opening serving session for escrow %s: %w", escrowID, err)
	}
	r.observeServedBindings(session, escrowID, model)

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return errors.Join(ErrClosed, session.Close())
	}
	if err := r.refuseIfDrainingLocked(escrowID); err != nil {
		return errors.Join(err, session.Close())
	}
	// Checked under the lock retire records under, so a retire either lands here first or finds the entry live.
	if listing.retiredLocked(escrowID) {
		return session.Close()
	}
	published := r.live.Load()
	if _, alreadyLive := published.byID[escrowID]; alreadyLive {
		return session.Close()
	}
	entry := newEscrowEntry(escrowID, model, r.sessions.Add(1), r.withChainMaxNonceLocked(session), r.now)
	entry.isReserve.Store(flags.isReserve)
	entry.hold = r.holdFor(entry)
	entry.rescueHold = r.escalationHoldFor(entry)
	r.live.Store(published.with(entry))
	r.pushMembershipLocked()
	if r.publications != nil {
		r.publications.EscrowPublished(escrowID, model)
	}
	r.bindPeerRPC(escrowID, session)
	if r.narrator != nil {
		r.narrator.EscrowServing(escrowID, model)
	}
	return nil
}

// openingLock serializes opens of one escrow, whose session is a SQLite file. See README.md, "Publishing, retiring and draining".
func (r *Registry) openingLock(escrowID string) *sync.Mutex {
	lock, _ := r.openings.LoadOrStore(escrowID, &sync.Mutex{})
	return lock.(*sync.Mutex)
}

func (r *Registry) refuseIfDraining(escrowID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.refuseIfDrainingLocked(escrowID)
}

func (r *Registry) refuseIfDrainingLocked(escrowID string) error {
	for entry := range r.draining {
		if entry.id == escrowID {
			return fmt.Errorf("escrow %s: %w", escrowID, ErrDraining)
		}
	}
	return nil
}

// Retire stops routing to an escrow; its session is released once the requests already running end.
func (r *Registry) Retire(escrowID string) error {
	return r.retire(escrowID, false)
}

// RetireAtNonceCap is Retire for an escrow at its nonce cap, whose running races may still escalate on it. See routing.md, "Picking an escrow".
func (r *Registry) RetireAtNonceCap(escrowID string) error {
	return r.retire(escrowID, true)
}

func (r *Registry) retire(escrowID string, nonceCapped bool) error {
	entry, closing := r.unpublish(escrowID, nonceCapped)
	if !closing {
		return nil
	}
	return r.closeDraining(entry)
}

// unpublish takes an escrow out of routing and reports whether the caller owns its close.
func (r *Registry) unpublish(escrowID string, nonceCapped bool) (*escrowEntry, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	// Recorded even for an escrow not live: one still opening must not publish after its retirement.
	for listing := range r.listings {
		listing.retired[escrowID] = struct{}{}
	}
	published := r.live.Load()
	entry, known := published.byID[escrowID]
	if !known {
		return nil, false
	}
	entry.nonceCapped = nonceCapped
	r.live.Store(published.without(escrowID))
	if r.membership != nil {
		r.membership.RemoveEscrow(escrowID)
	}
	r.pushMembershipLocked()
	r.draining[entry] = struct{}{}
	r.publishDrainingLocked()
	if entry.busyOrSwept() || entry.settlementHolds.Load() > 0 {
		if r.narrator != nil {
			r.narrator.EscrowRetiredDraining(escrowID, entry.inFlight.Load())
		}
		return nil, false
	}
	entry.closeClaimed = true
	if r.narrator != nil {
		r.narrator.EscrowRetired(escrowID)
	}
	return entry, true
}

// closeDraining releases the session with the registry lock free, in the session-then-registry lock order.
func (r *Registry) closeDraining(entry *escrowEntry) error {
	r.flushPendingAtRetirement(entry)
	if r.retiring != nil {
		r.retiring(entry.id, entry.session)
	}
	// Only an unreleased store leaves the entry in draining, so Add keeps refusing that id.
	released, err := entry.close()
	if !released {
		r.mu.Lock()
		entry.closeClaimed = false
		r.mu.Unlock()
		return err
	}
	if r.peerRPC != nil {
		r.peerRPC.ReleaseEscrow(entry.id)
	}
	r.mu.Lock()
	delete(r.draining, entry)
	r.publishDrainingLocked()
	r.mu.Unlock()
	return err
}

// publishDrainingLocked keeps Snapshot lock-free: a scrape cannot wait on a retirement.
func (r *Registry) publishDrainingLocked() {
	entries := drainingInIDOrder(r.draining)
	r.drainingView.Store(&entries)
}

// releaseHold closes a drained escrow off the holder's goroutine. See README.md, "Publishing, retiring and draining".
func (r *Registry) releaseHold(entry *escrowEntry, holds *atomic.Int64) {
	r.mu.Lock()
	holds.Add(-1)
	claimed := r.claimCloseLocked(entry)
	r.mu.Unlock()
	if claimed {
		r.closeInBackground(entry)
	}
}

func (r *Registry) closeInBackground(entry *escrowEntry) {
	go func() {
		defer r.closing.Done()
		closeErr := r.closeDraining(entry)
		if closeErr != nil {
			r.drainCloseFailures.Add(1)
		}
		if r.narrator != nil {
			r.narrator.DrainingEscrowClosed(entry.id, closeErr)
		}
	}()
}

func (r *Registry) claimCloseLocked(entry *escrowEntry) bool {
	if entry.busyOrSwept() || entry.settlementHolds.Load() > 0 || entry.closeClaimed || r.closed {
		return false
	}
	if _, isDraining := r.draining[entry]; !isDraining {
		return false
	}
	entry.closeClaimed = true
	r.closing.Add(1)
	return true
}

// DrainCloseFailures counts a flush or close failure with no caller left to return it to.
func (r *Registry) DrainCloseFailures() int64 { return r.drainCloseFailures.Load() }

// IsBusy satisfies escrow.SettlementSource: a settlement must not claim funds while nonces are still being spent.
func (r *Registry) IsBusy(escrowID string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if entry, known := r.live.Load().byID[escrowID]; known && entry.busyOrSwept() {
		return true
	}
	for entry := range r.draining {
		if entry.id == escrowID && entry.busyOrSwept() {
			return true
		}
	}
	return false
}

// ReserveTaken turns a reserve into a regular escrow at once; the manager rewrites the row and funds the next reserve.
func (r *Registry) ReserveTaken(escrowID string) {
	entry, known := r.live.Load().byID[escrowID]
	if !known || !entry.isReserve.CompareAndSwap(true, false) || r.exhaustion == nil {
		return
	}
	r.exhaustion.OnReserveTaken(escrowID)
}

// Exhausted reports an escrow routing declined as spent to the rotation lifecycle, which is what replaces it.
func (r *Registry) Exhausted(escrowID string, reason scheduler.ExhaustionReason) {
	if r.exhaustion == nil {
		return
	}
	r.exhaustion.OnBalanceExhausted(escrowID, reason)
}

// Close releases every session still held, including the escrows still draining.
func (r *Registry) Close() error {
	r.mu.Lock()
	r.closed = true
	r.mu.Unlock()
	r.closing.Wait()

	r.mu.Lock()
	published := r.live.Load()
	closing := make([]*escrowEntry, 0, len(published.byID)+len(r.draining))
	for _, id := range sortedKeys(published.byID) {
		closing = append(closing, published.byID[id])
	}
	closing = append(closing, drainingInIDOrder(r.draining)...)
	r.live.Store(emptyLiveSet())
	clear(r.draining)
	r.mu.Unlock()

	errs := make([]error, 0, len(closing))
	for _, entry := range closing {
		if r.closingObserver != nil {
			r.closingObserver(entry.id, entry.session)
		}
		_, err := entry.close()
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

func drainingInIDOrder(draining map[*escrowEntry]struct{}) []*escrowEntry {
	entries := make([]*escrowEntry, 0, len(draining))
	for entry := range draining {
		entries = append(entries, entry)
	}
	slices.SortFunc(entries, func(first, second *escrowEntry) int { return cmp.Compare(first.id, second.id) })
	return entries
}

// pushMembershipLocked republishes every live escrow's share. See routing.md, "Membership: what the capacity model is told".
func (r *Registry) pushMembershipLocked() {
	if r.membership == nil {
		return
	}
	published := r.live.Load()
	totalSlotsByModel := map[string]map[string]int{}
	for _, id := range sortedKeys(published.byID) {
		entry := published.byID[id]
		totalSlots := totalSlotsByModel[entry.model]
		if totalSlots == nil {
			totalSlots = map[string]int{}
			totalSlotsByModel[entry.model] = totalSlots
		}
		for participant, count := range entry.slots {
			totalSlots[participant] += count
		}
	}
	for _, id := range sortedKeys(published.byID) {
		entry := published.byID[id]
		r.membership.SetEscrowMembership(id, hostShares(entry.slots, totalSlotsByModel[entry.model]))
	}
}

func sortedKeys[K cmp.Ordered, V any](entries map[K]V) []K {
	keys := make([]K, 0, len(entries))
	for key := range entries {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys
}

func (r *Registry) observeServedBindings(session EscrowSession, escrowID, model string) {
	served := session.UserSession()
	if r.servedBindings == nil || served == nil {
		return
	}
	observe := r.servedBindings
	served.SetServedBindingHandler(func(nonce uint64, _ int, participant string, verdict user.ServedBinding) {
		observe(escrowID, model, participant, nonce, verdict)
	})
}

func (r *Registry) bindPeerRPC(escrowID string, session EscrowSession) {
	if r.peerRPC == nil {
		return
	}
	dials := session.HostDials()
	peers := make([]string, 0, len(dials))
	for _, dial := range dials {
		if peer := transport.PeerChildID(dial.ParticipantKey, dial.RoutePrefix); peer != "" {
			peers = append(peers, peer)
		}
	}
	r.peerRPC.BindEscrowHosts(escrowID, peers)
}
