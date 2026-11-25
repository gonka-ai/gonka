package scheduler

import "sync/atomic"

// BurnCapOff is the per-request burn cap that never caps.
const BurnCapOff int64 = 0

// BurnTally counts the nonces burned during one request across all its attempts and escrows; a nil tally never caps. See routing.md, "The burn cap of a request".
type BurnTally struct {
	burned atomic.Int64
}

// Charge counts one burned nonce against the request.
func (t *BurnTally) Charge() {
	if t != nil {
		t.burned.Add(1)
	}
}

// Reached reports whether the request has burned limit nonces; a limit of zero is never reached.
func (t *BurnTally) Reached(limit int64) bool {
	return t != nil && limit > BurnCapOff && t.burned.Load() >= limit
}

// WouldExceed reports whether burns more nonces would pass limit, that is whether Reached would refuse one of them.
func (t *BurnTally) WouldExceed(limit int64, burns int) bool {
	return t != nil && limit > BurnCapOff && burns > noBurns && t.burned.Load()+int64(burns) > limit
}
