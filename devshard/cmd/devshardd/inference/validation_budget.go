package inference

import (
	"slices"
	"sort"
	"sync"
	"time"

	"devshard/observability"
)

const defaultValidationCreditTTL = 60 * time.Minute

// validationBudget is shared by all escrows using this process's Engine.
// Credits expire individually and are spent oldest first. Restart starts empty.
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

func (e *Engine) reserveValidationCredit(path observability.Path, model string) (func(), bool) {
	if path != observability.PathValidate {
		return func() {}, true
	}
	return e.validationBudget.reserve(model)
}
