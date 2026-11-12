// Package liquidity splits one escrow's money into what it can spend now and what is still out. See README.md.
package liquidity

import (
	"time"

	"devshard/cmd/gateway/internal/safemath"
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
			result.Returning = safemath.AddSaturatingUint64(result.Returning, amount)
			busy = true
		case ClassLate:
			result.Late = safemath.AddSaturatingUint64(result.Late, amount)
			busy = true
		case ClassStuck:
			result.Stuck = safemath.AddSaturatingUint64(result.Stuck, amount)
		}
	}
	result.Idle = !busy
	result.Full = full(input)
	result.Starved = !result.Full && !input.NonceCapReached
	return result
}

func classOf(record types.InferenceRecord, input Input, refusalWindow, executionWindow time.Duration) (Class, uint64, bool) {
	startedAt := time.UnixMilli(record.StartedAtTimeMs)
	young := record.StartedAtTimeMs > 0 && input.Now.Before(startedAt.Add(returningSoon))
	switch record.Status {
	case types.StatusPending:
		switch {
		case young:
			return ClassReturning, surplus(record, input.Price), true
		case record.StartedAtTimeMs <= 0 || input.Now.Before(startedAt.Add(refusalWindow+refusedVoteLadder)):
			return ClassLate, record.ReservedCost, true
		}
		return ClassStuck, record.ReservedCost, true
	case types.StatusStarted:
		switch {
		case young:
			return ClassReturning, surplus(record, input.Price), true
		case record.ConfirmedAtTimeMs <= 0 || input.Now.Before(time.UnixMilli(record.ConfirmedAtTimeMs).Add(executionWindow)):
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
	return min(safemath.MulSaturatingUint64(surplusBytes, price.TokenPrice), record.ReservedCost)
}

func full(input Input) bool {
	return input.Price.Priced && input.Balance >= input.Price.Slot
}
