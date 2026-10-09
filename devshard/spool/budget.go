package spool

import (
	"fmt"
	"sync"
)

// Budget bounds RAM and disk bytes for one unit of work (one fold, one
// generation). ChargeRAM / ChargeDisk fail with ErrBudgetExceeded when the
// corresponding ceiling would be crossed.
type Budget struct {
	mu        sync.Mutex
	ramBytes  int64
	diskBytes int64
	ramLimit  int64
	diskLimit int64
}

func NewBudget(ramLimit, diskLimit int64) *Budget {
	if ramLimit < 0 {
		ramLimit = 0
	}
	if diskLimit < 0 {
		diskLimit = 0
	}
	return &Budget{ramLimit: ramLimit, diskLimit: diskLimit}
}

func (b *Budget) RAMAvailable(n int64) bool {
	if b == nil || n <= 0 {
		return true
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.ramBytes+n <= b.ramLimit
}

func (b *Budget) ChargeRAM(n int64) error {
	if b == nil || n <= 0 {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.ramBytes+n > b.ramLimit {
		return fmt.Errorf("%w: ram %d+%d > %d", ErrBudgetExceeded, b.ramBytes, n, b.ramLimit)
	}
	b.ramBytes += n
	return nil
}

func (b *Budget) ChargeDisk(n int64) error {
	if b == nil || n <= 0 {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.diskLimit > 0 && b.diskBytes+n > b.diskLimit {
		return fmt.Errorf("%w: disk %d+%d > %d", ErrBudgetExceeded, b.diskBytes, n, b.diskLimit)
	}
	b.diskBytes += n
	return nil
}

// ReclassifyToDisk moves n bytes already charged to RAM onto the disk counter
// (used when a store spills). Fails if the disk ceiling cannot absorb them.
func (b *Budget) ReclassifyToDisk(n int64) error {
	if b == nil || n <= 0 {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.diskLimit > 0 && b.diskBytes+n > b.diskLimit {
		return fmt.Errorf("%w: disk %d+%d > %d", ErrBudgetExceeded, b.diskBytes, n, b.diskLimit)
	}
	b.diskBytes += n
	b.ramBytes -= n
	if b.ramBytes < 0 {
		b.ramBytes = 0
	}
	return nil
}

func (b *Budget) Stats() (ram, disk int64) {
	if b == nil {
		return 0, 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.ramBytes, b.diskBytes
}

func (b *Budget) Limits() (ramLimit, diskLimit int64) {
	if b == nil {
		return 0, 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.ramLimit, b.diskLimit
}
