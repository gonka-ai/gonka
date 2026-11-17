package accounting

import (
	"fmt"
	"math"
	"slices"
	"strconv"
	"time"
)

// See docs/accounting.md, "Time to first content".
const (
	firstContentSmallestBound = 10 * time.Millisecond
	firstContentBucketGrowth  = 1.25
	firstContentBoundedCount  = 50
	firstContentOverflow      = FirstContentBucket(firstContentBoundedCount)

	firstContentMedianPercent  = 50
	firstContentTailPercent    = 95
	firstContentFarTailPercent = 99
	percentScale               = 100

	noFirstContentMilliseconds uint64 = 0
)

var firstContentBounds = firstContentBucketBounds()

// FirstContentBuckets counts timed attempts per bucket; a bucket no attempt fell in is absent.
type FirstContentBuckets map[FirstContentBucket]uint64

// FirstContentBucket is a bucket's index, ascending; on the wire it is the bucket's upper bound in milliseconds.
type FirstContentBucket uint8

func (bucket FirstContentBucket) MarshalText() ([]byte, error) {
	if bucket >= firstContentOverflow {
		return []byte(FirstContentOverflowBound), nil
	}
	return []byte(strconv.Itoa(int(bucket.upperBound().Milliseconds()))), nil
}

func (bucket *FirstContentBucket) UnmarshalText(text []byte) error {
	if string(text) == FirstContentOverflowBound {
		*bucket = firstContentOverflow
		return nil
	}
	milliseconds, err := strconv.Atoi(string(text))
	if err != nil {
		return fmt.Errorf("first-content bucket %q: %w", text, err)
	}
	index, known := slices.BinarySearch(firstContentBounds, time.Duration(milliseconds)*time.Millisecond)
	if !known {
		return fmt.Errorf("first-content bucket %q is not a bound of this build", text)
	}
	*bucket = FirstContentBucket(index)
	return nil
}

func (bucket FirstContentBucket) upperBound() time.Duration {
	return firstContentBounds[min(int(bucket), firstContentBoundedCount-1)]
}

func firstContentBucketBounds() []time.Duration {
	bounds := make([]time.Duration, firstContentBoundedCount)
	milliseconds := float64(firstContentSmallestBound.Milliseconds())
	for index := range bounds {
		bounds[index] = time.Duration(math.Round(milliseconds)) * time.Millisecond
		milliseconds *= firstContentBucketGrowth
	}
	return bounds
}

func bucketOf(delay time.Duration) FirstContentBucket {
	index, _ := slices.BinarySearch(firstContentBounds, delay)
	return FirstContentBucket(index)
}

func (buckets FirstContentBuckets) percentile(percent uint64) uint64 {
	if len(buckets) == 0 {
		return noFirstContentMilliseconds
	}
	var attempts uint64
	for _, count := range buckets {
		attempts += count
	}
	rank := (attempts*percent + percentScale - 1) / percentScale
	var seen uint64
	for bucket := range firstContentOverflow + 1 {
		seen += buckets[bucket]
		if seen >= rank {
			return uint64(bucket.upperBound().Milliseconds())
		}
	}
	return noFirstContentMilliseconds
}
