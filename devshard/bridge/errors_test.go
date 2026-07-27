package bridge

import (
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestClassifyQueryError_Unavailable(t *testing.T) {
	err := ClassifyQueryError(status.Error(codes.Unavailable, "connection refused"))
	require.ErrorIs(t, err, ErrChainUnavailable)
}

func TestClassifyQueryError_NotFound(t *testing.T) {
	err := ClassifyQueryError(status.Error(codes.NotFound, "missing"))
	require.ErrorIs(t, err, ErrEscrowNotFound)
	require.False(t, errors.Is(err, ErrChainUnavailable))
}

func TestClassifyQueryError_Transport(t *testing.T) {
	err := ClassifyQueryError(fmt.Errorf("HTTP GET: dial tcp: i/o timeout"))
	require.ErrorIs(t, err, ErrChainUnavailable)
}

func TestClassifyQueryError_PassthroughSentinels(t *testing.T) {
	assert.ErrorIs(t, ClassifyQueryError(ErrEscrowNotFound), ErrEscrowNotFound)
	assert.ErrorIs(t, ClassifyQueryError(ErrChainUnavailable), ErrChainUnavailable)
}
