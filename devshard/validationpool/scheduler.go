// Package validationpool implements the process-wide shared validation
// scheduler (see devshard/docs/shared-validation-pool-plan.md).
//
// It intentionally does not import devshard/host to avoid an import cycle;
// Host wiring (Offer from collectValidationJobs, ClearValidating) lands in
// later plan steps via EscrowID / InferenceID and the Abandon callback.
package validationpool

import (
	"container/heap"
	"context"
	"math/rand"
	"sync"
	"sync/atomic"
	"time"

	mlnodeclient "common/nodemanager"
	"devshard/observability"
)

// Job is one validation unit offered into the shared scheduler.
// Weight must be set by the Offer caller: max(1, len(host.slotIDs)).
// LeaseClaimedAt is required when LeaseHeld is true.
type Job struct {
	EscrowID       string
	InferenceID    uint64
	Weight         uint32
	LeaseHeld      bool
	LeaseClaimedAt time.Time
	// Work is opaque payload for Run (Host fills this in a later step).
	Work any
}

// RunFunc executes a validation job. Return ErrNoNodesAvailable (via
// errors.Is / mlnodeclient.IsNoNodesAvailable) to defer and retry later.
// Other errors are terminal for this attempt (job leaves the scheduler).
type RunFunc func(ctx context.Context, job Job) error

// AbandonFunc is called when the scheduler drops a job without completing it
// (pending HWM reject is handled by the Offer caller; this covers ForgetHost /
// Shutdown). Used later to ClearValidating on the Host.
type AbandonFunc func(escrowID string, inferenceID uint64)

// Scheduler is the process-wide validation router.
type Scheduler struct {
	cfg       Config
	run       RunFunc
	onAbandon AbandonFunc

	mu         sync.Mutex
	pending    *wfq
	deferred   deferHeap
	dispatchCh chan struct{}
	sweepCh    chan struct{}
	shutting   bool
	rootCtx    context.Context
	rootCancel context.CancelFunc

	outstanding atomic.Int64
	inflight    map[inflightKey]*inflightEntry

	wg sync.WaitGroup
}

type inflightKey struct {
	escrowID    string
	inferenceID uint64
}

type inflightEntry struct {
	cancel context.CancelFunc
	job    Job
}

// New creates a Scheduler and starts dispatchers + defer-sweeper.
func New(cfg Config, run RunFunc, onAbandon AbandonFunc) *Scheduler {
	if run == nil {
		panic("validationpool: RunFunc is required")
	}
	cfg = cfg.withDefaults()
	rootCtx, rootCancel := context.WithCancel(context.Background())
	s := &Scheduler{
		cfg:        cfg,
		run:        run,
		onAbandon:  onAbandon,
		pending:    newWFQ(),
		dispatchCh: make(chan struct{}, 1),
		sweepCh:    make(chan struct{}, 1),
		rootCtx:    rootCtx,
		rootCancel: rootCancel,
		inflight:   make(map[inflightKey]*inflightEntry),
	}
	observability.SetValidationSoftMaxOut(cfg.SoftMaxOut)
	observability.SetValidationEffectiveSoftMaxOut(cfg.EffectiveSoftMaxOut())
	observability.SetValidationPendingDepth(0)
	observability.SetValidationDeferredDepth(0)
	observability.SetValidationOutstanding(0)

	for i := 0; i < cfg.Dispatchers; i++ {
		s.wg.Add(1)
		go s.dispatchLoop()
	}
	s.wg.Add(1)
	go s.sweepLoop()
	return s
}

// EffectiveSoftMaxOut returns the per-process in-flight spawn cap.
func (s *Scheduler) EffectiveSoftMaxOut() int {
	return s.cfg.EffectiveSoftMaxOut()
}

