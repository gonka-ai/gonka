package main

import (
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

func (cost chatRequestCost) reservedOn(config types.SessionConfig) (uint64, error) {
	return state.ReservedCost(cost.inputLengthBytes, cost.maxTokens, config.TokenPrice)
}

func escrowCanFund(rt *devshardRuntime, cost chatRequestCost) bool {
	if rt == nil || rt.proxy == nil || rt.proxy.sm == nil {
		return true
	}
	reserved, err := cost.reservedOn(rt.proxy.sm.Config())
	if err != nil {
		return false
	}
	return rt.proxy.sm.Balance() >= reserved
}
