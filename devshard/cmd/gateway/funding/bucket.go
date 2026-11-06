package funding

import "time"

const bucketCapacity = 2

// Bucket limits one model's creates in wall time: two at once, then one per refill interval. See README.md, "Rate".
type Bucket struct {
	tokens     int
	refilledAt time.Time
}

// NewBucket starts full, so a model's first shortfall is answered at once.
func NewBucket(now time.Time) Bucket {
	return Bucket{tokens: bucketCapacity, refilledAt: now}
}

// Refill adds one token per whole interval since the last refill and re-anchors a full bucket at every call, so a full bucket banks no time.
func (bucket *Bucket) Refill(now time.Time, interval time.Duration) {
	if interval <= 0 || !now.After(bucket.refilledAt) {
		return
	}
	if bucket.tokens >= bucketCapacity {
		bucket.refilledAt = now
		return
	}
	elapsed := now.Sub(bucket.refilledAt)
	earned := min(elapsed/interval, bucketCapacity)
	if earned == 0 {
		return
	}
	bucket.tokens = min(bucketCapacity, bucket.tokens+int(earned))
	bucket.refilledAt = bucket.refilledAt.Add(elapsed - elapsed%interval)
	if bucket.tokens == bucketCapacity {
		bucket.refilledAt = now
	}
}

// Available is how many creates the bucket allows now.
func (bucket *Bucket) Available() int { return bucket.tokens }

// Take spends one token per create, never below zero.
func (bucket *Bucket) Take(count int) { bucket.tokens = max(0, bucket.tokens-count) }
