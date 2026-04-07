package bridge

import (
	"subnet/types"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockBridge implements MainnetBridge for testing BuildGroup.
type mockBridge struct {
	escrow    *EscrowInfo
	escrowErr error
	hosts     map[string]*HostInfo
	hostErr   error
}

func (m *mockBridge) GetEscrow(_ string) (*EscrowInfo, error) {
	return m.escrow, m.escrowErr
}
func (m *mockBridge) GetHostInfo(addr string) (*HostInfo, error) {
	if m.hostErr != nil {
		return nil, m.hostErr
	}
	info, ok := m.hosts[addr]
	if !ok {
		return nil, ErrParticipantNotFound
	}
	return info, nil
}
func (m *mockBridge) VerifyWarmKey(_, _ string) (bool, error) {
	return false, ErrNotImplemented
}
func (m *mockBridge) OnEscrowCreated(_ EscrowInfo) error                            { return ErrNotImplemented }
func (m *mockBridge) OnSettlementProposed(_ string, _ []byte, _ uint64) error       { return ErrNotImplemented }
func (m *mockBridge) OnSettlementFinalized(_ string) error                          { return ErrNotImplemented }
func (m *mockBridge) SubmitDisputeState(_ string, _ []byte, _ uint64, _ map[uint32][]byte) error {
	return ErrNotImplemented
}

func TestBuildGroup_HappyPath(t *testing.T) {
	b := &mockBridge{
		escrow: &EscrowInfo{
			EscrowID: "1",
			Slots:    []string{"valA", "valB", "valC"},
		},
	}

	group, err := BuildGroup("1", b)
	require.NoError(t, err)
	require.Len(t, group, 3)

	for i, slot := range group {
		assert.Equal(t, uint32(i), slot.SlotID)
	}
	assert.Equal(t, "valA", group[0].ValidatorAddress)
	assert.Equal(t, "valC", group[2].ValidatorAddress)
}

func TestBuildGroup_DuplicateAddresses(t *testing.T) {
	b := &mockBridge{
		escrow: &EscrowInfo{
			EscrowID: "1",
			// valA appears in slots 0, 1, and 3
			Slots: []string{"valA", "valA", "valB", "valA"},
		},
	}

	group, err := BuildGroup("1", b)
	require.NoError(t, err)
	require.Len(t, group, 4)

	// All slots should have correct SlotID
	for i, slot := range group {
		assert.Equal(t, uint32(i), slot.SlotID)
	}
	// Slots 0, 1, 3 all map to valA
	assert.Equal(t, "valA", group[0].ValidatorAddress)
	assert.Equal(t, "valA", group[1].ValidatorAddress)
	assert.Equal(t, "valB", group[2].ValidatorAddress)
	assert.Equal(t, "valA", group[3].ValidatorAddress)
}

func TestBuildGroup_EscrowError(t *testing.T) {
	b := &mockBridge{escrowErr: ErrEscrowNotFound}
	_, err := BuildGroup("1", b)
	assert.ErrorIs(t, err, ErrEscrowNotFound)
}

func TestBuildGroup_ValidateGroupPasses(t *testing.T) {
	b := &mockBridge{
		escrow: &EscrowInfo{
			EscrowID: "1",
			Slots:    []string{"valA"},
		},
	}

	group, err := BuildGroup("1", b)
	require.NoError(t, err)
	// ValidateGroup is called inside BuildGroup, but verify directly too
	assert.NoError(t, types.ValidateGroup(group))
}
