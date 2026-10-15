package inference

import (
	"context"
	"log/slog"
	"slices"
	"sort"
	"sync"
	"time"

	"devshard"
	"devshard/observability"
	"devshard/storage"
)

// creditProbeFresh is how long a shared-credit lookup can be reused. The
// check runs while the host lock is held, so a collect over many inferences
// must not query once per inference.
const creditProbeFresh = time.Second

type creditProbe struct {
	at time.Time
	ok bool
}

const defaultValidationCreditTTL = 60 * time.Minute

// defaultValidationCreditHold is how long a reserved shared credit stays out
// of the siblings' reach without a renewal.
const defaultValidationCreditHold = 2 * time.Minute

// validationBudget is shared by all escrows using this process's Engine.
// Credits expire individually and are spent oldest first. The process-local
// budget starts empty on restart. Replicas of one participant use the shared
// store instead, so a credit outlives the process that earned it.
type validationBudget struct {
	mu      sync.Mutex
	ttl     time.Duration
	credits map[string][]time.Time
	now     func() time.Time
}

func newValidationBudget(ttl time.Duration) *validationBudget {
	return &validationBudget{ttl: ttl, credits: make(map[string][]time.Time), now: time.Now}
}

// live must be called with mu held.
func (b *validationBudget) live(model string, now time.Time) []time.Time {
	credits := b.credits[model]
	for len(credits) > 0 && !credits[0].After(now) {
		credits = credits[1:]
	}
	return credits
}

func (b *validationBudget) earn(model string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := b.now()
	b.credits[model] = append(b.live(model, now), now.Add(b.ttl))
}

// available avoids payload fetching for an exhausted model. reserve is the
// authoritative check: concurrent workers cannot reserve the same credit.
func (b *validationBudget) available(model string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	credits := b.live(model, b.now())
	if len(credits) == 0 {
		delete(b.credits, model)
		return false
	}
	b.credits[model] = credits
	return true
}

// reserve takes a credit before node acquisition. The credit is spent unless
// refund runs.
func (b *validationBudget) reserve(model string) (refund func(), ok bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	credits := b.live(model, b.now())
	if len(credits) == 0 {
		delete(b.credits, model)
		return nil, false
	}
	expiry := credits[0]
	if len(credits) == 1 {
		delete(b.credits, model)
	} else {
		b.credits[model] = credits[1:]
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			b.mu.Lock()
			defer b.mu.Unlock()
			now := b.now()
			if !expiry.After(now) {
				return
			}
			credits := b.live(model, now)
			// Other workers may return reservations out of order. Preserve FIFO expiry.
			i := sort.Search(len(credits), func(i int) bool { return !credits[i].Before(expiry) })
			b.credits[model] = slices.Insert(credits, i, expiry)
		})
	}, true
}

// UseSharedValidationCredits spends this participant's credits from store.
// store is the Postgres table shared by replicas of participant. A nil store
// or an empty participant keeps the process-local budget.
func (e *Engine) UseSharedValidationCredits(store storage.ValidationCreditStore, participant string) {
	if e == nil || store == nil || participant == "" {
		return
	}
	e.sharedCredits = store
	e.participant = participant
}

func (e *Engine) creditAvailable(model string) bool {
	if e == nil || e.validationBudget == nil {
		return true
	}
	if e.sharedCredits == nil {
		return e.validationBudget.available(model)
	}
	return e.sharedCreditAvailable(model)
}

func (e *Engine) earnValidationCredit(ctx context.Context, model string) {
	if e.sharedCredits == nil {
		e.validationBudget.earn(model)
		return
	}
	if err := e.sharedCredits.EarnValidationCredit(ctx, e.participant, model, defaultValidationCreditTTL); err != nil {
		slog.Warn("devshardd: validation credit earn failed", "participant", e.participant, "model", model, "error", err)
		return
	}
	e.dropCreditProbe(model)
}

