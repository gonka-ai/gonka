// Package liquidity splits one escrow's money into what it can spend now and what is still out. See README.md.
package liquidity

import (
	"math"
	"time"

	"devshard/types"
)

const (
	returningSoon     = 5 * time.Minute
	refusedVoteLadder = 450 * time.Second
)

// Margins are the slack around the session's own timeouts that its config does not carry.
type Margins struct {
	TimeoutBuffer time.Duration
	SweepGrace    time.Duration
}

// Price is what full is measured against, at the escrow's own token price; Priced is false when a product overflowed.
type Price struct {
	TokenPrice    uint64
	BytesPerToken uint64
	Slot          uint64
	Priced        bool
}

// Input is everything Classify reads about one escrow.
type Input struct {
	Config          types.SessionConfig
	Balance         uint64
	Open            []types.InferenceRecord
	NonceCapReached bool
	Price           Price
	Margins         Margins
	Now             time.Time
}

// Escrow is one escrow's money by class, with the flags the planner decides on. See README.md, "Classes".
type Escrow struct {
	Free      uint64
	Returning uint64
	Late      uint64
	Stuck     uint64
	Full      bool
	Starved   bool
	Idle      bool
}

// Classify splits an escrow's money into free, returning, late and stuck, and flags it full, starved or idle. See README.md, "Classes".
func Classify(input Input) Escrow {
	result := Escrow{Free: input.Balance}
	busy := false
	refusalWindow := time.Duration(input.Config.RefusalTimeout)*time.Second + input.Margins.TimeoutBuffer
	executionWindow := time.Duration(input.Config.ExecutionTimeout)*time.Second + input.Margins.TimeoutBuffer + input.Margins.SweepGrace
	for _, record := range input.Open {
		class, amount, classified := classOf(record, input, refusalWindow, executionWindow)
		if !classified {
			continue
		}
		switch class {
		case ClassReturning:
			result.Returning = saturatingAdd(result.Returning, amount)
			busy = true
		case ClassLate:
			result.Late = saturatingAdd(result.Late, amount)
			busy = true
		case ClassStuck:
			result.Stuck = saturatingAdd(result.Stuck, amount)
		}
	}
	result.Idle = !busy
	result.Full = full(input)
	result.Starved = !result.Full && !input.NonceCapReached
	return result
}

// ExecutionAnchor clamps an executor clock that runs ahead to the latest moment a receipt could land, and falls back to the start without a stamp.
func ExecutionAnchor(record types.InferenceRecord, refusalWindow time.Duration) time.Time {
	startedAt := time.Unix(record.StartedAt, 0)
	if record.ConfirmedAt <= 0 {
		return startedAt
	}
	confirmedAt, latestReceipt := time.Unix(record.ConfirmedAt, 0), startedAt.Add(refusalWindow)
	if confirmedAt.After(latestReceipt) {
		return latestReceipt
	}
	return confirmedAt
}

func classOf(record types.InferenceRecord, input Input, refusalWindow, executionWindow time.Duration) (Class, uint64, bool) {
	startedAt := time.Unix(record.StartedAt, 0)
	young := input.Now.Before(startedAt.Add(returningSoon))
	switch record.Status {
	case types.StatusPending:
		switch {
		case young:
			return ClassReturning, surplus(record, input.Price), true
		case input.Now.Before(startedAt.Add(refusalWindow + refusedVoteLadder)):
			return ClassLate, record.ReservedCost, true
		}
		return ClassStuck, record.ReservedCost, true
	case types.StatusStarted:
		switch {
		case young:
			return ClassReturning, surplus(record, input.Price), true
		case input.Now.Before(ExecutionAnchor(record, refusalWindow).Add(executionWindow)):
			return ClassLate, record.ReservedCost, true
		}
		return ClassStuck, record.ReservedCost, true
	case types.StatusChallenged:
		return ClassStuck, record.ActualCost, true
	}
	return ClassNone, 0, false
}

func surplus(record types.InferenceRecord, price Price) uint64 {
	if price.BytesPerToken == 0 {
		return 0
	}
	surplusBytes := record.InputLength - record.InputLength/price.BytesPerToken
	return min(saturatingMul(surplusBytes, price.TokenPrice), record.ReservedCost)
}

func full(input Input) bool {
	return input.Price.Priced && input.Balance >= input.Price.Slot
}

func saturatingAdd(left, right uint64) uint64 {
	if sum := left + right; sum >= left {
		return sum
	}
	return math.MaxUint64
}

func saturatingMul(left, right uint64) uint64 {
	if left == 0 || right == 0 {
		return 0
	}
	if product := left * right; product/left == right {
		return product
	}
	return math.MaxUint64
}
