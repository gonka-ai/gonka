package user

import (
	"testing"

	"devshard/bridge"
	"devshard/types"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type bindParamsBridge struct {
	bridge.MainnetBridge
	live types.LiveSessionBindParams
}

func (b *bindParamsBridge) GetSessionBindParams() (types.LiveSessionBindParams, error) {
	return b.live, nil
}

type escrowOnlyBridge struct{}

func (escrowOnlyBridge) GetEscrow(string) (*bridge.EscrowInfo, error) { return nil, nil }
func (escrowOnlyBridge) GetHostInfo(string) (*bridge.HostInfo, error)   { return nil, nil }
func (escrowOnlyBridge) GetValidationThreshold(uint64, string) (*bridge.Decimal, error) {
	return nil, bridge.ErrNotImplemented
}
func (escrowOnlyBridge) VerifyWarmKey(string, string) (bool, error) { return false, nil }
func (escrowOnlyBridge) OnEscrowCreated(bridge.EscrowInfo) error   { return bridge.ErrNotImplemented }
func (escrowOnlyBridge) OnSettlementProposed(string, []byte, uint64) error {
	return bridge.ErrNotImplemented
}
func (escrowOnlyBridge) OnSettlementFinalized(string) error { return bridge.ErrNotImplemented }
func (escrowOnlyBridge) SubmitDisputeState(string, []byte, uint64, map[uint32][]byte) error {
	return bridge.ErrNotImplemented
}

func TestSessionConfigAtBind_AppliesChainParams(t *testing.T) {
	const groupSize = 16
	escrow := &bridge.EscrowInfo{
		TokenPrice:                1,
		CreateDevshardFee:         10_000,
		FeePerNonce:               1_000,
		InferenceSealGraceNonces:  55,
		InferenceSealGraceSeconds: 77,
		AutoSealEveryNNonces:      16,
	}
	b := &bindParamsBridge{
		live: types.LiveSessionBindParams{
			ValidationRate: 0,
		},
	}

	cfg, err := sessionConfigAtBind(groupSize, escrow, b)
	require.NoError(t, err)
	assert.Equal(t, uint32(55), cfg.InferenceSealGraceNonces, "grace nonces come from escrow")
	assert.Equal(t, uint32(77), cfg.InferenceSealGraceSeconds, "grace seconds come from escrow")
	assert.Equal(t, uint32(16), cfg.AutoSealEveryNNonces, "auto-seal interval comes from escrow")
	assert.Equal(t, uint32(0), cfg.ValidationRate)
}

func TestSessionConfigAtBind_FallsBackWithoutParamsBridge(t *testing.T) {
	const groupSize = 16
	escrow := &bridge.EscrowInfo{TokenPrice: 1}

	cfg, err := sessionConfigAtBind(groupSize, escrow, escrowOnlyBridge{})
	require.NoError(t, err)
	assert.Equal(t, types.DefaultInferenceSealGraceNonces(groupSize), cfg.InferenceSealGraceNonces)
	assert.Equal(t, uint32(types.DefaultInferenceSealGraceSeconds), cfg.InferenceSealGraceSeconds)
	assert.Equal(t, types.DefaultAutoSealEveryNNonces, cfg.AutoSealEveryNNonces)
}
