package types

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

type multiHookTestDouble struct {
	completedErr   error
	completedCalls int

	failedCloseRetry bool
	failedErr        error
	failedCalls      int
}

func (h *multiHookTestDouble) AfterThresholdSigningCompleted(context.Context, []byte, uint64) error {
	h.completedCalls++
	return h.completedErr
}

func (h *multiHookTestDouble) AfterThresholdSigningFailed(context.Context, []byte, uint64, string) (bool, error) {
	h.failedCalls++
	return h.failedCloseRetry, h.failedErr
}

func TestMultiBlsHooks_AfterThresholdSigningFailed_CloseRetryPropagates(t *testing.T) {
	h1 := &multiHookTestDouble{}
	h2 := &multiHookTestDouble{failedCloseRetry: true}

	hooks := NewMultiBlsHooks(h1, h2)
	closeRetry, err := hooks.AfterThresholdSigningFailed(context.Background(), []byte{1}, 7, "failed")

	require.NoError(t, err)
	require.True(t, closeRetry)
	require.Equal(t, 1, h1.failedCalls)
	require.Equal(t, 1, h2.failedCalls)
}

func TestMultiBlsHooks_AfterThresholdSigningFailed_ContinuesAfterError(t *testing.T) {
	firstErr := errors.New("first hook failed")
	lastErr := errors.New("last hook failed")

	h1 := &multiHookTestDouble{failedErr: firstErr}
	h2 := &multiHookTestDouble{failedCloseRetry: true}
	h3 := &multiHookTestDouble{failedErr: lastErr}

	hooks := NewMultiBlsHooks(h1, h2, h3)
	closeRetry, err := hooks.AfterThresholdSigningFailed(context.Background(), []byte{2}, 9, "deadline")

	require.Error(t, err)
	require.ErrorIs(t, err, firstErr)
	require.ErrorIs(t, err, lastErr)
	require.True(t, closeRetry)
	require.Equal(t, 1, h1.failedCalls)
	require.Equal(t, 1, h2.failedCalls)
	require.Equal(t, 1, h3.failedCalls)
}

func TestMultiBlsHooks_AfterThresholdSigningCompleted_ContinuesAfterError(t *testing.T) {
	firstErr := errors.New("first completed hook failed")
	lastErr := errors.New("last completed hook failed")

	h1 := &multiHookTestDouble{completedErr: firstErr}
	h2 := &multiHookTestDouble{}
	h3 := &multiHookTestDouble{completedErr: lastErr}

	hooks := NewMultiBlsHooks(h1, h2, h3)
	err := hooks.AfterThresholdSigningCompleted(context.Background(), []byte{3}, 10)

	require.Error(t, err)
	require.ErrorIs(t, err, firstErr)
	require.ErrorIs(t, err, lastErr)
	require.Equal(t, 1, h1.completedCalls)
	require.Equal(t, 1, h2.completedCalls)
	require.Equal(t, 1, h3.completedCalls)
}
