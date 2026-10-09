package main

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

// A rejection has to say how full the gateway was: the class alone cannot tell a gateway at its
// concurrency cap from one holding a single enormous request.
func TestLimiterRejection_CarriesTheOccupancyThatCausedIt(t *testing.T) {
	limiter := NewGatewayLimiter(2, 0)

	require.NoError(t, limiter.Acquire(10))
	require.NoError(t, limiter.Acquire(10))
	err := limiter.Acquire(10)

	require.Error(t, err)
	require.Equal(t, LimitedByConcurrentRequests, limiterReasonLabel(err))
	require.Equal(t, []any{"reason", LimitedByConcurrentRequests, "in_flight", int64(2), "limit", int64(2)}, limiterRejectionLogFields(err))
}

func TestLimiterRejection_ZeroLiveWeightIsNotOccupancy(t *testing.T) {
	limiter := NewGatewayLimiter(4, 0)
	limiter.ApplyScaleFactor(0)
	err := limiter.Acquire(1)

	require.Error(t, err)
	require.Equal(t, LimitedByZeroLiveWeight, limiterReasonLabel(err))
	require.Equal(t, "no live host capacity", err.Error())
	require.Equal(t, []any{"reason", LimitedByZeroLiveWeight, "in_flight", int64(0), "limit", int64(0)}, limiterRejectionLogFields(err))
}

func TestLimiterRejection_NamesTheInputTokenCap(t *testing.T) {
	limiter := NewGatewayLimiter(0, 100)

	require.NoError(t, limiter.Acquire(90))
	err := limiter.Acquire(20)

	require.Error(t, err)
	require.Equal(t, LimitedByInputTokens, limiterReasonLabel(err))
	require.Contains(t, err.Error(), "90/100")
}

// The label is read off the error's type, so a rejection worded differently still classifies.
func TestLimiterReasonLabel_IgnoresUnrelatedErrors(t *testing.T) {
	require.Equal(t, "unknown", limiterReasonLabel(nil))
	require.Equal(t, "unknown", limiterReasonLabel(errors.New("some other failure")))
}
