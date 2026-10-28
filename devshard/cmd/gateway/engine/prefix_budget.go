package engine

import (
	"errors"
	"sync/atomic"
)

const (
	prefixAttemptBytes     = 1 << 20
	prefixParticipantBytes = 10 << 20
	prefixGlobalBytes      = 100 << 20
)

var (
	errPrefixOverBudget      = errors.New("pre-content bytes past the attempt's or the host's budget")
	errPrefixGatewayPressure = errors.New("pre-content bytes past the gateway's budget")
)

func newPrefixBudget() *carryBudget {
	return &carryBudget{
		attemptLimit:     prefixAttemptBytes,
		participantLimit: prefixParticipantBytes,
		globalLimit:      prefixGlobalBytes,
		participants:     map[string]*atomic.Int64{},
	}
}

type prefixAllowance struct {
	budget      *carryBudget
	participant *atomic.Int64
	overflow    func(cause error)
	held        int64
}

func newPrefixAllowance(budget *carryBudget, participant string, overflow func(cause error)) prefixAllowance {
	return prefixAllowance{budget: budget, participant: budget.counterFor(participant), overflow: overflow}
}

func (a *prefixAllowance) charge(size int64) error {
	if a.held+size > a.budget.attemptLimit {
		return errPrefixOverBudget
	}
	switch a.budget.reserve(a.participant, size) {
	case budgetGranted:
		a.held += size
		return nil
	case budgetGlobalFull:
		return errPrefixGatewayPressure
	case budgetParticipantFull:
		return errPrefixOverBudget
	}
	return errPrefixOverBudget
}

func (a *prefixAllowance) release() {
	a.budget.refund(a.participant, a.held)
	a.held = 0
}
