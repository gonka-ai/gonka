package inference

import (
	"context"
	"log/slog"
	"slices"
	"sort"
	"sync"
	"time"

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

// reserve takes a credit before node acquisition. Refund failures before HTTP
// dispatch; dispatched requests spend the credit regardless of their outcome.
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

func (e *Engine) reserveValidationCredit(ctx context.Context, path observability.Path, model string) (func(), bool) {
	if path != observability.PathValidate {
		return func() {}, true
	}
	if e.sharedCredits == nil {
		return e.validationBudget.reserve(model)
	}
	reserveCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	expiresAt, ok, err := e.sharedCredits.ReserveValidationCredit(reserveCtx, e.participant, model)
	cancel()
	if err != nil {
		slog.Warn("devshardd: validation credit reserve failed", "participant", e.participant, "model", model, "error", err)
		return nil, false
	}
	if !ok {
		e.dropCreditProbe(model)
		return nil, false
	}
	e.dropCreditProbe(model)
	var once sync.Once
	return func() {
		once.Do(func() {
			refundCtx, refundCancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
			defer refundCancel()
			if refundErr := e.sharedCredits.RefundValidationCredit(refundCtx, e.participant, model, expiresAt); refundErr != nil {
				slog.Warn("devshardd: validation credit refund failed", "participant", e.participant, "model", model, "error", refundErr)
			}
			e.dropCreditProbe(model)
		})
	}, true
}
