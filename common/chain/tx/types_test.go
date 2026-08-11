package tx

import (
	"testing"
	"time"
)

func TestConfigWithDefaultsChainID(t *testing.T) {
	t.Parallel()

	got := Config{}.withDefaults()
	if got.ChainID != DefaultChainID {
		t.Fatalf("empty ChainID: got %q, want %q", got.ChainID, DefaultChainID)
	}
	if got.FeeDenom != DefaultFeeDenom {
		t.Fatalf("FeeDenom: got %q, want %q", got.FeeDenom, DefaultFeeDenom)
	}
	if got.FeeAmount != DefaultFeeAmount {
		t.Fatalf("FeeAmount: got %d, want %d", got.FeeAmount, DefaultFeeAmount)
	}
	if got.GasLimit != DefaultGasLimit {
		t.Fatalf("GasLimit: got %d, want %d", got.GasLimit, DefaultGasLimit)
	}
	if got.PollInterval != DefaultPollInterval {
		t.Fatalf("PollInterval: got %v, want %v", got.PollInterval, DefaultPollInterval)
	}
	if got.PollTimeout != DefaultPollTimeout {
		t.Fatalf("PollTimeout: got %v, want %v", got.PollTimeout, DefaultPollTimeout)
	}

	custom := Config{
		ChainID:      ChainIDTestnet,
		FeeDenom:     "uatom",
		FeeAmount:    42,
		GasLimit:     99,
		PollInterval: time.Second,
		PollTimeout:  3 * time.Second,
	}.withDefaults()
	if custom.ChainID != ChainIDTestnet {
		t.Fatalf("custom ChainID overridden: got %q", custom.ChainID)
	}
	if custom.FeeDenom != "uatom" || custom.FeeAmount != 42 || custom.GasLimit != 99 {
		t.Fatalf("custom fee/gas overridden: %+v", custom)
	}
}

func TestKnownChainIDs(t *testing.T) {
	t.Parallel()
	want := map[string]bool{
		DefaultChainID: true,
		ChainIDTestnet: true,
		ChainIDTestenv: true,
	}
	if len(KnownChainIDs) != len(want) {
		t.Fatalf("KnownChainIDs len=%d, want %d", len(KnownChainIDs), len(want))
	}
	for _, id := range KnownChainIDs {
		if !want[id] {
			t.Fatalf("unexpected known chain id %q", id)
		}
	}
}
