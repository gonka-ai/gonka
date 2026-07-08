package public

import (
	"context"
	"testing"
	"time"

	coretypes "github.com/cometbft/cometbft/rpc/core/types"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"decentralized-api/cosmosclient"
)

// TestValidateTimestampNoncePassesBoundedContext proves the request-path chain
// Status() lookup is now deadline-bounded (statusQueryTimeout) instead of using
// context.Background(), so a stalled node cannot hang the request goroutine.
func TestValidateTimestampNoncePassesBoundedContext(t *testing.T) {
	configManager := newTestConfigManager(t)
	status := &coretypes.ResultStatus{
		SyncInfo: coretypes.SyncInfo{
			LatestBlockHeight: 1,
			LatestBlockTime:   time.Now(),
		},
	}

	var gotDeadline bool
	mockCosmos := &cosmosclient.MockCosmosMessageClient{}
	mockCosmos.On("Status", mock.Anything).Run(func(args mock.Arguments) {
		_, gotDeadline = args.Get(0).(context.Context).Deadline()
	}).Return(status, nil)

	s := &Server{recorder: mockCosmos, configManager: configManager}

	request := &ChatRequest{
		Timestamp: status.SyncInfo.LatestBlockTime.UnixNano(),
		AuthKey:   "f6-unique-authkey-validate-timestamp-nonce",
	}

	require.NoError(t, s.validateTimestampNonce(request))
	require.True(t, gotDeadline, "Status must be called with a deadline-bounded context, not context.Background()")
	mockCosmos.AssertExpectations(t)
}