func (e *Engine) sharedCreditAvailable(model string) bool {
	e.creditMu.Lock()
	if p, ok := e.creditCache[model]; ok && time.Since(p.at) < creditProbeFresh {
		e.creditMu.Unlock()
		return p.ok
	}
	e.creditMu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	ok, err := e.sharedCredits.ValidationCreditAvailable(ctx, e.participant, model)
	if err != nil {
		slog.Warn("devshardd: validation credit lookup failed", "participant", e.participant, "model", model, "error", err)
		return false
	}
	e.creditMu.Lock()
	if e.creditCache == nil {
		e.creditCache = make(map[string]creditProbe)
	}
	e.creditCache[model] = creditProbe{at: time.Now(), ok: ok}
	e.creditMu.Unlock()
	return ok
}

func (e *Engine) dropCreditProbe(model string) {
	e.creditMu.Lock()
	delete(e.creditCache, model)
	e.creditMu.Unlock()
}

// validationCredit is one reserved credit. The first of spend and refund
// settles it and the other becomes a no-op. A nil credit costs nothing.
type validationCredit struct {
	once     sync.Once
	spendFn  func()
	refundFn func()
}

func (c *validationCredit) spend() {
	if c != nil {
		c.once.Do(c.spendFn)
	}
}

func (c *validationCredit) refund() {
	if c != nil {
		c.once.Do(c.refundFn)
	}
}

func (e *Engine) sharedCreditHold() time.Duration {
	if e.creditHold > 0 {
		return e.creditHold
	}
	return defaultValidationCreditHold
}

// reserveValidationCredit takes one credit for a validation before node
// acquisition. The caller spends it once an ML node answers and refunds it
// otherwise. Paths other than validation get a nil credit.
// ErrValidationDeferred means there is no credit to take.
func (e *Engine) reserveValidationCredit(ctx context.Context, path observability.Path, model string) (*validationCredit, error) {
	if path != observability.PathValidate {
		return nil, nil
	}
	if e.sharedCredits == nil {
		refund, ok := e.validationBudget.reserve(model)
		if !ok {
			return nil, devshard.ErrValidationDeferred
		}
		return &validationCredit{spendFn: func() {}, refundFn: refund}, nil
	}
	hold := e.sharedCreditHold()
	reserveCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	h, ok, err := e.sharedCredits.ReserveValidationCredit(reserveCtx, e.participant, model, hold)
	cancel()
	e.dropCreditProbe(model)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, devshard.ErrValidationDeferred
	}
	stopRenew := e.renewValidationCredit(h, model, hold)
	settle := func(op string, apply func(context.Context, storage.ValidationCreditHold) error) func() {
		return func() {
			stopRenew()
			opCtx, opCancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
			defer opCancel()
			if err := apply(opCtx, h); err != nil {
				slog.Warn("devshardd: validation credit "+op+" failed", "participant", e.participant, "model", model, "error", err)
			}
			e.dropCreditProbe(model)
		}
	}
	return &validationCredit{
		spendFn:  settle("spend", e.sharedCredits.SpendValidationCredit),
		refundFn: settle("refund", e.sharedCredits.RefundValidationCredit),
	}, nil
}

// renewValidationCredit keeps h held until the returned stop runs. An ML call
// may outlast many holds; a replica that dies stops renewing and the credit
// returns to its siblings once the hold lapses.
func (e *Engine) renewValidationCredit(h storage.ValidationCreditHold, model string, hold time.Duration) (stop func()) {
	done := make(chan struct{})
	go func() {
		ticker := time.NewTicker(hold / 3)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
			}
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			held, err := e.sharedCredits.RenewValidationCredit(ctx, h, hold)
			cancel()
			if err != nil {
				slog.Warn("devshardd: validation credit renew failed", "participant", e.participant, "model", model, "error", err)
				continue
			}
			if !held {
				return
			}
		}
	}()
	var once sync.Once
	return func() { once.Do(func() { close(done) }) }
}
