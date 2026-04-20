package types

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSubnetEscrow_SessionConfigFields_RoundTrip(t *testing.T) {
	original := &SubnetEscrow{
		Id:               42,
		Creator:          "inference1abc",
		Amount:           5000,
		Slots:            []string{"valA", "valB"},
		EpochIndex:       10,
		AppHash:          "deadbeef",
		Settled:          false,
		TokenPrice:       99,
		RefusalTimeout:   30,
		ExecutionTimeout: 900,
		ValidationRate:   8000,
	}

	data, err := original.Marshal()
	require.NoError(t, err)

	decoded := &SubnetEscrow{}
	err = decoded.Unmarshal(data)
	require.NoError(t, err)

	assert.Equal(t, original.Id, decoded.Id)
	assert.Equal(t, original.Creator, decoded.Creator)
	assert.Equal(t, original.TokenPrice, decoded.TokenPrice)
	assert.Equal(t, original.RefusalTimeout, decoded.RefusalTimeout)
	assert.Equal(t, original.ExecutionTimeout, decoded.ExecutionTimeout)
	assert.Equal(t, original.ValidationRate, decoded.ValidationRate)
}

func TestSubnetEscrow_SessionConfigFields_ZeroValues(t *testing.T) {
	// Escrow with zero session config fields (backward compat).
	original := &SubnetEscrow{
		Id:         1,
		Creator:    "inference1old",
		Amount:     100,
		TokenPrice: 10,
	}

	data, err := original.Marshal()
	require.NoError(t, err)

	decoded := &SubnetEscrow{}
	err = decoded.Unmarshal(data)
	require.NoError(t, err)

	assert.Equal(t, int64(0), decoded.RefusalTimeout)
	assert.Equal(t, int64(0), decoded.ExecutionTimeout)
	assert.Equal(t, uint32(0), decoded.ValidationRate)
}

func TestSubnetEscrow_SessionConfigFields_Size(t *testing.T) {
	withFields := &SubnetEscrow{
		Id:               1,
		TokenPrice:       10,
		RefusalTimeout:   60,
		ExecutionTimeout: 1200,
		ValidationRate:   5000,
	}
	withoutFields := &SubnetEscrow{
		Id:         1,
		TokenPrice: 10,
	}

	// Size with fields must be larger than without.
	assert.Greater(t, withFields.Size(), withoutFields.Size())
}

func TestSubnetEscrowParams_SessionConfigFields_RoundTrip(t *testing.T) {
	original := &SubnetEscrowParams{
		MinAmount:          100,
		MaxAmount:          10000,
		MaxEscrowsPerEpoch: 5,
		GroupSize:          3,
		TokenPrice:         42,
		RefusalTimeout:     30,
		ExecutionTimeout:   600,
		ValidationRate:     8000,
	}

	data, err := original.Marshal()
	require.NoError(t, err)

	decoded := &SubnetEscrowParams{}
	err = decoded.Unmarshal(data)
	require.NoError(t, err)

	assert.Equal(t, original.TokenPrice, decoded.TokenPrice)
	assert.Equal(t, original.RefusalTimeout, decoded.RefusalTimeout)
	assert.Equal(t, original.ExecutionTimeout, decoded.ExecutionTimeout)
	assert.Equal(t, original.ValidationRate, decoded.ValidationRate)
}

func TestSubnetEscrowParams_SessionConfigFields_ZeroValues(t *testing.T) {
	original := &SubnetEscrowParams{
		MinAmount:  100,
		TokenPrice: 10,
	}

	data, err := original.Marshal()
	require.NoError(t, err)

	decoded := &SubnetEscrowParams{}
	err = decoded.Unmarshal(data)
	require.NoError(t, err)

	assert.Equal(t, int64(0), decoded.RefusalTimeout)
	assert.Equal(t, int64(0), decoded.ExecutionTimeout)
	assert.Equal(t, uint32(0), decoded.ValidationRate)
}

func TestSubnetEscrow_Getters(t *testing.T) {
	e := &SubnetEscrow{
		RefusalTimeout:   45,
		ExecutionTimeout: 900,
		ValidationRate:   7500,
	}
	assert.Equal(t, int64(45), e.GetRefusalTimeout())
	assert.Equal(t, int64(900), e.GetExecutionTimeout())
	assert.Equal(t, uint32(7500), e.GetValidationRate())
}

func TestSubnetEscrow_Getters_Nil(t *testing.T) {
	var e *SubnetEscrow
	assert.Equal(t, int64(0), e.GetRefusalTimeout())
	assert.Equal(t, int64(0), e.GetExecutionTimeout())
	assert.Equal(t, uint32(0), e.GetValidationRate())
}

func TestSubnetEscrowParams_Getters(t *testing.T) {
	p := &SubnetEscrowParams{
		RefusalTimeout:   30,
		ExecutionTimeout: 600,
		ValidationRate:   8000,
	}
	assert.Equal(t, int64(30), p.GetRefusalTimeout())
	assert.Equal(t, int64(600), p.GetExecutionTimeout())
	assert.Equal(t, uint32(8000), p.GetValidationRate())
}

func TestSubnetEscrowParams_Getters_Nil(t *testing.T) {
	var p *SubnetEscrowParams
	assert.Equal(t, int64(0), p.GetRefusalTimeout())
	assert.Equal(t, int64(0), p.GetExecutionTimeout())
	assert.Equal(t, uint32(0), p.GetValidationRate())
}
