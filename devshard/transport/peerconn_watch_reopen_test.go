package transport

import (
	"errors"
	"io"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"
)

func TestWatchReopen(t *testing.T) {
	require.True(t, watchReopen(nil))
	require.True(t, watchReopen(io.EOF))
	require.True(t, watchReopen(connect.NewError(connect.CodeFailedPrecondition, errors.New("host shutting down"))))
	require.False(t, watchReopen(connect.NewError(connect.CodeUnauthenticated, errors.New("session replaced"))))
	require.False(t, watchReopen(connect.NewError(connect.CodeUnauthenticated, errors.New("session expired"))))
	require.False(t, watchReopen(errWatchStale))
	require.False(t, watchReopen(connect.NewError(connect.CodeUnavailable, errors.New("unavailable"))))
}
