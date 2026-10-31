package heights

import (
	"context"
	"testing"
	"time"

	"devshard/cmd/gateway/config"
	"devshard/heightsync"
)

type cadenceSpy struct{ started int }

func (s *cadenceSpy) StartHeartbeatLoop() { s.started++ }

// Test flow:
//  1. Call StartCadence with a cadence spy and height sync enabled.
//  2. Assert the cadence started exactly once.
func TestAHeightSyncingSessionOpensItsOwnCadence(t *testing.T) {
	cadence := &cadenceSpy{}

	StartCadence(cadence, config.HeightSync{Enabled: true})

	if cadence.started != 1 {
		t.Fatalf("the cadence started %d times, want 1", cadence.started)
	}
}

// Test flow:
//  1. Call StartCadence with a cadence spy and an empty (disabled) height sync config.
//  2. Assert the cadence never started.
func TestAGatewayWithoutHeightSyncOpensNoCadence(t *testing.T) {
	cadence := &cadenceSpy{}

	StartCadence(cadence, config.HeightSync{})

	if cadence.started != 0 {
		t.Fatalf("the cadence started %d times, want none", cadence.started)
	}
}

// Test flow:
//  1. Build a courier with height sync enabled.
//  2. Assert the courier is non-nil and carries a peer-tip cache.
//  3. Record a signed origin tip with a blob into that cache.
//  4. Ask the courier's scheduler to decide an anchor.
//  5. Assert it decides without an oracle miss and returns the recorded height.
func TestTheCourierStampsFromTheCacheItsClientsFill(t *testing.T) {
	courier := BuildCourier(config.HeightSync{Enabled: true}, nil)

	if courier == nil {
		t.Fatal("BuildCourier() = nil for an enabled height sync")
	}
	if courier.HeightSyncPeerTips == nil {
		t.Fatal("the courier carries no peer-tip cache, so the seed has nowhere to land")
	}
	courier.HeightSyncPeerTips.RecordOriginWithBlob(&heightsync.HeightSyncSection{
		MainnetHeight:         4242,
		MainnetBlockHashHex:   "ab",
		OriginatorSenderID:    "host-a",
		OriginatorTimestampMs: time.Now().UnixMilli(),
	}, []byte("origin-blob"), []byte("origin-signature"))

	section, err, oracleMiss := courier.HeightSync.Decide(context.Background(), heightsync.DecideHints{Nonce: 1})

	if err != nil {
		t.Fatalf("Decide() = %v", err)
	}
	if oracleMiss {
		t.Fatal("the scheduler missed its oracle, so it is not reading the cache the clients fill")
	}
	if section == nil || section.MainnetHeight != 4242 {
		t.Fatalf("the anchor carries %+v, want the height the cache holds", section)
	}
}

// Test flow:
//  1. Call BuildCourier with an empty (disabled) height sync config.
//  2. Assert it returns nil.
func TestAGatewayWithoutHeightSyncCarriesNoCourier(t *testing.T) {
	if courier := BuildCourier(config.HeightSync{}, nil); courier != nil {
		t.Fatalf("BuildCourier() = %+v, want nothing", courier)
	}
}

// Test flow:
//  1. Build a courier with height sync enabled.
//  2. Record an unsigned origin tip into its peer-tip cache.
//  3. Ask the courier's scheduler to decide an anchor.
//  4. Assert it reports an oracle miss and returns no section.
func TestAnUnsignedTipIsNeverStamped(t *testing.T) {
	courier := BuildCourier(config.HeightSync{Enabled: true}, nil)
	courier.HeightSyncPeerTips.RecordOrigin(&heightsync.HeightSyncSection{
		MainnetHeight:         4242,
		MainnetBlockHashHex:   "ab",
		OriginatorSenderID:    "host-a",
		OriginatorTimestampMs: time.Now().UnixMilli(),
	})

	section, _, oracleMiss := courier.HeightSync.Decide(context.Background(), heightsync.DecideHints{Nonce: 1})

	if !oracleMiss || section != nil {
		t.Fatalf("the scheduler stamped %+v from an unsigned tip", section)
	}
}
