package cmd

import "testing"

func TestInitCometBFTConfig_SubscriptionRoom(t *testing.T) {
	cfg := initCometBFTConfig()
	if cfg.RPC.MaxSubscriptionsPerClient != 10 {
		t.Fatalf("max_subscriptions_per_client = %d, want 10", cfg.RPC.MaxSubscriptionsPerClient)
	}
}
