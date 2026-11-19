package types

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDevshardValidationRateForCreate(t *testing.T) {
	require.Equal(t, DefaultDevshardValidationRate, DevshardValidationRateForCreate(nil))
	require.Equal(t, DefaultDevshardValidationRate, DevshardValidationRateForCreate(&DevshardEscrowParams{}))
	require.Equal(t, uint32(3000), DevshardValidationRateForCreate(&DevshardEscrowParams{ValidationRate: 3000}))
	require.Equal(t, uint32(10000), DevshardValidationRateForCreate(&DevshardEscrowParams{ValidationRate: 10000}))
}

func TestDevshardChatFrameScheduleForCreate(t *testing.T) {
	require.Equal(t, DefaultDevshardChatFrameJoin, DevshardChatFrameJoinForCreate(nil))
	require.Equal(t, DefaultDevshardChatFrameJoin, DevshardChatFrameJoinForCreate(&DevshardEscrowParams{}))
	require.Equal(t, uint32(7), DevshardChatFrameJoinForCreate(&DevshardEscrowParams{ChatFrameJoin: 7}))

	require.Equal(t, DefaultDevshardChatFrameSignEvery, DevshardChatFrameSignEveryForCreate(nil))
	require.Equal(t, DefaultDevshardChatFrameSignEvery, DevshardChatFrameSignEveryForCreate(&DevshardEscrowParams{}))
	require.Equal(t, uint32(11), DevshardChatFrameSignEveryForCreate(&DevshardEscrowParams{ChatFrameSignEvery: 11}))
}
