package main

import (
	"math"

	"devshard/state"
	"devshard/types"
)

type chatRequestCost struct {
	promptTokens     int64
	inputLengthBytes uint64
	maxTokens        uint64
}

func newChatRequestCost(body []byte, req chatRequest, promptTokens int64) chatRequestCost {
	return chatRequestCost{
		promptTokens:     promptTokens,
		inputLengthBytes: uint64(len(body)) + upstreamStreamRewriteMargin,
		maxTokens:        req.MaxTokens,
	}
}

func (cost chatRequestCost) startChargeOn(config types.SessionConfig) (uint64, error) {
	reserved, err := state.ReservedCost(cost.inputLengthBytes, cost.maxTokens, config.TokenPrice)
	if err != nil {
		return 0, err
	}
	if reserved > math.MaxUint64-config.FeePerNonce {
		return 0, types.ErrCostOverflow
	}
	return reserved + config.FeePerNonce, nil
}

func escrowCanFund(rt *devshardRuntime, cost chatRequestCost) bool {
	if rt == nil || rt.proxy == nil || rt.proxy.sm == nil {
		return true
	}
	charge, err := cost.startChargeOn(rt.proxy.sm.Config())
	if err != nil {
		return false
	}
	return rt.proxy.sm.Balance() >= charge
}
