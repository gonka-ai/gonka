package public

import (
	"testing"

	"decentralized-api/apiconfig"
)

func TestValidateAgentEnvelopeConfig(t *testing.T) {
	// Disabled: returned unchanged regardless of chain_id.
	if got := validateAgentEnvelopeConfig(apiconfig.AgentEnvelopeConfig{Enabled: false}); got.Enabled {
		t.Error("disabled config should stay disabled")
	}

	// Enabled with a chain_id: returned unchanged.
	got := validateAgentEnvelopeConfig(apiconfig.AgentEnvelopeConfig{Enabled: true, ChainID: "gonka-mainnet"})
	if !got.Enabled || got.ChainID != "gonka-mainnet" {
		t.Error("enabled config with a chain_id should be returned unchanged")
	}

	// Enabled with an empty chain_id: must panic so the node refuses to start.
	defer func() {
		if recover() == nil {
			t.Error("enabled config with empty chain_id should panic")
		}
	}()
	validateAgentEnvelopeConfig(apiconfig.AgentEnvelopeConfig{Enabled: true, ChainID: ""})
}
