package devshard

import (
	"testing"

	"decentralized-api/apiconfig"
	"decentralized-api/chainphase"

	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestConfigManagerAvailability_ReflectsCache(t *testing.T) {
	cm := &apiconfig.ConfigManager{}
	avail := NewConfigManagerAvailability(cm, nil)

	got := avail.CurrentAvailability()
	require.False(t, got.Enabled)

	cm.SetDevshardVersions(apiconfig.DevshardVersionsCache{
		DevshardRequestsEnabled: true,
	})
	got = avail.CurrentAvailability()
	assert.True(t, got.Enabled)
	assert.NotZero(t, got.Time)

	cm.SetDevshardVersions(apiconfig.DevshardVersionsCache{
		DevshardRequestsEnabled: false,
	})
	got = avail.CurrentAvailability()
	assert.False(t, got.Enabled)
}

func TestConfigManagerAvailability_UsesEpochFromTracker(t *testing.T) {
	cm := &apiconfig.ConfigManager{}
	cm.SetDevshardVersions(apiconfig.DevshardVersionsCache{DevshardRequestsEnabled: true})

	tracker := &chainphase.ChainPhaseTracker{}
	tracker.Update(
		chainphase.BlockInfo{Height: 10, Hash: "h"},
		&types.Epoch{Index: 42, PocStartBlockHeight: 1},
		&types.EpochParams{},
		true,
		nil,
	)

	avail := NewConfigManagerAvailability(cm, tracker)
	got := avail.CurrentAvailability()
	assert.True(t, got.Enabled)
	assert.Equal(t, uint64(42), got.EpochID)
}

func TestConfigManagerAvailability_NilSafe(t *testing.T) {
	var a *ConfigManagerAvailability
	got := a.CurrentAvailability()
	assert.True(t, got.Enabled)
}