// Offer enqueues a job into the pending WFQ. Returns false if shutting down or
// pending HWM is exceeded (caller must ClearValidating).
func (s *Scheduler) Offer(job Job) bool {
	if job.Weight == 0 {
		job.Weight = 1
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.shutting {
		return false
	}
	if s.pending.Len() >= s.cfg.PendingHWM {
		observability.IncValidationQueueDrop()
		return false
	}
	s.pending.Push(job)
	observability.SetValidationPendingDepth(s.pending.Len())
	s.signalDispatchLocked()
	return true
}

// ForgetHost drops pending + deferred jobs for escrowID, cancels in-flight
// contexts, and invokes onAbandon for each dropped/cancelled job.
func (s *Scheduler) ForgetHost(escrowID string) {
	var abandoned []Job
	s.mu.Lock()
	abandoned = append(abandoned, s.pending.DropEscrow(escrowID)...)
	abandoned = append(abandoned, s.deferred.dropEscrow(escrowID)...)
	var cancels []context.CancelFunc
	for k, e := range s.inflight {
		if k.escrowID == escrowID {
			cancels = append(cancels, e.cancel)
			abandoned = append(abandoned, e.job)
			delete(s.inflight, k)
		}
	}
	observability.SetValidationPendingDepth(s.pending.Len())
	observability.SetValidationDeferredDepth(s.deferred.Len())
	s.signalDispatchLocked()
	s.mu.Unlock()

	for _, c := range cancels {
		c()
	}
	for _, job := range abandoned {
		s.abandon(job)
	}
}

// Shutdown stops dispatchers and the sweeper, abandons pending/deferred, and
// cancels in-flight jobs. Blocks until dispatchers, sweeper, and in-flight
// validate goroutines exit, or ctx is done.
func (s *Scheduler) Shutdown(ctx context.Context) error {
	s.mu.Lock()
	if s.shutting {
		s.mu.Unlock()
		return nil
	}
	s.shutting = true
	pending := s.pending.Drain()
	deferred := s.deferred.drain()
	var cancels []context.CancelFunc
	var inflightJobs []Job
	for k, e := range s.inflight {
		cancels = append(cancels, e.cancel)
		inflightJobs = append(inflightJobs, e.job)
		delete(s.inflight, k)
	}
	observability.SetValidationPendingDepth(0)
	observability.SetValidationDeferredDepth(0)
	s.rootCancel()
	s.signalDispatchLocked()
	s.signalSweepLocked()
	s.mu.Unlock()

	for _, c := range cancels {
		c()
	}
	for _, job := range pending {
		s.abandon(job)
	}
	for _, job := range deferred {
		s.abandon(job)
	}
	for _, job := range inflightJobs {
		s.abandon(job)
	}

	done := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *Scheduler) abandon(job Job) {
	if s.onAbandon != nil {
		s.onAbandon(job.EscrowID, job.InferenceID)
	}
}

func (s *Scheduler) signalDispatchLocked() {
	select {
	case s.dispatchCh <- struct{}{}:
	default:
	}
}

func (s *Scheduler) signalSweepLocked() {
	select {
	case s.sweepCh <- struct{}{}:
	default:
	}
}

func (s *Scheduler) dispatchLoop() {
	defer s.wg.Done()
	for {
		job, ok := s.popRunnable()
		if !ok {
			select {
			case <-s.rootCtx.Done():
				return
			case <-s.dispatchCh:
			}
			continue
		}
		s.spawn(job)
	}
}

// popRunnable pops one pending job and reserves an outstanding slot under the
// same lock so softMaxOut cannot be exceeded by concurrent dispatchers.
func (s *Scheduler) popRunnable() (Job, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.shutting {
		return Job{}, false
	}
	eff := int64(s.cfg.EffectiveSoftMaxOut())
	if s.outstanding.Load() >= eff {
		return Job{}, false
	}
	job, ok := s.pending.Pop()
	if !ok {
		return Job{}, false
	}
	observability.SetValidationPendingDepth(s.pending.Len())
	n := s.outstanding.Add(1)
	observability.SetValidationOutstanding(int(n))
	return job, true
}

func (s *Scheduler) spawn(job Job) {
	ctx, cancel := context.WithCancel(s.rootCtx)
	key := inflightKey{escrowID: job.EscrowID, inferenceID: job.InferenceID}

	s.mu.Lock()
	if s.shutting {
		n := s.outstanding.Add(-1)
		observability.SetValidationOutstanding(int(n))
		s.mu.Unlock()
		cancel()
		s.abandon(job)
		return
	}
	s.inflight[key] = &inflightEntry{cancel: cancel, job: job}
	// Track the validate goroutine on wg so Shutdown waits for it (same lock
	// as the !shutting guard — never Add after Wait begins).
	s.wg.Add(1)
	s.mu.Unlock()

	go func() {
		defer s.wg.Done()
		defer cancel()
		err := s.run(ctx, job)

		s.mu.Lock()
		// ForgetHost/Shutdown delete the entry first; !tracked means already
		// abandoned — do not re-defer a stale busy completion.
		_, tracked := s.inflight[key]
		delete(s.inflight, key)
		n := s.outstanding.Add(-1)
		observability.SetValidationOutstanding(int(n))
		busy := mlnodeclient.IsNoNodesAvailable(err)
		shutting := s.shutting
		if busy && tracked && !shutting {
			s.deferLocked(job)
			observability.IncValidationAcquireBusyRequeue()
			observability.SetValidationDeferredDepth(s.deferred.Len())
		}
		s.signalDispatchLocked()
		s.mu.Unlock()
		if busy && tracked && shutting {
			s.abandon(job)
		}
		// Terminal leave (success / fail / cancel): ClearValidating is owned by
		// Host validateAsync or ForgetHost/Shutdown abandon above.
	}()
}

func (s *Scheduler) deferLocked(job Job) {
	elig := time.Now().Add(s.cfg.DeferDelay)
	if s.cfg.DeferJitter > 0 {
		elig = elig.Add(time.Duration(rand.Int63n(int64(s.cfg.DeferJitter) + 1)))
	}
	heap.Push(&s.deferred, &deferredItem{job: job, eligibleAt: elig})
	s.signalSweepLocked()
}

func (s *Scheduler) sweepLoop() {
	defer s.wg.Done()
	timer := time.NewTimer(time.Hour)
	if !timer.Stop() {
		<-timer.C
	}
	defer timer.Stop()

	for {
		s.mu.Lock()
		if s.shutting {
			s.mu.Unlock()
			return
		}
		next := s.deferred.peek()
		if next == nil {
			s.mu.Unlock()
			select {
			case <-s.rootCtx.Done():
				return
			case <-s.sweepCh:
			}
			continue
		}
		wait := time.Until(next.eligibleAt)
		s.mu.Unlock()

		if wait > 0 {
			timer.Reset(wait)
			select {
			case <-s.rootCtx.Done():
				if !timer.Stop() {
					select {
					case <-timer.C:
					default:
					}
				}
				return
			case <-timer.C:
			case <-s.sweepCh:
				if !timer.Stop() {
					select {
					case <-timer.C:
					default:
					}
				}
				continue
			}
		}

		s.mu.Lock()
		now := time.Now()
		for {
			it := s.deferred.peek()
			if it == nil || it.eligibleAt.After(now) {
				break
			}
			item := heap.Pop(&s.deferred).(*deferredItem)
			s.pending.Push(item.job)
		}
		observability.SetValidationDeferredDepth(s.deferred.Len())
		observability.SetValidationPendingDepth(s.pending.Len())
		s.signalDispatchLocked()
		s.mu.Unlock()
	}
}

// PendingLen / DeferredLen / Outstanding are test helpers.
func (s *Scheduler) PendingLen() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.pending.Len()
}

func (s *Scheduler) DeferredLen() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.deferred.Len()
}

func (s *Scheduler) Outstanding() int64 {
	return s.outstanding.Load()
}
