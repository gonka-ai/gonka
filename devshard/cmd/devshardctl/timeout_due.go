package main

import (
	"bytes"
	"math"
	"time"

	"devshard/chainoracle/blocks/headertime"
	"devshard/state"
	"devshard/types"
	"devshard/user"
)

// executionTimeoutSweepGrace is the v6 sweep delay past the gateway deadline.
// The live waiter uses TimeoutBuffer only. The sweep waits out that buffer
// and this grace, so it does not vote a record the attempt is still sleeping on.
const executionTimeoutSweepGrace = 2 * time.Minute

// headerTimeStore is the stored header map. A miss stays a miss.
type headerTimeStore interface {
	StoredAt(height uint64) (headertime.Record, bool)
}

func storedHeaders(sm *state.StateMachine) headerTimeStore {
	if sm == nil || sm.HeaderTimes() == nil {
		return nil
	}
	store, _ := sm.HeaderTimes().(headerTimeStore)
	return store
}

func timeoutReason(rec *types.InferenceRecord, hasRefusalPayload func(uint64) bool, nonce uint64) (types.TimeoutReason, bool) {
	if rec == nil {
		return 0, false
	}
	switch rec.Status {
	case types.StatusStarted:
		return types.TimeoutReason_TIMEOUT_REASON_EXECUTION, true
	case types.StatusPending:
		if hasRefusalPayload != nil && hasRefusalPayload(nonce) {
			return types.TimeoutReason_TIMEOUT_REASON_REFUSED, true
		}
	}
	return 0, false
}

// timeoutWall is the legacy anchor. A non-positive field is the zero Time,
// which Due treats as unarmed. It is not the Unix epoch.
func timeoutWall(rec *types.InferenceRecord, reason types.TimeoutReason) time.Time {
	if rec == nil {
		return time.Time{}
	}
	unix := rec.StartedAt
	if reason == types.TimeoutReason_TIMEOUT_REASON_EXECUTION {
		unix = rec.ConfirmedAt
	}
	if unix <= 0 {
		return time.Time{}
	}
	return time.Unix(unix, 0)
}

// recordStamp is the header anchor: carried *TimeMs, otherwise one stored
// map row. A missing row is not an error and does not open the challenge.
func recordStamp(rec *types.InferenceRecord, reason types.TimeoutReason, store headerTimeStore) (ms int64, missing bool, height uint64) {
	if rec == nil {
		return 0, true, 0
	}
	var carried int64
	var hash []byte
	if reason == types.TimeoutReason_TIMEOUT_REASON_EXECUTION {
		carried = rec.ConfirmedAtTimeMs
		height = rec.ConfirmedAtHeight
		hash = rec.ConfirmedAtBlockHash
	} else {
		carried = rec.StartedAtTimeMs
		height = rec.StartedAtHeight
		hash = rec.StartedAtBlockHash
	}
	if carried > 0 {
		return carried, false, height
	}
	if height == 0 || store == nil {
		return 0, true, height
	}
	row, ok := store.StoredAt(height)
	if !ok || row.TimeMs <= 0 {
		return 0, true, height
	}
	if len(hash) > 0 && len(row.Hash) > 0 && !bytes.Equal(row.Hash, hash) {
		return 0, true, height
	}
	return row.TimeMs, false, height
}

func protocolInterval(seconds int64) time.Duration {
	if seconds <= 0 {
		return 0
	}
	maxSeconds := int64(math.MaxInt64 / int64(time.Second))
	if seconds > maxSeconds {
		return time.Duration(math.MaxInt64)
	}
	return time.Duration(seconds) * time.Second
}

// sweepExtra is TimeoutBuffer plus the sweep grace on the legacy clock.
// Header mode ignores Extra, and the sweep passes 0 there.
func sweepExtra(header bool) time.Duration {
	if header {
		return 0
	}
	extra := user.TimeoutBuffer
	if extra > time.Duration(math.MaxInt64)-executionTimeoutSweepGrace {
		return extra
	}
	return extra + executionTimeoutSweepGrace
}

func holdExtra(header bool) time.Duration {
	if header {
		return 0
	}
	return user.TimeoutBuffer
}

// protocolTimeoutDue is the sweep and hold check. It does not record a
// clock mismatch and it does not read a clock of its own: now is the caller's.
func protocolTimeoutDue(cfg types.SessionConfig, rec *types.InferenceRecord, reason types.TimeoutReason, store headerTimeStore, now time.Time, extra time.Duration) (time.Time, bool) {
	kind := headertime.KindRefused
	seconds := cfg.RefusalTimeout
	if reason == types.TimeoutReason_TIMEOUT_REASON_EXECUTION {
		kind = headertime.KindExecution
		seconds = cfg.ExecutionTimeout
	}
	if cfg.HeightsyncTimeoutClock {
		extra = 0
	}
	ms, missing, height := recordStamp(rec, reason, store)
	deadline, _, armed := headertime.Due(headertime.ClockDueInput{
		HeightsyncTimeoutClock: cfg.HeightsyncTimeoutClock,
		Reason:                 kind,
		Now:                    now,
		Wall:                   timeoutWall(rec, reason),
		StampTimeMs:            ms,
		StampMissing:           missing,
		StampHeight:            height,
		Interval:               protocolInterval(seconds),
		Extra:                  extra,
		Caller:                 headertime.CallerGateway,
	})
	return deadline, armed
}
